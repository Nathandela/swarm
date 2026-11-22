package voice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestStoredVMStatusUsesCurrentWorkerProvenance(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		enrolled, mailbox, vm, wantSend bool
	}{
		{"enrolled old status in VM", true, false, true, false},
		{"enrolled old status after rollback", true, false, false, false},
		{"ambiguous old mailbox", true, true, true, false},
		{"ordinary VM without mailbox", false, false, true, true},
		{"ordinary VM with mailbox", false, true, true, true},
		{"VM status outside VM policy", false, false, false, false},
	} {
		for _, phase := range []string{"", "prepared"} {
			t.Run(tc.name+"/"+phase, func(t *testing.T) {
				c, store, runtime, b := newControllerFixture(t)
				ep, parent, _ := protocol.ParseID(b.SessionID)
				row := protocol.SessionView{ID: ep + "/child", EndpointID: ep, SpawnedFrom: parent, Group: status.GroupReadyForReview, Status: status.Status{Process: status.ProcessRunning}, Capabilities: &protocol.SessionCapabilities{SessionInstance: "physical"}}
				if tc.mailbox {
					row.WorkerDiscussion, row.WorkerInstance, row.WorkerAttempt = b.ConversationID, "physical", "vm-attempt"
				}
				c.workers = &controllerWorkerFixture{rows: []protocol.SessionView{row}}
				c.discussion = &DiscussionScope{VMProjectAccess: true, VerifyAudience: func(context.Context) error { return nil }}
				st, err := store.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if _, err = c.observeVMWorkers(st); err != nil {
					t.Fatal(err)
				}
				st, err = store.Snapshot()
				if err != nil || len(st.Wakes) != 1 {
					t.Fatalf("expected one original VM status: %v %v", st.Wakes, err)
				}
				var wake Wake
				for _, entry := range st.Wakes {
					wake = entry
					break
				}
				wake, err = store.AdvanceWake(b, wake.ID, "admitted", "")
				if err != nil {
					t.Fatal(err)
				}
				if err = store.update(func(st *State) error {
					if tc.enrolled {
						for k, w := range st.Workers {
							w.ActionID = "original-enrolled-launch"
							putRecord(st, "workers", &st.Workers, k, w)
						}
					}
					if phase == "prepared" {
						a := st.Admissions[admissionKey(b.ConversationID, wake.Admission)]
						a.Phase, a.OperationID, a.NativeInputID = phase, wake.OperationID, strings.Repeat("a", 64)
						a.Request, err = json.Marshal(protocol.ComposerSendReq{Session: b.SessionID, SessionInstance: b.SessionInstance, Text: "worker status", ManagerAdmission: &protocol.ManagerAdmission{Binding: runtimeBinding(b), Nonce: a.NativeInputID}})
						if err != nil {
							return err
						}
						putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				c.discussion.VMProjectAccess = tc.vm
				if tc.vm {
					st, err = store.Snapshot()
					if err != nil {
						t.Fatal(err)
					}
					if _, err = c.observeVMWorkers(st); err != nil {
						t.Fatal(err)
					}
				}
				if err = c.reconcileDiscussionWorkerEvidence(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err = c.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
				st, err = store.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				a := st.Admissions[admissionKey(b.ConversationID, wake.Admission)]
				if tc.wantSend {
					if len(runtime.sent) != 1 {
						t.Fatalf("genuine VM status lost: %+v", a)
					}
				} else if len(runtime.sent) != 0 || a.Phase != "rejected" || a.SettledAt.IsZero() {
					t.Fatalf("legacy status sent %d, phase %q", len(runtime.sent), a.Phase)
				}
			})
		}
	}
}

func TestMailboxRosterPromotesProvisionalVMWorkerAcrossRestart(t *testing.T) {
	for _, route := range []string{"roster", "retained duplicate", "note before roster"} {
		t.Run(route, func(t *testing.T) {
			c, store, _, _ := provenControllerFixture(t)
			ep, parent, _ := protocol.ParseID(c.binding.SessionID)
			row := protocol.SessionView{ID: ep + "/child", EndpointID: ep, SpawnedFrom: parent, Cwd: "/fixture", Group: status.GroupWorking, Status: status.Status{Process: status.ProcessRunning}}
			workers := &controllerWorkerFixture{rows: []protocol.SessionView{row}}
			c.workers = workers
			st, err := store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.observeVMWorkers(st); err != nil {
				t.Fatal(err)
			}
			row.WorkerDiscussion, row.WorkerInstance, row.WorkerAttempt = c.binding.ConversationID, "physical", "attempt"
			workers.rows[0] = row
			alias := key(c.binding.ConversationID, key(row.ID, row.ID))
			exact := key(c.binding.ConversationID, key(row.ID, row.WorkerInstance))
			if route == "retained duplicate" {
				err = store.update(func(st *State) error {
					w := st.Workers[alias]
					w.Mailbox, w.SessionInstance, w.AttemptID = true, row.WorkerInstance, row.WorkerAttempt
					putRecord(st, "workers", &st.Workers, alias, w)
					putRecord(st, "workers", &st.Workers, exact, w)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			st, err = store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if route == "note before roster" {
				w := st.Workers[alias]
				w.SessionInstance, w.AttemptID, w.Mailbox = row.WorkerInstance, row.WorkerAttempt, true
				input := workerMessageInput{ID: "explicit-note", Kind: "needs-input", Text: "Proceed?"}
				_, err = c.queueWorkerNote(w, input, workerNoteMetadata{Session: w.Session, Instance: w.SessionInstance, Kind: input.Kind, NoteID: input.ID, Text: input.Text})
			} else {
				_, err = c.observeVMWorkers(st)
			}
			if err != nil {
				t.Fatal(err)
			}
			st, err = store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Workers) != 1 || !st.Workers[exact].supervisorMailbox() || st.Workers[exact].AttemptID != row.WorkerAttempt {
				t.Fatalf("want one exact association, got %+v", st.Workers)
			}
		})
	}
}

func TestMailboxRosterLeavesConflictingProvisionalAttemptUntouched(t *testing.T) {
	c, store, _, _ := provenControllerFixture(t)
	ep, parent, _ := protocol.ParseID(c.binding.SessionID)
	row := protocol.SessionView{ID: ep + "/child", EndpointID: ep, SpawnedFrom: parent, Cwd: "/fixture", Group: status.GroupWorking,
		WorkerDiscussion: c.binding.ConversationID, WorkerAttempt: "new-attempt", WorkerInstance: "same-physical", Status: status.Status{Process: status.ProcessRunning}}
	c.workers = &controllerWorkerFixture{rows: []protocol.SessionView{row}}
	old := WorkerAssociation{VMChild: true, Mailbox: true, ConversationID: c.binding.ConversationID, Session: row.ID, SessionInstance: row.WorkerInstance,
		Parent: parent, Project: row.Cwd, AttemptID: "old-attempt", JobID: "vm-child-" + hashValue([]string{c.binding.ConversationID, row.ID}), RecoveryRequired: true}
	alias := key(c.binding.ConversationID, key(row.ID, row.ID))
	if err := store.update(func(st *State) error { putRecord(st, "workers", &st.Workers, alias, old); return nil }); err != nil {
		t.Fatal(err)
	}
	st, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.observeVMWorkers(st); err != nil {
		t.Fatal(err)
	}
	st, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	w := st.Workers[key(c.binding.ConversationID, key(row.ID, row.WorkerInstance))]
	if st.Workers[alias] != old || !w.supervisorMailbox() || w.AttemptID != row.WorkerAttempt {
		t.Fatalf("ambiguous old attempt changed or new mailbox absent: %+v", st.Workers)
	}
}
