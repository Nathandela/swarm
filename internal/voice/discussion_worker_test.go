package voice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestDiscussionNativeTurnRequiresExactSendReceipt(t *testing.T) {
	worker := WorkerAssociation{Session: "local/worker", SessionInstance: "instance", JobID: "job", AttemptID: "attempt", ActionID: "action"}
	request := controllerActionRequest{Tool: "swarm_send", Origin: &schema.MessageOrigin{}, Send: &protocol.ComposerSendReq{Session: worker.Session, SessionInstance: worker.SessionInstance, Text: "authorized"}}
	raw, _ := json.Marshal(request)
	action := Action{ID: worker.ActionID, OperationID: "operation", State: "settled", Request: raw}
	receipt := schema.SemanticMessageReceipt{OperationID: action.OperationID, Session: worker.Session, SessionInstance: worker.SessionInstance, NativeTurnID: "turn"}
	receipt.State = "submitted"
	action.Receipt, _ = json.Marshal(receipt)
	c := &Controller{}
	if _, err := c.discussionWorkerEvidence(context.Background(), worker, action); !errors.Is(err, ErrControllerNativeUnavailable) {
		t.Fatalf("submission without native admission: %v", err)
	}
	receipt.State, receipt.NativeInputID, receipt.SessionInstance = "admitted", "native-input", "private"
	action.Receipt, _ = json.Marshal(receipt)
	if _, err := c.discussionWorkerEvidence(context.Background(), worker, action); !errors.Is(err, ErrControllerNativeUnavailable) {
		t.Fatalf("foreign instance: %v", err)
	}
}

type discussionEvidenceWorker struct {
	controllerWorkerFixture
	req schema.InteractionHistoryReq
}

func (w *discussionEvidenceWorker) NativeHistoryPage(req schema.InteractionHistoryReq) (schema.NativeHistoryPage, error) {
	w.req = req
	w.nativeReads++
	return schema.NativeHistoryPage{Version: 1, Scope: "authorized_worker_turn", Floor: true,
		Worker: &schema.WorkerTurnEvidence{TurnID: "exact-turn", State: "completed", Text: "verified result"}}, nil
}

