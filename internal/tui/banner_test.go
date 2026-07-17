package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// E7.2 / V-5 — WHEN a session transitions INTO needs_input, an in-TUI notification
// banner naming the session surfaces. Banner copy is pinned as "<agent> needs
// input" (mixed-case, distinct from the uppercase group header "NEEDS INPUT").

func TestBanner_OnNeedsInputTransition(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", 2*time.Minute))
	tm := startTM(t, New(f, detectMixed()))
	waitContains(t, tm, "building") // initial paint done

	f.emit(protocol.Event{Session: sNeedsInput("endpoint/s1", "claude", "~/Code/x", "Permission: run tests?", 0)})

	waitContains(t, tm, "claude needs input") // the transient banner
	tm.Quit()
}

// E7.2 / V-5 — WHEN a session transitions INTO ready_for_review, the banner fires.

func TestBanner_OnReadyForReviewTransition(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", 2*time.Minute))
	tm := startTM(t, New(f, detectMixed()))
	waitContains(t, tm, "building")

	f.emit(protocol.Event{Session: sReview("endpoint/s1", "claude", "~/Code/x", "Turn finished", 0)})

	waitContains(t, tm, "claude ready for review")
	tm.Quit()
}

// E7.2 / V-5 — the banner is TRANSITION-triggered: the initial listing of a
// needs_input session raises NO banner (there is no prior state to transition
// from).

func TestBanner_NotShownOnInitialListing(t *testing.T) {
	f := newFakeClient(sNeedsInput("endpoint/s1", "claude", "~/Code/x", "Permission?", 5*time.Minute))
	m := newModel(t, f, detectMixed())
	v := view(m)

	if !strings.Contains(v, "NEEDS INPUT") { // group header present
		t.Fatalf("expected NEEDS INPUT group on initial listing:\n%s", v)
	}
	if strings.Contains(v, "claude needs input") { // banner must NOT be present
		t.Fatalf("initial listing must not raise a transition banner:\n%s", v)
	}
}

// E7.2 / V-5 — a transition into Working (a non-triggering group) raises no
// banner.

func TestBanner_NotShownOnWorkingTransition(t *testing.T) {
	f := newFakeClient(sNeedsInput("endpoint/s1", "claude", "~/Code/x", "Permission?", 5*time.Minute))
	tm := startTM(t, New(f, detectMixed()))
	waitContains(t, tm, "NEEDS INPUT")

	f.emit(protocol.Event{Session: sWorking("endpoint/s1", "claude", "~/Code/x", "resumed work", 0)})
	waitContains(t, tm, "resumed work") // event applied
	tm.Quit()

	final := finalView(t, tm)
	if strings.Contains(final, "claude needs input") || strings.Contains(final, "claude ready for review") {
		t.Fatalf("a Working transition must not raise a needs_input/review banner:\n%s", final)
	}
}
