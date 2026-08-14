package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestResponsiveColumnsKeepBoundedFieldsAndGrowIdentityFields(t *testing.T) {
	base := rowColumnsFor(120, 2)
	wide := rowColumnsFor(180, 2)
	if base.name != colName || base.cwd != colCwd || base.summary != colSummaryBaseline {
		t.Fatalf("120-column baseline = %+v; want name=%d cwd=%d summary=%d", base, colName, colCwd, colSummaryBaseline)
	}
	if wide.name <= base.name || wide.cwd <= base.cwd {
		t.Fatalf("wide identity columns = %+v; both must grow from %+v", wide, base)
	}
	if colAgent != 9 || colStatus != 17 || colElapsed != 6 {
		t.Fatalf("bounded semantic columns changed: agent=%d status=%d elapsed=%d", colAgent, colStatus, colElapsed)
	}
}

func TestResponsiveRowsNeverExceedTerminalWidth(t *testing.T) {
	s := sWorking(
		"endpoint/child",
		"opencode-with-an-impossible-suffix",
		"~/a/very/long/path/to/a/worktree/whose/text/used/to/wrap",
		"A deliberately long summary that must be clamped before the terminal wraps it onto a second line",
		time.Minute,
	)
	s.Name = "a very long discussion name that should grow on a wide terminal and clamp on a narrow one"
	s.RemoteControlled = true
	s.SpawnedFrom = "parent-with-a-long-name"

	for _, width := range []int{48, 72, 92, 120, 160, 200} {
		t.Run(itoa(width), func(t *testing.T) {
			gm := generalModel{sessions: []protocol.SessionView{s}, width: width}
			for _, confirm := range []bool{false, true} {
				gm.confirm = confirm
				gm.confirmID = s.ID
				row := gm.renderRow(s, status.GroupWorking, true)
				if got := lipgloss.Width(row); got > width {
					t.Fatalf("row width = %d, terminal = %d, confirm=%v:\n%s", got, width, confirm, stripANSI(row))
				}
			}
		})
	}
}

func TestWorkingIndicatorAdvancesOnExistingRepaint(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "building", time.Minute))
	m := newModel(t, f, detectMixed())
	before := lineContaining(view(m), "building")
	if !strings.Contains(before, "◐") {
		t.Fatalf("initial Working frame must preserve the current ◐ icon:\n%s", before)
	}

	m = send(m, repaintMsg{})
	after := lineContaining(view(m), "building")
	if !strings.Contains(after, "◓") || strings.Contains(after, "◐") {
		t.Fatalf("one existing repaint must advance Working to ◓:\n%s", after)
	}
}
