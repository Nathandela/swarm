package opencode

// The legacy OpenCode HTTP/SSE wire shapes (the P3/P4 authoritative surface):
// envelope {id,type,properties} events plus the /session/status snapshot map.
// This file is pure decode -- no state, no I/O, no heuristics. Unknown event
// kinds decode to fact-less events with no error (forward compatibility is the
// reducer's decision); malformed payloads for known kinds are errors. The v2
// event family is not decoded here.

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Event kinds of the legacy SSE surface this module normalizes.
const (
	EventSessionStatus      = "session.status"
	EventSessionIdle        = "session.idle"
	EventSessionError       = "session.error"
	EventMessageUpdated     = "message.updated"
	EventMessagePartUpdated = "message.part.updated"
)

// Event is the decoded envelope. Exactly one fact pointer is non-nil for a
// known kind; an unknown kind carries none.
type Event struct {
	ID   string
	Kind string

	SessionStatus      *SessionStatusEvent
	SessionIdle        *SessionIdleEvent
	SessionError       *SessionErrorEvent
	MessageUpdated     *MessageUpdatedEvent
	MessagePartUpdated *MessagePartUpdatedEvent
}

// SessionStatusEvent is the session.status payload: the root's busy/idle/retry
// state, live.
type SessionStatusEvent struct {
	SessionID string
	Status    SessionStatus
}

// SessionStatus is the status union. Type is decoded verbatim, so a subtype
// this build does not know still reaches the reducer as itself instead of
// being flattened into a known one. Retry detail is meaningful for "retry".
type SessionStatus struct {
	Type    string       `json:"type"`
	Attempt int          `json:"attempt"`
	Message string       `json:"message"`
	Next    int64        `json:"next"`
	Action  *RetryAction `json:"action"`
}

// RetryAction is the retry status's optional action metadata (provider name,
// human labels).
type RetryAction struct {
	Reason   string `json:"reason"`
	Provider string `json:"provider"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Label    string `json:"label"`
}

// SessionIdleEvent is the session.idle payload.
type SessionIdleEvent struct {
	SessionID string
}

// SessionErrorEvent is the session.error payload carrying a native error.
type SessionErrorEvent struct {
	SessionID string
	Error     MessageError
}

// MessageUpdatedEvent is the message.updated payload; Info is the whole native
// message record (Raw retains its exact bytes).
type MessageUpdatedEvent struct {
	SessionID string
	Info      Message
}

// Message is a normalized native message record.
type Message struct {
	ID        string
	SessionID string
	Role      string
	ParentID  string
	Time      MessageTime
	Finish    string
	Error     *MessageError
	// Summary is ordinary diff metadata on user messages and a compaction marker
	// only when an assistant carries summary:true.
	Summary json.RawMessage
	// Raw is the complete native `info` object as received.
	Raw json.RawMessage
}

// IsAssistantSummary reports the assistant-side compaction marker
// (`summary: true`).
func (m Message) IsAssistantSummary() bool {
	return m.Role == "assistant" && bytes.Equal(bytes.TrimSpace(m.Summary), []byte("true"))
}

// MessageTime is the native message clock in epoch milliseconds.
type MessageTime struct {
	Created   int64 `json:"created"`
	Completed int64 `json:"completed"`
}

// MessageError is a native terminal error. Data is kept structurally (the raw
// message may be shown; no field is invented when the wire omitted it).
type MessageError struct {
	Name string
	Data MessageErrorData
}

// MessageErrorData is the common error payload.
type MessageErrorData struct {
	Message     string
	StatusCode  *int
	IsRetryable *bool
}

// MessagePartUpdatedEvent is the message.part.updated payload.
type MessagePartUpdatedEvent struct {
	SessionID string
	Part      Part
}

// Part is a normalized native message part. Only the fields this module
// tracks are typed; Raw retains the exact part bytes for everything else.
type Part struct {
	ID        string
	SessionID string
	MessageID string
	Type      string
	Text      string
	Synthetic bool
	Ignored   bool
	CallID    string
	Tool      string
	State     *ToolState
	// Auto is the compaction part's marker (type "compaction").
	Auto bool
	Raw  json.RawMessage
}

// ToolState is a tool part's execution state.
type ToolState struct {
	Status string          `json:"status"`
	Title  string          `json:"title"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Error  string          `json:"error"`
}

// DecodeEvent decodes one legacy event JSON document.
const MaxNativeEventBytes = 1 << 20
const MaxNativeMessageBytes = 2 << 20

