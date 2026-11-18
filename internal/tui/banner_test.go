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
	quitTM(t, tm)
}

// E7.2 / V-5 — WHEN a session transitions INTO ready_for_review, the banner fires.

func TestBanner_OnReadyForReviewTransition(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", 2*time.Minute))
	tm := startTM(t, New(f, detectMixed()))
	waitContains(t, tm, "building")

	f.emit(protocol.Event{Session: sReview("endpoint/s1", "claude", "~/Code/x", "Turn finished", 0)})

	waitContains(t, tm, "claude ready for review")
	quitTM(t, tm)
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
	quitTM(t, tm)

	final := finalView(t, tm)
	if strings.Contains(final, "claude needs input") || strings.Contains(final, "claude ready for review") {
		t.Fatalf("a Working transition must not raise a needs_input/review banner:\n%s", final)
	}
}

func TestBanner_DoesNotShiftMainBoardContent(t *testing.T) {
	longName := strings.Repeat("very-long-session-name-", 7) // >140 chars to exercise single-line clamping
	s1Needs := sNeedsInput("endpoint/s1", "claude", "~/Code/x", "Permission: run tests?", time.Hour)
	s1Needs.Name = longName
	s2 := sReview("endpoint/s2", "claude", "~/Code/soml", "soml-review-summary", 2*time.Hour)
	s3 := sCompleted("endpoint/s3", "gemini", "~/Code/scratch", "scratch-ended-summary", 3*time.Hour)

	baseline := newModel(t, newFakeClient(s1Needs, s2, s3), detectMixed())
	baselineStart, baselineRows, _, _ := baseline.(rootModel).boardGeometry()
	if baselineStart != 2 {
		t.Fatalf("baseline contentStart = %d, want 2", baselineStart)
	}
	wantY := map[string]int{
		"NEEDS INPUT":           rowY(t, baseline, "▾ NEEDS INPUT"),
		"READY FOR REVIEW":      rowY(t, baseline, "▾ READY FOR REVIEW"),
		"soml-review-summary":   rowY(t, baseline, "soml-review-summary"),
		"COMPLETED":             rowY(t, baseline, "▾ COMPLETED"),
		"scratch-ended-summary": rowY(t, baseline, "scratch-ended-summary"),
	}
	if wantY["NEEDS INPUT"] != 2 {
		t.Fatalf("NEEDS INPUT row = %d, want 2", wantY["NEEDS INPUT"])
	}

	s1Working := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Hour)
	s1Working.Name = longName
	m := newModel(t, newFakeClient(s1Working, s2, s3), detectMixed())

	// Pre-existing groups below WORKING share fixed geometry height; capture before banner too.
	beforeReviewY := rowY(t, m, "▾ READY FOR REVIEW")
	beforeCompletedY := rowY(t, m, "▾ COMPLETED")
	if beforeReviewY != wantY["READY FOR REVIEW"] || beforeCompletedY != wantY["COMPLETED"] {
		t.Fatalf("unexpected pre-transition rows: review=%d completed=%d, want %d %d",
			beforeReviewY, beforeCompletedY, wantY["READY FOR REVIEW"], wantY["COMPLETED"])
	}

	// Trigger V-5 attention banner with a >140-char session name.
	m = send(m, eventMsg{ev: protocol.Event{Session: s1Needs}})
	rm := m.(rootModel)
	start, rows, banner, _ := rm.boardGeometry()
	if start != 2 || rows != baselineRows || len(banner) != 1 {
		t.Fatalf("active V-5 banner changed geometry: start=%d rows=%d bannerLines=%d (want start=2 rows=%d len=1)",
			start, rows, len(banner), baselineRows)
	}
	lines := strings.Split(view(m), "\n")
	if !strings.Contains(lines[1], "● very-long-session-name-") {
		t.Fatalf("row 1 should render the static notification slot; lines[1]=%q\n%s", lines[1], view(m))
	}
	for needle, want := range wantY {
		if got := rowY(t, m, needle); got != want {
			t.Fatalf("with active V-5 banner, %q shifted to row %d, want static row %d\n%s", needle, got, want, view(m))
		}
	}

	// Expire banner: rows must remain at the exact same coordinates.
	rm.general.bannerExpiry = time.Now().Add(-time.Second)
	m = send(rm, bannerExpireMsg{})
	for needle, want := range wantY {
		if got := rowY(t, m, needle); got != want {
			t.Fatalf("after V-5 banner expiry, %q shifted to row %d, want %d\n%s", needle, got, want, view(m))
		}
	}

	// Routine 1-line setBanner notification must also occupy row 1 without shifting rows.
	m = send(m, tagDoneMsg{id: "endpoint/s1", tag: "prod"})
	lines = strings.Split(view(m), "\n")
	if !strings.Contains(lines[1], "● session tagged prod") {
		t.Fatalf("row 1 should render setBanner notice; lines[1]=%q\n%s", lines[1], view(m))
	}
	for needle, want := range wantY {
		if got := rowY(t, m, needle); got != want {
			t.Fatalf("with setBanner notice, %q shifted to row %d, want static row %d\n%s", needle, got, want, view(m))
		}
	}
}

func TestBanner_AppearingMidClickPreservesRowHitbox(t *testing.T) {
	m, runner := mouseBoard(t)
	reviewY := rowY(t, m, "review-row-summary")
	m = click(t, m, reviewY) // select the review row first
	if selectedOf(m) != "endpoint/r1" {
		t.Fatalf("selected = %q, want endpoint/r1", selectedOf(m))
	}

	// Press on the selected row, then fire a 1-line notification before releasing.
	m = send(m, press(reviewY))
	m = send(m, tagDoneMsg{id: "endpoint/w1", tag: "urgent"})
	m, cmd := m.Update(release(reviewY))
	_ = settle(m, cmd)

	if len(runner.calls) != 1 || runner.calls[0].session.ID != "endpoint/r1" {
		t.Fatalf("banner appearing between press and release broke row attach: calls=%+v", runner.calls)
	}
}
