package voice

import (
	"encoding/json"
	"errors"
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

func TestReconciliationRepairsHistoricalEnrolledWorkerProvenance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mailbox bool
		matches int
		want    string
	}{
		{"only VM flag changed", false, 0, "original"},
		{"exact launch", true, 1, "launch1"},
		{"wrong physical worker", true, 0, ""},
		{"ambiguous launch", true, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, store, _, b := newControllerFixture(t)
			c.workers = &controllerWorkerFixture{rows: []protocol.SessionView{}} // The physical worker can already have exited.
			w := WorkerAssociation{ConversationID: b.ConversationID, Session: "machine/worker", SessionInstance: "old", ActionID: "latest-action", JobID: "job", AttemptID: "original", Project: "project", Parent: "manager", VMChild: true, Mailbox: tc.mailbox}
			if tc.mailbox {
				w.AttemptID = "vm-child-launch:copied-from-roster"
			}
			k := key(w.ConversationID, key(w.Session, w.SessionInstance))
			if err := store.update(func(st *State) error {
				putRecord(st, "workers", &st.Workers, k, w)
				origin := discussionFixtureOrigin("source")
				req, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Project: w.Project, Origin: &origin, Launch: &protocol.LaunchReq{SpawnedFrom: w.Parent, Cwd: w.Project}})
				for i, id := range []string{"launch1", "launch2"} {
					instance := "other"
					if i < tc.matches {
						instance = w.SessionInstance
					}
					receipt, _ := json.Marshal(createdWorkerResult{State: "created", Session: w.Session, SessionInstance: instance})
					action := Action{ID: id, ConversationID: w.ConversationID, State: "settled", Request: req, Receipt: receipt}
					putRecord(st, "actions", &st.Actions, key(w.ConversationID, id), action)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			c.discussion = &DiscussionScope{}
			if err := c.reconcileDiscussionWorkerEvidence(t.Context()); err != nil {
				t.Fatal(err)
			}
			after, err := store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			got := after.Workers[k]
			if got.VMChild || got.Mailbox || got.RecoveryRequired != tc.mailbox || got.AttemptID != tc.want || got.ActionID != w.ActionID || got.JobID != w.JobID || len(after.Wakes) != 0 {
				t.Fatalf("repaired worker %+v", got)
			}
			if err := c.reconcileDiscussionWorkerEvidence(t.Context()); err != nil {
				t.Fatal(err)
			}
			again, err := store.Snapshot()
			if err != nil || again.Workers[k] != got || len(again.Wakes) != 0 {
				t.Fatal("repair changed worker on second pass", err)
			}
		})
	}
}

func TestVMObserverCannotAdoptConcurrentEnrolledLaunch(t *testing.T) {
	c, store, _, b := newControllerFixture(t)
	ep, parent, _ := protocol.ParseID(b.SessionID)
	w := WorkerAssociation{ConversationID: b.ConversationID, Session: ep + "/worker", SessionInstance: "exact", ActionID: "created"}
	stale, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	k := key(w.ConversationID, key(w.Session, w.SessionInstance))
	if err := store.update(func(st *State) error { putRecord(st, "workers", &st.Workers, k, w); return nil }); err != nil {
		t.Fatal(err)
	}
	c.workers = &controllerWorkerFixture{rows: []protocol.SessionView{{ID: w.Session, EndpointID: ep, SpawnedFrom: parent, Group: status.GroupReadyForReview, Status: status.Status{Process: status.ProcessRunning}}}}
	if _, err := c.observeVMWorkers(stale); !errors.Is(err, ErrStale) {
		t.Fatal("expected current enrollment to win", err)
	}
	st, err := store.Snapshot()
	if err != nil || len(st.Workers) != 1 || st.Workers[k] != w || len(st.Wakes) != 0 {
		t.Fatal("observer changed original worker or woke the dispatcher", err)
	}
}
