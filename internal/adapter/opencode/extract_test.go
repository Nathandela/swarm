package opencode

// R-E6 adversarial extraction tests: the LAST "ses_<alnum>" token, gated by a
// LEFT WORD BOUNDARY (the byte before "ses_" must be non-alphanumeric — else a
// scrolled snake_case identifier like "increases_..."/"phases_..." would
// false-positive), a minimum id length (>=10 alnum chars after the prefix, so a
// stray short "ses_" substring is not misread), and a terminator requirement
// after the token (C3: a token running to EOF is a transcript read mid-write and
// must not be committed partial).

import "testing"

// TestExtractConversationID_WordBoundary_RejectsProseToken: "increases_" carries
// the substring "ses_" starting mid-word (preceded by the alphanumeric 'a'), so
// the left word boundary must reject it even though a long, well-terminated
// alnum run follows.
func TestExtractConversationID_WordBoundary_RejectsProseToken(t *testing.T) {
	a := New()
	if id, ok := a.ExtractConversationID(nil, []byte("increases_abcdef12345 ")); ok {
		t.Errorf("ExtractConversationID matched the prose token %q; want no match (left word boundary)", id)
	}
}

// TestExtractConversationID_MinLength_RejectsShortToken: "ses_abc12345" carries
// only 8 alnum chars after the prefix, below the 10-char floor.
func TestExtractConversationID_MinLength_RejectsShortToken(t *testing.T) {
	a := New()
	if id, ok := a.ExtractConversationID(nil, []byte(" ses_abc12345 ")); ok {
		t.Errorf("ExtractConversationID matched a short token %q; want no match (< 10 alnum chars)", id)
	}
}

// TestExtractConversationID_LastOccurrenceWins: an earlier, validly-shaped
// child/subagent session id precedes the real exit-screen id; the LAST
// occurrence must win.
func TestExtractConversationID_LastOccurrenceWins(t *testing.T) {
	a := New()
	tail := []byte("child ses_1111111111 mid-transcript text opencode -s " + fixtureConversationID + "\x1b[0m\r\n")
	id, ok := a.ExtractConversationID(nil, tail)
	if !ok || id != fixtureConversationID {
		t.Fatalf("ExtractConversationID(tail) = (%q, %v); want (%q, true) — the LAST occurrence wins", id, ok, fixtureConversationID)
	}
}

// TestExtractConversationID_TruncatedAtEOF_Rejected: a well-formed, long-enough
// token with no terminator byte after it. The capture may have been read
// mid-write, so it must not be committed partial (C3).
func TestExtractConversationID_TruncatedAtEOF_Rejected(t *testing.T) {
	a := New()
	if id, ok := a.ExtractConversationID(nil, []byte("opencode -s "+fixtureConversationID)); ok {
		t.Errorf("ExtractConversationID matched an EOF-truncated token %q; want no match (C3)", id)
	}
}

// TestExtractConversationID_ControlByteTerminator: \x1b[0m butted directly
// against the id with no whitespace — verified real in the committed fixture
// (Phase B, raw offset 76243).
func TestExtractConversationID_ControlByteTerminator(t *testing.T) {
	a := New()
	tail := []byte("opencode -s " + fixtureConversationID + "\x1b[0m\r\n")
	id, ok := a.ExtractConversationID(nil, tail)
	if !ok || id != fixtureConversationID {
		t.Fatalf("ExtractConversationID(tail) = (%q, %v); want (%q, true)", id, ok, fixtureConversationID)
	}
}
