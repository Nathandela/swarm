package attach

// Failing-first suite for R4.2.2 (agents-tracker-rs8): detach teardown restores
// the primary screen buffer when the attach snapshot recorded the emulator in
// the alternate screen. The client never parses the live passthrough stream, so
// it cannot know whether the agent's own output already exited alt before
// teardown runs; teardown re-asserts exit-alt + cursor-visible + SGR-reset
// whenever the SNAPSHOT was alt, an idempotent sequence on a terminal already
// back in the main buffer. Out of scope: ATTACH-time SGR pen/DEC continuity
// (agents-tracker-97u) — this suite is teardown only.

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/vt"
)

// mustAltSnap builds a real vt snapshot captured in the alternate screen, so a
// test can drive the alt-tracking half of R4.2.2 without a live PTY.
func mustAltSnap(t *testing.T, text string) []byte {
	t.Helper()
	e := vt.NewEmulator(80, 24)
	defer func() { _ = e.Close() }()
	e.Feed([]byte("\x1b[?1049h" + text))
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return b
}

// teardownSeq is the exact restore sequence emitted when the snapshot was alt:
// exit alt-screen, show the cursor, reset SGR — an order that is idempotent on
// a terminal already in the main buffer (exit-alt is a no-op there; cursor-show
// and SGR-reset are always safe).
const teardownSeq = "\x1b[?1049l\x1b[?25h\x1b[0m"

// (a) — an alt-screen snapshot: an explicit detach emits the restore sequence.
func TestDetachTeardown_AltSnapshotEmitsRestoreSequence(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustAltSnap(t, "ALT-GRID"))
	ch := runInBackground(Config{Term: term, Session: sess})

	eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("ALT-GRID")) })
	term.feed([]byte{DefaultDetachKey})

	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
	if !bytes.Contains(term.outBytes(), []byte(teardownSeq)) {
		t.Fatalf("an alt-screen snapshot must emit the exit-alt+cursor-visible+SGR-reset teardown sequence on detach; got %q", term.outBytes())
	}
}

// (b) — a NOT-alt snapshot: detach must not emit exit-alt (never disturb a
// main-buffer session it never entered alt on).
func TestDetachTeardown_NonAltSnapshotEmitsNoExitAlt(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "MAIN-GRID")) // mustSnap (render_wire_test.go) never enters alt
	ch := runInBackground(Config{Term: term, Session: sess})

	eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("MAIN-GRID")) })
	term.feed([]byte{DefaultDetachKey})

	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
	if bytes.Contains(term.outBytes(), []byte("\x1b[?1049l")) {
		t.Fatalf("a non-alt snapshot must not emit exit-alt on detach; got %q", term.outBytes())
	}
}

// (d) — combined alt-screen snapshot + chrome (deployment-committee, codex HIGH
// via agents-tracker-rs8): the chrome bottom-row cleanup must be emitted BEFORE the
// alt-exit. Run before the alt-exit, chromeCleanup's bottom-row clear lands on the
// abandoned alt buffer (harmless); run after it (the old order) it lands on the
// freshly restored PRIMARY buffer and erases the user's shell line. The DECSTBM
// reset chromeCleanup carries is global state, effective from either buffer.
func TestDetachTeardown_ChromeCleanupPrecedesAltExit(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustAltSnap(t, "ALT-GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	// Wait until the reserved-row region is established over the alt snapshot.
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("\x1b[1;23r")) })
	term.feed([]byte{DefaultDetachKey})

	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}

	out := term.outBytes()
	chromeReset := bytes.LastIndex(out, []byte("\x1b[r")) // chrome cleanup DECSTBM reset (distinct from the 1;23r set)
	altExit := bytes.Index(out, []byte("\x1b[?1049l"))    // teardownAlt exit-alt
	if chromeReset < 0 {
		t.Fatalf("chrome+alt detach must emit the scroll-region reset; got %q", out)
	}
	if altExit < 0 {
		t.Fatalf("an alt snapshot must emit exit-alt on detach; got %q", out)
	}
	if chromeReset > altExit {
		t.Fatalf("chrome cleanup must land on the abandoned alt buffer BEFORE the alt-exit, "+
			"else its bottom-row clear erases the restored primary buffer; chromeReset=%d altExit=%d out=%q",
			chromeReset, altExit, out)
	}
}

// (c) — the alt flag is tracked from the snapshot alone (the client never
// parses the live stream), so the restore sequence is emitted on EVERY teardown
// path, not just an explicit key-press detach: here the live stream closes
// (session end) while the snapshot was alt.
func TestDetachTeardown_AltSnapshotEmitsRestoreSequenceOnSessionEnd(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustAltSnap(t, "ALT-GRID"))
	ch := runInBackground(Config{Term: term, Session: sess})

	eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("ALT-GRID")) })
	sess.endSession()

	res := waitResult(t, ch)
	if res.reason != ReasonSessionEnd {
		t.Fatalf("reason = %v, want ReasonSessionEnd", res.reason)
	}
	if !bytes.Contains(term.outBytes(), []byte(teardownSeq)) {
		t.Fatalf("an alt-screen snapshot must emit the restore sequence on session-end teardown too; got %q", term.outBytes())
	}
}
