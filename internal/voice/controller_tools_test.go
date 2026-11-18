package voice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type controllerWorkerFixture struct {
	nativeReads, retainedReads int
	nativeErr                  error
	rows                       []protocol.SessionView
	launches                   int
	launchErr                  error
	outcome                    schema.OperationOutcomeView
	beforeLaunch               func(string, protocol.LaunchReq)
}

func (w *controllerWorkerFixture) List() ([]protocol.SessionView, error) {
	if w.rows == nil {
		return []protocol.SessionView{{ID: "machine/created", Agent: "codex", Name: "created", Capabilities: &schema.SessionCapabilities{SessionInstance: "created-instance"}}}, nil
	}
	return w.rows, nil
}
func (w *controllerWorkerFixture) NativeHistoryPage(schema.InteractionHistoryReq) (schema.NativeHistoryPage, error) {
	w.nativeReads++
	return schema.NativeHistoryPage{Version: 2, Scope: strings.Repeat("a", 64), Items: []json.RawMessage{json.RawMessage(`{"v":2,"item_id":"native-item","kind":"message_snapshot","provenance":"imported"}`)}, NextCursor: "native-next"}, w.nativeErr
}

func (w *controllerWorkerFixture) LaunchOperation(op string, req protocol.LaunchReq) (string, string, error) {
	w.launches++
	if w.beforeLaunch != nil {
		w.beforeLaunch(op, req)
	}
	return "machine/created", "created", w.launchErr
}
func (w *controllerWorkerFixture) LaunchOperationStatus(string) (schema.OperationOutcomeView, error) {
	return w.outcome, nil
}
func (w *controllerWorkerFixture) SemanticSend(string, protocol.ComposerSendReq) error {
	return errors.New("not configured")
}
func (w *controllerWorkerFixture) SemanticOperation(string, string) (protocol.SemanticMessageReceipt, error) {
	return protocol.SemanticMessageReceipt{}, errors.New("not configured")
}
func (w *controllerWorkerFixture) NativeRequests(protocol.NativeRequestsReq) (protocol.NativeRequestsReply, error) {
	return protocol.NativeRequestsReply{}, nil
}
func (w *controllerWorkerFixture) NativeDecide(string, protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, error) {
	return protocol.NativeDecisionReceipt{}, errors.New("not configured")
}
func (w *controllerWorkerFixture) NativeDecisionStatus(string, string) (protocol.NativeDecisionReceipt, error) {
	return protocol.NativeDecisionReceipt{}, errors.New("not configured")
}
func provenControllerFixture(t *testing.T) (*Controller, *Store, *controllerRuntimeFixture, ManagerBinding) {
	t.Helper()
	c, s, r, b := newControllerFixture(t)
	if _, err := c.SubmitText(Utterance{ID: "task", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "manage work"); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	nonce := r.sent[0].ManagerAdmission.Nonce
	r.status.ReadyForInput = false
	r.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: "main-turn"}
	r.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: "main-turn", NativeCallID: "native-call"}
	return c, s, r, b
}
func TestControllerPublicationStopAndClarificationUseNativeProof(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	call := ManagerToolCall{Name: "publish", Arguments: json.RawMessage(`{"output_id":"first","sequence":1,"text":"Working","final":false}`), Meta: map[string]any{"callId": "native-call"}}
	raw, err := c.ManagerCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var first Publication
	if json.Unmarshal(raw, &first) != nil || !first.Speak {
		t.Fatal("proven phrase did not receive speech permit")
	}
	if err = c.StopSpeech("main-turn"); err != nil {
		t.Fatal(err)
	}
	call.Arguments = json.RawMessage(`{"output_id":"second","sequence":2,"text":"Later text","final":true}`)
	raw, err = c.ManagerCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var second Publication
	if json.Unmarshal(raw, &second) != nil || second.Speak {
		t.Fatal("stopped native turn reacquired speech")
	}
	ask := ManagerToolCall{Name: "ask", Arguments: json.RawMessage(`{"request_id":"question","task_id":"task","question":"Which?","choices":["A","B"]}`)}
	raw, err = c.ManagerCall(context.Background(), ask)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ManagerCall(context.Background(), ask); err != nil {
		t.Fatalf("exact ask retry changed expiry/hash: %v", err)
	}
	var question Request
	if json.Unmarshal(raw, &question) != nil {
		t.Fatal("invalid question")
	}
	st, _ := s.Snapshot()
	if !st.Admissions[admissionKey(b.ConversationID, 1)].SettledAt.IsZero() {
		t.Fatal("spoken final or ask completed native work")
	}
	r.status.Turn.State = "completed"
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if st.Requests[key(b.ConversationID, question.ID)].State != "pending" {
		t.Fatal("native completion expired manager clarification")
	}
	a, err := c.AnswerQuestion(question.ID, question.Hash, "answer", json.RawMessage(`"A"`))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := c.AnswerQuestion(question.ID, question.Hash, "answer", json.RawMessage(`"A"`))
	if err != nil || replay.Sequence != a.Sequence {
		t.Fatal("answer retry opened another input", err)
	}
	r.status.ReadyForInput = true
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 2 {
		t.Fatal("exact human answer did not enter manager FIFO")
	}
}
func TestControllerUnprovenChildAndManualCallsCannotPublishOrAct(t *testing.T) {
	for _, mutate := range []func(*controllerRuntimeFixture){
		func(r *controllerRuntimeFixture) { r.proof.Main = false },
		func(r *controllerRuntimeFixture) { r.proof.NativeInputID = "unknown" },
		func(r *controllerRuntimeFixture) { r.proof.NativeTurnID = "model-claimed" },
		func(r *controllerRuntimeFixture) { r.status.Turn.ExternalInput++ },
		func(r *controllerRuntimeFixture) { r.status.Turn.ExternallySteered = true },
	} {
		c, s, r, _ := provenControllerFixture(t)
		mutate(r)
		if _, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "publish", Arguments: json.RawMessage(`{"output_id":"out","sequence":1,"text":"spoof","final":true}`)}); err == nil {
			t.Fatal("unproven authority published")
		}
		st, _ := s.Snapshot()
		if len(st.Publications) != 0 || len(st.Actions) != 0 {
			t.Fatal("refused proof mutated effect authority")
		}
	}
}
func TestControllerCreatePersistsOrdinaryWorkerIntentAndLostReplyUsesStatus(t *testing.T) {
	c, s, _, b := provenControllerFixture(t)
	worker := &controllerWorkerFixture{launchErr: errors.New("lost reply"), outcome: schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "machine/created"}}
	c.workers = worker
	worker.beforeLaunch = func(op string, req protocol.LaunchReq) {
		st, err := s.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		var a Action
		for _, candidate := range st.Actions {
			if candidate.OperationID == op {
				a = candidate
				break
			}
		}
		if a.State != "executing" || a.OperationID != op || req.Manager != nil || req.SpawnedFrom != "session" || req.Cols != 120 || req.Rows != 32 || req.Env != nil {
			t.Fatal("worker effect preceded exact ordinary-worker intent")
		}
	}
	call := ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"create","agent":"codex","cwd":"/tmp","prompt":"Do work"}`)}
	raw, err := c.ManagerCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ManagerCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if worker.launches != 1 || !json.Valid(raw) {
		t.Fatal("lost launch reply launched another worker")
	}
	var launched createdWorkerResult
	if json.Unmarshal(raw, &launched) != nil || launched.State != "created" || launched.Session != "machine/created" || launched.SessionInstance != "created-instance" {
		t.Fatalf("lost-reply reconciliation returned the wrong worker: %s", raw)
	}
	observed, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_read", Arguments: json.RawMessage(`{"session":"machine/created","session_instance":"created-instance"}`)})
	var page WorkerOutputPage
	if err != nil || json.Unmarshal(observed, &page) != nil || page.Session != launched.Session || page.SessionInstance != launched.SessionInstance || len(page.Items) != 1 {
		t.Fatalf("worker result not observed on exact instance: %s %v", observed, err)
	}
	var item struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(page.Items[0], &item) != nil || item.Text != "Final worker result" {
		t.Fatalf("worker returned the wrong result: %s", page.Items[0])
	}
	output, _ := json.Marshal(PublishInput{OutputID: "result", Sequence: 1, Text: item.Text, Final: true})
	publication, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "publish", Arguments: output})
	var fact Publication
	if err != nil || json.Unmarshal(publication, &fact) != nil || !fact.Final || fact.Admission != 1 || fact.Text != item.Text {
		t.Fatalf("observed result was not published by the owning admission: %s %v", publication, err)
	}
	saved, err := s.Snapshot()
	if err != nil || saved.Publications[key(b.ConversationID, fact.ID)].Text != item.Text || worker.launches != 1 {
		t.Fatal("publication was lost or reconciled worker launched twice", err)
	}
	call.Arguments = json.RawMessage(`{"action_id":"create","agent":"codex","cwd":"/tmp","prompt":"Changed work"}`)
	if _, err = c.ManagerCall(context.Background(), call); !errors.Is(err, ErrConflict) {
		t.Fatalf("same logical ID changed its business request: %v", err)
	}
}
func TestControllerSelfTargetsAndMissingExactOperationsRefuse(t *testing.T) {
	c, _, _, b := provenControllerFixture(t)
	c.workers = &controllerWorkerFixture{rows: []protocol.SessionView{{ID: b.SessionID, Role: "manager", Capabilities: &schema.SessionCapabilities{SessionInstance: b.SessionInstance, NativeOwnerInput: true}}}}
	for _, name := range []string{"swarm_read", "swarm_send", "swarm_resume", "swarm_interrupt"} {
		args, _ := json.Marshal(map[string]any{"session": b.SessionID, "session_instance": b.SessionInstance, "action_id": "self", "text": "loop", "native_turn_id": "main-turn"})
		if _, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: name, Arguments: args}); err == nil {
			t.Fatalf("%s allowed self delegation or unsupported fallback", name)
		}
	}
}

func TestControllerCancelledTaskCannotDispatchAnAlreadyQueuedAnswer(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	raw, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "ask", Arguments: json.RawMessage(`{"request_id":"question","task_id":"task","question":"Which?"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var question Request
	if err = json.Unmarshal(raw, &question); err != nil {
		t.Fatal(err)
	}
	if _, err = c.AnswerQuestion(question.ID, question.Hash, "answer", json.RawMessage(`"yes"`)); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelTaskRequests(b, question.TaskID); err != nil {
		t.Fatal(err)
	}
	r.status.Turn.State = "completed"
	r.status.ReadyForInput = true
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 {
		t.Fatal("task cancellation let its queued answer reach native input")
	}
	st, _ := s.Snapshot()
	answer := st.Admissions[admissionKey(b.ConversationID, 2)]
	if answer.Phase != "rejected" || answer.SettledAt.IsZero() || answer.NativeInputID != "" {
		t.Fatal("known unsent cancellation lost its terminal authority")
	}
}

