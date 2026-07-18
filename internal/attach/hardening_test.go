package attach

// v0.3.0 reserved-row hardening (three-reviewer v0.3 audit). FAILING-FIRST tests,
// written against the FROZEN public Run/Config API — no new symbols, so they compile
// against the pre-hardening loop and fail on BEHAVIOR:
//
//   - item 1: re-assert bytes are injected ONLY at a safe boundary (the output
//     parser in GROUND), never mid escape sequence — proven across split CSI and
//     split OSC frames, and that a GROUND-ending frame still injects.
//   - item 2: a trailing heal timer re-asserts the row after a damaging-but-quiet
//     burst, with no further frames.
//   - item 3: chromeHint neutralizes origin mode (DECOM) so the reserved-row CUP is
//     absolute; the byte order is pinned.
//   - item 4: a Chrome:true run on a rows<=2 terminal is byte-identical to
//     Chrome:false through resize AND detach (no stray region reset / bottom clear).
//   - item 5: widened damage signatures (ED0, ?47, ?1047) re-assert immediately.
//   - item 8: a panic with chrome engaged best-effort resets the region.

import (
	"bytes"
	"testing"
)

// item 1 — a frame ending MID CSI must NOT have re-assert bytes injected after it:
// the pump defers to the next safe boundary, so the agent's split SGR
// ("\x1b[3" + "8;5;42m") stays contiguous on the wire.
func TestPassthrough_SplitCSIDefersInjection(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) == 1 })

	// Frame 1 carries a damage signature (ED2) — injection pressure — but ENDS mid
	// CSI ("\x1b[3"), an incomplete SGR. Frame 2 completes it ("8;5;42m").
	sess.pushFrame([]byte("\x1b[2Jx\x1b[3"))
	sess.pushFrame([]byte("8;5;42mtail"))

	out := waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) >= 2 })
	if !bytes.Contains(out, []byte("\x1b[38;5;42m")) {
		t.Fatalf("re-assert must not split the agent's CSI across the frame boundary; the SGR \\x1b[38;5;42m must stay contiguous. out=%q", out)
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// item 1 — the same safe-boundary rule for a string (OSC) sequence split across
// frames: "\x1b]0;my titl" + "e\x07" must stay contiguous, no injection between.
func TestPassthrough_SplitOSCDefersInjection(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) == 1 })

	sess.pushFrame([]byte("\x1b[2Jx\x1b]0;my titl")) // damage + start of an OSC (no terminator yet)
	sess.pushFrame([]byte("e\x07done"))              // completes the OSC with BEL

	out := waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) >= 2 })
	if !bytes.Contains(out, []byte("\x1b]0;my title\x07")) {
		t.Fatalf("re-assert must not split the agent's OSC across the frame boundary; the OSC must stay contiguous. out=%q", out)
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// item 1 — a GROUND-ending damage frame injects normally (the deferral is only for
// unsafe boundaries).
func TestPassthrough_GroundEndingFrameInjectsNormally(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) == 1 })
	sess.pushFrame([]byte("\x1b[2Jclean")) // ED2 damage, ends in GROUND
	out := waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) >= 2 })
	if bytes.Count(out, []byte("\x1b[1;23r")) < 2 {
		t.Fatalf("a ground-ending damage frame must inject immediately; regions=%d", bytes.Count(out, []byte("\x1b[1;23r")))
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// item 2 — after a damaging-but-quiet frame (no damage signature, within the
// throttle window, ends in ground) the trailing heal timer re-asserts the row WITH
// NO further frames. A bare absolute CUP (999;1H) stomps the bottom row but carries
// no damage signature, so only the timer can heal it.
func TestPassthrough_TrailingHealTimerReasserts(t *testing.T) {
	if testing.Short() {
		t.Skip("trailing heal crosses the ~300ms heal interval")
	}
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) == 1 })
	sess.pushFrame([]byte("\x1b[999;1Hstomp")) // damages the bottom row; no damage signature, ends ground
	// No further frames: only the trailing heal timer can re-assert the row.
	out := waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) >= 2 })
	if bytes.Count(out, []byte("\x1b[1;23r")) < 2 {
		t.Fatalf("the trailing heal timer must re-assert the row after a quiet damaging burst; regions=%d", bytes.Count(out, []byte("\x1b[1;23r")))
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// item 3 — chromeHint neutralizes origin mode so the reserved-row CUP is absolute
// even when the agent enabled DECOM. The byte order is pinned: DECSC, DECOM-off,
// region, CUP.
func TestPassthrough_ChromeNeutralizesOriginMode(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	out := waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("returns to swarm")) })
	// DECSC (\x1b7), then DECOM off (\x1b[?6l), then the region (\x1b[1;23r), then the
	// absolute CUP (\x1b[24;1H) — contiguous, in this order.
	want := []byte("\x1b7\x1b[?6l\x1b[1;23r\x1b[24;1H")
	if !bytes.Contains(out, want) {
		t.Fatalf("chromeHint must emit DECSC, DECOM-off, region, CUP in order; want %q in\n%q", want, out)
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// item 4 — a Chrome:true run on a rows<=2 terminal (too small to reserve a row) must
// be BYTE-IDENTICAL to Chrome:false through resize AND detach: no stray region reset,
// no bottom-row clear. Chrome that never ENGAGED must not tear anything down.
func TestPassthrough_SmallTerminalByteIdenticalToChromeOff(t *testing.T) {
	run := func(chrome bool) []byte {
		term := newFakeTerm(80, 2)
		sess := newFakeSession(mustSnap(t, "GRID"))
		ch := runInBackground(Config{Term: term, Session: sess, Chrome: chrome, Name: "claude"})
		waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("GRID")) })
		term.setSize(80, 2) // a resize that stays below the reserve threshold
		eventually(t, func() bool { return len(sess.resizeCalls()) >= 2 })
		term.feed([]byte{DefaultDetachKey})
		_ = waitResult(t, ch)
		return term.outBytes()
	}
	withChrome := run(true)
	without := run(false)
	if !bytes.Equal(withChrome, without) {
		t.Fatalf("Chrome:true on a rows<=2 terminal must be byte-identical to Chrome:false through resize+detach;\n chrome=%q\n plain =%q", withChrome, without)
	}
}

