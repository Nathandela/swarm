package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func recoveryRequest(t *testing.T, c *managerControl, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "http://voice-control"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.ServeHTTP(w, req)
	return w
}

func TestManagerControlResumeRequiresExactBindingAndExternalInput(t *testing.T) {
	c, s, runtime, binding := newControllerFixture(t)
	control := &managerControl{store: s, binding: binding, controller: c, wake: make(chan struct{}, 1)}
	runtime.status.Turn.ExternalInput = 4
	if err := s.observeController(binding, 4, true); err != nil {
		t.Fatal(err)
	}
	bad := binding
	bad.Generation++
	for _, body := range []map[string]any{
		{"expected": bad, "external_input": 4},
		{"expected": binding, "external_input": 3},
	} {
		if w := recoveryRequest(t, control, http.MethodPost, "/v1/recovery/resume", body); w.Code != http.StatusPreconditionFailed {
			t.Fatalf("stale resume status %d: %s", w.Code, w.Body)
		}
	}
	w := recoveryRequest(t, control, http.MethodPost, "/v1/recovery/resume", map[string]any{"expected": binding, "external_input": 4})
	if w.Code != http.StatusOK {
		t.Fatalf("resume status %d: %s", w.Code, w.Body)
	}
	st, err := s.Snapshot()
	if err != nil || st.Conversations[binding.ConversationID].SchedulerPaused {
		t.Fatalf("manual resume did not clear the exact pause: %v", err)
	}
	if len(runtime.sent) != 0 {
		t.Fatal("resuming scheduling submitted a new native input")
	}
}

func TestManagerControlClarificationReturnsAndAnswersExactQuestion(t *testing.T) {
	c, s, _, binding := newControllerFixture(t)
	control := &managerControl{store: s, binding: binding, controller: c, wake: make(chan struct{}, 1)}
	question, err := s.PutRequest(binding, Request{ID: "question", Kind: "manager", Payload: json.RawMessage(`{"question":"Proceed?"}`), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	w := recoveryRequest(t, control, http.MethodGet, "/v1/recovery/clarification", nil)
	var got Request
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.ID != question.ID || got.Hash != question.Hash {
		t.Fatalf("clarification status %d: %s", w.Code, w.Body)
	}
	body := map[string]any{"expected": binding, "request_id": question.ID, "request_hash": question.Hash, "answer_id": "owner-answer", "answer": "yes"}
	for range 2 {
		w = recoveryRequest(t, control, http.MethodPost, "/v1/recovery/answer", body)
		var receipt Admission
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Sequence != 1 || receipt.InputText != "" {
			t.Fatalf("answer status %d: %s", w.Code, w.Body)
		}
	}
	body["answer"] = "changed"
	if w = recoveryRequest(t, control, http.MethodPost, "/v1/recovery/answer", body); w.Code != http.StatusConflict {
		t.Fatalf("conflicting exact answer status %d: %s", w.Code, w.Body)
	}
}

type replacementServiceFixture struct {
	*controllerRuntimeFixture
	mu           sync.Mutex
	rows         []protocol.SessionView
	native       protocol.ManagerRuntimeStatus
	outcome      schema.OperationOutcomeView
	refusal      error
	beforeLaunch func(string, protocol.LaunchReq)
	calls        []string
}

func (f *replacementServiceFixture) List() ([]protocol.SessionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.SessionView(nil), f.rows...), nil
}

func (f *replacementServiceFixture) LaunchOperation(op string, req protocol.LaunchReq) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refusal != nil {
		return "", "", f.refusal
	}
	f.beforeLaunch(op, req)
	f.calls = append(f.calls, op)
	f.rows = append(f.rows, protocol.SessionView{ID: "endpoint/fresh", EndpointID: "endpoint", Role: "manager", Agent: req.Agent,
		ManagerConversationID: req.Manager.ConversationID, Cwd: req.Cwd, Capabilities: &protocol.SessionCapabilities{SessionInstance: "fresh-instance"},
		Status: status.Status{Process: status.ProcessRunning}})
	return "", "", errors.New("lost owner launch reply")
}

func (f *replacementServiceFixture) LaunchOperationStatus(string) (schema.OperationOutcomeView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outcome, nil
}

func (f *replacementServiceFixture) ManagerRuntime(session, instance string) (protocol.ManagerRuntimeStatus, error) {
	if session != "endpoint/fresh" || instance != "fresh-instance" {
		return protocol.ManagerRuntimeStatus{}, managerproof.ErrQuarantined
	}
	return f.native, nil
}