func TestControllerSameProviderRebindPreservesFloorAndManagerClarification(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	next := b
	next.NativeGeneration = "next-generation"
	next.SessionInstance = "next-instance"
	if _, err := s.RebindManager(b, next); !errors.Is(err, ErrConflict) {
		t.Fatal("rebind transferred an unresolved native admission")
	}
	raw, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "ask", Arguments: json.RawMessage(`{"request_id":"question","task_id":"task","question":"Which?"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var question Request
	if err = json.Unmarshal(raw, &question); err != nil {
		t.Fatal(err)
	}
	r.status.Turn.State = "completed"
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rebound, err := s.RebindManager(b, next)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	convo := st.Conversations[b.ConversationID]
	if convo.LastAdmission != 1 || convo.RetiredThrough != 1 || !convo.SchedulerPaused || st.Requests[key(b.ConversationID, question.ID)].State != "pending" {
		t.Fatal("rebind reset counters or expired a still-live manager clarification")
	}
	if _, err = c.SubmitText(Utterance{ID: "stale", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "old"); !errors.Is(err, ErrStale) {
		t.Fatal("old manager incarnation survived CAS")
	}
	r.status.Binding = runtimeBinding(rebound)
	r.status.Turn = protocol.ManagerNativeTurn{State: "idle"}
	r.status.ReadyForInput = true
	replacement, err := NewController(s, r, rebound, c.refs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = replacement.AnswerQuestion(question.ID, question.Hash, "answer", json.RawMessage(`"yes"`)); err != nil {
		t.Fatal("same-provider native resume lost its pending clarification", err)
	}
	if err = replacement.ResumeManual(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if err = replacement.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 2 || r.sent[1].ManagerAdmission.Binding.NativeGeneration != next.NativeGeneration {
		t.Fatal("answer was not bound to reconciled new native generation")
	}
}

func TestControllerForeignBridgeRefsAndUnknownNativeStateRefuse(t *testing.T) {
	c, _, r, _ := newControllerFixture(t)
	if _, err := c.SubmitText(Utterance{ID: "human", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "exact"); err != nil {
		t.Fatal(err)
	}
	r.status.BridgeSocket = "/foreign/socket"
	if err := c.Tick(context.Background()); !errors.Is(err, ErrStale) {
		t.Fatal("foreign bridge custody adopted")
	}
	r.status.BridgeSocket = c.refs.Socket
	r.status.ReadyForInput = false
	r.status.Turn.State = "unknown"
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 0 {
		t.Fatal("unknown native state automatically dispatched")
	}
}

func TestControllerUncertainWorkerEffectBlocksNextInputUntilReadOnlyRecovery(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	worker := &controllerWorkerFixture{launchErr: errors.New("lost launch reply"), outcome: schema.OperationOutcomeView{State: schema.OutcomeUnknown}}
	c.workers = worker
	_, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"create","agent":"codex","cwd":"/tmp","prompt":"Do work"}`)})
	if !errors.Is(err, ErrControllerUncertain) {
		t.Fatal(err)
	}
	if _, err = c.SubmitText(Utterance{ID: "next", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "next task"); err != nil {
		t.Fatal(err)
	}
	r.status.Turn.State = "completed"
	r.status.ReadyForInput = true
	if err = c.Tick(context.Background()); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal("unresolved worker effect was retired", err)
	}
	if len(r.sent) != 1 || worker.launches != 1 {
		t.Fatal("uncertainty dispatched another input/effect")
	}
	worker.outcome = schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "machine/created"}
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if len(r.sent) != 2 || worker.launches != 1 || st.Conversations[b.ConversationID].RetiredThrough != 1 || len(st.Actions) != 1 {
		t.Fatal("read-only receipt recovery failed to release the exact FIFO head")
	}
	for _, a := range st.Actions {
		if a.State != "settled" {
			t.Fatal("action receipt remained unresolved")
		}
	}
}

