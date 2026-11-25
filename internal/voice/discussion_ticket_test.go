package voice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestTicketWorkerCompletionBindsOriginalDispatcherAndCurrentNativeProof(t *testing.T) {
	d := DiscussionController{cfg: Config{ConversationID: "conversation"}}
	st, worker := completedTicketWorkerFixture("/approved")
	completion := d.ticketWorkerCompletion(st)
	if completion == nil || completion.AdmissionID != "ta_ticket" ||
		completion.ManagerSession != "owner/original-manager" || completion.ManagerInstance != "original-instance" ||
		completion.WorkerSession != worker.Session || completion.WorkerInstance != worker.SessionInstance ||
		completion.JobID != worker.JobID || completion.AttemptID != worker.AttemptID ||
		completion.ActionID != worker.ActionID || completion.NativeTurnID != "native-worker-turn" {
		t.Fatalf("lost exact completion coordinates: %+v", completion)
	}

	workerKey := key(worker.ConversationID, key(worker.Session, worker.SessionInstance))
	eventKey := key(worker.ConversationID, discussionEvidenceID(worker))
	admission := admissionKey(worker.ConversationID, 1)
	cases := map[string]func(*State){
		"worker note alone": func(st *State) {
			event := st.Events[eventKey]
			event.WorkerProof, event.WorkerMessage = false, true
			st.Events[eventKey] = event
		},
		"failed native turn": func(st *State) {
			event := st.Events[eventKey]
			event.Metadata = json.RawMessage(`{"session":"owner/worker","instance":"worker-instance","data":{"turn_id":"native-worker-turn","state":"failed"}}`)
			st.Events[eventKey] = event
		},
		"foreign worker instance": func(st *State) {
			event := st.Events[eventKey]
			event.Metadata = json.RawMessage(`{"session":"owner/worker","instance":"other","data":{"turn_id":"native-worker-turn","state":"completed"}}`)
			st.Events[eventKey] = event
		},
		"forged manager nonce": func(st *State) {
			source := st.Admissions[admission]
			var forged protocol.ComposerSendReq
			_ = json.Unmarshal(source.Request, &forged)
			forged.ManagerAdmission.Nonce = strings.Repeat("b", 64)
			source.Request, _ = json.Marshal(forged)
			st.Admissions[admission] = source
		},
		"new worker action": func(st *State) {
			newer := st.Workers[workerKey]
			newer.ActionID = "new"
			st.Workers[workerKey] = newer
		},
		"pending worker effect": func(st *State) {
			st.Actions["pending"] = Action{ConversationID: worker.ConversationID, Target: key(worker.Session, worker.SessionInstance), State: "executing"}
		},
		"recovery only": func(st *State) {
			event := st.Events[eventKey]
			event.RecoveryOnly = true
			st.Events[eventKey] = event
		},
	}
	for label, mutate := range cases {
		t.Run(label, func(t *testing.T) {
			st, _ := completedTicketWorkerFixture("/approved")
			mutate(&st)
			if completion := d.ticketWorkerCompletion(st); completion != nil {
				t.Fatalf("unqualified history returned a completion: %+v", completion)
			}
		})
	}
}

func completedTicketWorkerFixture(project string) (State, WorkerAssociation) {
	worker := WorkerAssociation{ConversationID: "conversation", Session: "owner/worker", SessionInstance: "worker-instance",
		Project: project, Parent: "original-manager", JobID: "action", AttemptID: "action", ActionID: "action", InitialSourceID: "discussion:ta_ticket"}
	origin := discussionFixtureOrigin("ta_ticket")
	origin.Kind, origin.ActorID, origin.RequesterID, origin.DiscussionID, origin.Revision = "ticket_assignment", "cerbere", "cerbere", "discussion", "revision"
	input := protocol.ComposerSendReq{Session: "owner/original-manager", SessionInstance: "original-instance", Origin: &origin,
		ManagerAdmission: &protocol.ManagerAdmission{
			Nonce: strings.Repeat("a", 64),
			Binding: protocol.ManagerRuntimeBinding{Session: "owner/original-manager", SessionInstance: "original-instance",
				ConversationID: worker.ConversationID, Provider: "codex", NativeSessionID: "native-session",
				NativeGeneration: "native-generation", ObservationGeneration: "observation-generation", LoginEpoch: "login-epoch"},
		},
	}
	rawInput, _ := json.Marshal(input)
	intent, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Origin: &origin, Project: worker.Project,
		Launch: &protocol.LaunchReq{Agent: "codex", Cwd: worker.Project, SpawnedFrom: worker.Parent, InitialPrompt: "authorized synthetic ticket"}})
	receipt, _ := json.Marshal(createdWorkerResult{State: "created", Session: worker.Session, SessionInstance: worker.SessionInstance})
	eventID := discussionEvidenceID(worker)
	return State{
		Conversations: map[string]Conversation{worker.ConversationID: {Binding: ManagerBinding{SessionID: "owner/replacement", SessionInstance: "replacement-instance"}}},
		Workers:       map[string]WorkerAssociation{key(worker.ConversationID, key(worker.Session, worker.SessionInstance)): worker},
		Admissions: map[string]Admission{admissionKey(worker.ConversationID, 1): {ConversationID: worker.ConversationID, Project: worker.Project, Sequence: 1,
			Origin: &origin, SourceCutover: true, NativeTurnID: "native-manager-turn", NativeInputID: input.ManagerAdmission.Nonce, Request: rawInput}},
		Actions: map[string]Action{key(worker.ConversationID, worker.ActionID): {ID: worker.ActionID, ConversationID: worker.ConversationID,
			Admission: 1, NativeInputID: input.ManagerAdmission.Nonce, Request: intent, Receipt: receipt, State: "settled"}},
		Events: map[string]Event{key(worker.ConversationID, eventID): {ID: eventID, ConversationID: worker.ConversationID,
			JobID: worker.JobID, AttemptID: worker.AttemptID, NativeTurnID: "native-worker-turn", WorkerProof: true,
			Metadata: json.RawMessage(`{"session":"owner/worker","instance":"worker-instance","data":{"turn_id":"native-worker-turn","state":"completed","text":"untrusted worker text"}}`)}},
	}, worker
}

