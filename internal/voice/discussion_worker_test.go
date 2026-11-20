package voice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/claudeinput"
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
	req         schema.InteractionHistoryReq
	unavailable bool
	text        string
}

func (w *discussionEvidenceWorker) NativeHistoryPage(req schema.InteractionHistoryReq) (schema.NativeHistoryPage, error) {
	w.req = req
	w.nativeReads++
	if w.unavailable {
		return schema.NativeHistoryPage{}, ErrControllerNativeUnavailable
	}
	text := w.text
	if text == "" {
		text = "verified result"
	}
	return schema.NativeHistoryPage{Version: 1, Scope: "authorized_worker_turn", Floor: true,
		Worker: &schema.WorkerTurnEvidence{TurnID: "exact-turn", State: "completed", Text: text}}, nil
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

func TestMailboxCompletionRetryKeepsOriginalEvidence(t *testing.T) {
	c, s, _, binding := newControllerFixture(t)
	workers := &discussionEvidenceWorker{unavailable: true}
	c.workers = workers
	worker := WorkerAssociation{Mailbox: true, VMChild: true, ConversationID: binding.ConversationID,
		Session: "machine/worker", SessionInstance: "worker-instance", Parent: "original-manager", JobID: "job", AttemptID: "attempt"}
	input := workerMessageInput{ID: "result-1", Kind: "complete", Text: "worker claim"}
	src := workerNoteMetadata{Session: worker.Session, Instance: worker.SessionInstance, Agent: "codex",
		Text: input.Text, Kind: input.Kind, NoteID: input.ID, Sequence: 1}
	receipt, err := c.queueWorkerNote(worker, input, src)
	if err != nil || receipt.State != "queued" || workers.nativeReads != 1 {
		t.Fatalf("initial queue: %+v, %v, %d reads", receipt, err, workers.nativeReads)
	}
	if scope := workers.req.Worker; scope == nil || scope.Parent != worker.Parent ||
		scope.SessionInstance != worker.SessionInstance || scope.ManagerConversation != binding.ConversationID ||
		scope.PromptHash != "" || scope.TurnID != "" || scope.InputID != "" {
		t.Fatalf("native lookup lost exact original launch scope: %+v", scope)
	}
	workers.unavailable = false
	retry, err := c.queueWorkerNote(worker, input, src)
	if err != nil || retry != receipt || workers.nativeReads != 1 {
		t.Fatalf("retry consumed newly available evidence: %+v, %v, %d reads", retry, err, workers.nativeReads)
	}
	snapshot, err := s.Snapshot()
	if err != nil || len(snapshot.Wakes) != 1 || len(snapshot.Events) != 1 {
		t.Fatalf("replay duplicated work: %d wakes, %d events, %v", len(snapshot.Wakes), len(snapshot.Events), err)
	}
	for _, event := range snapshot.Events {
		var metadata workerNoteMetadata
		if err := json.Unmarshal(event.Metadata, &metadata); err != nil || metadata.LaunchTurnEvidence != nil {
			t.Fatalf("duplicate changed stored evidence: %+v, %v", metadata, err)
		}
	}

	input.ID, src.NoteID, src.Sequence = "result-2", "result-2", 2
	if _, err := c.queueWorkerNote(worker, input, src); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.Snapshot()
	if err != nil || workers.nativeReads != 2 {
		t.Fatalf("new note failed to check available evidence: %v (%d reads)", err, workers.nativeReads)
	}
	event := snapshot.Events[key(binding.ConversationID, "worker-message-"+hashValue([]string{worker.Session, worker.SessionInstance, worker.AttemptID, input.ID}))]
	var metadata workerNoteMetadata
	if err := json.Unmarshal(event.Metadata, &metadata); err != nil || metadata.LaunchTurnEvidence == nil || metadata.LaunchTurnEvidence.TurnID != "exact-turn" {
		t.Fatalf("new note lost available proof: %+v, %v", metadata, err)
	}
}

func TestMailboxWakeFitsNativeInputAndReadsFullExactNote(t *testing.T) {
	c, s, runtime, binding := newControllerFixture(t)
	c.discussion = &DiscussionScope{VMProjectAccess: true}
	workers := &discussionEvidenceWorker{text: strings.Repeat("<", 4096)}
	c.workers = workers
	worker := WorkerAssociation{Mailbox: true, VMChild: true, ConversationID: binding.ConversationID,
		Session: "machine/worker", SessionInstance: "worker-instance", Parent: "original-manager", JobID: "job", AttemptID: "attempt"}
	note := "The issue is " + claudeinput.ManagerEnvelopePrefix + strings.Repeat(">", 4083-len(claudeinput.ManagerEnvelopePrefix))
	input := workerMessageInput{ID: "result", Kind: "complete", Text: note}
	src := workerNoteMetadata{Session: worker.Session, Instance: worker.SessionInstance, Agent: "codex",
		Text: note, Kind: input.Kind, NoteID: input.ID, Sequence: 1}
	if _, err := c.queueWorkerNote(worker, input, src); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil || len(runtime.sent) != 1 {
		t.Fatalf("native admission: %d sends, %v", len(runtime.sent), err)
	}
	nonce := runtime.sent[0].ManagerAdmission.Nonce
	runtime.status.ReadyForInput = false
	runtime.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: "manager-turn"}
	runtime.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: "manager-turn", NativeCallID: "native-note-read"}
	st, err := s.Snapshot()
	if err != nil || len(st.Wakes) != 1 {
		t.Fatalf("expected one wake: %d, %v", len(st.Wakes), err)
	}
	for _, wake := range st.Wakes {
		admission := Admission{SourceID: "wake:" + wake.ID, Sequence: wake.Admission}
		prompt, err := c.admissionText(st, admission)
		if err != nil || len(prompt) > protocol.MaxSendInputText ||
			!strings.Contains(prompt, "The issue is ") || !strings.Contains(prompt, "swarm_read_note") {
			t.Fatalf("invalid preview: %d bytes, %v", len(prompt), err)
		}
		read := SessionInput{Session: worker.Session, SessionInstance: worker.SessionInstance}
		args, err := json.Marshal(read)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_read_note", Arguments: args})
		var stored workerNoteMetadata
		if err != nil || json.Unmarshal(raw, &stored) != nil || stored.Text != note ||
			stored.LaunchTurnEvidence == nil || stored.LaunchTurnEvidence.Text != workers.text {
			t.Fatalf("full note read lost data: %v", err)
		}
		if _, err = managerToolResponse(raw); err != nil {
			t.Fatalf("full note does not fit its tool envelope: %v", err)
		}
		read.SessionInstance = "different-worker-instance"
		if _, err = c.readDiscussionWorkerNote(read, admission); !errors.Is(err, ErrStale) {
			t.Fatalf("foreign worker note read: %v", err)
		}
	}
}