func TestControllerAskReturnsWhileWorkerCreationStillAwaitsReceipt(t *testing.T) {
	c, _, _, _ := provenControllerFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	worker := &controllerWorkerFixture{beforeLaunch: func(string, protocol.LaunchReq) { close(entered); <-release }}
	c.workers = worker
	complete := make(chan error, 1)
	go func() {
		_, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"create","agent":"codex","cwd":"/tmp","prompt":"Work"}`)})
		complete <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker create did not enter receipt wait")
	}
	pending := make(chan error, 1)
	go func() {
		_, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "ask", Arguments: json.RawMessage(`{"request_id":"question","task_id":"task","question":"Which?"}`)})
		pending <- err
	}()
	select {
	case err := <-pending:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ask blocked behind worker effect")
	}
	once.Do(func() { close(release) })
	select {
	case err := <-complete:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("create receipt did not complete")
	}
}

func TestControllerTrackedWorkerSurvivesReceiptCompactionAndStopsOnlyByExplicitCAS(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	c.workers = &controllerWorkerFixture{}
	if _, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"create","agent":"codex","cwd":"/tmp","prompt":"Long work"}`)}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	association := st.Workers[key(b.ConversationID, key("machine/created", "created-instance"))]
	if association.Admission != 1 || association.SourceID != "utterance:task" || !strings.HasPrefix(association.ActionID, "action:1:") || association.Reserved {
		t.Fatal("worker lost its exact source admission/task reference")
	}
	r.status.Turn.State = "completed"
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(b, time.Now().Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if len(st.Actions) != 0 || len(st.Admissions) != 0 || len(st.Workers) != 1 {
		t.Fatal("receipt compaction forgot still-tracked worker")
	}
	wake, err := c.AcceptWorkerEvent("machine/created", "created-instance", Event{ID: "done", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{"state":"finished"}`)})
	if err != nil || wake.Admission == 0 {
		t.Fatal("tracked worker stopped waking after receipt compaction", err)
	}
	stale := association
	stale.ActionID = "old"
	if err = c.StopTrackingWorker(stale); !errors.Is(err, ErrStale) {
		t.Fatal("stale tracking selection deleted current task association")
	}
	if err = c.StopTrackingWorker(association); err != nil {
		t.Fatal(err)
	}
	ignored, err := c.AcceptWorkerEvent("machine/created", "created-instance", Event{ID: "later", Cursor: JournalCursor{Binding: "journal", Sequence: 2}, Metadata: json.RawMessage(`{}`)})
	if err != nil || ignored.Admission != 0 {
		t.Fatal("stopped/untracked worker opened a wake", err)
	}
	st, _ = s.Snapshot()
	if len(st.Wakes) != 1 || st.Wakes[key(b.ConversationID, wake.ID)].Admission != wake.Admission || st.Journal[b.ConversationID].Sequence != 2 {
		t.Fatal("stopping tracking discarded accepted authority or lost ignored checkpoint")
	}
}
func TestControllerUntrackedWorkerOnlyAdvancesCheckpoint(t *testing.T) {
	c, s, _, b := newControllerFixture(t)
	if _, err := c.AcceptWorkerEvent("machine/foreign", "unknown", Event{ID: "foreign", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{"instruction":"do something"}`)}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if len(st.Wakes) != 0 || len(st.Admissions) != 0 || st.Journal[b.ConversationID].Sequence != 1 {
		t.Fatal("untracked worker became new manager authority")
	}
}
func TestControllerObservationReplacementCannotBorrowResetExternalCounter(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	if _, err := c.SubmitText(Utterance{ID: "human", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "queued before observation gap"); err != nil {
		t.Fatal(err)
	}
	r.status.Binding.ObservationGeneration = "replacement-observer"
	r.status.Turn.ExternalInput = 0
	if err := c.Tick(context.Background()); !errors.Is(err, ErrStale) {
		t.Fatal("new observer silently authorized old queued work", err)
	}
	if err := c.ResumeManual(context.Background(), 0); !errors.Is(err, ErrStale) {
		t.Fatal("counter-only acknowledgement crossed observation identity")
	}
	next := b
	next.ObservationGeneration = "replacement-observer"
	if _, err := s.RebindManager(b, next); !errors.Is(err, ErrConflict) {
		t.Fatal("pending old observation work was silently transferred")
	}
	if len(r.sent) != 0 {
		t.Fatal("old input crossed changed observer generation")
	}
}

func (w *controllerWorkerFixture) RetainedHistoryPage(req schema.InteractionHistoryReq) (protocol.RetainedHistoryPage, error) {
	w.retainedReads++
	return protocol.RetainedHistoryPage{Records: []protocol.JournalRecord{{SessionID: req.Session, Type: "interaction", Item: json.RawMessage(`{"item_id":"item","kind":"message","text":"Final worker result"}`)}}, Floor: true}, nil
}

type delayedWorkerIncarnation struct {
	*controllerWorkerFixture
	reads int
}

func (w *delayedWorkerIncarnation) ListContext(ctx context.Context) ([]protocol.SessionView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.reads++
	rows, err := w.List()
	if w.reads == 1 {
		rows[0].Capabilities = nil
	}
	if w.reads == 2 {
		rows[0].Capabilities = &schema.SessionCapabilities{SessionInstance: "created-instance"}
	}
	return rows, err
}

func TestControllerCreateWaitsForExactIntendedIncarnation(t *testing.T) {
	c, _, _, _ := provenControllerFixture(t)
	worker := &delayedWorkerIncarnation{controllerWorkerFixture: &controllerWorkerFixture{rows: []protocol.SessionView{
		{ID: "machine/created", Agent: "codex"},
		{ID: "machine/unrelated", Agent: "codex", Capabilities: &schema.SessionCapabilities{SessionInstance: "other-instance"}},
	}}}
	c.workers = worker
	call := ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"create","agent":"codex","cwd":"/tmp","prompt":"Create once"}`)}
	raw, err := c.ManagerCall(context.Background(), call)
	var receipt createdWorkerResult
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.Session != "machine/created" ||
		receipt.SessionInstance != "created-instance" || worker.launches != 1 || worker.reads != 2 {
		t.Fatalf("incarnation did not appear on the original launch: %s %v, launches=%d reads=%d", raw, err, worker.launches, worker.reads)
	}
}