func TestTicketCompletionProjectsTheReconciledControllerWorkerProof(t *testing.T) {
	c, store, _, binding := newControllerFixture(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := completedTicketWorkerFixture(project)
	c.discussion = &DiscussionScope{ProjectEnrolled: func(candidate string) bool { return candidate == project }}
	worker := &discussionEvidenceWorker{}
	c.workers = worker
	if err := store.update(func(st *State) error {
		conversation := st.Conversations[binding.ConversationID]
		conversation.LastAdmission = 1
		putRecord(st, "conversations", &st.Conversations, binding.ConversationID, conversation)
		for key, association := range fixture.Workers {
			putRecord(st, "workers", &st.Workers, key, association)
		}
		for key, admission := range fixture.Admissions {
			putRecord(st, "admissions", &st.Admissions, key, admission)
		}
		for key, action := range fixture.Actions {
			putRecord(st, "actions", &st.Actions, key, action)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.reconcileDiscussionWorkerEvidence(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := DiscussionController{store: store, cfg: Config{ConversationID: binding.ConversationID}}
	status, err := d.CheckoutStatus()
	if err != nil || status.TicketCompletion == nil || status.TicketCompletion.NativeTurnID != "exact-turn" || worker.nativeReads != 1 {
		t.Fatalf("reconciled native proof was not projected: %+v reads=%d %v", status.TicketCompletion, worker.nativeReads, err)
	}
}

func TestTicketCompletionUsesLatestVerifiedFollowUp(t *testing.T) {
	d := DiscussionController{cfg: Config{ConversationID: "conversation"}}
	st, worker := completedTicketWorkerFixture("/approved")
	originalEventKey := key(worker.ConversationID, discussionEvidenceID(worker))
	worker.ActionID = "follow-up"
	workerTarget := key(worker.Session, worker.SessionInstance)
	st.Workers[key(worker.ConversationID, workerTarget)] = worker
	human := discussionFixtureOrigin("operator-follow-up")
	request, _ := json.Marshal(controllerActionRequest{Tool: "swarm_send", Origin: &human,
		Send: &protocol.ComposerSendReq{Session: worker.Session, SessionInstance: worker.SessionInstance}})
	st.Actions[key(worker.ConversationID, worker.ActionID)] = Action{ID: worker.ActionID, ConversationID: worker.ConversationID,
		State: "settled", Target: workerTarget, Request: request}
	if completion := d.ticketWorkerCompletion(st); completion != nil {
		t.Fatalf("superseded initial proof was accepted: %+v", completion)
	}
	followUp := st.Events[originalEventKey]
	followUp.ID, followUp.NativeTurnID = discussionEvidenceID(worker), "follow-up-turn"
	followUp.Metadata = json.RawMessage(`{"session":"owner/worker","instance":"worker-instance","data":{"turn_id":"follow-up-turn","state":"completed"}}`)
	st.Events[key(worker.ConversationID, followUp.ID)] = followUp
	completion := d.ticketWorkerCompletion(st)
	if completion == nil || completion.ActionID != worker.ActionID || completion.NativeTurnID != "follow-up-turn" || completion.ManagerSession != "owner/original-manager" {
		t.Fatalf("latest follow-up lost its original dispatcher or exact action: %+v", completion)
	}
}
