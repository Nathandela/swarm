package attach

// v0.3 reserved bottom hint row (bead: "v0.3: attached-view reserved bottom hint
// row"). FAILING-FIRST tests, written against the FROZEN public Run/Config API — no
// new symbols, so they compile against the pre-implementation loop and fail on
// behavior. The design (ADR-006 amendment):
//
//   - Chrome-on attach reserves the REAL terminal's bottom row: the session PTY is
//     resized to (cols, rows-1), a DECSTBM scroll region of rows 1..rows-1 is set,
//     and a single faint/dim hint (session name + "ctrl+q returns to swarm") is
//     painted on the real bottom row with DECSC/CUP/EL/DECRC so the cursor survives.
//   - The output pump re-asserts region+hint after frame batches, throttled to at
//     most once per ~250ms and always immediately after a damage signature
//     ("\x1bc" / "[2J" / "?1049" / a bare "\x1b[r").
//   - rows<=2 disables the hint (behaves exactly as Chrome:false).
//   - Detach resets the region to full ("\x1b[r") and clears the hint row.
//
// Chrome:false is unchanged and byte-identical — proven by the existing passthrough
// suite, which stays green.

import (
	"bytes"
	"testing"
	"time"
)

// waitOut polls until cond(out) holds or the deadline, returning the last snapshot.
func waitOut(t *testing.T, term *fakeTerm, cond func([]byte) bool) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out := term.outBytes()
		if cond(out) {
			return out
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within 2s; out=%q", term.outBytes())
	return nil
}

// Chrome reserves the bottom row by sizing the session PTY to rows-1 (the agent
// believes the terminal is one row shorter, so its absolute addressing never reaches
// the real bottom row).
func TestPassthrough_ChromeReservesBottomRowPTY(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	eventually(t, func() bool {
		for _, r := range sess.resizeCalls() {
			if r == [2]int{80, 23} {
				return true
			}
		}
		return false
	})
	for _, r := range sess.resizeCalls() {
		if r == [2]int{80, 24} {
			t.Fatalf("chrome attach must size the PTY to rows-1 (80,23), not the full (80,24); got %v", sess.resizeCalls())
		}
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// Chrome sets a DECSTBM scroll region of 1..rows-1 and paints the faint/dim hint
// on the real bottom row, cursor-preserved (DECSC/DECRC).
func TestPassthrough_ChromeSetsScrollRegionAndHint(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	out := waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("returns to swarm")) })

	for _, want := range [][]byte{
		[]byte("\x1b[1;23r"), // DECSTBM scroll region 1..rows-1
		[]byte("\x1b[24;1H"), // hint painted on the real bottom row
		[]byte("\x1b7"),      // DECSC: cursor saved before the hint
		[]byte("\x1b8"),      // DECRC: cursor restored after the hint
		[]byte("\x1b[2m"),    // faint/dim (v0.4 P2: subtle hint, not a harsh reverse-video bar)
		[]byte("claude"),     // session name
		[]byte("returns to swarm"),
	} {
		if !bytes.Contains(out, want) {
			t.Fatalf("chrome hint must contain %q; got %q", want, out)
		}
	}
	// v0.4 P2 (dim not reverse): the hint is faint default-colored text, never the harsh
	// reverse-video bar that painted a full-width white strip on dark terminals.
	if bytes.Contains(out, []byte("\x1b[7m")) {
		t.Fatalf("chrome hint must use faint (SGR 2), not reverse video (SGR 7); got %q", out)
	}
	// The hint must NOT be painted on the top row (v0.2 overdraw regression).
	if bytes.Contains(out, []byte("\x1b7\x1b[1;1H")) {
		t.Fatalf("chrome hint must live on the reserved bottom row, not row 1; got %q", out)
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// The hint is truncated to the terminal width on a narrow terminal (a wide row would
// wrap, and a wrap scrolls).
func TestPassthrough_ChromeHintTruncatedToWidth(t *testing.T) {
	term := newFakeTerm(12, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	longName := "verylongsessionname"
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: longName})

	out := waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("\x1b[24;1H")) })
	if bytes.Contains(out, []byte(longName)) {
		t.Fatalf("hint on a 12-col terminal must be truncated; full name leaked: %q", out)
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// A resize while attached recomputes the reserved-row PTY size AND re-establishes the
// scroll region + hint at the new dimensions.
func TestPassthrough_ChromeResizeRecomputesRegionAndPTY(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("\x1b[1;23r")) })
	term.setSize(120, 40)

	eventually(t, func() bool {
		for _, r := range sess.resizeCalls() {
			if r == [2]int{120, 39} {
				return true
			}
		}
		return false
	})
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("\x1b[1;39r")) })

	sess.endSession()
	_ = waitResult(t, ch)
}

