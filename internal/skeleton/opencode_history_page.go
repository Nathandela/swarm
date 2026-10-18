package skeleton

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

var errNativeHistoryBounds = errors.New("native history whole message exceeds the bounded display page")

const maxNativeHistoryReads = 16

type nativeHistorySlice struct {
	Session    json.RawMessage
	Messages   []json.RawMessage
	NextCursor string
}
type nativeHistoryRead func(context.Context, int, string) (nativeHistorySlice, error)
type nativeHistoryCursor struct {
	Version int    `json:"v"`
	Scope   string `json:"scope"`
	Mode    string `json:"mode"`
	Before  string `json:"before,omitempty"`
	Anchor  string `json:"anchor"`
	At      int64  `json:"at"`
}

func decodeNativeHistoryCursor(raw, scope string) (nativeHistoryCursor, error) {
	if raw == "" {
		return nativeHistoryCursor{}, nil
	}
	if len(raw) > schema.MaxNativeHistoryCursorBytes {
		return nativeHistoryCursor{}, errors.New("native history cursor exceeds bound")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nativeHistoryCursor{}, errors.New("native history cursor is invalid")
	}
	var cursor nativeHistoryCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.Scope != scope || cursor.At <= 0 || !strings.HasPrefix(cursor.Anchor, "msg_") || len(cursor.Anchor) > 128 || len(cursor.Before) > 4096 || (cursor.Mode != "sdk" && cursor.Mode != "export") || cursor.Mode == "sdk" && cursor.Before == "" {
		return cursor, errors.New("native history cursor does not match this profile and root")
	}
	return cursor, nil
}
func encodeNativeHistoryCursor(scope, mode, before string, row json.RawMessage) (string, error) {
	info, _, err := opencode.DecodeMessageRow(row)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(nativeHistoryCursor{Version: 1, Scope: scope, Mode: mode, Before: before, Anchor: info.ID, At: info.Time.Created})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > schema.MaxNativeHistoryCursorBytes {
		return "", errors.New("native history cursor exceeds bound")
	}
	return encoded, nil
}
func validateNativeHistorySlice(meta persist.Meta, page nativeHistorySlice, limit int, before string) error {
	var session struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
		Time      struct {
			Created int64 `json:"created"`
		} `json:"time"`
	}
	if json.Unmarshal(page.Session, &session) != nil || session.ID != meta.ConversationID || session.Directory != meta.ProviderCwd() || session.Time.Created != meta.OpenCodeBinding.NativeCreatedAtMS || page.Messages == nil || len(page.Messages) > limit || len(page.NextCursor) > 4096 || page.NextCursor != "" && page.NextCursor == before || len(page.Messages) == 0 && page.NextCursor != "" {
		return errors.New("native history page identity or pagination is unconfirmed")
	}
	seen := map[string]bool{}
	for _, raw := range page.Messages {
		info, _, err := opencode.DecodeMessageRow(raw)
		if err != nil || info.SessionID != meta.ConversationID || seen[info.ID] {
			return errors.New("native history contains an invalid or foreign message")
		}
		seen[info.ID] = true
	}
	sortNativeHistory(page.Messages)
	return nil
}

