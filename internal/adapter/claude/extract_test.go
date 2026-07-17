package claude

// C3: the write-once conversation-id capture must never commit a PARTIAL id. A
// transcript read mid-write can end in the middle of the "Session <id>" line, so an
// id with no terminator after it is NOT accepted as complete — it is captured on a
// later read once the line is whole.

import "testing"

func TestExtractConversationID_RequiresTerminator(t *testing.T) {
	a := New()

	// Truncated at EOF (no whitespace/newline after the id): not yet complete.
	for _, truncated := range []string{"  Session abc12", "Session 3f2a1c9e-7b4d"} {
		if id, ok := a.ExtractConversationID(nil, []byte(truncated)); ok || id != "" {
			t.Errorf("ExtractConversationID(%q) = (%q,%v); want (\"\",false) — a mid-write id must not be committed partial", truncated, id, ok)
		}
	}

	// A complete, terminated line yields the whole id.
	for term, want := range map[string]string{
		"  Session abc123\n":   "abc123",
		"  Session abc123\r\n": "abc123",
		"  Session abc123 ":    "abc123",
	} {
		if id, ok := a.ExtractConversationID(nil, []byte(term)); !ok || id != want {
			t.Errorf("ExtractConversationID(%q) = (%q,%v); want (%q,true)", term, id, ok, want)
		}
	}
}
