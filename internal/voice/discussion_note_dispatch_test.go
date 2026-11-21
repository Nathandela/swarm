package voice

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestWorkerEvidenceRechecksOriginalGrantBeforeNativeDispatch(t *testing.T) {
	for _, test := range []struct {
		name               string
		note, recovery, vm bool
	}{
		{"ordinary enrolled note", true, false, false}, {"recovered enrolled note", true, true, false},
		{"enrolled note retained under VM policy", true, false, true}, {"native turn proof", false, true, false}, {"native turn proof after VM policy switch", false, true, true},
	} {
		for _, phase := range []string{"", "prepared"} {
			t.Run(test.name+"/phase-"+phase, func(t *testing.T) {
				c, store, runtime, binding := newControllerFixture(t)
				project := t.TempDir()
				origin := discussionFixtureOrigin("opening")
				origin.DiscussionID, origin.Revision = "discussion", "revision"
				var checks int
				c.discussion = &DiscussionScope{VMProjectAccess: test.vm, DiscussionID: origin.DiscussionID, InstallationID: origin.InstallationID, GuildID: origin.GuildID, ThreadID: origin.ChannelID,
					ProjectEnrolled: func(path string) bool { return path == project },
					VerifyAudience:  func(context.Context) error { return nil },
					VerifyGrant: func(_ context.Context, got schema.MessageOrigin) error {
						if got != origin {
							t.Fatalf("checked wrong origin: %+v", got)
						}
						checks++
						return ErrDiscussionSourceChanged
					},
				}
				endpoint, parent, _ := protocol.ParseID(binding.SessionID)
				w := WorkerAssociation{ActionID: "attempt", Parent: parent, ConversationID: binding.ConversationID, Session: endpoint + "/worker", SessionInstance: "inc", JobID: "attempt", AttemptID: "attempt", RecoveryRequired: test.recovery, Ended: true, Project: project}
				metadata, _ := json.Marshal(workerNoteMetadata{Session: w.Session, Instance: w.SessionInstance, Kind: "complete", Text: "private result"})
				request, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Project: project, Origin: &origin, Launch: &protocol.LaunchReq{Cwd: project}})
				var seq uint64
				if err := store.update(func(st *State) error {
					a, err := allocateAdmission(st, binding, "wake:note")
					if err != nil {
						return err
					}
					seq = a.Sequence
					a.RecoveryOnly, a.Phase = test.recovery, phase
					if phase == "prepared" {
						a.OperationID, err = protocol.NewSemanticOperationID()
						if err != nil {
							return err
						}
						a.NativeInputID = strings.Repeat("a", 64)
						a.Request, err = json.Marshal(protocol.ComposerSendReq{Session: binding.SessionID, SessionInstance: binding.SessionInstance, Text: "private result", ManagerAdmission: &protocol.ManagerAdmission{Binding: runtimeBinding(binding), Nonce: a.NativeInputID}})
						if err != nil {
							return err
						}
					}
					putRecord(st, "admissions", &st.Admissions, admissionKey(w.ConversationID, seq), a)
					putRecord(st, "workers", &st.Workers, key(w.ConversationID, key(w.Session, w.SessionInstance)), w)
					putRecord(st, "actions", &st.Actions, key(w.ConversationID, w.AttemptID), Action{State: "settled", Request: request})
					putRecord(st, "wakes", &st.Wakes, key(w.ConversationID, "note"), Wake{ID: "note", ConversationID: w.ConversationID, Admission: seq, State: "admitted", EventID: "event", Request: json.RawMessage(`{"text":"private result"}`)})
					putRecord(st, "events", &st.Events, key(w.ConversationID, "event"), Event{ID: "event", WakeID: "note", WorkerMessage: test.note, WorkerProof: !test.note, JobID: w.JobID, AttemptID: w.AttemptID, Metadata: metadata})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if test.vm {
					c.workers = &controllerWorkerFixture{rows: []protocol.SessionView{{ID: w.Session, EndpointID: endpoint, SpawnedFrom: parent, Group: status.GroupWorking, Status: status.Status{Process: status.ProcessRunning}}}}
					st, err := store.Snapshot()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := c.observeVMWorkers(st); err != nil {
						t.Fatal(err)
					}
				}
				if err := c.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
				st, err := store.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				a := st.Admissions[admissionKey(w.ConversationID, seq)]
				if st.Workers[key(w.ConversationID, key(w.Session, w.SessionInstance))].VMChild || checks != 1 || len(runtime.sent) != 0 || a.Phase != "rejected" || a.SettledAt.IsZero() {
					t.Fatalf("checks %d, sends %d, phase %q", checks, len(runtime.sent), a.Phase)
				}
			})
		}
	}
}