// The only SDK cursors we forward were returned by the native page read itself.
func paginateNativeHistory(ctx context.Context, meta persist.Meta, req schema.InteractionHistoryReq, read nativeHistoryRead) (schema.NativeHistoryPage, map[string]json.RawMessage, error) {
	var empty schema.NativeHistoryPage
	scope, err := nativeProjectionScope(meta)
	if err != nil {
		return empty, nil, err
	}
	cursor, err := decodeNativeHistoryCursor(req.NativeCursor, scope)
	if err != nil {
		return empty, nil, err
	}
	if req.Limit <= 0 {
		return empty, nil, errors.New("native history requires a positive page limit")
	}
	boundary := nativeHistoryCursor{}
	if cursor.Mode == "sdk" {
		boundary = cursor
	}
	advance := func(row json.RawMessage) {
		info, _, _ := opencode.DecodeMessageRow(row)
		boundary.Anchor, boundary.At = info.ID, info.Time.Created
	}
	reads := 0
	boundedRead := func(limit int, before string) (nativeHistorySlice, error) {
		reads++
		if reads > maxNativeHistoryReads {
			return nativeHistorySlice{}, errors.New("native history read budget exhausted")
		}
		if err := ctx.Err(); err != nil {
			return nativeHistorySlice{}, err
		}
		page, err := read(ctx, limit, before)
		if err != nil {
			return page, err
		}
		if err := validateNativeHistorySlice(meta, page, limit, before); err != nil {
			return page, err
		}
		for _, row := range page.Messages {
			info, _, _ := opencode.DecodeMessageRow(row)
			if boundary.At > 0 && !ocAfter(boundary.At, boundary.Anchor, info.Time.Created, info.ID) {
				return page, errors.New("native history continuation crossed its exact boundary")
			}
		}
		return page, nil
	}
	before := cursor.Before
	anchor := req.BeforeItem
	if cursor.Mode == "export" {
		anchor = ""
		before = ""
	}
	if req.NativeCursor != "" && cursor.Mode == "sdk" {
		anchor = ""
	}
	if anchor != "" || cursor.Mode == "export" {
		found := false
		for !found && reads < maxNativeHistoryReads-2 {
			page, err := boundedRead(50, before)
			if err != nil {
				return empty, nil, err
			}
			for i, row := range page.Messages {
				match := nativeHistoryHasItem(scope, meta.ConversationID, row, anchor)
				if cursor.Mode == "export" {
					info, _, _ := opencode.DecodeMessageRow(row)
					match = info.ID == cursor.Anchor && info.Time.Created == cursor.At
				}
				if !match {
					continue
				}
				// Requery the exact newer prefix, including the anchor, to obtain its real before cursor.
				prefix, err := boundedRead(len(page.Messages)-i, before)
				if err != nil {
					return empty, nil, err
				}
				if len(prefix.Messages) == 0 {
					return empty, nil, errors.New("native history anchor moved during read")
				}
				expected, _, _ := opencode.DecodeMessageRow(row)
				actual, _, _ := opencode.DecodeMessageRow(prefix.Messages[0])
				if expected.ID != actual.ID || expected.Time.Created != actual.Time.Created {
					return empty, nil, errors.New("native history anchor moved during read")
				}
				if prefix.NextCursor == "" {
					return schema.NativeHistoryPage{Version: 2, Scope: scope, Items: []json.RawMessage{}, Floor: true}, nil, nil
				}
				advance(prefix.Messages[0])
				before = prefix.NextCursor
				found = true
				break
			}
			if found {
				break
			}
			if page.NextCursor == "" {
				break
			}
			advance(page.Messages[0])
			before = page.NextCursor
		}
		if !found {
			return empty, nil, errors.New("native history anchor is unavailable within the bounded read")
		}
	}
	limit := min(req.Limit, 50)
	for reads < maxNativeHistoryReads {
		page, err := boundedRead(limit, before)
		if err != nil {
			return empty, nil, err
		}
		items, details, err := shapeNativeHistoryRows(meta, scope, page.Messages)
		if errors.Is(err, errNativeHistoryBounds) && limit > 1 {
			limit = max(1, limit/2)
			continue
		}
		if err != nil {
			return empty, nil, err
		}
		result := schema.NativeHistoryPage{Version: 2, Scope: scope, Items: items, Floor: page.NextCursor == ""}
		if !result.Floor {
			result.NextCursor, err = encodeNativeHistoryCursor(scope, "sdk", page.NextCursor, page.Messages[0])
			if err != nil {
				return empty, nil, err
			}
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return empty, nil, err
		}
		if len(raw) > schema.MaxNativeHistoryPageBytes {
			if limit > 1 {
				limit = max(1, limit/2)
				continue
			}
			return empty, nil, errNativeHistoryBounds
		}
		if len(items) > 0 || result.Floor {
			return result, details, nil
		}
		advance(page.Messages[0])
		before = page.NextCursor // skip only a whole, non-rendering native page
	}
	return empty, nil, errors.New("native history has no displayable items within the bounded read")
}
func nativeHistoryHasItem(scope, root string, row json.RawMessage, itemID string) bool {
	if itemID == "" {
		return false
	}
	info, _, err := opencode.DecodeMessageRow(row)
	if err != nil {
		return false
	}
	if info.Role == "user" && nativeProjectionID(scope, "input_outcome", info.ID) == itemID {
		return true
	}
	for _, item := range opencode.NativeInteractions().Interactions(adapter.HookPayload{Event: opencode.NativeMessageRowEvent, Raw: row}) {
		kind, fields := nativeSnapshotFields(root, info, item)
		if fields != nil && nativeProjectionID(scope, kind, info.SessionID+":"+item.Ref) == itemID {
			return true
		}
	}
	return false
}
func shapeNativeHistoryRows(meta persist.Meta, scope string, rows []json.RawMessage) ([]json.RawMessage, map[string]json.RawMessage, error) {
	items := []json.RawMessage{}
	details := map[string]json.RawMessage{}
	index := map[string]int{}
	for _, row := range rows {
		info, _, err := opencode.DecodeMessageRow(row)
		if err != nil || info.SessionID != meta.ConversationID {
			return nil, nil, errors.New("native history message scope is invalid")
		}
		for _, item := range opencode.NativeInteractions().Interactions(adapter.HookPayload{Event: opencode.NativeMessageRowEvent, Raw: row}) {
			if err := item.Validate(); err != nil {
				return nil, nil, err
			}
			kind, fields := nativeSnapshotFields(meta.ConversationID, info, item)
			if fields == nil {
				continue
			}
			fields["provenance"] = "imported"
			id := nativeProjectionID(scope, kind, info.SessionID+":"+item.Ref)
			envelope := daemon.InteractionItem{V: 2, ItemID: id, TS: time.UnixMilli(info.Time.Created).UTC(), Kind: kind, Status: item.Status}
			if item.TurnRef != "" {
				envelope.TurnID = nativeProjectionID(scope, "input", item.TurnRef)
			}
			raw, full, err := fitItem(envelope, fields)
			if err != nil {
				return nil, nil, err
			}
			if old, exists := index[id]; exists {
				items[old] = raw
			} else {
				index[id] = len(items)
				items = append(items, raw)
			}
			if full != nil {
				details[id] = full
			}
			if len(items) > schema.MaxNativeHistoryItems {
				return nil, nil, errNativeHistoryBounds
			}
		}
	}
	return items, details, nil
}

