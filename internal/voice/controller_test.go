package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
)

type controllerRuntimeFixture struct {
	mu         sync.Mutex
	status     protocol.ManagerRuntimeStatus
	sent       []protocol.ComposerSendReq
	operations []string
	receipts   map[string]protocol.SemanticMessageReceipt
	failSend   error
	beforeSend func(string, protocol.ComposerSendReq)
	proof      protocol.ManagerCallProof
}

func (r *controllerRuntimeFixture) ManagerRuntime(string, string) (protocol.ManagerRuntimeStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, nil
}
func (r *controllerRuntimeFixture) SemanticSend(op string, req protocol.ComposerSendReq) error {
	if r.beforeSend != nil {
		r.beforeSend(op, req)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, req)
	r.operations = append(r.operations, op)
	r.receipts[op] = protocol.SemanticMessageReceipt{OperationID: op, Session: req.Session, SessionInstance: req.SessionInstance, NativeInputID: req.ManagerAdmission.Nonce, NativeGeneration: req.ManagerAdmission.Binding.NativeGeneration, State: "submitted"}
	return r.failSend
}
func (r *controllerRuntimeFixture) SemanticOperation(session, op string) (protocol.SemanticMessageReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.receipts[op]
	if !ok {
		return v, ErrNotFound
	}
	return v, nil
}
func (r *controllerRuntimeFixture) ResolveManagerCall(context.Context, protocol.ManagerCallReq) (protocol.ManagerCallProof, error) {
	return r.proof, nil
}
func newControllerFixture(t *testing.T) (*Controller, *Store, *controllerRuntimeFixture, ManagerBinding) {
	t.Helper()
	s, b, _ := testStore(t)
	runtime := &controllerRuntimeFixture{status: protocol.ManagerRuntimeStatus{Binding: runtimeBinding(b), ReadyForInput: true, BridgeSocket: "/private/bridge.sock", AuthFile: "/private/bridge.auth", Turn: protocol.ManagerNativeTurn{State: "idle"}}, receipts: map[string]protocol.SemanticMessageReceipt{}}
	c, err := NewController(s, runtime, b, ManagerBridgeRefs{Socket: runtime.status.BridgeSocket, AuthFile: runtime.status.AuthFile})
	if err != nil {
		t.Fatal(err)
	}
	return c, s, runtime, b
}
func TestControllerPersistsExactInputBeforeEffectAndKeepsFIFO(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	for _, id := range []string{"first", "second"} {
		if _, err := c.SubmitText(Utterance{ID: id, DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, id); err != nil {
			t.Fatal(err)
		}
	}
	r.beforeSend = func(op string, req protocol.ComposerSendReq) {
		st, err := s.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		a := st.Admissions[admissionKey(b.ConversationID, 1)]
		var exact protocol.ComposerSendReq
		if err = json.Unmarshal(a.Request, &exact); err != nil {
			t.Fatal(err)
		}
		if a.Phase != "executing" || a.OperationID != op || a.NativeInputID != req.ManagerAdmission.Nonce || exact.Text != "first" {
			t.Fatalf("effect preceded exact durable intent: %+v", a)
		}
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 || r.sent[0].Text != "first" {
		t.Fatal("FIFO not respected")
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 {
		t.Fatal("queue receipt caused another native input")
	}
}
func TestControllerLostSendReplyReconcilesWithoutAnotherDispatch(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	r.failSend = errors.New("lost reply")
	if _, err := c.SubmitText(Utterance{ID: "human", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "exact"); err != nil {
		t.Fatal(err)
	}
	_ = c.Tick(context.Background())
	c2, err := NewController(s, r, b, ManagerBridgeRefs{Socket: r.status.BridgeSocket, AuthFile: r.status.AuthFile})
	if err != nil {
		t.Fatal(err)
	}
	if err = c2.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 {
		t.Fatal("recovery blindly replayed native send")
	}
	st, _ := s.Snapshot()
	a := st.Admissions[admissionKey(b.ConversationID, 1)]
	if a.Phase != "submitted" || a.OperationID != r.operations[0] {
		t.Fatalf("lost exact receipt: %+v", a)
	}
}
func TestControllerCompletedDoesNotInventReadinessAndManualTUISuspends(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	_, _ = c.SubmitText(Utterance{ID: "one", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "one")
	_, _ = c.SubmitText(Utterance{ID: "two", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "two")
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.beforeSend = nil
	nonce := r.sent[0].ManagerAdmission.Nonce
	r.status.ReadyForInput = false
	r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: nonce, NativeTurnID: "turn"}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 {
		t.Fatal("completion invented native idle")
	}
	r.status.ReadyForInput = true
	r.status.Turn.ExternalInput = 1
	r.status.Turn.ExternallySteered = true
	if err := c.Tick(context.Background()); !errors.Is(err, ErrControllerPaused) {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if !st.Conversations[b.ConversationID].SchedulerPaused {
		t.Fatal("manual suspension not durable")
	}
	if err := c.ResumeManual(context.Background(), 0); !errors.Is(err, ErrStale) {
		t.Fatal("stale UI resumed scheduling")
	}
	if err := c.ResumeManual(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 2 {
		t.Fatal("explicit reconciled resume did not release FIFO")
	}
}

func TestControllerUnknownExecutingInputNeverResendsAndSurvivesRealStoreRestart(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	a, err := c.SubmitText(Utterance{ID: "human", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "exact")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.observeController(b, 0, false); err != nil {
		t.Fatal(err)
	}
	op, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 64)
	prepared, err := s.prepareControllerInput(b, a.Sequence, nonce, op, protocol.ComposerSendReq{Session: b.SessionID, SessionInstance: b.SessionInstance, Text: "exact", ManagerAdmission: &protocol.ManagerAdmission{Binding: runtimeBinding(b), Nonce: nonce}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.beginControllerInput(b, prepared); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(s.db.Path())
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fresh, err := reopened.BindManager(b)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := NewController(reopened, r, fresh, c.refs)
	if err != nil {
		t.Fatal(err)
	}
	if err = recovered.Tick(context.Background()); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal(err)
	}
	if len(r.sent) != 0 {
		t.Fatal("uncertain executing input was dispatched again")
	}
	st, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	saved := st.Admissions[admissionKey(b.ConversationID, a.Sequence)]
	if saved.Phase != "uncertain" || saved.NativeInputID != nonce || saved.OperationID != op {
		t.Fatalf("restart changed input identity: %+v", saved)
	}
	saved.Request[0] = 'X'
	st, err = reopened.Snapshot()
	if err != nil || !json.Valid(st.Admissions[admissionKey(b.ConversationID, a.Sequence)].Request) {
		t.Fatal("admission bytes escaped cache", err)
	}
}

func TestControllerWorkerEventsCheckpointOnceAndNeverWakeForManagerOutput(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	trackControllerFixtureWorker(t, s, b)
	own := Event{ID: "own", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{"text":"manager published"}`)}
	if _, err := c.AcceptWorkerEvent(b.SessionID, b.SessionInstance, own); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if len(st.Wakes) != 0 || len(st.Admissions) != 0 || st.Journal[b.ConversationID].Sequence != 1 {
		t.Fatal("self event woke the manager or lost checkpoint")
	}
	event := Event{ID: "worker", Cursor: JournalCursor{Binding: "journal", Sequence: 2}, Metadata: json.RawMessage(`{"task":"finished"}`)}
	wake, err := c.AcceptWorkerEvent("machine/worker", "instance", event)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := c.AcceptWorkerEvent("machine/worker", "instance", event)
	if err != nil || repeat.Admission != wake.Admission {
		t.Fatal("event replay created another wake", err)
	}
	st, _ = s.Snapshot()
	if len(st.Wakes) != 1 || len(st.Admissions) != 1 {
		t.Fatal("duplicate wake admission")
	}
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 || r.operations[0] != wake.OperationID {
		t.Fatal("wake did not retain its accepted semantic operation")
	}
	r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[0].ManagerAdmission.Nonce, NativeTurnID: "turn"}
	r.status.ReadyForInput = true
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if st.Wakes[key(b.ConversationID, wake.ID)].State != "settled" {
		t.Fatal("native completion did not settle the same durable wake")
	}
	if len(r.sent) != 1 {
		t.Fatal("device-independent completion created a loop")
	}
}

func TestControllerInputDiskFailurePreventsNativeDispatch(t *testing.T) {
	c, s, r, _ := newControllerFixture(t)
	_, err := c.SubmitText(Utterance{ID: "human", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "exact")
	if err != nil {
		t.Fatal(err)
	}
	s.beforeCommit = func() error { return syscall.ENOSPC }
	if err = c.Tick(context.Background()); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.beforeCommit = nil
	if len(r.sent) != 0 {
		t.Fatal("native input escaped failed durable transaction")
	}
}

func TestControllerNativeRefusalIsTerminalWithoutInventingStart(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	_, err := c.SubmitText(Utterance{ID: "human", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "exact")
	if err != nil {
		t.Fatal(err)
	}
	r.failSend = protocol.ErrSemanticSendRejected
	if err = c.Tick(context.Background()); !errors.Is(err, protocol.ErrSemanticSendRejected) {
		t.Fatal(err)
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	a := st.Admissions[admissionKey(b.ConversationID, 1)]
	if a.Phase != "rejected" || a.NativeTurnID != "" || a.SettledAt.IsZero() {
		t.Fatalf("safe refusal invented native execution: %+v", a)
	}
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 {
		t.Fatal("terminal refusal retried native admission")
	}
}

func TestControllerEightHourPreparedWakeRenewsOnlyBeforeExecuting(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	trackControllerFixtureWorker(t, s, b)
	event := Event{ID: "worker", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{"done":true}`)}
	wake, err := c.AcceptWorkerEvent("machine/worker", "instance", event)
	if err != nil {
		t.Fatal(err)
	}
	oldOperation := fmt.Sprintf("semantic:%d:%s", time.Now().Add(-8*time.Hour).UnixMilli(), strings.Repeat("a", 32))
	if err = s.update(func(st *State) error {
		w := st.Wakes[key(b.ConversationID, wake.ID)]
		w.OperationID = oldOperation
		putRecord(st, "wakes", &st.Wakes, key(b.ConversationID, w.ID), w)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.beforeSend = func(op string, req protocol.ComposerSendReq) {
		st, err := s.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		a := st.Admissions[admissionKey(b.ConversationID, wake.Admission)]
		w := st.Wakes[key(b.ConversationID, wake.ID)]
		if op == oldOperation || a.OperationID != op || w.OperationID != op || a.Phase != "executing" || !protocol.SemanticOperationReplayable(op, time.Now()) {
			t.Fatal("held wake did not atomically renew before its first effect")
		}
	}
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 {
		t.Fatal("authorized held work was not dispatched")
	}
	st, _ := s.Snapshot()
	a := st.Admissions[admissionKey(b.ConversationID, wake.Admission)]
	replacement, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.refreshPreparedInput(b, a, replacement, 0, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("submitted/native input was allowed to renew operation identity")
	}
}

func TestControllerPreparedRenewalCASRollsBackAndSurvivesCrashBeforeDispatch(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	trackControllerFixtureWorker(t, s, b)
	wake, err := c.AcceptWorkerEvent("machine/worker", "instance", Event{ID: "worker", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.observeController(b, 0, false); err != nil {
		t.Fatal(err)
	}
	expired := fmt.Sprintf("semantic:%d:%s", time.Now().Add(-8*time.Hour).UnixMilli(), strings.Repeat("b", 32))
	if err = s.update(func(st *State) error {
		w := st.Wakes[key(b.ConversationID, wake.ID)]
		w.OperationID = expired
		putRecord(st, "wakes", &st.Wakes, key(b.ConversationID, w.ID), w)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("c", 64)
	original, err := s.prepareControllerInput(b, wake.Admission, nonce, expired, protocol.ComposerSendReq{Session: b.SessionID, SessionInstance: b.SessionInstance, Text: "event", ManagerAdmission: &protocol.ManagerAdmission{Binding: runtimeBinding(b), Nonce: nonce}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	s.beforeCommit = func() error { return syscall.ENOSPC }
	if _, err = s.refreshPreparedInput(b, original, current, 0, ""); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.beforeCommit = nil
	st, _ := s.Snapshot()
	if st.Wakes[key(b.ConversationID, wake.ID)].OperationID != expired || st.Admissions[admissionKey(b.ConversationID, wake.Admission)].OperationID != expired {
		t.Fatal("failed renewal split wake/admission identity")
	}
	renewed, err := s.refreshPreparedInput(b, original, current, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.beginControllerInput(b, original); !errors.Is(err, ErrConflict) {
		t.Fatal("stale pre-renewal dispatcher crossed executing boundary")
	}
	dir := filepath.Dir(s.db.Path())
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	binding, err := restarted.BindManager(b)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := NewController(restarted, r, binding, c.refs)
	if err != nil {
		t.Fatal(err)
	}
	if err = replacement.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 || r.operations[0] != renewed.OperationID || r.sent[0].ManagerAdmission.Nonce != nonce {
		t.Fatal("crash after prepared renewal changed exact input or operation")
	}
	st, _ = restarted.Snapshot()
	input := st.Admissions[admissionKey(b.ConversationID, wake.Admission)]
	if input.Phase != "submitted" || st.Wakes[key(b.ConversationID, wake.ID)].OperationID != current {
		t.Fatal("renewed identity did not survive restart")
	}
}

func trackControllerFixtureWorker(t *testing.T, s *Store, b ManagerBinding) {
	t.Helper()
	if err := s.update(func(st *State) error {
		convo := st.Conversations[b.ConversationID]
		if convo.LastAdmission == 0 {
			convo.LastAdmission = 1
			convo.RetiredThrough = 1
			putRecord(st, "conversations", &st.Conversations, b.ConversationID, convo)
		}
		worker := WorkerAssociation{ConversationID: b.ConversationID, Session: "machine/worker", SessionInstance: "instance", Admission: 1, SourceID: "utterance:prior-task", ActionID: "prior-worker-create"}
		putRecord(st, "workers", &st.Workers, key(b.ConversationID, key(worker.Session, worker.SessionInstance)), worker)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestControllerRejectsOversizedClaudeHeadWithoutBlockingFIFO(t *testing.T) {
	for _, tc := range []struct {
		name               string
		recovery, prepared bool
	}{
		{name: "ordinary"},
		{name: "recovery prefix", recovery: true},
		{name: "recovery prefix added after prepare", recovery: true, prepared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s, r, b := newControllerFixture(t)
			b.Provider = "claude"
			b.ConversationID = "claude-conversation"
			b, err := s.BindManager(b)
			if err != nil {
				t.Fatal(err)
			}
			r.status.Binding = runtimeBinding(b)
			c, err := NewController(s, r, b, ManagerBridgeRefs{Socket: r.status.BridgeSocket, AuthFile: r.status.AuthFile})
			if err != nil {
				t.Fatal(err)
			}
			length := 5000
			if tc.recovery {
				length = 4096 - len("[SWARM_MANAGER_INPUT_V1:") - 64 - len("]\n")
			}
			text := strings.Repeat("a", length)
			first, err := c.SubmitText(Utterance{ID: "large", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, text)
			if err != nil {
				t.Fatal(err)
			}
			if tc.prepared {
				if err = s.observeController(b, 0, false); err != nil {
					t.Fatal(err)
				}
				op, err := protocol.NewSemanticOperationID()
				if err != nil {
					t.Fatal(err)
				}
				nonce := strings.Repeat("a", 64)
				req := protocol.ComposerSendReq{Session: b.SessionID, SessionInstance: b.SessionInstance, Text: text, ManagerAdmission: &protocol.ManagerAdmission{Binding: runtimeBinding(b), Nonce: nonce}}
				if _, err = s.prepareControllerInput(b, first.Sequence, nonce, op, req, 0); err != nil {
					t.Fatal(err)
				}
			}
			if tc.recovery {
				if _, err = s.restrictControllerRecovery(b, first.Sequence); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = c.SubmitText(Utterance{ID: "next", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "valid"); err != nil {
				t.Fatal(err)
			}
			if err = c.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			st, err := s.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			rejected := st.Admissions[admissionKey(b.ConversationID, first.Sequence)]
			if rejected.Phase != "rejected" || rejected.SettledAt.IsZero() || rejected.Reason == "" || len(r.sent) != 1 || r.sent[0].Text != "valid" {
				t.Fatalf("oversized input stalled or reached the native FIFO: phase=%q, sends=%d", rejected.Phase, len(r.sent))
			}
		})
	}
}

type lateDraftRuntime struct {
	*controllerRuntimeFixture
	draft, hideReceipt, loseReply bool
	held                          protocol.SemanticMessageReceipt
}

func (r *lateDraftRuntime) ManagerRuntime(session, instance string) (protocol.ManagerRuntimeStatus, error) {
	if r.draft {
		return protocol.ManagerRuntimeStatus{}, managerproof.ErrDirectInputUnresolved
	}
	return r.controllerRuntimeFixture.ManagerRuntime(session, instance)
}

func (r *lateDraftRuntime) SemanticSend(op string, req protocol.ComposerSendReq) error {
	if r.held.OperationID != "" {
		return r.controllerRuntimeFixture.SemanticSend(op, req)
	}
	r.draft = true
	r.held = protocol.SemanticMessageReceipt{OperationID: op, Session: req.Session, SessionInstance: req.SessionInstance,
		State: "rejected", RejectionCode: protocol.CodeManagerDirectInput}
	if r.loseReply {
		return errors.New("transport closed before refusal acknowledgement")
	}
	return managerproof.ErrDirectInputUnresolved
}

func (r *lateDraftRuntime) SemanticOperation(session, operation string) (protocol.SemanticMessageReceipt, error) {
	if operation == r.held.OperationID {
		if r.hideReceipt {
			return protocol.SemanticMessageReceipt{}, errors.New("receipt temporarily unavailable")
		}
		return r.held, nil
	}
	return r.controllerRuntimeFixture.SemanticOperation(session, operation)
}

func TestDiscussionLateDraftRefusalPreservesExactFIFO(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost acknowledgement %t", lost), func(t *testing.T) {
			c, s, fixture, b := newControllerFixture(t)
			r := &lateDraftRuntime{controllerRuntimeFixture: fixture, loseReply: lost, hideReceipt: lost}
			c.runtime = r
			verifications := 0
			c.discussion = &DiscussionScope{VMProjectAccess: true, InstallationID: "install", GuildID: "guild", ThreadID: "thread",
				VerifyOrigin: func(context.Context, protocol.MessageOrigin) error {
					verifications++
					return nil
				}}
			first, err := c.submitDiscussion(discussionFixtureOrigin("first"), "first", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.submitDiscussion(discussionFixtureOrigin("second"), "second", "", ""); err != nil {
				t.Fatal(err)
			}
			err = c.Tick(t.Context())
			if lost {
				if !errors.Is(err, ErrControllerUncertain) {
					t.Fatalf("lost refusal was initially certain: %v", err)
				}
				r.hideReceipt = false
				err = c.Tick(t.Context())
			}
			if !errors.Is(err, managerproof.ErrDirectInputUnresolved) {
				t.Fatalf("draft did not surface as a hold: %v", err)
			}
			st, err := s.Snapshot()
			a := st.Admissions[admissionKey(b.ConversationID, first.Sequence)]
			if err != nil || a.Phase != "prepared" || !a.SourceCutover || a.OperationID == r.held.OperationID ||
				a.InputText != "first" || len(fixture.sent) != 0 {
				t.Fatalf("wrong admission after no-effect refusal: %+v, %v", a, err)
			}
			r.draft = false
			if err := c.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(fixture.sent) != 1 || fixture.sent[0].Text != "first" || fixture.operations[0] != a.OperationID || verifications != 1 {
				t.Fatalf("sealed FIFO changed: %+v, operations %v, verifications %d", fixture.sent, fixture.operations, verifications)
			}
		})
	}
}
