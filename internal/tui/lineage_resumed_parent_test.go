package tui

// FAILING-FIRST: a child names its parent by name even after the parent was resumed.
// A resume (or a CLI refresh, or a relogin) continues a discussion under a NEW session
// id and hides the old attempt from the board, while the child's SpawnedFrom still
// holds the attempt it was spawned from. The visible row of that discussion lists the
// hidden attempt in Supersedes, so the board can still name it -- and count the child
// as one it spawned -- instead of falling back to a raw id.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestRoster_ChildNamesAParentThatWasResumed(t *testing.T) {
	current := lineageRow("endpoint/attempt2", "parent-work", "", "")
	current.Supersedes = []string{"endpoint/attempt1"}
	child := lineageRow("endpoint/child9", "child-work", "attempt1", "delegate")

	gm := generalModel{sessions: []protocol.SessionView{current, child}, width: testCols}

	row := stripANSI(gm.renderRow(child, child.Group, false))
	if !strings.Contains(row, "from parent-work") {
		t.Errorf("a child of a resumed discussion must name it; got:\n%q", row)
	}
	if strings.Contains(row, "attempt1") {
		t.Errorf("the child's badge still shows the parent's old session id; got:\n%q", row)
	}

	parentRow := stripANSI(gm.renderRow(current, current.Group, false))
	if !strings.Contains(parentRow, "spawned 1") {
		t.Errorf("the resumed discussion must count the child spawned by its earlier attempt; got:\n%q", parentRow)
	}
}

// The live row wins over a hidden attempt: a parent that is itself on the board is
// named by its own row, and a deleted parent still falls back to its id.
func TestRoster_LineagePrefersTheParentsOwnRow(t *testing.T) {
	own := lineageRow("endpoint/parent1", "own-name", "", "")
	other := lineageRow("endpoint/other2", "other-name", "", "")
	other.Supersedes = []string{"endpoint/parent1"}
	child := lineageRow("endpoint/child9", "child-work", "parent1", "handoff")

	if got := lineageBadge(child, []protocol.SessionView{other, own, child}); !strings.Contains(got, "from own-name") {
		t.Fatalf("lineageBadge = %q, want the parent's own row name", got)
	}
}
