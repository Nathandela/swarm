package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// E7.3 / V-2 / L1 — a Subscribe status-change event updates the affected row
// within the model's update cycle: the row moves group and the render reflects it,
// driven by the event alone (no polling/timer advance). This is the client half of
// the L1 <=1 s composite; the daemon half is E10.7 and the composition is E14.

func TestLiveness_EventMovesRowGroup(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling now", 3*time.Minute))
	tm := startTM(t, New(f, detectMixed()))

	// Initial state: the session sits under WORKING.
	waitContains(t, tm, "WORKING")
	waitContains(t, tm, "compiling now")

	// A status event moves the SAME session (same id) into needs_input with a new
	// summary. The view must reflect the move purely from the event.
	f.emit(protocol.Event{Session: sNeedsInput("endpoint/s1", "codex", "~/Code/x", "Permission: run tests?", 0)})

	waitContains(t, tm, "NEEDS INPUT")
	waitContains(t, tm, "Permission: run tests?")
	tm.Quit()

	// Final single-screen render: the row is under NEEDS INPUT, the old WORKING
	// placement and old summary are gone, and there is exactly one codex row (the
	// event updated in place, it did not duplicate).
	final := finalView(t, tm)
	if !strings.Contains(final, "NEEDS INPUT") || !strings.Contains(final, "Permission: run tests?") {
		t.Fatalf("event not reflected in final render:\n%s", final)
	}
	if strings.Contains(final, "WORKING") || strings.Contains(final, "compiling now") {
		t.Fatalf("stale WORKING placement/summary still present after move:\n%s", final)
	}
	if n := strings.Count(final, "codex"); n != 1 {
		t.Fatalf("expected the session updated in place (one codex row), found %d:\n%s", n, final)
	}
}
