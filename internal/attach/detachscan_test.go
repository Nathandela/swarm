package attach

import (
	"bytes"
	"testing"
)

// ADR-019 — detach recognition becomes boundary-aware, superseding D4's solo-read
// test (ADR-006 / agents-tracker-rs8). Field evidence: agent CLIs enable mouse
// tracking (CSI ?1003h, every pointer MOVEMENT reported) and focus reporting
// (CSI ?1004h), and the attach passes both straight through to the client's
// terminal, so the terminal streams reports into swarm's stdin for the whole
// attach. Under the solo-read test a report landing in the same read as Ctrl+q
// swallowed the detach — measured at 6% of presses with a pointer moving at 40
// reports/s over a busy session, ~33% while the pointer moves quickly, and 100%
// when a focus event immediately precedes the key.
//
// The rule now: the detach key counts wherever it lands in a read, PROVIDED the
// parser is at a GROUND boundary (never inside an escape sequence the terminal
// sent) and not inside a bracketed paste (where the byte is pasted data).

// A mouse report, a focus event, or an ordinary keystroke ahead of the detach key
// in ONE read must still detach.
func TestDetachKey_RecognizedBehindTerminalReports(t *testing.T) {
	for name, prefix := range map[string][]byte{
		"sgr mouse motion": []byte("\x1b[<35;10;5M"),
		"sgr mouse press":  []byte("\x1b[<0;12;7M"),
		"focus in":         []byte("\x1b[I"),
		"focus out":        []byte("\x1b[O"),
		"arrow key":        []byte("\x1b[B"),
		"typed text":       []byte("hello"),
		"repeat press":     []byte{DefaultDetachKey},
	} {
		t.Run(name, func(t *testing.T) {
			term := newFakeTerm(80, 24)
			sess := newFakeSession([]byte("S"))
			ch := runInBackground(Config{Term: term, Session: sess})

			term.feed(append(append([]byte(nil), prefix...), DefaultDetachKey))

			res := waitResult(t, ch)
			if res.reason != ReasonDetached {
				t.Fatalf("reason = %v, want ReasonDetached (detach key behind %q)", res.reason, prefix)
			}
		})
	}
}

// The bytes AHEAD of the detach key are real input and are still forwarded; the
// detach key itself never is.
func TestDetachKey_PrecedingBytesForwardedKeyIsNot(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	term.feed([]byte{'a', 'b', DefaultDetachKey})

	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
	if got := sess.inputBytes(); !bytes.Equal(got, []byte("ab")) {
		t.Fatalf("forwarded input = %q, want %q (preceding bytes forwarded, detach key withheld)", got, "ab")
	}
}

// Inside a bracketed paste the byte is pasted DATA, not a keypress: it is forwarded
// and does not detach. The paste markers may straddle reads.
func TestDetachKey_InsideBracketedPasteIsData(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	pasted := []byte("\x1b[200~x")
	pasted = append(pasted, DefaultDetachKey)
	// split the closing marker across two reads to prove the state carries
	term.feed(append(pasted, []byte("y\x1b[20")...))
	term.feed([]byte("1~"))

	eventually(t, func() bool { return bytes.Contains(sess.inputBytes(), []byte{DefaultDetachKey}) })
	if sess.detachCalls != 0 {
		t.Fatalf("a detach byte inside a bracketed paste must not detach; Detach called %d times", sess.detachCalls)
	}

	// after the paste closes, the key detaches again
	term.feed([]byte{DefaultDetachKey})
	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached once the paste has closed", res.reason)
	}
}

// A byte that merely LOOKS like the key but sits inside an escape sequence the
// terminal sent is not a keypress. (0x11 cannot appear in a well-formed sequence,
// so this pins the GROUND gate itself rather than a realistic sequence.)
func TestDetachKey_InsideEscapeSequenceIsNotAKeypress(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	// DCS ... ST carrying the byte in its payload.
	term.feed(append([]byte("\x1bP1$r"), DefaultDetachKey))
	eventually(t, func() bool { return bytes.Contains(sess.inputBytes(), []byte{DefaultDetachKey}) })
	if sess.detachCalls != 0 {
		t.Fatalf("a detach byte inside a DCS string must not detach; Detach called %d times", sess.detachCalls)
	}

	term.feed([]byte("\x1b\\"))          // ST closes the string
	term.feed([]byte{DefaultDetachKey}) // now at GROUND: a real keypress
	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached after the string terminator", res.reason)
	}
}

// Read-only attaches (completed/lost sessions, G3) forward nothing, but the detach
// key still returns to the board even when other bytes share its read.
func TestDetachKey_ReadOnlyStillDetachesBehindReports(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess, ReadOnly: true})

	term.feed([]byte("\x1b[<35;9;9M\x11"))

	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
	if got := sess.inputBytes(); len(got) != 0 {
		t.Fatalf("read-only attach forwarded %q, want nothing (G3)", got)
	}
}

// A terminal report split across two reads must not swallow the keypress that lands
// in the second read: CSI bytes are 0x20-0x7e, so a C0 byte arriving while one is
// open is a press the user made DURING the report (ADR-019).
func TestDetachKey_RecognizedAfterSplitTerminalReport(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	term.feed([]byte("\x1b[<35;12")) // first half of an SGR motion report
	term.feed([]byte{DefaultDetachKey})

	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached (key behind a split report)", res.reason)
	}
}
