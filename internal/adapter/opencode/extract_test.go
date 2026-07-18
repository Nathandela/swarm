package opencode

// R-E6 adversarial extraction tests (amended post-committee, R-H4 finding 1):
// the id following the LAST "opencode -s " exit-command marker occurrence,
// requiring idPrefix immediately after the marker, a minimum id length (>=10
// alnum chars after the prefix, so a stray short "ses_" substring is not
// misread), and a terminator requirement after the token (C3: a token running
// to EOF is a transcript read mid-write and must not be committed partial). A
// standalone "ses_..." token anywhere else in the transcript — e.g. a
// scrolled snake_case identifier or a prose mention — is never matched,
// because it is not anchored to the marker (see the new
// TestExtractConversationID_ProseToken* tests below for the adversarial
// cases the anchor closes).

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

// R-H4 committee finding: a standalone "ses_..." token anywhere in the
// transcript is NOT the conversation id — only the token immediately after
// the exit-command marker "opencode -s " is (mirrors agy's
// "agy --conversation=" anchor design). The daemon scans the live transcript
// every 200ms and persists write-once first-wins, so a mid-session prose
// mention of a ses_-shaped token must never be captured.

// TestExtractConversationID_ProseTokenAlone_NotExtracted: a standalone,
// well-formed ses_ token with no exit-command marker anywhere in the capture
// must not be extracted.
func TestExtractConversationID_ProseTokenAlone_NotExtracted(t *testing.T) {
	a := New()
	tail := []byte("please inspect ses_abcdefghij0 for me\n")
	if id, ok := a.ExtractConversationID(nil, tail); ok {
		t.Errorf("ExtractConversationID(tail) = (%q, true); want no match — no exit-command marker precedes the token", id)
	}
}

// TestExtractConversationID_ProseTokenThenExitLine_ExitLineWins: a prose
// ses_ token precedes the real exit line; the marker-anchored id must win.
func TestExtractConversationID_ProseTokenThenExitLine_ExitLineWins(t *testing.T) {
	a := New()
	tail := []byte("please inspect ses_abcdefghij0 for me\nmore transcript\nopencode -s " + fixtureConversationID + "\x1b[0m\r\n")
	id, ok := a.ExtractConversationID(nil, tail)
	if !ok || id != fixtureConversationID {
		t.Fatalf("ExtractConversationID(tail) = (%q, %v); want (%q, true) — the exit-line marker anchors extraction", id, ok, fixtureConversationID)
	}
}

// TestExtractConversationID_ExitLineThenLaterProseToken_ExitLineStillWins: an
// exit line is followed by a LATER, well-formed prose ses_ token; the
// exit-line id must still win because the anchor is the last MARKER
// occurrence, not the last ses_ token overall.
func TestExtractConversationID_ExitLineThenLaterProseToken_ExitLineStillWins(t *testing.T) {
	a := New()
	tail := []byte("opencode -s " + fixtureConversationID + "\x1b[0m\r\nsome later chatter mentions ses_prosetoken99 here\n")
	id, ok := a.ExtractConversationID(nil, tail)
	if !ok || id != fixtureConversationID {
		t.Fatalf("ExtractConversationID(tail) = (%q, %v); want (%q, true) — the anchor is the LAST marker occurrence, not the last ses_ token", id, ok, fixtureConversationID)
	}
}