func TestManagerServiceReplacementReconcilesExactLaunchAfterRestart(t *testing.T) {
	dir := privateServerDir(t)
	cfg := testConfig()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.BindManager(ManagerBinding{ConversationID: cfg.ConversationID, Provider: cfg.Provider, SessionID: "endpoint/old",
		SessionInstance: "old-instance", NativeSessionID: "old-native", NativeGeneration: "old-generation",
		ObservationGeneration: "old-observation", LoginEpoch: "account"})
	if err != nil {
		t.Fatal(err)
	}
	mustAdmit(t, store, old, "historical")
	if err = store.noteControllerCustodyLost(old); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	refs := ManagerBridgeRefs{Socket: filepath.Join(dir, "bridge.sock"), AuthFile: filepath.Join(dir, managerBridgeAuth)}
	fresh := old
	fresh.SessionID, fresh.SessionInstance, fresh.NativeSessionID = "endpoint/fresh", "fresh-instance", "fresh-native"
	fresh.NativeGeneration, fresh.ObservationGeneration = "fresh-generation", "fresh-observation"
	worker := protocol.SessionView{ID: "endpoint/worker", Agent: cfg.Provider, Status: status.Status{Process: status.ProcessRunning}}
	previous := protocol.SessionView{ID: old.SessionID, Agent: cfg.Provider, Role: "manager", ManagerConversationID: cfg.ConversationID,
		Capabilities: &protocol.SessionCapabilities{SessionInstance: old.SessionInstance}, Status: status.Status{Process: status.ProcessRunning}}
	fixture := &replacementServiceFixture{
		controllerRuntimeFixture: &controllerRuntimeFixture{receipts: map[string]protocol.SemanticMessageReceipt{}},
		rows:                     []protocol.SessionView{previous, worker},
		native: protocol.ManagerRuntimeStatus{Binding: runtimeBinding(fresh), ReadyForInput: true, BridgeSocket: refs.Socket, AuthFile: refs.AuthFile,
			Turn: protocol.ManagerNativeTurn{State: "idle"}},
	}
	launchObserved := make(chan error, 1)
	fixture.beforeLaunch = func(op string, req protocol.LaunchReq) {
		intent, err := (&managerReplacement{dir: dir, endpoint: "endpoint", config: cfg}).read()
		if err == nil && (intent.Receipt.Phase != "executing" || intent.Receipt.OperationID != op ||
			intent.Receipt.Previous.SessionID != old.SessionID || req.InitialPrompt != "" || req.Options[protocol.OptionResumeFrom] != "" ||
			req.Manager == nil || req.Manager.ConversationID != cfg.ConversationID) {
			err = errors.New("launch lacked an exact durable fresh intent")
		}
		select {
		case launchObserved <- err:
		default:
		}
	}
	client := NewManagerClient(dir)
	defer client.Close()
	var stop context.CancelFunc
	var done chan error
	start := func() {
		var ctx context.Context
		ctx, stop = context.WithCancel(context.Background())
		done = make(chan error, 1)
		go func() { done <- serveManager(ctx, dir, "endpoint", cfg, fixture, nil, nil) }()
	}
	wait := func(check func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			select {
			case err := <-done:
				done = nil
				t.Fatalf("foreground service exited during recovery: %v", err)
			default:
			}
			if check() {
				return
			}
		}
		t.Fatal("recovery did not become observable")
	}
	defer func() {
		if done != nil {
			stop()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	ctx := context.Background()
	start()
	var before ManagerStatus
	wait(func() bool {
		before, err = client.Status(ctx)
		return err == nil && before.CustodyLost && before.Binding.SessionID == old.SessionID
	})
	stale := before.Binding
	stale.Generation--
	if _, err = client.ReplaceManager(ctx, stale); !errors.Is(err, ErrStale) {
		t.Fatalf("replacement accepted stale generation: %v", err)
	}
	if _, err = client.ReplaceManager(ctx, before.Binding); err == nil {
		t.Fatal("replacement accepted an old session without exit proof")
	}
	if _, err = client.Replacement(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused replacement created an intent: %v", err)
	}
	fixture.mu.Lock()
	fixture.rows[0].Status.Process = status.ProcessExited
	foreign := previous
	foreign.ID = "endpoint/foreign-manager"
	fixture.rows = append(fixture.rows, foreign)
	fixture.mu.Unlock()
	if _, err = client.ReplaceManager(ctx, before.Binding); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement ignored a competing manager: %v", err)
	}
	fixture.mu.Lock()
	fixture.rows = fixture.rows[:2]
	fixture.mu.Unlock()
	queued, err := client.ReplaceManager(ctx, before.Binding)
	if err != nil || queued.Phase != "prepared" {
		t.Fatalf("explicit replacement queue: %+v %v", queued, err)
	}
	select {
	case launchErr := <-launchObserved:
		if launchErr != nil {
			t.Fatal(launchErr)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("replacement launch was never dispatched")
	}
	wait(func() bool {
		current, err := client.Replacement(ctx)
		return err == nil && current.OperationID == queued.OperationID && current.Phase == "executing" && current.Attention != ""
	})
	if receipt, err := client.Receipt(ctx, "historical"); err != nil || receipt.Sequence != 1 {
		t.Fatalf("prior receipt unavailable in quarantine: %+v %v", receipt, err)
	}
	if repeated, err := client.ReplaceManager(ctx, before.Binding); err != nil || repeated.OperationID != queued.OperationID {
		t.Fatalf("duplicate replacement forked an operation: %+v %v", repeated, err)
	}
	stop()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	done = nil
	fixture.mu.Lock()
	fixture.outcome = schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "endpoint/fresh"}
	fixture.mu.Unlock()
	start()
	wait(func() bool {
		current, err := client.Status(ctx)
		return err == nil && current.Ready && !current.CustodyLost && current.Binding.SessionID == fresh.SessionID &&
			current.Binding.Generation > before.Binding.Generation
	})
	adopted, err := client.Replacement(ctx)
	if err != nil || adopted.Phase != "adopted" || adopted.OperationID != queued.OperationID || adopted.SessionID != fresh.SessionID {
		t.Fatalf("replacement was not reconciled from its exact owner receipt: %+v %v", adopted, err)
	}
	if receipt, err := client.Receipt(ctx, "historical"); err != nil || receipt.Sequence != 1 || !receipt.NativeBindingRetired {
		t.Fatalf("historical admission was lost or replayable: %+v %v", receipt, err)
	}
	fixture.mu.Lock()
	count, workerProcess := len(fixture.calls), fixture.rows[1].Status.Process
	fixture.mu.Unlock()
	if count != 1 || workerProcess != status.ProcessRunning {
		t.Fatal("recovery relaunched the operation or stopped a worker")
	}
}