func TestDiscussionWorkerProofWakesOnceWithoutAttributingPrivateJournal(t *testing.T) {
	c, s, _, binding := newControllerFixture(t)
	c.discussion = &DiscussionScope{}
	workers := &discussionEvidenceWorker{}
	c.workers = workers
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worker := WorkerAssociation{Project: project, ConversationID: binding.ConversationID, Session: "machine/created", SessionInstance: "created-instance", JobID: "job", AttemptID: "action", ActionID: "action"}
	created, _ := json.Marshal(createdWorkerResult{State: "created", Session: worker.Session, SessionInstance: worker.SessionInstance})
	intent, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Project: project, Origin: &schema.MessageOrigin{}, Launch: &protocol.LaunchReq{InitialPrompt: "authorized prompt", SpawnedFrom: "previous-manager"}})
	action := Action{ID: worker.ActionID, State: "settled", Request: intent, Receipt: created}
	workerKey := key(binding.ConversationID, key(worker.Session, worker.SessionInstance))
	if err := s.update(func(st *State) error {
		putRecord(st, "workers", &st.Workers, workerKey, worker)
		putRecord(st, "actions", &st.Actions, key(binding.ConversationID, worker.ActionID), action)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cursor := JournalCursor{Binding: "daemon", Sequence: 8}
	if err := s.ReconcileJournal(binding, cursor, JournalSnapshot{Metadata: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	private := protocol.JournalRecord{Cursor: 9, SessionID: worker.Session, SessionInstance: worker.SessionInstance, Type: "interaction",
		Item: json.RawMessage(`{"kind":"agent_message","status":"completed","turn_id":"private-turn","text":"private answer"}`)}
	if err := c.consumeJournalRecord("machine", "daemon", cursor.Sequence, private); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := c.reconcileDiscussionWorkerEvidence(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if workers.nativeReads != 1 || workers.req.Worker.Parent != "previous-manager" ||
		workers.req.Worker.PromptHash != hashBytes([]byte("authorized prompt")) {
		t.Fatalf("native reads %d, scope %+v", workers.nativeReads, workers.req.Worker)
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Wakes) != 1 || st.Journal[binding.ConversationID] != cursor {
		t.Fatalf("journal changed or wake duplicated: %+v, %+v", st.Journal, st.Wakes)
	}
	event := st.Events[key(binding.ConversationID, discussionEvidenceID(worker))]
	if !event.WorkerProof || event.NativeTurnID != "exact-turn" || event.JobID != worker.JobID || event.AttemptID != worker.AttemptID || !strings.Contains(string(event.Metadata), "verified result") {
		t.Fatalf("event %+v", event)
	}
	for _, wake := range st.Wakes {
		if !strings.Contains(string(wake.Request), "verified result") || strings.Contains(string(wake.Request), "private answer") {
			t.Fatalf("wake lost verified evidence or gained private output: %s", wake.Request)
		}
	}
	workers.rows = []protocol.SessionView{}
	read, err := c.readWorker(ReadInput{SessionInput: SessionInput{worker.Session, worker.SessionInstance}, JobID: worker.JobID, AttemptID: worker.AttemptID})
	if err != nil || workers.nativeReads != 1 || !strings.Contains(string(read), "verified result") || strings.Contains(string(read), "private answer") {
		t.Fatalf("retained proof read: %s, %d, %v", read, workers.nativeReads, err)
	}
	if _, err := c.readWorker(ReadInput{SessionInput: SessionInput{worker.Session, worker.SessionInstance}, JobID: "different-job"}); !errors.Is(err, ErrStale) {
		t.Fatalf("wrong job could read the worker: %v", err)
	}
	if _, err := c.readWorker(ReadInput{SessionInput: SessionInput{worker.Session, worker.SessionInstance}, Source: "retained"}); !errors.Is(err, ErrControllerNativeUnavailable) {
		t.Fatalf("retained history available: %v", err)
	}
	replacement, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(func(st *State) error {
		rebound := worker
		rebound.Project = replacement
		putRecord(st, "workers", &st.Workers, workerKey, rebound)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if read, err := c.readWorker(ReadInput{SessionInput: SessionInput{worker.Session, worker.SessionInstance}}); !errors.Is(err, ErrStale) || len(read) > 0 {
		t.Fatalf("rebound worker read cached proof: %s, %v", read, err)
	}
	if err := s.update(func(st *State) error {
		putRecord(st, "workers", &st.Workers, workerKey, worker)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated := worker
	updated.ActionID = "newer-authorized-action"
	if err := s.update(func(st *State) error {
		putRecord(st, "workers", &st.Workers, workerKey, updated)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := Event{ID: discussionEvidenceID(updated), WorkerProof: true, NativeTurnID: "newer-turn", JobID: worker.JobID, AttemptID: worker.AttemptID,
		Metadata: json.RawMessage(`{"turn_id":"newer-turn","state":"completed","text":"newer result"}`)}
	if _, err := c.AcceptWorkerEvent(worker.Session, worker.SessionInstance, next); err != nil {
		t.Fatal(err)
	}
	input := ReadInput{SessionInput: SessionInput{worker.Session, worker.SessionInstance}}
	read, err = c.readDiscussionWorker(input, Admission{SourceID: "wake:" + event.WakeID})
	if err != nil || !strings.Contains(string(read), "verified result") || strings.Contains(string(read), "newer result") {
		t.Fatalf("earlier wake read the newer turn: %s, %v", read, err)
	}
	input.NativeTurnID = "private-turn"
	if _, err := c.readWorker(input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unattested private turn could be read by ID: %v", err)
	}
	c.discussion.ProjectEnrolled = func(string) bool { return false }
	if read, err := c.readWorker(ReadInput{SessionInput: SessionInput{worker.Session, worker.SessionInstance}}); !errors.Is(err, ErrExpired) || len(read) > 0 {
		t.Fatalf("cached proof after revocation: %s, %v", read, err)
	}
}

func TestDiscussionWorkerRejectedSendPreservesPriorWorker(t *testing.T) {
	s, binding, _ := testStore(t)
	binding.ConversationID = DiscussionConversationPrefix + "send-rejected"
	binding, err := s.BindManager(binding)
	if err != nil {
		t.Fatal(err)
	}
	worker := WorkerAssociation{Session: "machine/worker", SessionInstance: "instance", JobID: "job", AttemptID: "attempt", ActionID: "original-action"}
	workerKey := key(binding.ConversationID, key(worker.Session, worker.SessionInstance))
	request, _ := json.Marshal(controllerActionRequest{Tool: "swarm_send", Origin: &schema.MessageOrigin{}})
	action := Action{ID: "rejected-action", OperationID: "operation", State: "executing", Request: request}
	if err = s.update(func(st *State) error {
		a, err := allocateAdmission(st, binding, "discussion:human")
		if err != nil {
			return err
		}
		action.Admission = a.Sequence
		putRecord(st, "workers", &st.Workers, workerKey, worker)
		putRecord(st, "actions", &st.Actions, key(binding.ConversationID, action.ID), action)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.settleControllerWorkerAction(binding, action, worker.Session, worker.SessionInstance, json.RawMessage(`{"state":"rejected"}`)); err != nil {
		t.Fatal(err)
	}
	st, err := s.Snapshot()
	if err != nil || st.Workers[workerKey] != worker || st.Actions[key(binding.ConversationID, action.ID)].State != "settled" {
		t.Fatalf("rejected send changed worker scope or remained executing: %v", err)
	}
}
