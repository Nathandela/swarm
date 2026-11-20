package tui

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestManagerBadge(t *testing.T) {
	manager := protocol.SessionView{ID: "manager", Role: "manager", Name: "A long manager conversation", Group: status.GroupWorking}
	worker := protocol.SessionView{ID: "worker", Name: "Worker", Group: status.GroupWorking}
	for _, width := range []int{40, 120} {
		m := newGeneralModel([]protocol.SessionView{manager, worker})
		m.width = width
		if row := m.renderRow(manager, manager.Group, false); !strings.Contains(row, "◈") || !strings.Contains(row, "[manager]") {
			t.Errorf("manager badge missing at width %d: %q", width, row)
		}
		if row := m.renderRow(worker, worker.Group, false); strings.Contains(row, "◈") || strings.Contains(row, "[manager]") {
			t.Errorf("unrelated session has manager badge at width %d: %q", width, row)
		}
		m.setLayout(groupByTag, orderByArrival)
		if m.groupKey(manager) != "manager" || m.groupKey(worker) != untaggedGroup {
			t.Errorf("manager role missing from tag grouping")
		}
	}
}
