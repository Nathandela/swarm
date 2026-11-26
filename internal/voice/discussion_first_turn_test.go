package voice

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestDiscussionWorkerFirstTurnUsesReconciledNativeEvidence(t *testing.T) {
	c, store, _, binding := newControllerFixture(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := firstWorkerTurnFixture(binding.ConversationID, project)
	c.discussion = &DiscussionScope{ProjectEnrolled: func(candidate string) bool { return candidate == project }}
	worker := &discussionEvidenceWorker{}
	c.workers = worker
	if err = store.update(func(st *State) error {
		conversation := st.Conversations[binding.ConversationID]
		conversation.LastAdmission = 1
		putRecord(st, "conversations", &st.Conversations, binding.ConversationID, conversation)
		for k, association := range fixture.Workers {
			putRecord(st, "workers", &st.Workers, k, association)
		}
		for k, admission := range fixture.Admissions {
			putRecord(st, "admissions", &st.Admissions, k, admission)
		}
		for k, action := range fixture.Actions {
			putRecord(st, "actions", &st.Actions, k, action)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	controller := DiscussionController{store: store, cfg: Config{ConversationID: binding.ConversationID}}
	if before, err := controller.CheckoutStatus(); err != nil || len(before.WorkerFirstTurns) != 0 {
		t.Fatal(before.WorkerFirstTurns, err)
	}
	if err = c.reconcileDiscussionWorkerEvidence(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err := controller.CheckoutStatus()
	if err != nil || len(status.WorkerFirstTurns) != 1 || status.WorkerFirstTurns[0].NativeTurnID != "exact-turn" || worker.nativeReads != 1 {
		t.Fatalf("reconciled proof %v, native reads %d, error %v", status.WorkerFirstTurns, worker.nativeReads, err)
	}
}

func TestDiscussionWorkerFirstTurnRetainsOriginalCoordinates(t *testing.T) {
	t.Run("later action cannot replace the launch proof", func(t *testing.T) {
		st, original := firstWorkerTurnFixture("conversation", "/project")
		newer := original
		newer.ActionID = "later-action"
		st.Workers[key(original.ConversationID, key(original.Session, original.SessionInstance))] = newer
		got := discussionWorkerFirstTurns(st, "conversation")
		if len(got) != 1 || got[0].AdmissionID != "external_event" ||
			got[0].ManagerSession != "owner/initial" || got[0].ManagerInstance != "initial-instance" ||
			got[0].WorkerSession != original.Session || got[0].WorkerInstance != original.SessionInstance ||
			got[0].JobID != original.JobID || got[0].AttemptID != original.AttemptID ||
			got[0].ActionID != original.ActionID || got[0].NativeTurnID != "completed-turn" {
			t.Fatal(got)
		}
	})

	for _, tc := range []string{"worker note", "wrong instance", "failed native turn", "later turn only", "stale admission", "different launch origin"} {
		t.Run(tc, func(t *testing.T) {
			st, worker := firstWorkerTurnFixture("conversation", "/project")
			eventID := discussionEvidenceID(worker)
			eventKey := key("conversation", eventID)
			event := st.Events[eventKey]
			operationKey := key("conversation", worker.ActionID)
			sourceKey := admissionKey("conversation", 1)
			switch tc {
			case "worker note":
				event.WorkerMessage = true
			case "wrong instance":
				event.Metadata = json.RawMessage(strings.Replace(string(event.Metadata), "worker-instance", "wrong-instance", 1))
			case "failed native turn":
				event.Metadata = json.RawMessage(strings.Replace(string(event.Metadata), `"state":"completed"`, `"state":"failed"`, 1))
			case "later turn only":
				delete(st.Events, eventKey)
				worker.ActionID = "later-action"
				event.ID = discussionEvidenceID(worker)
				eventKey = key("conversation", event.ID)
			case "stale admission":
				source := st.Admissions[sourceKey]
				source.SourceCutover = false
				st.Admissions[sourceKey] = source
			case "different launch origin":
				action := st.Actions[operationKey]
				action.Request = json.RawMessage(strings.Replace(string(action.Request), "external_event", "unrelated_event", 1))
				st.Actions[operationKey] = action
			}
			st.Events[eventKey] = event
			if got := discussionWorkerFirstTurns(st, "conversation"); len(got) != 0 {
				t.Fatalf("unverified worker receipt %v", got)
			}
		})
	}
}

func firstWorkerTurnFixture(conversation, project string) (State, WorkerAssociation) {
	worker := WorkerAssociation{ConversationID: conversation, Session: "owner/worker", SessionInstance: "worker-instance",
		Project: project, Parent: "initial", JobID: "create-action", AttemptID: "create-action", ActionID: "create-action", InitialSourceID: "discussion:external_event"}
	origin := discussionFixtureOrigin("external_event")
	origin.DiscussionID, origin.Revision = "discussion", "revision"
	input := protocol.ComposerSendReq{Session: "owner/initial", SessionInstance: "initial-instance", Origin: &origin,
		ManagerAdmission: &protocol.ManagerAdmission{Nonce: strings.Repeat("a", 64), Binding: protocol.ManagerRuntimeBinding{
			Session: "owner/initial", SessionInstance: "initial-instance", ConversationID: conversation,
			Provider: "claude", NativeSessionID: "native-session", NativeGeneration: "native-generation",
			ObservationGeneration: "observation-generation", LoginEpoch: "login-epoch",
		}},
	}
	rawInput, _ := json.Marshal(input)
	intent, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Origin: &origin, Project: project,
		Launch: &protocol.LaunchReq{Agent: "codex", Cwd: project, SpawnedFrom: worker.Parent, InitialPrompt: "Prepare a report"}})
	receipt, _ := json.Marshal(createdWorkerResult{State: "created", Session: worker.Session, SessionInstance: worker.SessionInstance})
	proof := discussionEvidenceID(worker)
	return State{
		Workers: map[string]WorkerAssociation{key(conversation, key(worker.Session, worker.SessionInstance)): worker},
		Admissions: map[string]Admission{admissionKey(conversation, 1): {ConversationID: conversation,
			Project: project, Sequence: 1, SourceID: worker.InitialSourceID, Origin: &origin, SourceCutover: true,
			NativeTurnID: "manager-turn", NativeInputID: input.ManagerAdmission.Nonce, Request: rawInput}},
		Actions: map[string]Action{key(conversation, worker.ActionID): {ID: worker.ActionID, ConversationID: conversation,
			Admission: 1, NativeInputID: input.ManagerAdmission.Nonce, Request: intent, Receipt: receipt, State: "settled"}},
		Events: map[string]Event{key(conversation, proof): {ID: proof, ConversationID: conversation,
			JobID: worker.JobID, AttemptID: worker.AttemptID, NativeTurnID: "completed-turn", WorkerProof: true,
			Metadata: json.RawMessage(`{"session":"owner/worker","instance":"worker-instance","data":{"turn_id":"completed-turn","state":"completed","text":"private output"}}`)}},
	}, worker
}