func DecodeEvent(raw []byte) (Event, error) {
	if len(raw) > MaxNativeEventBytes || !jsonObject(bytes.TrimSpace(raw)) {
		return Event{}, fmt.Errorf("opencode: event is not a bounded object")
	}
	var w wireEvent
	if err := json.Unmarshal(raw, &w); err != nil {
		return Event{}, err
	}
	if w.Type == "" {
		return Event{}, fmt.Errorf("opencode: event has no type")
	}
	ev := Event{ID: w.ID, Kind: w.Type}
	switch w.Type {
	case EventSessionStatus:
		var p struct {
			SessionID string         `json:"sessionID"`
			Status    *SessionStatus `json:"status"`
		}
		if err := decodeProperties(w.Properties, &p); err != nil {
			return Event{}, err
		}
		if p.SessionID == "" || p.Status == nil || p.Status.Type == "" {
			return Event{}, fmt.Errorf("opencode: invalid session status")
		}
		ev.SessionStatus = &SessionStatusEvent{SessionID: p.SessionID, Status: *p.Status}
	case EventSessionIdle:
		var p struct {
			SessionID string `json:"sessionID"`
		}
		if err := decodeProperties(w.Properties, &p); err != nil {
			return Event{}, err
		}
		if p.SessionID == "" {
			return Event{}, fmt.Errorf("opencode: idle event has no session")
		}
		ev.SessionIdle = &SessionIdleEvent{SessionID: p.SessionID}
	case EventSessionError:
		var p struct {
			SessionID string            `json:"sessionID"`
			Error     *wireMessageError `json:"error"`
		}
		if err := decodeProperties(w.Properties, &p); err != nil {
			return Event{}, err
		}
		if p.SessionID == "" || p.Error == nil || p.Error.Name == "" {
			return Event{}, fmt.Errorf("opencode: session.error without error")
		}
		ev.SessionError = &SessionErrorEvent{SessionID: p.SessionID, Error: p.Error.messageError()}
	case EventMessageUpdated:
		var p struct {
			SessionID string          `json:"sessionID"`
			Info      json.RawMessage `json:"info"`
		}
		if err := decodeProperties(w.Properties, &p); err != nil {
			return Event{}, err
		}
		info, err := decodeMessage(p.Info, p.SessionID)
		if err != nil {
			return Event{}, err
		}
		ev.MessageUpdated = &MessageUpdatedEvent{SessionID: info.SessionID, Info: info}
	case EventMessagePartUpdated:
		var p struct {
			SessionID string          `json:"sessionID"`
			Part      json.RawMessage `json:"part"`
		}
		if err := decodeProperties(w.Properties, &p); err != nil {
			return Event{}, err
		}
		part, err := decodePart(p.Part, p.SessionID)
		if err != nil {
			return Event{}, err
		}
		ev.MessagePartUpdated = &MessagePartUpdatedEvent{SessionID: part.SessionID, Part: part}
	}
	return ev, nil
}

// DecodeStatusSnapshot decodes GET /session/status: a map of session id to
// status. Idle sessions are simply absent from the map; interpretation
// (absence is meaningful only after a successful, correctly scoped read) is
// the reducer's business, not this decoder's.
func DecodeStatusSnapshot(raw []byte) (map[string]SessionStatus, error) {
	if len(raw) > MaxNativeMessageBytes || !jsonObject(bytes.TrimSpace(raw)) {
		return nil, fmt.Errorf("opencode: status snapshot is not a bounded object")
	}
	var wire map[string]*SessionStatus
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	result := make(map[string]SessionStatus, len(wire))
	for id, st := range wire {
		if id == "" || st == nil || st.Type == "" {
			return nil, fmt.Errorf("opencode: malformed status row")
		}
		result[id] = *st
	}
	return result, nil
}

