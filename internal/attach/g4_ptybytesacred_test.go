package attach

// FAILING-FIRST (TDD RED, GG-5) for Wave G item G.4:
// docs/specifications/chat-surface-plan.md §9, "Nothing is written into the terminal's own
// output ... A test proves the attribution chrome writes zero bytes into the PTY". Bead:
// agents-tracker-tbpm.9. Evidence: docs/verification/chat-surface.md, Wave G.
//
// THE RULE AND ITS TWO HARMS. ADR-017 T10 -- "The PTY is sacred ... the shim-owned PTY hosts
// the vendor's real TUI, byte-exact, always" (ADR-013 decision 1) -- and the plan names
// what breaking it costs on each side of the wire: writing attribution into the INPUT
// direction changes the prompt the agent receives, and writing display bytes into the OUTPUT
// direction corrupts the CLI's own screen. Wave G's attribution -- the board's `phone` marker
// and the reserved row this file's chrome paints -- must therefore stay entirely on the
// OWNER'S OWN TERMINAL and reach the session never.
//
// THIS IS THE HALF THAT CAN BE PROVED HERE. attach.Session is the whole of what the
// passthrough may do to a session (Snapshot/Frames/Input/Resize/Detach/Generation), and
// fakeSession records every byte handed to Input -- so "the chrome wrote nothing into the
// PTY" is an OBSERVATION on the one seam that could carry it, not an inference from reading
// the painter. The other half -- that the phone's message reaches the agent byte-exact, with
// no attribution prefixed to it -- is proved end to end in
// internal/skeleton/g4_ptybytesacred_test.go, on the path that actually writes.
//
// WHY THE CHROME IS THE RIGHT SUBJECT. It is the only thing in this package that paints on
// the owner's behalf rather than relaying the session, it re-asserts itself on damage and on
// resize (so it writes MANY times over one attach, not once), and it is where Wave G deleted
// a phone claim that had been rendered here for real.

import (
	"bytes"
	"testing"
)

// chromeMarks are the reserved row's own bytes: the scroll region it sets, the cursor
// save/restore that brackets it, the pen it paints in and the words it paints. Not one of
// them may appear on the wire toward the session.
var chromeMarks = [][]byte{
	[]byte("\x1b[1;23r"),       // DECSTBM: the region the chrome reserves
	[]byte("\x1b7"),            // DECSC
	[]byte("\x1b8"),            // DECRC
	[]byte("\x1b[2m"),          // the faint pen
	[]byte("returns to swarm"), // the escape affordance
	[]byte("a-session-name"),   // the session label the chrome carries
	[]byte("phone"),            // the board's attribution word (internal/tui, phoneSentMarker)
}

// TestG4_TheAttachChromeWritesZeroBytesTowardTheSession is the claim at its strongest: over a
// whole attach in which the chrome paints, is damaged, re-asserts and is recomputed for a new
// size, the session's input receives NOTHING AT ALL.
func TestG4_TheAttachChromeWritesZeroBytesTowardTheSession(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "a-session-name"})

	// 1. The chrome paints.
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("returns to swarm")) })

	// 2. The agent clears the screen, which is a damage signature: the chrome re-asserts
	//    itself immediately rather than waiting for its throttle.
	sess.pushFrame([]byte("\x1b[2J"))
	eventually(t, func() bool {
		return bytes.Count(term.outBytes(), []byte("returns to swarm")) >= 2
	})

	// 3. A resize recomputes the region and repaints the row on the new geometry.
	term.setSize(100, 30)
	eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("\x1b[1;29r")) })

	sess.endSession()
	_ = waitResult(t, ch)

	if got := sess.inputBytes(); len(got) != 0 {
		t.Fatalf("the attach wrote %q toward the session while nobody typed a key.\n"+
			"THE CHROME IS THE OWNER'S, NOT THE AGENT'S. Bytes on this seam are bytes the CLI "+
			"reads as input: a reserved row that leaks into it changes the prompt the agent "+
			"receives, and ADR-017 T10 keeps that PTY byte-exact.", got)
	}
}

// TestG4_TheChromePaintsOnTheOwnersTerminalAndNotIntoTheSession is the same claim stated so it
// cannot be satisfied by an attach that simply never painted: every mark the chrome makes is
// found on the OWNER'S terminal, and none of them is on the wire toward the session, while the
// owner's own keystrokes cross that wire byte-exact.
func TestG4_TheChromePaintsOnTheOwnersTerminalAndNotIntoTheSession(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "a-session-name"})

	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("returns to swarm")) })

	const typed = "the owner's own keystrokes"
	term.feed([]byte(typed))
	eventually(t, func() bool { return bytes.Contains(sess.inputBytes(), []byte(typed)) })

	sess.endSession()
	_ = waitResult(t, ch)

	out, in := term.outBytes(), sess.inputBytes()
	for _, mark := range chromeMarks {
		if bytes.Contains(in, mark) {
			t.Errorf("the session's input carried the chrome's %q. The whole of the session's "+
				"input was %q, and every one of those bytes is a byte the agent reads as typing",
				mark, in)
		}
	}
	// The chrome really did paint -- otherwise the loop above proves only that nothing
	// happened at all. The label and the affordance are the two marks that are the chrome
	// rather than a bare control sequence.
	for _, mark := range [][]byte{[]byte("returns to swarm"), []byte("a-session-name"), []byte("\x1b[1;23r")} {
		if !bytes.Contains(out, mark) {
			t.Fatalf("the owner's terminal never received the chrome's %q, so this test's "+
				"subject was never on screen; out=%q", mark, out)
		}
	}
	if !bytes.Equal(in, []byte(typed)) {
		t.Fatalf("the session's input was %q, want exactly the owner's %q: the passthrough owes "+
			"the agent the keystrokes and nothing else", in, typed)
	}
}
