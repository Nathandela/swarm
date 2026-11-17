package tui

// FAILING-FIRST for ADR-006 Amendment "session cycling keys": Ctrl+P / Ctrl+N inside an
// attach move to the previous / next running session without passing through the board.
// The board hands the runner a ring of its running rows, in the order the board itself
// shows them (the order Down walks), wrapping at both ends; the runner chains attaches
// inside one terminal handoff; and on return the board selects the session the user
// was last in, not the one the attach started from.

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/protocol"
)

// boardOrder walks the board with Down from the top and returns the selected ids, in
// order, once each.
func boardOrder(t *testing.T, m tea.Model) []string {
	t.Helper()
	var ids []string
	seen := map[string]bool{}
	for range 16 {
		id := m.(rootModel).general.selectedID()
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
		m = send(m, keyDown)
	}
	return ids
}

func cycleBoard(t *testing.T, r AttachRunner, sessions ...protocol.SessionView) tea.Model {
	t.Helper()
	m := New(newFakeClient(sessions...), detectMixed(), WithAttachRunner(r))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	return m
}

func TestAttachCycling_RingFollowsTheBoardOrderOfRunningRows(t *testing.T) {
	var ring SessionRing
	r := func(_ protocol.SessionView, _, _ bool, got SessionRing) error { ring = got; return nil }
	m := cycleBoard(t, r,
		sWorking("endpoint/a", "claude", "~/Code/a", "a", 3*time.Minute),
		sNeedsInput("endpoint/b", "codex", "~/Code/b", "b", 2*time.Minute),
		sCompleted("endpoint/x", "claude", "~/Code/x", "x", time.Minute),
		sWorking("endpoint/c", "claude", "~/Code/c", "c", time.Minute),
	)
	var running []string
	for _, id := range boardOrder(t, m) {
		if id != "endpoint/x" {
			running = append(running, id)
		}
	}
	if len(running) != 3 {
		t.Fatalf("board order of running rows = %v, want three", running)
	}

	_ = runAttachCmd(m)
	if ring == nil {
		t.Fatal("the attach runner received no session ring")
	}
	n := len(running)
	for i, id := range running {
		next, ok := ring(id, +1)
		if !ok || next.ID != running[(i+1)%n] {
			t.Errorf("ring(%s, +1) = %q, %v; want %q", id, next.ID, ok, running[(i+1)%n])
		}
		prev, ok := ring(id, -1)
		if !ok || prev.ID != running[(i-1+n)%n] {
			t.Errorf("ring(%s, -1) = %q, %v; want %q", id, prev.ID, ok, running[(i-1+n)%n])
		}
	}
}

func TestAttachCycling_LoneRunningRowHasNoNeighbour(t *testing.T) {
	var ring SessionRing
	r := func(_ protocol.SessionView, _, _ bool, got SessionRing) error { ring = got; return nil }
	m := cycleBoard(t, r,
		sWorking("endpoint/a", "claude", "~/Code/a", "a", time.Minute),
		sCompleted("endpoint/x", "claude", "~/Code/x", "x", 2*time.Minute),
	)
	_ = runAttachCmd(m)
	if ring == nil {
		t.Fatal("the attach runner received no session ring")
	}
	if s, ok := ring("endpoint/a", +1); ok {
		t.Fatalf("ring offered %q as a neighbour of the only running row", s.ID)
	}
}

func TestAttachCycling_BoardSelectsTheSessionLastAttached(t *testing.T) {
	r := func(s protocol.SessionView, _, _ bool, ring SessionRing) error {
		_, _ = ring(s.ID, +1)
		return nil
	}
	m := cycleBoard(t, r,
		sWorking("endpoint/a", "claude", "~/Code/a", "a", 2*time.Minute),
		sWorking("endpoint/b", "claude", "~/Code/b", "b", time.Minute),
	)
	order := boardOrder(t, m)
	start := m.(rootModel).general.selectedID()
	want := order[0]
	if start == order[0] {
		want = order[1]
	}

	m = runAttachCmd(m)
	if got := m.(rootModel).general.selectedID(); got != want {
		t.Fatalf("board selection after cycling = %q, want the session last attached %q (started on %q)", got, want, start)
	}
}

// The chain itself: one attach per step, each to the ring's answer; no neighbour keeps
// the current session; the session ending, a detach or an error stops the chain.
func TestAttachChain_FollowsTheKeysThroughTheRing(t *testing.T) {
	ids := []string{"a", "b", "c"}
	ring := func(from string, step int) (protocol.SessionView, bool) {
		for i, id := range ids {
			if id == from {
				return protocol.SessionView{ID: ids[(i+step+len(ids))%len(ids)]}, true
			}
		}
		return protocol.SessionView{}, false
	}
	reasons := []attach.Reason{attach.ReasonNext, attach.ReasonNext, attach.ReasonPrev, attach.ReasonPrev, attach.ReasonPrev, attach.ReasonDetached}
	var visited []string
	err := attachChain(protocol.SessionView{ID: "a"}, ring, func(s protocol.SessionView) (attach.Reason, error) {
		visited = append(visited, s.ID)
		r := reasons[0]
		reasons = reasons[1:]
		return r, nil
	})
	if err != nil {
		t.Fatalf("attachChain: %v", err)
	}
	want := []string{"a", "b", "c", "b", "a", "c"}
	if len(visited) != len(want) {
		t.Fatalf("visited %v, want %v", visited, want)
	}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("visited %v, want %v", visited, want)
		}
	}
}

func TestAttachChain_NoNeighbourStaysAndErrorsStop(t *testing.T) {
	none := func(string, int) (protocol.SessionView, bool) { return protocol.SessionView{}, false }
	var visited []string
	reasons := []attach.Reason{attach.ReasonNext, attach.ReasonSessionEnd}
	if err := attachChain(protocol.SessionView{ID: "a"}, none, func(s protocol.SessionView) (attach.Reason, error) {
		visited = append(visited, s.ID)
		r := reasons[0]
		reasons = reasons[1:]
		return r, nil
	}); err != nil {
		t.Fatalf("attachChain: %v", err)
	}
	if len(visited) != 2 || visited[0] != "a" || visited[1] != "a" {
		t.Fatalf("with no neighbour the chain must stay on the session; visited %v", visited)
	}

	boom := errors.New("dial refused")
	calls := 0
	err := attachChain(protocol.SessionView{ID: "a"}, nil, func(protocol.SessionView) (attach.Reason, error) {
		calls++
		return attach.ReasonNext, boom
	})
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("attachChain = %v after %d attaches, want the error after one", err, calls)
	}
}