// DecodeMessageRow reads a complete legacy history row without inventing live events.
func DecodeMessageRow(raw []byte) (Message, []Part, error) {
	if len(raw) > MaxNativeMessageBytes || !jsonObject(bytes.TrimSpace(raw)) {
		return Message{}, nil, fmt.Errorf("opencode: history row is not a bounded object")
	}
	var row struct {
		Info  json.RawMessage   `json:"info"`
		Parts []json.RawMessage `json:"parts"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return Message{}, nil, err
	}
	if row.Parts == nil {
		return Message{}, nil, fmt.Errorf("opencode: history row has no parts array")
	}
	m, err := decodeMessage(row.Info, "")
	if err != nil {
		return Message{}, nil, err
	}
	parts := make([]Part, 0, len(row.Parts))
	seen := make(map[string]bool, len(row.Parts))
	for _, rawPart := range row.Parts {
		p, err := decodePart(rawPart, m.SessionID)
		if err != nil {
			return Message{}, nil, err
		}
		if p.MessageID != m.ID || seen[p.ID] {
			return Message{}, nil, fmt.Errorf("opencode: history part identity mismatch")
		}
		seen[p.ID] = true
		parts = append(parts, p)
	}
	return m, parts, nil
}

func decodeMessage(raw json.RawMessage, outerSession string) (Message, error) {
	raw = bytes.TrimSpace(raw)
	if !jsonObject(raw) {
		return Message{}, fmt.Errorf("opencode: message has no info object")
	}
	var w wireMessage
	if err := json.Unmarshal(raw, &w); err != nil {
		return Message{}, err
	}
	sid, err := sessionScope(outerSession, w.SessionID)
	if err != nil {
		return Message{}, err
	}
	if w.ID == "" || len(w.ID) > 128 || len(w.ParentID) > 128 || len(w.Finish) > 64 || (w.Role != "user" && w.Role != "assistant") || w.Time.Created <= 0 || (w.Error != nil && (w.Error.Name == "" || len(w.Error.Name) > 128)) {
		return Message{}, fmt.Errorf("opencode: malformed message identity or lifecycle")
	}
	m := w.message()
	m.SessionID = sid
	m.Raw = append(json.RawMessage(nil), raw...)
	return m, nil
}

func decodePart(raw json.RawMessage, outerSession string) (Part, error) {
	raw = bytes.TrimSpace(raw)
	if !jsonObject(raw) {
		return Part{}, fmt.Errorf("opencode: event has no part object")
	}
	var w wirePart
	if err := json.Unmarshal(raw, &w); err != nil {
		return Part{}, err
	}
	sid, err := sessionScope(outerSession, w.SessionID)
	if err != nil {
		return Part{}, err
	}
	if w.ID == "" || len(w.ID) > 128 || w.MessageID == "" || len(w.MessageID) > 128 || w.Type == "" || len(w.Type) > 64 {
		return Part{}, fmt.Errorf("opencode: malformed part identity")
	}
	p := w.part()
	p.SessionID = sid
	p.Raw = append(json.RawMessage(nil), raw...)
	return p, nil
}

func sessionScope(outer, inner string) (string, error) {
	if len(outer) > 128 || len(inner) > 128 {
		return "", fmt.Errorf("opencode: oversized session identity")
	}
	if outer != "" && inner != "" && outer != inner {
		return "", fmt.Errorf("opencode: contradictory session identities")
	}
	if inner != "" {
		return inner, nil
	}
	return "", fmt.Errorf("opencode: missing session identity")
}

// wireEvent is the shared envelope.
type wireEvent struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type wireMessage struct {
	ID        string            `json:"id"`
	SessionID string            `json:"sessionID"`
	Role      string            `json:"role"`
	ParentID  string            `json:"parentID"`
	Time      MessageTime       `json:"time"`
	Finish    string            `json:"finish"`
	Error     *wireMessageError `json:"error"`
	Summary   json.RawMessage   `json:"summary"`
}

func (w wireMessage) message() Message {
	m := Message{
		ID:        w.ID,
		SessionID: w.SessionID,
		Role:      w.Role,
		ParentID:  w.ParentID,
		Time:      w.Time,
		Finish:    w.Finish,
		Summary:   append(json.RawMessage(nil), w.Summary...),
	}
	if w.Error != nil {
		e := w.Error.messageError()
		m.Error = &e
	}
	return m
}

type wirePart struct {
	ID        string     `json:"id"`
	SessionID string     `json:"sessionID"`
	MessageID string     `json:"messageID"`
	Type      string     `json:"type"`
	Text      string     `json:"text"`
	Synthetic bool       `json:"synthetic"`
	Ignored   bool       `json:"ignored"`
	CallID    string     `json:"callID"`
	Tool      string     `json:"tool"`
	State     *ToolState `json:"state"`
	Auto      bool       `json:"auto"`
}

func (w wirePart) part() Part {
	return Part{
		ID:        w.ID,
		SessionID: w.SessionID,
		MessageID: w.MessageID,
		Type:      w.Type,
		Text:      w.Text,
		Synthetic: w.Synthetic,
		Ignored:   w.Ignored,
		CallID:    w.CallID,
		Tool:      w.Tool,
		State:     w.State,
		Auto:      w.Auto,
	}
}

type wireMessageError struct {
	Name string `json:"name"`
	Data struct {
		Message     string `json:"message"`
		StatusCode  *int   `json:"statusCode"`
		IsRetryable *bool  `json:"isRetryable"`
	} `json:"data"`
}

func (w wireMessageError) messageError() MessageError {
	return MessageError{
		Name: w.Name,
		Data: MessageErrorData{
			Message:     w.Data.Message,
			StatusCode:  w.Data.StatusCode,
			IsRetryable: w.Data.IsRetryable,
		},
	}
}

// decodeProperties requires the envelope's properties to be a JSON object.
func decodeProperties(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if !jsonObject(raw) {
		return fmt.Errorf("opencode: event properties missing or not an object")
	}
	return json.Unmarshal(raw, v)
}

func jsonObject(raw []byte) bool {
	return len(raw) > 0 && raw[0] == '{'
}
