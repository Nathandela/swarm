package tui

// ADR-010 Amendment 3 C3/C4 -- the board's half of passive supervision. A handoff
// child with an undelivered attention event carries the amber "supervisor pending"
// marker in its summary tail (same width discipline as the phone-control marker);
// when its source has left the roster or is no longer running, the row says
// "supervisor gone" instead, so the human sees that nobody will be woken.
//
// RED today: supervisionPendingMarker / supervisionGoneMarker do not exist and
// renderRow ignores SessionView.SupervisionPending.

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// supervisedChild is a passive handoff child of parent1 with an attention event
// awaiting delivery.
func supervisedChild(id string) protocol.SessionView {
	child := sReview(id, "claude", "~/Code/x", "", time.Minute)
	child.Name = "child-work"
	child.SpawnedFrom = "parent1"
	child.SpawnIntent = protocol.SpawnIntentHandoff
	child.Supervision = protocol.SupervisionPassive
	child.SupervisionPending = true
	return child
}

func TestSupervisionMarkers_AreBareWords(t *testing.T) {
	if supervisionPendingMarker != "supervisor pending" {
		t.Errorf("supervisionPendingMarker = %q, want %q", supervisionPendingMarker, "supervisor pending")
	}
	if supervisionGoneMarker != "supervisor gone" {
		t.Errorf("supervisionGoneMarker = %q, want %q", supervisionGoneMarker, "supervisor gone")
	}
}

func TestRoster_SupervisionPendingRowIsMarked(t *testing.T) {
	parent := sWorking("endpoint/parent1", "codex", "~/Code/x", "", time.Minute)
	parent.Name = "source-work"
	child := supervisedChild("endpoint/child1")

	quiet := supervisedChild("endpoint/child2")
	quiet.SupervisionPending = false

	gm := generalModel{sessions: []protocol.SessionView{parent, child, quiet}, width: testCols}

	childRow := stripANSI(gm.renderRow(child, child.Group, false))
	if !strings.Contains(childRow, supervisionPendingMarker) {
		t.Errorf("a child with a pending supervision event must carry %q; got:\n%q", supervisionPendingMarker, childRow)
	}
	if strings.Contains(childRow, supervisionGoneMarker) {
		t.Errorf("a child whose source is running must not say %q; got:\n%q", supervisionGoneMarker, childRow)
	}
	if !strings.Contains(childRow, "from") || !strings.Contains(childRow, "source-work") {
		t.Errorf("the supervision marker displaced the lineage badge; got:\n%q", childRow)
	}

	for _, s := range []protocol.SessionView{quiet, parent} {
		row := stripANSI(gm.renderRow(s, s.Group, false))
		if strings.Contains(row, "supervisor") {
			t.Errorf("row %s without a pending event carries a supervision marker; got:\n%q", s.ID, row)
		}
	}
}

func TestRoster_SupervisionGoneWhenSourceAbsentOrNotRunning(t *testing.T) {
	child := supervisedChild("endpoint/child1")
	running := sWorking("endpoint/parent1", "codex", "~/Code/x", "", time.Minute)
	ended := sCompleted("endpoint/parent1", "codex", "~/Code/x", "", time.Minute)
	lost := sCompleted("endpoint/parent1", "codex", "~/Code/x", "", time.Minute)
	lost.Status.Process = status.ProcessLost
	lost.Group = status.GroupCompleted
	unrelated := sWorking("endpoint/other", "codex", "~/Code/x", "", time.Minute)

	cases := []struct {
		name   string
		roster []protocol.SessionView
		want   string
	}{
		{name: "source running", roster: []protocol.SessionView{running, child}, want: supervisionPendingMarker},
		{name: "source exited", roster: []protocol.SessionView{ended, child}, want: supervisionGoneMarker},
		{name: "source lost", roster: []protocol.SessionView{lost, child}, want: supervisionGoneMarker},
		{name: "source off the roster", roster: []protocol.SessionView{unrelated, child}, want: supervisionGoneMarker},
		{name: "child alone", roster: []protocol.SessionView{child}, want: supervisionGoneMarker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gm := generalModel{sessions: tc.roster, width: testCols}
			row := stripANSI(gm.renderRow(child, child.Group, false))
			if !strings.Contains(row, tc.want) {
				t.Fatalf("row missing %q; got:\n%q", tc.want, row)
			}
			other := supervisionGoneMarker
			if tc.want == supervisionGoneMarker {
				other = supervisionPendingMarker
			}
			if strings.Contains(row, other) {
				t.Fatalf("row carries both markers (%q unexpected); got:\n%q", other, row)
			}
		})
	}
}

// The whole board renders the marker from the roster it holds, not only renderRow
// under test-controlled inputs.
func TestRoster_SupervisionMarkerShowsOnTheBoard(t *testing.T) {
	parent := sWorking("endpoint/parent1", "codex", "~/Code/x", "", time.Minute)
	parent.Name = "source-work"
	child := supervisedChild("endpoint/child1")

	m := newModel(t, newFakeClient(parent, child), detectMixed())
	if got := view(m); !strings.Contains(got, supervisionPendingMarker) {
		t.Fatalf("board does not show %q for a pending child:\n%s", supervisionPendingMarker, got)
	}
	gone := sCompleted("endpoint/parent1", "codex", "~/Code/x", "", time.Minute)
	gone.Name = "source-work"
	m = send(m, eventMsg{ev: protocol.Event{Session: gone}})
	got := view(m)
	if !strings.Contains(got, supervisionGoneMarker) || strings.Contains(got, supervisionPendingMarker) {
		t.Fatalf("board does not switch to %q once the source ends:\n%s", supervisionGoneMarker, got)
	}
}

// The marker obeys the same width discipline as the phone-control marker: a row
// never exceeds the terminal, whatever else the tail carries.
func TestRoster_SupervisionMarkerKeepsRowWithinTerminalWidth(t *testing.T) {
	child := supervisedChild("endpoint/child1")
	child.Name = "a very long discussion name that should grow on a wide terminal and clamp on a narrow one"
	child.Cwd = "~/a/very/long/path/to/a/worktree/whose/text/used/to/wrap"
	child.Summary = "A deliberately long summary that must be clamped before the terminal wraps it onto a second line"
	child.SpawnedFrom = "parent-with-a-long-name"
	child.RemoteControlled = true

	for _, width := range []int{48, 72, 92, 120, 160, 200} {
		t.Run(itoa(width), func(t *testing.T) {
			gm := generalModel{sessions: []protocol.SessionView{child}, width: width}
			for _, selected := range []bool{false, true} {
				row := gm.renderRow(child, child.Group, selected)
				if got := lipgloss.Width(row); got > width {
					t.Fatalf("row width = %d, terminal = %d, selected=%v:\n%s", got, width, selected, stripANSI(row))
				}
			}
		})
	}

	// On a comfortable terminal the marker survives the long summary intact.
	gm := generalModel{sessions: []protocol.SessionView{child}, width: 200}
	child.RemoteControlled = false
	if row := stripANSI(gm.renderRow(child, child.Group, false)); !strings.Contains(row, supervisionGoneMarker) {
		t.Errorf("wide row lost the supervision marker to the summary; got:\n%q", row)
	}
}