func TestManagerControlAcknowledgesExactUncertaintyWithoutSettlingEffect(t *testing.T) {
	c, s, runtime, binding := provenControllerFixture(t)
	c.workers = &controllerWorkerFixture{launchErr: errors.New("reply lost"), outcome: schema.OperationOutcomeView{State: schema.OutcomeUnknown}}
	if _, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"work","agent":"codex","cwd":"/tmp","prompt":"work"}`)}); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal(err)
	}
	runtime.status.Turn.State, runtime.status.ReadyForInput = "completed", true
	if err := c.Tick(context.Background()); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal(err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	admission := admissionKey(binding.ConversationID, 1)
	a := snap.Admissions[admission]
	control := &managerControl{store: s, binding: binding, controller: c, wake: make(chan struct{}, 1)}
	body := map[string]any{"expected": binding, "admission": a.Sequence, "operation_id": a.OperationID, "native_turn_id": "stale", "external_input": 0}
	if w := recoveryRequest(t, control, http.MethodPost, "/v1/recovery/acknowledge", body); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("acknowledgement accepted the wrong native turn: %d", w.Code)
	}
	body["native_turn_id"] = a.NativeTurnID
	if w := recoveryRequest(t, control, http.MethodPost, "/v1/recovery/acknowledge", body); w.Code != http.StatusOK {
		t.Fatalf("exact acknowledgement status %d", w.Code)
	}
	snap, err = s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	a = snap.Admissions[admission]
	if !a.RecoveryAcknowledged || !a.SettledAt.IsZero() || snap.Conversations[binding.ConversationID].RetiredThrough != 0 {
		t.Fatal("acknowledgement retired unresolved effect authority")
	}
}

func TestManagerReplacementFenceDoesNotResetMissingIntent(t *testing.T) {
	dir := privateServerDir(t)
	r := &managerReplacement{dir: dir, config: testConfig()}
	if err := writeVoiceFile(dir, r.file()+".initialized", []byte("replacement-v1\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.read(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("missing previously dispatched intent reset its namespace: %v", err)
	}
}

func TestManagerReplacementRenewsOnlyUndispatchedExpiredIntent(t *testing.T) {
	_, store, _, binding := newControllerFixture(t)
	if err := store.noteControllerCustodyLost(binding); err != nil {
		t.Fatal(err)
	}
	dir := privateServerDir(t)
	cfg := testConfig()
	cfg.ConversationID = binding.ConversationID
	runtime := &replacementServiceFixture{rows: []protocol.SessionView{{
		ID: binding.SessionID, Role: "manager", Agent: binding.Provider, ManagerConversationID: binding.ConversationID,
		Capabilities: &protocol.SessionCapabilities{SessionInstance: binding.SessionInstance}, Status: status.Status{Process: status.ProcessExited},
	}}}
	r := &managerReplacement{dir: dir, endpoint: "machine", config: cfg, runtime: runtime,
		refs: ManagerBridgeRefs{Socket: filepath.Join(dir, "bridge.sock"), AuthFile: filepath.Join(dir, managerBridgeAuth)}}
	control := &managerControl{store: store, binding: binding, replacement: r}
	ctx := context.Background()
	expired := fmt.Sprintf("launch:%d:%s", time.Now().Add(-2*time.Hour).UnixMilli(), strings.Repeat("a", 32))
	intent := replacementIntent{Version: 1, Receipt: ManagerReplacement{Previous: binding, OperationID: expired, Phase: "prepared"},
		Request: managerLaunchRequest(cfg, r.refs)}
	if err := r.write(intent); err != nil {
		t.Fatal(err)
	}
	if adopted, err := control.progressReplacement(ctx); adopted || !errors.Is(err, ErrExpired) {
		t.Fatalf("expired unstarted launch was dispatched: %v %v", adopted, err)
	}
	retry, err := control.queueReplacement(ctx, binding)
	if err != nil || retry.OperationID == expired || retry.Phase != "prepared" {
		t.Fatalf("explicit owner request did not renew known unsent intent: %+v %v", retry, err)
	}
	intent.Receipt.Phase = "executing"
	if err := r.write(intent); err != nil {
		t.Fatal(err)
	}
	retry, err = control.queueReplacement(ctx, binding)
	if err != nil || retry.OperationID != expired || retry.Phase != "executing" {
		t.Fatalf("expired uncertain intent was replaced: %+v %v", retry, err)
	}
}

func TestManagerReplacementPrerequisiteRetainsPreparedIntent(t *testing.T) {
	_, store, _, binding := newControllerFixture(t)
	if err := store.noteControllerCustodyLost(binding); err != nil {
		t.Fatal(err)
	}
	dir := privateServerDir(t)
	cfg := testConfig()
	cfg.ConversationID = binding.ConversationID
	runtime := &replacementServiceFixture{
		refusal: protocol.ErrManagerLaunchPrerequisite,
		rows: []protocol.SessionView{{
			ID: binding.SessionID, Role: "manager", Agent: binding.Provider, ManagerConversationID: binding.ConversationID,
			Capabilities: &protocol.SessionCapabilities{SessionInstance: binding.SessionInstance},
			Status:       status.Status{Process: status.ProcessExited},
		}},
	}
	r := &managerReplacement{dir: dir, endpoint: "machine", config: cfg, runtime: runtime,
		refs: ManagerBridgeRefs{Socket: filepath.Join(dir, "bridge.sock"), AuthFile: filepath.Join(dir, managerBridgeAuth)}}
	control := &managerControl{store: store, binding: binding, replacement: r}
	ctx := t.Context()
	queued, err := control.queueReplacement(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if adopted, err := control.progressReplacement(ctx); adopted || !errors.Is(err, protocol.ErrManagerLaunchPrerequisite) ||
		serviceAttention(err) != protocol.ErrManagerLaunchPrerequisite.Error() {
		t.Fatalf("known-unsent replacement had the wrong outcome: adopted=%v %v", adopted, err)
	}
	intent, err := r.read()
	if err != nil || intent.Receipt.Phase != "prepared" || intent.Receipt.OperationID != queued.OperationID {
		t.Fatalf("replacement lost its prepared operation: %+v %v", intent, err)
	}
}
