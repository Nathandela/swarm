package tui

// FAILING-FIRST tests for ADR-010 Phase 2 PIECE 3 (D4): the roster shows session
// lineage. A session whose SessionView.SpawnedFrom is set carries a small textual
// badge naming its parent; a session that another VISIBLE row names in SpawnedFrom
// carries a badge saying it spawned children. Both are computed from the roster
// slice the model already holds — no new RPC, no extra state.
//
// SpawnedFrom holds the LOCAL id of the parent (the value the daemon injects as
// SWARM_SESSION_ID, exactly as Meta.ResumedFrom does), while a row's ID is
// namespaced <endpoint>/<local> — so the match is against the local half
// (protocol.ParseID).
//
// The assertions are deliberately shape-tolerant: they require the badge to be
// PRESENT and to NAME the related session (its short id or its name — both spell
// "parent"/"child" here), not any particular wording, since the exact token is a
// rendering choice.
//
// RED today: SessionView has no SpawnedFrom field, so this file fails to compile.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// lineageRow builds one working roster row with an explicit name and lineage. The
// summary is empty so nothing but the badge can put text past the status columns.
func lineageRow(id, name, spawnedFrom, intent string) protocol.SessionView {
	v := sWorking(id, "claude", "~/Code/x", "", time.Minute)
	v.Name = name
	v.SpawnedFrom = spawnedFrom
	v.SpawnIntent = intent
	return v
}

// TestRoster_LineageBadges pins all three cases against one roster: the child names
// its parent, the parent reports that it spawned, and an unrelated session shows
// neither badge (no session is decorated with a link it does not have).
func TestRoster_LineageBadges(t *testing.T) {
	parent := lineageRow("endpoint/parent1", "parent-work", "", "")
	child := lineageRow("endpoint/child9", "child-work", "parent1", "handoff")
	other := lineageRow("endpoint/solo7", "solo-work", "", "")

	gm := generalModel{sessions: []protocol.SessionView{parent, child, other}, width: testCols}

	childRow := stripANSI(gm.renderRow(child, child.Group, false))
	if !strings.Contains(childRow, "from") {
		t.Errorf("a spawned session's row must carry a lineage badge; got:\n%q", childRow)
	}
	if !strings.Contains(childRow, "parent") {
		t.Errorf("the child's badge must name its parent (short id or name); got:\n%q", childRow)
	}

	parentRow := stripANSI(gm.renderRow(parent, parent.Group, false))
	if !strings.Contains(parentRow, "spawned") {
		t.Errorf("a session another visible row was spawned from must say so; got:\n%q", parentRow)
	}

	otherRow := stripANSI(gm.renderRow(other, other.Group, false))
	if strings.Contains(otherRow, "from") || strings.Contains(otherRow, "spawned") {
		t.Errorf("an unrelated session must carry no lineage badge; got:\n%q", otherRow)
	}
}

// TestRoster_OrphanChildStillBadged: the parent may already have been closed (the
// source session stays alive after a handoff only until the user closes it — D4), so
// a child whose parent is no longer on the roster still shows where it came from.
func TestRoster_OrphanChildStillBadged(t *testing.T) {
	child := lineageRow("endpoint/child9", "child-work", "ghost-parent", "delegate")
	gm := generalModel{sessions: []protocol.SessionView{child}, width: testCols}

	row := stripANSI(gm.renderRow(child, child.Group, false))
	if !strings.Contains(row, "from") || !strings.Contains(row, "ghost-parent") {
		t.Errorf("a child whose parent has left the roster must still name it; got:\n%q", row)
	}
}

// TestRoster_LineageBadgesRenderInTheView: the badges reach the painted roster, not
// just the row helper.
func TestRoster_LineageBadgesRenderInTheView(t *testing.T) {
	parent := lineageRow("endpoint/parent1", "parent-work", "", "")
	child := lineageRow("endpoint/child9", "child-work", "parent1", "handoff")

	gm := generalModel{sessions: []protocol.SessionView{parent, child}, width: testCols}
	out := stripANSI(gm.view())
	if !strings.Contains(out, "from") || !strings.Contains(out, "spawned") {
		t.Errorf("the rendered roster shows no lineage badges:\n%s", out)
	}
}

// keep the status import used if a future edit drops the only reference.
var _ = status.GroupWorking
