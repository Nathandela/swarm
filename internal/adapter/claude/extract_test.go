package claude

// C3: the write-once conversation-id capture must never commit a PARTIAL id. A
// transcript read mid-write can end in the middle of the "Session <id>" line, so an
// id with no terminator after it is NOT accepted as complete — it is captured on a
// later read once the line is whole.

import "testing"

func TestExtractConversationID_RequiresTerminator(t *testing.T) {
	a := New()

	// Truncated at EOF (no whitespace/newline after the id): not yet complete.
	for _, truncated := range []string{"  Session 3f2a1c9e-7b4d", "Session 3f2a1c9e-7b4d-4e2a-9f10-abc123def45"} {
		if id, ok := a.ExtractConversationID(nil, []byte(truncated)); ok || id != "" {
			t.Errorf("ExtractConversationID(%q) = (%q,%v); want (\"\",false) — a mid-write id must not be committed partial", truncated, id, ok)
		}
	}

	// A complete, terminated line yields the whole id.
	for term, want := range map[string]string{
		"  Session " + fixtureConversationID + "\n":   fixtureConversationID,
		"  Session " + fixtureConversationID + "\r\n": fixtureConversationID,
		"  Session " + fixtureConversationID + " ":    fixtureConversationID,
	} {
		if id, ok := a.ExtractConversationID(nil, []byte(term)); !ok || id != want {
			t.Errorf("ExtractConversationID(%q) = (%q,%v); want (%q,true)", term, id, ok, want)
		}
	}
}

// TestExtractConversationID_RejectsTokensThatCouldPoisonWriteOnceIdentity pins the terminal
// fallback to the same canonical UUID contract as the authenticated hook. Merely finding the
// word Session in prose is not provider identity evidence.
func TestExtractConversationID_RejectsTokensThatCouldPoisonWriteOnceIdentity(t *testing.T) {
	a := New()
	for name, tail := range map[string]string{
		"terminated arbitrary token":  "  Session abc123\n",
		"uppercase UUID":              "  Session 3F2A1C9E-7B4D-4E2A-9F10-ABC123DEF456\n",
		"prose decoy":                 "please mention Session " + fixtureConversationID + " in the answer\n",
		"conflicting session markers": "  Session " + fixtureConversationID + "\n  Session 1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a\n",
	} {
		t.Run(name, func(t *testing.T) {
			if id, ok := a.ExtractConversationID(nil, []byte(tail)); ok || id != "" {
				t.Fatalf("ExtractConversationID(%q) = (%q,%v); want (\"\",false)", tail, id, ok)
			}
		})
	}
}

func TestExtractConversationID_AcceptsRepeatedSameCanonicalID(t *testing.T) {
	tail := []byte("  Session " + fixtureConversationID + "\n  Session " + fixtureConversationID + "\n")
	if id, ok := New().ExtractConversationID(nil, tail); !ok || id != fixtureConversationID {
		t.Fatalf("repeated same session id = (%q,%v), want (%q,true)", id, ok, fixtureConversationID)
	}
}
