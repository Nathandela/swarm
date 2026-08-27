package codex

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/adapter"
)

const (
	threadNameSetMethod     = "thread/name/set"
	threadNameUpdatedMethod = "thread/name/updated"
	maxNameEventBytes       = 1 << 20
)

// SetSessionName describes Codex's app-server naming RPC. The assembly sends the
// request over the session's existing backend connection.
func (codexAdapter) SetSessionName(threadID, name string) (adapter.SessionNameRequest, bool) {
	if !adapter.IsCanonicalConversationID(threadID) {
		return adapter.SessionNameRequest{}, false
	}
	return adapter.SessionNameRequest{
		Method: threadNameSetMethod,
		Params: map[string]string{"threadId": threadID, "name": name},
	}, true
}

// SessionNameFromEvent reads Codex's app-server rename notification. A null
// threadName is treated as clearing the native label, matching Swarm's empty-name
// fallback semantics.
func (codexAdapter) SessionNameFromEvent(p adapter.HookPayload) (string, string, bool) {
	if p.Event != threadNameUpdatedMethod || len(p.Raw) == 0 || len(p.Raw) > maxNameEventBytes {
		return "", "", false
	}
	var frame struct {
		Method string `json:"method"`
		Params struct {
			ThreadID   string          `json:"threadId"`
			ThreadName json.RawMessage `json:"threadName"`
		} `json:"params"`
	}
	if json.Unmarshal(p.Raw, &frame) != nil || frame.Method != threadNameUpdatedMethod ||
		!adapter.IsCanonicalConversationID(frame.Params.ThreadID) || len(frame.Params.ThreadName) == 0 {
		return "", "", false
	}
	if string(frame.Params.ThreadName) == "null" {
		return frame.Params.ThreadID, "", true
	}
	var name string
	if json.Unmarshal(frame.Params.ThreadName, &name) != nil {
		return "", "", false
	}
	return frame.Params.ThreadID, name, true
}
