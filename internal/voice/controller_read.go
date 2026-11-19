package voice

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type workerReadCursor struct {
	Version  int    `json:"v"`
	Source   string `json:"source"`
	Session  string `json:"session"`
	Instance string `json:"instance"`
	Value    string `json:"value"`
}
type WorkerOutputPage struct {
	Encoding        string            `json:"encoding"`
	Source          string            `json:"source"`
	Session         string            `json:"session"`
	SessionInstance string            `json:"session_instance"`
	Scope           string            `json:"scope"`
	Complete        bool              `json:"complete"`
	Floor           bool              `json:"floor"`
	NativeScope     string            `json:"native_scope,omitempty"`
	Items           []json.RawMessage `json:"items"`
	NextCursor      string            `json:"next_cursor,omitempty"`
}

func decodeWorkerReadCursor(raw string, input ReadInput) (workerReadCursor, error) {
	var cursor workerReadCursor
	if raw == "" {
		return cursor, nil
	}
	if len(raw) > MaxReadCursorBytes {
		return cursor, ErrInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || (cursor.Source != "native" && cursor.Source != "retained") || cursor.Session != input.Session || cursor.Instance != input.SessionInstance || cursor.Value == "" {
		return workerReadCursor{}, ErrStale
	}
	if input.Source != "" && input.Source != cursor.Source {
		return workerReadCursor{}, ErrStale
	}
	return cursor, nil
}
func encodeWorkerReadCursor(source string, input ReadInput, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	raw, err := json.Marshal(workerReadCursor{1, source, input.Session, input.SessionInstance, value})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > MaxReadCursorBytes {
		return "", ErrInvalid
	}
	return encoded, nil
}
func (c *Controller) readWorker(input ReadInput) (json.RawMessage, error) {
	if c.discussion != nil {
		return c.readDiscussionWorker(input)
	}
	row, err := c.workerTarget(input.SessionInput, false)
	if err != nil {
		return nil, err
	}
	cursor, err := decodeWorkerReadCursor(input.Cursor, input)
	if err != nil {
		return nil, err
	}
	source := input.Source
	if cursor.Source != "" {
		source = cursor.Source
	}
	if source == "" {
		source = "retained"
		if row.Agent == "opencode" {
			source = "native"
		}
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	for {
		page := WorkerOutputPage{Source: source, Session: input.Session, SessionInstance: input.SessionInstance, Complete: false, Items: []json.RawMessage{}}
		switch source {
		case "native":
			native, err := c.workers.NativeHistoryPage(schema.InteractionHistoryReq{Session: input.Session, Native: true, NativeCursor: cursor.Value, Limit: limit})
			if err != nil {
				return nil, err
			}
			page.Encoding = "native_replacement_snapshots"
			page.Scope = "native_session_page"
			page.NativeScope = native.Scope
			page.Floor = native.Floor
			page.Items = native.Items
			page.NextCursor, err = encodeWorkerReadCursor(source, input, native.NextCursor)
			if err != nil {
				return nil, err
			}
		case "retained":
			retained, err := c.workers.RetainedHistoryPage(schema.InteractionHistoryReq{Session: input.Session, BeforeItem: cursor.Value, Limit: limit})
			if err != nil {
				return nil, err
			}
			page.Scope = "daemon_retained_discussion_window; items may include earlier process incarnations"
			page.Floor = retained.Floor
			page.Encoding = "folded_retained_events"
			var before string
			page.Items, before, err = retainedWorkerItems(retained.Records)
			if err != nil {
				return nil, err
			}
			if !retained.Floor && before != "" {
				page.NextCursor, err = encodeWorkerReadCursor(source, input, before)
				if err != nil {
					return nil, err
				}
			}
		default:
			return nil, ErrInvalid
		}
		raw, err := controllerResult(page)
		if err == nil {
			_, err = managerToolResponse(raw)
		}
		if err == nil {
			// The read names a discussion; recheck its current incarnation before returning it.
			if _, err = c.workerTarget(input.SessionInput, false); err != nil {
				return nil, err
			}
			return raw, nil
		}
		if !errors.Is(err, errManagerPayloadTooLarge) || limit == 1 {
			return nil, err
		}
		limit = max(limit/2, 1)
	}
}
