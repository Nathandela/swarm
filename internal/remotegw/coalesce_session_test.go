package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests for B1: the coalescing stash is NOT KEYED BY SESSION.
//
// THE DEFECT. CoalescingSink holds ONE stashed snapshot, but the gateway runs one
// Gateway.RunTerminal per WATCHED SESSION (TerminalWatcher.Watch, called from the command
// loop's terminal_watch with no implicit unwatch), and every one of them forwards into the
// SAME sink. So N concurrent peeks contend for a single latest-wins slot and clobber each
// other:
//
//	t=0     A "A-0"      window open      -> forwarded
//	t=10ms  A "A-FINAL"  inside window    -> stashed; A then goes idle
//	t=130ms B "B-0"      window elapsed   -> the slot is cleared (A-FINAL DISCARDED) and B
//	                                         is forwarded
//	        A's idle-wake Flush()         -> nothing of A's is left to ship
//
// A-FINAL never ships, so session A's peek sits on a STALE GRID until A emits again -- for an
// idle terminal, never. That is exactly what TestGatewayRunTerminal_CoalescedPeekShowsLatestGrid
// forbids; it passes today only because it drives a single session.
//
// The same root cause breaks the teardown blank (Blocker 1d): RunTerminal appends a blank
// snapshot on the daemon's OpError and then flushes, and a concurrent session's snapshot
// landing between those two steps takes the slot the blank was holding.
//
// THE FIX these tests pin: the stash is keyed by session (latest-wins PER SESSION) and the
// held snapshots are released OLDEST-FIRST through the one shared slot -- so no session is
// clobbered and no session is starved, while the §6.0 budget stays COMBINED (<= 8 appends/s
// across journal and terminal, for ANY number of sessions).

import (
	"fmt"
	"github.com/Nathandela/swarm/internal/protocol"
	"strings"
	"testing"
	"time"
)