// Ended sessions have a bounded native export rather than an SDK pagination cursor.
func paginateEndedNativeHistory(meta persist.Meta, req schema.InteractionHistoryReq, rows []json.RawMessage) (schema.NativeHistoryPage, map[string]json.RawMessage, error) {
	var empty schema.NativeHistoryPage
	scope, err := nativeProjectionScope(meta)
	if err != nil {
		return empty, nil, err
	}
	cursor, err := decodeNativeHistoryCursor(req.NativeCursor, scope)
	if err != nil {
		return empty, nil, err
	}
	sortNativeHistory(rows)
	end := len(rows)
	if req.NativeCursor != "" {
		for end > 0 {
			info, _, err := opencode.DecodeMessageRow(rows[end-1])
			if err != nil {
				return empty, nil, err
			}
			if ocAfter(cursor.At, cursor.Anchor, info.Time.Created, info.ID) {
				break
			}
			end--
		}
	} else if req.BeforeItem != "" {
		found := false
		for i := len(rows) - 1; i >= 0; i-- {
			if nativeHistoryHasItem(scope, meta.ConversationID, rows[i], req.BeforeItem) {
				end = i
				found = true
				break
			}
		}
		if !found {
			return empty, nil, errors.New("native history anchor is unavailable")
		}
	}
	limit := min(req.Limit, 50)
	if limit <= 0 {
		return empty, nil, errors.New("native history requires a positive page limit")
	}
	for scan := 0; scan < maxNativeHistoryReads; scan++ {
		start := max(0, end-limit)
		selected := rows[start:end]
		items, details, err := shapeNativeHistoryRows(meta, scope, selected)
		if errors.Is(err, errNativeHistoryBounds) && limit > 1 {
			limit = max(1, limit/2)
			continue
		}
		if err != nil {
			return empty, nil, err
		}
		result := schema.NativeHistoryPage{Version: 2, Scope: scope, Items: items, Floor: start == 0}
		if !result.Floor {
			result.NextCursor, err = encodeNativeHistoryCursor(scope, "export", "", selected[0])
			if err != nil {
				return empty, nil, err
			}
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return empty, nil, err
		}
		if len(raw) > schema.MaxNativeHistoryPageBytes {
			if limit > 1 {
				limit = max(1, limit/2)
				continue
			}
			return empty, nil, errNativeHistoryBounds
		}
		if len(items) > 0 || result.Floor {
			return result, details, nil
		}
		end = start
	}
	return empty, nil, fmt.Errorf("native history has no displayable items within %d bounded reads", maxNativeHistoryReads)
}

