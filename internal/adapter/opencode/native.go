// The legacy OpenCode HTTP/SSE wire shapes (the P3/P4 authoritative surface):
// envelope {id,type,properties} events plus the /session/status snapshot map.
// This file is pure decode -- no state, no I/O, no heuristics. Unknown event
// kinds decode to fact-less events with no error (forward compatibility is the
// reducer's decision); malformed payloads for known kinds are errors. The v2
// event family is not decoded here.
package opencode

import "encoding/json"

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
	Next    int          `json:"next"`
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
	// Summary is the raw `summary` value: an OBJECT on user messages that carry
	// conversation-summary context (compaction artifacts) and a BOOLEAN on
	// assistant messages (a continuation summary). It is kept raw because the
	// two roles mean different things by it.
	Summary json.RawMessage
	// Raw is the complete native `info` object as received.
	Raw json.RawMessage
}

// IsAssistantSummary reports the assistant-side compaction marker
// (`summary: true`).
func (m Message) IsAssistantSummary() bool {
	return m.Role == "assistant" && string(m.Summary) == "true"
}

// HasUserSummary reports whether the user message carries the summary OBJECT,
// which marks it as compaction context rather than a requested input.
func (m Message) HasUserSummary() bool {
	return m.Role == "user" && len(m.Summary) > 0 && m.Summary[0] == '{'
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
func DecodeEvent(raw []byte) (Event, error) {
	return Event{}, nil
}

// DecodeStatusSnapshot decodes GET /session/status: a map of session id to
// status. Idle sessions are simply absent from the map; interpretation
// (absence is meaningful only after a successful, correctly scoped read) is
// the reducer's business, not this decoder's.
func DecodeStatusSnapshot(raw []byte) (map[string]SessionStatus, error) {
	return nil, nil
}