func TestMailboxWakeFitsClaudeEnvelopeAtProtocolBoundary(t *testing.T) {
	c, store, _, binding := newControllerFixture(t)
	worker := WorkerAssociation{Mailbox: true, VMChild: true, ConversationID: binding.ConversationID,
		Session: "machine/worker", SessionInstance: "worker-instance", Parent: "original-manager", JobID: "job", AttemptID: "attempt"}
	queue := func(id, text string) string {
		t.Helper()
		input := workerMessageInput{ID: id, Kind: "needs-input", Text: text}
		source := workerNoteMetadata{Session: worker.Session, Instance: worker.SessionInstance, Agent: "claude",
			Text: text, Kind: input.Kind, NoteID: id, Sequence: 1}
		if _, err := c.queueWorkerNote(worker, input, source); err != nil {
			t.Fatal(err)
		}
		st, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		wakeID := "worker-message-" + hashValue([]string{worker.Session, worker.SessionInstance, worker.AttemptID, id})
		wake := st.Wakes[key(binding.ConversationID, wakeID)]
		prompt, err := c.admissionText(st, Admission{Sequence: wake.Admission, SourceID: "wake:" + wakeID})
		if err != nil {
			t.Fatal(err)
		}
		return prompt
	}
	marker := claudeinput.ManagerEnvelopePrefix
	quoted := queue("quote", marker)
	var decoded workerNoteMetadata
	if start := strings.IndexByte(quoted, '{'); start < 0 || json.Unmarshal([]byte(quoted[start:]), &decoded) != nil || decoded.Text != marker {
		t.Fatal("framing marker did not round-trip in note JSON")
	}
	small := queue("small", "x")
	for _, prompt := range []string{quoted, queue("marks", strings.Repeat(marker, (claudeinput.MaxManagerTextBytes-len(small))/len(marker)))} {
		if _, err := claudeinput.ManagerEnvelope(strings.Repeat("a", 64), prompt); err != nil {
			t.Fatalf("quoted admission marker invalidated native worker wake: %v", err)
		}
	}

	prompt := queue("large", strings.Repeat("x", protocol.MaxSendInputText-len(small)+1))
	if !strings.Contains(prompt, "swarm_read_note") {
		t.Fatal("full input cannot fit with native framing, but no preview was provided")
	}
	if _, err := claudeinput.ManagerEnvelope(strings.Repeat("a", 64), prompt); err != nil {
		t.Fatalf("mailbox wake was rejected by the Claude envelope: %d bytes, %v", len(prompt), err)
	}
}
