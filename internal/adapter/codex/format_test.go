package codex

// FIX 6: the codex adapter's event descriptors + fixture use the REAL app-server
// JSON-RPC format (methods turn/started, turn/completed,
// item/commandExecution/requestApproval; a threadId conversation id), not the
// earlier invented {type, conversation_id} shape. The frozen suite keys off the
// MAPPED turn/interaction values (drift-resilient), so these lock the format itself.

import "testing"

func TestSignalSources_UseJSONRPCMethodNames(t *testing.T) {
	want := map[string]bool{
		"turn/started":                          false,
		"turn/completed":                        false,
		"item/commandExecution/requestApproval": false,
	}
	for _, s := range New().SignalSources() {
		if s.Kind == "event" {
			if _, ok := want[s.Descriptor["event"]]; ok {
				want[s.Descriptor["event"]] = true
			}
		}
	}
	for ev, seen := range want {
		if !seen {
			t.Errorf("codex SignalSources missing the app-server JSON-RPC method %q", ev)
		}
	}
}

// TestExtractConversationID_FromThreadIDJSON proves the id is read from a JSON-RPC
// threadId field in the transcript tail (the real app-server format), not from a
// "session <id>" terminal marker (the old invented format).
func TestExtractConversationID_FromThreadIDJSON(t *testing.T) {
	a := New()
	tail := []byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"tid-42","turnId":"t-1"}}`)
	if id, ok := a.ExtractConversationID(nil, tail); !ok || id != "tid-42" {
		t.Fatalf("ExtractConversationID(threadId json) = (%q,%v); want (\"tid-42\", true)", id, ok)
	}
	if id, ok := a.ExtractConversationID(nil, []byte("session abc123")); ok {
		t.Errorf("ExtractConversationID read a legacy 'session' marker %q; the format is now JSON-RPC threadId", id)
	}
}