// item 5 — widened damage signatures re-assert immediately (bypassing the throttle):
// ED0 (\x1b[J and \x1b[0J), and the ?47 / ?1047 alt-screen swaps.
func TestPassthrough_WidenedDamageSignaturesReassert(t *testing.T) {
	for _, dmg := range [][]byte{
		[]byte("\x1b[Jredraw"),    // ED0: clear to end of screen (default param)
		[]byte("\x1b[0Jredraw"),   // ED0 explicit
		[]byte("\x1b[?47hredraw"), // legacy alt-screen swap
		[]byte("\x1b[?1047hdraw"), // alt-screen swap (no cursor save)
	} {
		term := newFakeTerm(80, 24)
		sess := newFakeSession(mustSnap(t, "GRID"))
		ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

		waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) == 1 })
		sess.pushFrame(dmg)
		out := waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) >= 2 })
		if bytes.Count(out, []byte("\x1b[1;23r")) < 2 {
			t.Fatalf("damage signature %q must re-assert immediately; regions=%d", dmg, bytes.Count(out, []byte("\x1b[1;23r")))
		}

		sess.endSession()
		_ = waitResult(t, ch)
	}
}

// item 8 — a panic with chrome engaged best-effort resets the scroll region (\x1b[r)
// so the agent is not left confined, before the terminal is restored.
func TestPassthrough_PanicWithChromeResetsRegion(t *testing.T) {
	term := newFakeTerm(80, 24)
	term.panicSentinel = []byte("BOOM")
	sess := newFakeSession(mustSnap(t, "SNAP"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	// Wait until chrome is engaged (region set) so the panic happens AFTER engage.
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("\x1b[1;23r")) })
	sess.pushFrame([]byte("BOOM")) // the render write for this frame panics

	res := waitResult(t, ch)
	if res.reason != ReasonError {
		t.Fatalf("a mid-frame panic must surface as ReasonError; got %v", res.reason)
	}
	if _, restore, _ := term.counts(); restore == 0 {
		t.Fatal("termios must be restored on panic (E8.2)")
	}
	// The chrome establishment writes "\x1b[1;23r" (never a bare "\x1b[r"); the only
	// bare region reset comes from the best-effort panic cleanup.
	if n := bytes.Count(term.outBytes(), []byte("\x1b[r")); n < 1 {
		t.Fatalf("panic with chrome engaged must best-effort reset the region; out=%q", term.outBytes())
	}
}