func (d *Daemon) nativeInteractionHistory(req schema.InteractionHistoryReq) (schema.NativeHistoryPage, protocol.ErrorCode, error) {
	var empty schema.NativeHistoryPage
	local, err := d.resolveChatSession("", req.Session)
	if err != nil || !req.Native || req.Limit <= 0 || len(req.NativeCursor) > schema.MaxNativeHistoryCursorBytes || len(req.BeforeItem) > 128 {
		return empty, protocol.CodeInvalidField, errors.New("native history request is invalid")
	}
	if d.core == nil {
		return empty, protocol.CodeUnavailable, errors.New("native history is unavailable")
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.AgentType != "opencode" || meta.RequiredRuntime != persist.RequiredRuntimeOpenCode || meta.OpenCodeBinding == nil || meta.OpenCodeBinding.NativeCreatedAtMS <= 0 || !adapter.IsOpenCodeConversationID(meta.ConversationID) {
		return empty, protocol.CodeUnavailable, errors.New("native history root is unconfirmed")
	}
	identity := nativeHistoryIdentity(meta)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var result schema.NativeHistoryPage
	var details map[string]json.RawMessage
	if meta.Status.Process == status.ProcessRunning {
		binding, bindingErr := d.core.SessionOpenCodeBridge(local)
		if bindingErr != nil || !openCodeMetaMatches(meta, identity) {
			return empty, protocol.CodeUnavailable, errors.New("native history bridge is unavailable")
		}
		client, clientErr := opencodebridge.NewClient(binding.SocketPath, binding.Token)
		if clientErr != nil {
			return empty, protocol.CodeUnavailable, clientErr
		}
		defer client.Close()
		result, details, err = paginateNativeHistory(ctx, meta, req, func(ctx context.Context, limit int, before string) (nativeHistorySlice, error) {
			snapshot, e := client.Snapshot(ctx, limit, before)
			if e == nil {
				e = validateOpenCodeSnapshot(snapshot, identity)
			}
			return nativeHistorySlice{Session: snapshot.Session, Messages: snapshot.Messages, NextCursor: snapshot.NextCursor}, e
		})
	} else {
		resolver := openCodeHistoryResolver{core: d.core}
		exported, exportErr := resolver.exportEnded(ctx, meta)
		err = exportErr
		if err == nil {
			result, details, err = paginateEndedNativeHistory(meta, req, exported.Messages)
		}
	}
	if err != nil {
		return empty, protocol.CodeUnavailable, err
	}
	current, ok := d.core.Get(local)
	if !ok || current.OpenCodeBinding == nil || nativeHistoryIdentity(current) != identity {
		return empty, protocol.CodeUnavailable, errors.New("native history binding changed during read")
	}
	currentScope, err := nativeProjectionScope(current)
	if err != nil || currentScope != result.Scope {
		return empty, protocol.CodeUnavailable, errors.New("native history profile changed during read")
	}
	d.itemMu.Lock()
	for id, full := range details {
		d.retainDetailLocked(local, id, full)
	}
	d.itemMu.Unlock()
	return result, "", nil
}
