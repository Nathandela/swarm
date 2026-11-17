package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestAttachCycling_FreshAutoAttachedSessionStaysInRing(t *testing.T) {
	var ring SessionRing
	r := func(_ protocol.SessionView, _, _ bool, got SessionRing) error {
		ring = got
		return nil
	}
	m := cycleBoard(t, r,
		sWorking("endpoint/existing", "claude", "~/Code/a", "working", time.Minute),
	)
	// Simulate launchResultMsg auto-attaching a freshly launched session before its
	// subscription event has arrived on m.general.sessions.
	m, cmd := m.Update(launchResultMsg{id: "endpoint/fresh", agent: "codex", name: "fresh-work"})
	if cmd == nil {
		t.Fatal("expected runAttach command on launchResultMsg")
	}
	msg := cmd()
	_, _ = m.Update(msg)
	if ring == nil {
		t.Fatal("expected non-nil SessionRing")
	}

	hop1, ok1 := ring("endpoint/fresh", +1)
	if !ok1 || hop1.ID != "endpoint/existing" {
		t.Fatalf("ring(fresh, +1) = (%q, %v), want endpoint/existing", hop1.ID, ok1)
	}
	hop2, ok2 := ring("endpoint/existing", +1)
	if !ok2 || hop2.ID != "endpoint/fresh" {
		t.Fatalf("ring(existing, +1) = (%q, %v), want endpoint/fresh (fresh launch dropped out of ring)", hop2.ID, ok2)
	}
}

func TestAttachCycling_OffscreenWorkingSelectionArmsSpinnerOnReturn(t *testing.T) {
	var sessions []protocol.SessionView
	for i := range 20 {
		id := "endpoint/in-" + itoa(i)
		s := sNeedsInput(id, "claude", "~/Code/p", "approval", time.Duration(i+1)*time.Minute)
		s.Name = "input-" + itoa(i)
		sessions = append(sessions, s)
	}
	w := sWorking("endpoint/work-last", "codex", "~/Code/w", "running tests", time.Minute)
	w.Name = "work-last"
	sessions = append(sessions, w)

	r := func(_ protocol.SessionView, _, _ bool, ring SessionRing) error {
		// Cycle backwards from top row to the offscreen Working row at the bottom.
		_, _ = ring("endpoint/in-0", -1)
		return nil
	}
	m := New(newFakeClient(sessions...), detectMixed(), WithAttachRunner(r))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	m, _ = m.Update(workingAnimationMsg{})
	if m.(rootModel).animatingWorking {
		t.Fatal("precondition: offscreen Working row must not animate before cycling")
	}

	// Enter attach on top row (no Working row visible in initial viewport), cycle to work-last, and return.
	m, attachCmd := m.Update(keyEnter)
	doneMsg := attachCmd()
	m, returnCmd := m.Update(doneMsg)
	rm := m.(rootModel)
	if rm.general.selectedID() != "endpoint/work-last" {
		t.Fatalf("selectedID = %q, want endpoint/work-last", rm.general.selectedID())
	}
	if !rm.animatingWorking || returnCmd == nil {
		t.Fatalf("animatingWorking = %v (cmd=%v), want true after revealing offscreen Working row", rm.animatingWorking, returnCmd != nil)
	}
}

func TestHandoffNarrowDisclosureNeverHiddenWhileUnguarded(t *testing.T) {
	f := newHandsOffClient(sWorking("endpoint/source", "codex", "/repo/project", "working", time.Minute))
	m := newModel(t, f, handoffAgents())
	m = send(m, tea.WindowSizeMsg{Width: 70, Height: 8})
	m = openForm(m)
	m = selectMethod(t, m, handoffMethodHandsOff)
	m = send(m, keyEnter)
	if !m.(rootModel).handoff.confirmPending() {
		t.Fatal("expected cross-CLI confirmation pending")
	}

	v := view(m)
	compact := strings.Join(strings.Fields(v), " ")
	// Either the full cross-CLI disclosure is visible right now, OR the reading guard is active
	// so pressing 'y' does not launch unseen.
	disclosureVisible := strings.Contains(compact, "cross-CLI handoff:") && strings.Contains(compact, "y confirm, n cancel")
	if !disclosureVisible {
		_, cmd := m.Update(keyRune('y'))
		if cmd != nil {
			t.Fatalf("cross-CLI disclosure was clipped offscreen yet 'y' launched immediately:\n%s", v)
		}
	}
}

func TestInlineConfirmAt80x24KeepsCompactRowsStableAndNameReadable(t *testing.T) {
	s1 := sNeedsInput("endpoint/s1", "claude", "~/Code/quanthome-api", "approval", time.Hour)
	s1.Name = "my-service-production"
	s2 := sCompleted("endpoint/s2", "codex", "~/Code/agents-tracker", "exit 0", 2*time.Hour)
	s2.Name = "my-service-staging"

	m := newModel(t, newFakeClient(s1, s2), detectMixed())
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	// Select s2 (completed) first, then verify Ctrl+X keeps non-target s1 byte-identical.
	m = send(m, keyDown)
	before := view(m)
	beforeS1Line := ""
	for _, line := range strings.Split(before, "\n") {
		if strings.Contains(line, "my-service-production") {
			beforeS1Line = line
			break
		}
	}
	if beforeS1Line == "" {
		t.Fatalf("missing s1 line before confirm:\n%s", before)
	}

	m = send(m, keyCtrlX)
	after := view(m)
	if !strings.Contains(after, "delete? y/n") {
		t.Fatalf("missing inline delete? y/n at 80x24:\n%s", after)
	}
	if !strings.Contains(after, "my-service-staging") {
		t.Fatalf("confirmed session name truncated at 80x24:\n%s", after)
	}
	if !strings.Contains(after, beforeS1Line) {
		t.Fatalf("non-target row jumped layout during 80x24 inline confirm:\nbefore: %q\nafter:\n%s", beforeS1Line, after)
	}
}
