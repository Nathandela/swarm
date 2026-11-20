package voice

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestVMWorkersWakeTheirThreadOncePerObservedTransition(t *testing.T) {
	c, store, _, _ := provenControllerFixture(t)
	endpoint, parent, ok := protocol.ParseID(c.binding.SessionID)
	if !ok {
		t.Fatal(c.binding.SessionID)
	}
	now := time.Now().UTC()
	workers := &controllerWorkerFixture{rows: []protocol.SessionView{
		{ID: endpoint + "/child", EndpointID: endpoint, SpawnedFrom: parent, Group: status.GroupWorking, GroupEnteredAt: now, Status: status.Status{Process: status.ProcessRunning}},
		{ID: endpoint + "/other", EndpointID: endpoint, SpawnedFrom: "unrelated", Group: status.GroupCompleted, GroupEnteredAt: now},
	}}
	c.workers = workers
	sync := func(want int) {
		t.Helper()
		state, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.observeVMWorkers(state); err != nil {
			t.Fatal(err)
		}
		state, err = store.Snapshot()
		if err != nil || len(state.Workers) != 1 || len(state.Wakes) != want {
			t.Fatalf("workers %d wakes %d err %v", len(state.Workers), len(state.Wakes), err)
		}
	}
	sync(0)
	workers.rows[0].Group = status.GroupReadyForReview
	sync(1)
	sync(1)
	workers.rows[0].GroupEnteredAt = now.Add(time.Minute)
	sync(2)
}
