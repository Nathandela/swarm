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
	tail := []byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"` + fixtureConversationID + `","turnId":"t-1"}}`)
	if id, ok := a.ExtractConversationID(nil, tail); !ok || id != fixtureConversationID {
		t.Fatalf("ExtractConversationID(threadId json) = (%q,%v); want (%q, true)", id, ok, fixtureConversationID)
	}
	if id, ok := a.ExtractConversationID(nil, []byte("session abc123")); ok {
		t.Errorf("ExtractConversationID read a legacy 'session' marker %q; the format is now JSON-RPC threadId", id)
	}
}

// TestExtractConversationID_RejectsTokensThatCouldPoisonWriteOnceIdentity ensures the PTY
// compatibility fallback cannot beat the authoritative app-server id with a prose decoy or an
// arbitrary token. A valid value is one canonical lowercase UUID in a complete JSON object.
func TestExtractConversationID_RejectsTokensThatCouldPoisonWriteOnceIdentity(t *testing.T) {
	a := New()
	for name, tail := range map[string]string{
		"arbitrary token":       `{"threadId":"tid-42"}` + "\n",
		"uppercase UUID":        `{"threadId":"A1B2C3D4-E5F6-7A8B-9C0D-1E2F3A4B5C6D"}` + "\n",
		"truncated quoted UUID": `{"threadId":"a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d`,
		"prose decoy":           `model said {"threadId":"a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"}` + "\n",
		"duplicate key":         `{"threadId":"a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d","threadId":"01a00339-a80e-72a0-966f-116427b6b9ce"}` + "\n",
		"conflicting frames":    `{"threadId":"a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"}` + "\n" + `{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce"}` + "\n",
		"trailing JSON":         `{"threadId":"a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"}{}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if id, ok := a.ExtractConversationID(nil, []byte(tail)); ok || id != "" {
				t.Fatalf("ExtractConversationID(%q) = (%q,%v); want (\"\",false)", tail, id, ok)
			}
		})
	}
}

func TestExtractConversationID_AcceptsRepeatedSameCanonicalID(t *testing.T) {
	tail := []byte(`{"threadId":"` + fixtureConversationID + `"}` + "\n" +
		`{"threadId":"` + fixtureConversationID + `"}` + "\n")
	if id, ok := New().ExtractConversationID(nil, tail); !ok || id != fixtureConversationID {
		t.Fatalf("repeated same thread id = (%q,%v), want (%q,true)", id, ok, fixtureConversationID)
	}
}
