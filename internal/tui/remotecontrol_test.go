package tui

// agents-tracker-nx44.7 -- the board's half of the roster badge. A session a paired
// device currently controls carries a small marker on its row, so the owner can see
// at the board (not only after attaching) that the phone holds the lease.
//
// Shape-tolerant on purpose: the assertion requires the marker to be PRESENT and to
// name the controlling surface ("phone"), not any particular wording -- the exact
// token is a rendering choice.
//
// RED today: SessionView has no RemoteControlled field, so this file does not
// compile.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestRoster_RemoteControlledRowIsMarked(t *testing.T) {
	held := sWorking("endpoint/held1", "claude", "~/Code/x", "", time.Minute)
	held.Name = "held-work"
	held.RemoteControlled = true

	free := sWorking("endpoint/free2", "claude", "~/Code/x", "", time.Minute)
	free.Name = "free-work"

	gm := generalModel{sessions: []protocol.SessionView{held, free}, width: testCols}

	heldRow := stripANSI(gm.renderRow(held, held.Group, false))
	if !strings.Contains(heldRow, "phone") {
		t.Errorf("a remote-controlled session's row must carry a control marker naming the phone; got:\n%q", heldRow)
	}

	freeRow := stripANSI(gm.renderRow(free, free.Group, false))
	if strings.Contains(freeRow, "phone") {
		t.Errorf("an uncontrolled session must carry no control marker; got:\n%q", freeRow)
	}
}

// TestRoster_ControlMarkerCoexistsWithLineage: the marker is additive -- a row that
// already shows a lineage badge keeps it.
func TestRoster_ControlMarkerCoexistsWithLineage(t *testing.T) {
	child := sWorking("endpoint/child9", "claude", "~/Code/x", "", time.Minute)
	child.Name = "child-work"
	child.SpawnedFrom = "parent1"
	child.SpawnIntent = "handoff"
	child.RemoteControlled = true

	gm := generalModel{sessions: []protocol.SessionView{child}, width: testCols}
	row := stripANSI(gm.renderRow(child, child.Group, false))
	if !strings.Contains(row, "phone") {
		t.Errorf("the control marker was dropped from a row that also carries lineage; got:\n%q", row)
	}
	if !strings.Contains(row, "from") || !strings.Contains(row, "parent1") {
		t.Errorf("the control marker displaced the lineage badge; got:\n%q", row)
	}
}