// A terminal of rows<=2 is too small to reserve a row: the hint is disabled and the
// attach behaves exactly as Chrome:false (no chrome bytes, PTY sized to the full
// height).
func TestPassthrough_ChromeSmallTerminalDisablesHint(t *testing.T) {
	term := newFakeTerm(80, 2)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	eventually(t, func() bool {
		for _, r := range sess.resizeCalls() {
			if r == [2]int{80, 2} {
				return true
			}
		}
		return false
	})
	// No chrome painted at all: DECSC (\x1b7) is the chrome-only signature.
	time.Sleep(50 * time.Millisecond)
	out := term.outBytes()
	if bytes.Contains(out, []byte("\x1b7")) {
		t.Fatalf("rows<=2 must disable the hint entirely (behave as Chrome:false); got chrome bytes %q", out)
	}
	if bytes.Contains(out, []byte("returns to swarm")) {
		t.Fatalf("rows<=2 must not paint the hint; got %q", out)
	}
	for _, r := range sess.resizeCalls() {
		if r == [2]int{80, 1} {
			t.Fatalf("rows<=2 must not reserve a row (PTY stays full height); got %v", sess.resizeCalls())
		}
	}

	// Teardown must also be chrome-free (item 4): chrome never ENGAGED, so a resize
	// that stays small and the final teardown must emit no scroll-region reset and no
	// bottom-row clear — byte-for-byte as if Chrome:false.
	term.setSize(80, 2)
	eventually(t, func() bool { return len(sess.resizeCalls()) >= 2 })
	term.feed([]byte{DefaultDetachKey})
	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
	if bytes.Contains(term.outBytes(), []byte("\x1b[r")) {
		t.Fatalf("a never-engaged (rows<=2) run must emit no scroll-region reset through resize+detach; got %q", term.outBytes())
	}
}

// A frame carrying a damage signature (ED2 clear here) re-asserts the region+hint
// immediately, bypassing the throttle.
func TestPassthrough_ChromeDamageSignatureReasserts(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) == 1 })
	sess.pushFrame([]byte("\x1b[2Jredraw")) // ED2: a full-screen clear damages the hint row
	out := waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) >= 2 })
	if bytes.Count(out, []byte("\x1b[1;23r")) < 2 {
		t.Fatalf("a damage-signature frame must re-assert region+hint; regions=%d out=%q",
			bytes.Count(out, []byte("\x1b[1;23r")), out)
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// A benign frame within the throttle window does NOT re-assert (at most once per
// interval for undamaged output).
func TestPassthrough_ChromeBenignFrameThrottled(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) == 1 })
	sess.pushFrame([]byte("plain output, no damage"))
	// Detaching drains the loop; by the time Run returns the benign frame was fully
	// processed (its throttle decision made). Detach cleanup emits a bare "\x1b[r",
	// never another "\x1b[1;23r", so the region count is unambiguous.
	term.feed([]byte{DefaultDetachKey})
	_ = waitResult(t, ch)

	if n := bytes.Count(term.outBytes(), []byte("\x1b[1;23r")); n != 1 {
		t.Fatalf("a benign frame within the throttle window must not re-assert; regions=%d", n)
	}
}

// After the throttle interval elapses, a benign frame DOES re-assert (the periodic
// belt-and-suspenders re-assert).
func TestPassthrough_ChromePeriodicReassertAfterInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("periodic re-assert crosses the ~250ms throttle interval")
	}
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) == 1 })
	time.Sleep(400 * time.Millisecond) // exceed the ~250ms throttle interval
	sess.pushFrame([]byte("late benign output"))
	out := waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("\x1b[1;23r")) >= 2 })
	if bytes.Count(out, []byte("\x1b[1;23r")) < 2 {
		t.Fatalf("a benign frame after the throttle interval must re-assert; regions=%d",
			bytes.Count(out, []byte("\x1b[1;23r")))
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// On detach the scroll region is reset to full ("\x1b[r") and the hint row cleared,
// after the hint was established, so the board repaints on a clean full-height screen.
func TestPassthrough_ChromeDetachResetsRegionAndClearsHint(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("\x1b[1;23r")) })
	term.feed([]byte{DefaultDetachKey})
	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}

	out := term.outBytes()
	reset := bytes.LastIndex(out, []byte("\x1b[r"))
	region := bytes.Index(out, []byte("\x1b[1;23r"))
	if reset < 0 {
		t.Fatalf("detach must reset the scroll region to full (\\x1b[r); got %q", out)
	}
	if region < 0 || reset < region {
		t.Fatalf("the region reset must follow the hint establishment; region=%d reset=%d", region, reset)
	}
}

// S10/A-4 within the reserved area: the snapshot is still painted EXACTLY ONCE before
// any live frame, and the reserved-row chrome is present around it.
func TestPassthrough_ChromeSnapshotOnceBeforeFramesWithinReserve(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "SNAPGRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("SNAPGRID")) })
	sess.pushFrame([]byte("LIVEONE"))
	out := waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("LIVEONE")) })

	snapAt := bytes.Index(out, []byte("SNAPGRID"))
	frameAt := bytes.Index(out, []byte("LIVEONE"))
	if snapAt < 0 || frameAt < 0 || snapAt >= frameAt {
		t.Fatalf("snapshot must precede the first live frame (S10): snapAt=%d frameAt=%d", snapAt, frameAt)
	}
	if n := bytes.Count(out, []byte("SNAPGRID")); n != 1 {
		t.Fatalf("snapshot painted %d times, want exactly one (S10)", n)
	}
	if !bytes.Contains(out, []byte("\x1b[1;23r")) {
		t.Fatalf("the reserved-row region must be present alongside the snapshot; got %q", out)
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// The snapshot paint is clipped to the reserved area (rows-1): content the snapshot
// carried on the real bottom row is dropped so it never paints over the hint row.
func TestPassthrough_ChromeSnapshotClippedToReservedRows(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "\x1b[1;1HTOPROW\x1b[24;1HBOTTOMROW"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})

	out := waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("TOPROW")) })
	if bytes.Contains(out, []byte("BOTTOMROW")) {
		t.Fatalf("chrome must clip the snapshot to rows-1 so the bottom row stays reserved; leaked bottom content: %q", out)
	}

	sess.endSession()
	_ = waitResult(t, ch)
}