// sendTerminal offers one single-line snapshot for session to the sink.
func sendTerminal(t *testing.T, sink *CoalescingSink, session, line string) {
	t.Helper()
	if err := sink.Terminal(protocol.TerminalViewV1{Session: session, Lines: []string{line}, Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("terminal(%s, %s): %v", session, line, err)
	}
}

// forwardedLines groups everything the inner sink received by session, in order. A blank
// snapshot (the teardown signal) is recorded as "".
func forwardedLines(rec *snapshotRecorder) map[string][]string {
	out := map[string][]string{}
	for _, s := range rec.all() {
		line := ""
		if len(s.Lines) > 0 {
			line = s.Lines[0]
		}
		out[s.Session] = append(out[s.Session], line)
	}
	return out
}

// drain runs the idle-wake flushes every live peek performs (RunTerminal calls Flush on each
// idle read wake), one window apart, until production has fully drained.
func drain(t *testing.T, sink *CoalescingSink, clk *vclock, windows int) {
	t.Helper()
	for i := 0; i < windows; i++ {
		clk.Advance(DefaultAppendWindow)
		if err := sink.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}
}

// TestCoalescingSink_StashIsPerSession replays the reviewer's repro: a snapshot held back for
// one session must survive a DIFFERENT session taking the shared slot. Latest-wins is a
// per-session rule -- a peek must never be left on a stale grid because another peek emitted.
func TestCoalescingSink_StashIsPerSession(t *testing.T) {
	clk := newVClock()
	rec := &snapshotRecorder{}
	sink := NewCoalescingSink(CoalesceConfig{Inner: rec, Window: DefaultAppendWindow, Now: clk.Now})

	// t=0: A's first frame opens the window and is forwarded.
	sendTerminal(t, sink, "m/a", "A-0")
	// t=10ms: A's LAST frame lands inside the window, so it is held back. A then goes idle:
	// nothing more will be produced for it ever again.
	clk.Advance(10 * time.Millisecond)
	sendTerminal(t, sink, "m/a", "A-FINAL")
	// t=130ms: a DIFFERENT session's first frame arrives after the window elapsed.
	clk.Advance(120 * time.Millisecond)
	sendTerminal(t, sink, "m/b", "B-0")
	// Both peeks now idle-wake and flush whatever is still held.
	drain(t, sink, clk, 4)

	got := forwardedLines(rec)
	if want := []string{"A-0", "A-FINAL"}; strings.Join(got["m/a"], ",") != strings.Join(want, ",") {
		t.Errorf("session m/a reached the phone as %v, want %v: the stash is a SINGLE slot shared by "+
			"every peek, so session m/b's snapshot discarded A's held-back final frame and A's peek is "+
			"stuck on a stale grid forever (B1; latest-wins must be PER SESSION)", got["m/a"], want)
	}
	if want := []string{"B-0"}; strings.Join(got["m/b"], ",") != strings.Join(want, ",") {
		t.Errorf("session m/b reached the phone as %v, want %v", got["m/b"], want)
	}
}

// TestCoalescingSink_TeardownBlankSurvivesConcurrentSession pins the Blocker-1d regression the
// single slot reopens: RunTerminal blanks the phone's latest-wins cache when the daemon ends a
// peek and then flushes, and a concurrent peek's snapshot arriving between those two steps must
// not take the blank's place. Without the blank the phone keeps showing the pre-teardown screen.
func TestCoalescingSink_TeardownBlankSurvivesConcurrentSession(t *testing.T) {
	clk := newVClock()
	rec := &snapshotRecorder{}
	sink := NewCoalescingSink(CoalesceConfig{Inner: rec, Window: DefaultAppendWindow, Now: clk.Now})

	sendTerminal(t, sink, "m/a", "A-0") // opens the window
	// The daemon ends A's peek: RunTerminal seals a BLANK snapshot for A (Blocker 1d).
	clk.Advance(10 * time.Millisecond)
	if err := sink.Terminal(protocol.TerminalViewV1{Session: "m/a", Lines: nil, Cols: 0, Rows: 0}); err != nil {
		t.Fatalf("teardown blank: %v", err)
	}
	// Between the blank and the teardown flush, session B's peek forwards a frame of its own.
	clk.Advance(5 * time.Millisecond)
	sendTerminal(t, sink, "m/b", "B-0")
	// RunTerminal's teardown flush (the real gateway seam), then the surviving peek's idle wakes.
	if err := flushTerminal(sink); err != nil {
		t.Fatalf("teardown flush: %v", err)
	}
	drain(t, sink, clk, 4)

	got := forwardedLines(rec)
	blanked := false
	for _, line := range got["m/a"] {
		if line == "" {
			blanked = true
		}
	}
	if !blanked {
		t.Errorf("session m/a reached the phone as %v with NO blank: session m/b's snapshot took the "+
			"single stash slot the teardown blank was held in, so the phone keeps showing m/a's "+
			"pre-teardown grid after the peek ended (Blocker 1d, reopened by B1)", got["m/a"])
	}
	if want := []string{"B-0"}; strings.Join(got["m/b"], ",") != strings.Join(want, ",") {
		t.Errorf("session m/b reached the phone as %v, want %v: keying the stash must not lose the "+
			"other peek's frame either", got["m/b"], want)
	}
}

// TestCoalescingSink_MultiSessionStaysUnderCombinedBudget is the counterweight to the two tests
// above: keying the stash by session must NOT turn the §6.0 budget into a PER-SESSION budget.
// Four peeks render at the real 16 ms debounce and each one idle-wakes and flushes once per
// window (the worst case for a flush that ignores the shared slot); the combined append rate
// must still be <= 8/s, and no session may be starved by the others.
func TestCoalescingSink_MultiSessionStaysUnderCombinedBudget(t *testing.T) {
	clk := newVClock()
	rec := &snapshotRecorder{}
	sink := NewCoalescingSink(CoalesceConfig{Inner: rec, Window: DefaultAppendWindow, Now: clk.Now})

	sessions := []string{"m/s1", "m/s2", "m/s3", "m/s4"}
	const peek = 30 * time.Second
	start := clk.Now()
	flushEvery := int(DefaultAppendWindow / renderDebounceRate)
	for frame := 0; clk.Now().Sub(start) < peek; frame++ {
		for _, s := range sessions {
			sendTerminal(t, sink, s, fmt.Sprintf("%s-%d", s, frame))
		}
		if frame%flushEvery == 0 {
			for range sessions { // one idle-wake flush per live peek
				if err := sink.Flush(); err != nil {
					t.Fatalf("flush: %v", err)
				}
			}
		}
		clk.Advance(renderDebounceRate)
	}
	elapsed := clk.Now().Sub(start)

	got := forwardedLines(rec)
	total := 0
	for _, lines := range got {
		total += len(lines)
	}
	// The +2 absorbs the first append and the trailing flush at the boundaries, exactly as in
	// TestRelaySink_SustainedPeekStaysUnderAppendBudget.
	if budget := int(elapsed/DefaultAppendWindow) + 2; total > budget {
		t.Errorf("%d appends over %s = %.1f/s for %d peeks, over the §6.0 budget of %d (<= 8/s COMBINED): "+
			"a per-session stash must keep sharing ONE slot -- N sessions must not buy N budgets",
			total, elapsed, float64(total)/elapsed.Seconds(), len(sessions), budget)
	}

	// No starvation: the shared slot must rotate over the held sessions (oldest-first), not be
	// won every window by whichever peek happens to be offered first.
	share := total / (2 * len(sessions))
	for _, s := range sessions {
		if n := len(got[s]); n < share {
			t.Errorf("session %s got %d of %d appends (< %d): one peek must not monopolize the shared "+
				"slot while the others sit on stale grids", s, n, total, share)
		}
	}

	// Latest-wins PER SESSION: a session's forwarded frames must be strictly newer over time.
	for _, s := range sessions {
		prev := -1
		for i, line := range got[s] {
			var idx int
			if _, err := fmt.Sscanf(line, s+"-%d", &idx); err != nil {
				t.Fatalf("session %s forwarded %q, not a frame marker: %v", s, line, err)
			}
			if idx <= prev {
				t.Fatalf("session %s forwarded frame %d after frame %d (append %d): a coalescer must "+
					"forward the LATEST snapshot it holds for a session, never an older one", s, idx, prev, i)
			}
			prev = idx
		}
	}
}
