package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/protocol"
)

// TestComposerDurability_KeystrokeDialFailureStaysPrepared pins the actual at-most-once
// boundary on the shim-backed composer. Acquiring the tap is preflight: when that dial fails,
// no message bytes can have reached the provider, so the durable operation must remain
// prepared/replayable. Crossing Begin before subscribe would permanently turn this definite
// refusal into outcome_unknown even though there was no possible side effect.
func TestComposerDurability_KeystrokeDialFailureStaysPrepared(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-composer-dial-failure"))
	if err := r.att.Detach(); err != nil {
		t.Fatalf("detach owner before replacing tap: %v", err)
	}
	dialErr := errors.New("shim unavailable before submit")
	r.sk.api.tap = newTapManager(func(string) (protocol.SessionStream, error) {
		return nil, dialErr
	})
	instance, ok := r.sk.sessionInstance(r.local)
	if !ok || instance == "" {
		t.Fatal("rig has no current session instance")
	}
	const operationID = "devA:01JKEYSTROKEDIALFAIL000"
	phase, _, err := r.sk.api.ClaimComposerOperation(operationID, protocol.ActionComposerSend, r.local, instance, "dial-failure-request")
	if err != nil || phase != "prepared" {
		t.Fatalf("initial durable claim = phase %q err %v, want prepared", phase, err)
	}
	beginCalled := false
	code, err := r.sk.api.ComposerSendTransactional(r.sk.api.endpointID, operationID, protocol.ComposerSendReq{
		Session: r.session, SessionInstance: instance, Text: "must stay replayable",
	}, func() error {
		beginCalled = true
		return r.sk.api.BeginComposerOperation(operationID)
	})
	if err == nil || !errors.Is(err, dialErr) {
		t.Fatalf("dial failure = code %q err %v, want the definite pre-send error %v", code, err, dialErr)
	}
	if errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
		t.Fatalf("definite pre-send dial failure was labeled outcome_unknown: %v", err)
	}
	if beginCalled {
		t.Fatal("durable composer crossed Begin before the keystroke tap was acquired")
	}
	if code == protocol.CodeOutcomeUnknown {
		t.Fatalf("definite pre-send dial failure code = %q, want a terminal refusal", code)
	}
	phase, _, err = r.sk.api.ClaimComposerOperation(operationID, protocol.ActionComposerSend, r.local, instance, "dial-failure-request")
	if err != nil || phase != "prepared" {
		t.Fatalf("durable operation after pre-send refusal = phase %q err %v, want replayable prepared", phase, err)
	}
}

type orderedComposerUpstream struct {
	mu     sync.Mutex
	order  []string
	frames chan []byte
}

type backendWriteBoundaryRecorder struct{ base *r7FakeBackend }

func (b *backendWriteBoundaryRecorder) Call(ctx context.Context, method string, params, out any) error {
	return b.base.Call(ctx, method, params, out)
}
func (b *backendWriteBoundaryRecorder) Respond(ctx context.Context, id json.RawMessage, result any) error {
	return b.base.Respond(ctx, id, result)
}
func (b *backendWriteBoundaryRecorder) Close() error { return b.base.Close() }

// CallAtWriteBoundary models the production app-server client's seam: enter the caller's
// boundary, record the request write, then release Stop while the reply is outstanding.
func (b *backendWriteBoundaryRecorder) CallAtWriteBoundary(_ context.Context, method string, params, out any, beforeWrite func() error, afterWrite func()) error {
	if err := beforeWrite(); err != nil {
		return err
	}
	body, _ := json.Marshal(params)
	b.base.mu.Lock()
	b.base.calls = append(b.base.calls, r7Call{Method: method, Params: body})
	err := b.base.callErr[method]
	rep := b.base.reply[method]
	b.base.mu.Unlock()
	afterWrite()
	if err != nil {
		return err
	}
	if out != nil && len(rep) > 0 {
		return json.Unmarshal(rep, out)
	}
	return nil
}

func newOrderedComposerUpstream() *orderedComposerUpstream {
	return &orderedComposerUpstream{frames: make(chan []byte)}
}

func (u *orderedComposerUpstream) Snapshot() []byte      { return nil }
func (u *orderedComposerUpstream) Frames() <-chan []byte { return u.frames }
func (u *orderedComposerUpstream) Resize(int, int) error { return nil }
func (u *orderedComposerUpstream) Close() error          { return nil }
func (u *orderedComposerUpstream) Input([]byte) error    { return nil }
func (u *orderedComposerUpstream) Submit(string) error {
	u.mu.Lock()
	u.order = append(u.order, "submit")
	u.mu.Unlock()
	return nil
}
func (u *orderedComposerUpstream) ControlInput([]byte) error {
	u.mu.Lock()
	u.order = append(u.order, "stop")
	u.mu.Unlock()
	return nil
}
func (u *orderedComposerUpstream) recordedOrder() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.order...)
}

// TestComposerQueue_KeystrokeBeginAndSubmitAreAtomicAgainstStop proves that crossing the
// durable side-effect boundary and starting the shim transaction are one ordering event. If
// Stop can publish its barrier and finish in between, a message admitted before Stop is typed
// only after Stop reported success -- exactly the queue inversion the barrier exists to rule
// out. The correct order lets Submit finish first, then Stop publishes and interrupts.
func TestComposerQueue_KeystrokeBeginAndSubmitAreAtomicAgainstStop(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-composer-stop-boundary"))
	if err := r.att.Detach(); err != nil {
		t.Fatalf("detach owner before replacing tap: %v", err)
	}
	upstream := newOrderedComposerUpstream()
	r.sk.api.tap = newTapManager(func(string) (protocol.SessionStream, error) { return upstream, nil })
	turn := r6OpenTurn(t, r.sk, r.local, "turn to stop", len(interactionItems(t, r.sk, r.local)))
	instance, ok := r.sk.sessionInstance(r.local)
	if !ok || instance == "" {
		t.Fatal("rig has no current session instance")
	}

	begun := make(chan struct{})
	releaseSubmit := make(chan struct{})
	hook := func(string) {
		close(begun)
		<-releaseSubmit
	}
	testHookComposerBegunNotYetSubmitted.Store(&hook)
	t.Cleanup(func() { testHookComposerBegunNotYetSubmitted.Store(nil) })
	type outcome struct {
		code protocol.ErrorCode
		err  error
	}
	composerDone := make(chan outcome, 1)
	go func() {
		code, err := r.sk.api.ComposerSendTransactional(r.sk.api.endpointID,
			"devA:01JKEYSTROKESTOPRACE000", protocol.ComposerSendReq{
				Session: r.session, SessionInstance: instance, ExpectedTurn: turn, Text: "ordered before stop",
			}, func() error { return nil })
		composerDone <- outcome{code: code, err: err}
	}()
	select {
	case <-begun:
	case <-time.After(5 * time.Second):
		t.Fatal("composer never reached the Begin-to-Submit boundary")
	}

	stopDone := make(chan outcome, 1)
	go func() {
		code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID,
			"devA:01JKEYSTROKESTOPINT000", protocol.TurnInterruptReq{
				Session: r.session, ExpectedTurn: turn,
			})
		stopDone <- outcome{code: code, err: err}
	}()
	stopFinishedBeforeSubmit := false
	select {
	case <-stopDone:
		stopFinishedBeforeSubmit = true
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseSubmit)
	select {
	case got := <-composerDone:
		if got.err != nil || got.code != "" {
			t.Fatalf("composer = code %q err %v, want success before Stop", got.code, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("composer remained blocked after release")
	}
	if !stopFinishedBeforeSubmit {
		select {
		case got := <-stopDone:
			if got.err != nil || got.code != "" {
				t.Fatalf("Stop = code %q err %v", got.code, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Stop remained blocked after composer submission")
		}
	}
	if stopFinishedBeforeSubmit {
		t.Fatal("Stop completed after durable Begin but before shim Submit; the old message can start after Stop success")
	}
	if got := upstream.recordedOrder(); len(got) != 2 || got[0] != "submit" || got[1] != "stop" {
		t.Fatalf("shim write order = %v, want [submit stop]", got)
	}
}

// TestComposerQueue_BackendBeginAndRequestWriteAreAtomicAgainstStop is the structured-backend
// twin of the shim test above. The lock need last only until the JSON-RPC request is written;
// Stop may interrupt while its reply is outstanding, but it must not report success and then
// let a pre-Stop message begin afterward.
func TestComposerQueue_BackendBeginAndRequestWriteAreAtomicAgainstStop(t *testing.T) {
	r := newR7ComposerRig(t, true)
	turn := r7OpenNativeTurn(t, r)
	wrapped := &backendWriteBoundaryRecorder{base: r.backend}
	r.sk.registerBackend(r.local, "01a00339-a80e-72a0-966f-116427b6b9ce", wrapped)
	instance, ok := r.sk.sessionInstance(r.local)
	if !ok || instance == "" {
		t.Fatal("rig has no current session instance")
	}

	begun := make(chan struct{})
	releaseCall := make(chan struct{})
	hook := func(string) {
		close(begun)
		<-releaseCall
	}
	testHookComposerBackendBegunNotYetCalled.Store(&hook)
	t.Cleanup(func() { testHookComposerBackendBegunNotYetCalled.Store(nil) })
	type outcome struct {
		code protocol.ErrorCode
		err  error
	}
	composerDone := make(chan outcome, 1)
	go func() {
		code, err := r.sk.api.ComposerSendTransactional(r.sk.api.endpointID,
			"devA:01JBACKENDSTOPRACE00000", protocol.ComposerSendReq{
				Session: r.session, SessionInstance: instance, ExpectedTurn: turn, Text: "ordered before stop",
			}, func() error { return nil })
		composerDone <- outcome{code: code, err: err}
	}()
	select {
	case <-begun:
	case <-time.After(5 * time.Second):
		t.Fatal("composer never reached the Begin-to-backend-write boundary")
	}

	stopDone := make(chan outcome, 1)
	go func() {
		code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID,
			"devA:01JBACKENDSTOPINT00000", protocol.TurnInterruptReq{
				Session: r.session, ExpectedTurn: turn,
			})
		stopDone <- outcome{code: code, err: err}
	}()
	stopFinishedBeforeCall := false
	select {
	case <-stopDone:
		stopFinishedBeforeCall = true
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseCall)
	select {
	case got := <-composerDone:
		if got.err != nil || got.code != "" {
			t.Fatalf("composer = code %q err %v, want success before Stop", got.code, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("composer remained blocked after release")
	}
	if !stopFinishedBeforeCall {
		select {
		case got := <-stopDone:
			if got.err != nil || got.code != "" {
				t.Fatalf("Stop = code %q err %v", got.code, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Stop remained blocked after backend request write")
		}
	}
	if stopFinishedBeforeCall {
		t.Fatal("Stop completed after durable Begin but before backend request write; the old message can start after Stop success")
	}
	methods := methodsOf(r.backend)
	if len(methods) < 2 || methods[len(methods)-2] != "turn/steer" || methods[len(methods)-1] != "turn/interrupt" {
		t.Fatalf("backend call order = %v, want steer written before interrupt", methods)
	}
}

// retryReplyBoundaryBackend models the exact retry interval the provider exposes: the
// selected steer is definitively refused because its turn is already gone, the replacement
// turn/start request is written, and its reply (which carries the new native id) is delayed.
type retryReplyBoundaryBackend struct {
	mu           sync.Mutex
	methods      []string
	startWritten chan struct{}
	releaseStart chan struct{}
	startOnce    sync.Once
}

func newRetryReplyBoundaryBackend() *retryReplyBoundaryBackend {
	return &retryReplyBoundaryBackend{
		startWritten: make(chan struct{}),
		releaseStart: make(chan struct{}),
	}
}

func (b *retryReplyBoundaryBackend) Call(ctx context.Context, method string, params, out any) error {
	return b.call(ctx, method, params, out)
}

func (b *retryReplyBoundaryBackend) CallAtWriteBoundary(ctx context.Context, method string, params, out any, beforeWrite func() error, afterWrite func()) error {
	if err := beforeWrite(); err != nil {
		return err
	}
	b.mu.Lock()
	b.methods = append(b.methods, method)
	b.mu.Unlock()
	afterWrite()
	return b.result(ctx, method, out)
}

func (b *retryReplyBoundaryBackend) call(ctx context.Context, method string, _ any, out any) error {
	b.mu.Lock()
	b.methods = append(b.methods, method)
	b.mu.Unlock()
	return b.result(ctx, method, out)
}

func (b *retryReplyBoundaryBackend) result(ctx context.Context, method string, out any) error {
	switch method {
	case "turn/steer":
		return &appserver.RPCError{Code: -32600, Message: "no active turn to steer"}
	case "turn/start":
		b.startOnce.Do(func() { close(b.startWritten) })
		select {
		case <-b.releaseStart:
			return json.Unmarshal([]byte(`{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea5999"}}`), out)
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		return nil
	}
}

func (b *retryReplyBoundaryBackend) Respond(context.Context, json.RawMessage, any) error {
	return nil
}
func (b *retryReplyBoundaryBackend) Close() error { return nil }

func (b *retryReplyBoundaryBackend) recordedMethods() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.methods...)
}

// TestComposerQueue_StopDoesNotWaitForNoActiveRetryReply makes the retry latency contract
// explicit. Once the replacement turn/start request is written, the old native turn is
// already proven dead and Stop rendered against it is stale. Stop must not wait up to the
// backend reply timeout merely because the new native id has not arrived yet.
func TestComposerQueue_StopDoesNotWaitForNoActiveRetryReply(t *testing.T) {
	r := newR7ComposerRig(t, true)
	turn := r7OpenNativeTurn(t, r)
	backend := newRetryReplyBoundaryBackend()
	r.sk.registerBackend(r.local, "01a00339-a80e-72a0-966f-116427b6b9ce", backend)

	type outcome struct {
		code protocol.ErrorCode
		err  error
	}
	composerDone := make(chan outcome, 1)
	go func() {
		code, err := r.sendBare(turn, "retry without blocking Stop", "devA:01JRETRYSTOPREPLY00000")
		composerDone <- outcome{code: code, err: err}
	}()
	select {
	case <-backend.startWritten:
	case <-time.After(5 * time.Second):
		t.Fatal("composer never wrote the retry turn/start request")
	}

	stopDone := make(chan outcome, 1)
	go func() {
		code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID,
			"devA:01JRETRYSTOPINTERRUPT00", protocol.TurnInterruptReq{
				Session: r.session, ExpectedTurn: turn,
			})
		stopDone <- outcome{code: code, err: err}
	}()
	var stopBeforeReply *outcome
	select {
	case got := <-stopDone:
		stopBeforeReply = &got
	case <-time.After(150 * time.Millisecond):
	}
	close(backend.releaseStart)
	select {
	case got := <-composerDone:
		if got.err != nil || got.code != "" {
			t.Fatalf("composer = code %q err %v, want successful retry", got.code, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("composer remained blocked after retry reply release")
	}
	if stopBeforeReply == nil {
		select {
		case <-stopDone:
		case <-time.After(5 * time.Second):
			t.Fatal("Stop remained blocked after retry reply")
		}
		t.Fatalf("Stop waited for the no-active retry reply; it can be delayed for the full backend timeout")
	}
	if stopBeforeReply.code != protocol.CodeStaleTurn || stopBeforeReply.err == nil {
		t.Fatalf("Stop during replacement start = code %q err %v, want stale_turn against the proven-dead old turn", stopBeforeReply.code, stopBeforeReply.err)
	}
	for _, method := range backend.recordedMethods() {
		if method == "turn/interrupt" {
			t.Fatalf("backend calls = %v: stale Stop interrupted while replacement turn was starting", backend.recordedMethods())
		}
	}
}

// stopBarrierBackend makes the provider order deterministic: composer steer A enters first
// but does not resolve; Stop interrupts A; only then does the steer learn that A is gone.
// A queue that treats Stop as a barrier must not turn either the in-flight or queued message
// into a fresh turn after Stop has already reported success.
type stopBarrierBackend struct {
	mu            sync.Mutex
	methods       []string
	steers        int
	steerEntered  chan struct{}
	steerReturned chan struct{}
	releaseSteer  chan struct{}
	once          sync.Once
	returnOnce    sync.Once
}

func newStopBarrierBackend() *stopBarrierBackend {
	return &stopBarrierBackend{
		steerEntered: make(chan struct{}), steerReturned: make(chan struct{}), releaseSteer: make(chan struct{}),
	}
}

func (b *stopBarrierBackend) Call(ctx context.Context, method string, _, out any) error {
	b.mu.Lock()
	b.methods = append(b.methods, method)
	if method == "turn/steer" {
		b.steers++
	}
	steer := b.steers
	b.mu.Unlock()

	switch {
	case method == "turn/steer" && steer == 1:
		b.once.Do(func() { close(b.steerEntered) })
		select {
		case <-b.releaseSteer:
			b.returnOnce.Do(func() { close(b.steerReturned) })
			return &appserver.RPCError{Code: -32600, Message: "no active turn to steer"}
		case <-ctx.Done():
			return ctx.Err()
		}
	case method == "turn/start":
		return json.Unmarshal([]byte(`{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea5999"}}`), out)
	default:
		return nil
	}
}

func TestComposerQueue_StopPrecheckAndBarrierAreAtomicAgainstSteerRetry(t *testing.T) {
	r := newR7ComposerRig(t, true)
	turn := r7OpenNativeTurn(t, r)
	backend := newStopBarrierBackend()
	r.sk.registerBackend(r.local, "01a00339-a80e-72a0-966f-116427b6b9ce", backend)

	composerDone := make(chan error, 1)
	go func() {
		_, err := r.sendBare(turn, "message racing Stop", "devA:01JATOMICSTOPRETRY00000")
		composerDone <- err
	}()
	select {
	case <-backend.steerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("composer never reached initial steer")
	}

	validated := make(chan struct{})
	releaseBarrier := make(chan struct{})
	hook := func(string) {
		close(validated)
		<-releaseBarrier
	}
	testHookInterruptValidatedNotYetBarrier.Store(&hook)
	t.Cleanup(func() { testHookInterruptValidatedNotYetBarrier.Store(nil) })
	stopDone := make(chan error, 1)
	go func() {
		_, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JATOMICSTOPINTERRUPT0", protocol.TurnInterruptReq{
			Session: r.session, ExpectedTurn: turn,
		})
		stopDone <- err
	}()
	select {
	case <-validated:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never reached the validated precheck/barrier boundary")
	}

	// The initial steer now learns the provider turn ended, while Stop is paused at the
	// exact old race window. Its retry must wait for the same lane lock, then observe the
	// barrier; it must not start a replacement turn before Stop publishes the barrier.
	close(backend.releaseSteer)
	select {
	case <-backend.steerReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("initial steer did not return its typed no-active refusal")
	}
	close(releaseBarrier)
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish")
	}
	select {
	case <-composerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("composer did not leave after Stop barrier")
	}

	methods := backend.recordedMethods()
	for _, method := range methods {
		if method == "turn/start" {
			t.Fatalf("backend calls = %v: composer retried into a new turn between Stop precheck and barrier", methods)
		}
	}
}

func (b *stopBarrierBackend) Respond(context.Context, json.RawMessage, any) error { return nil }
func (b *stopBarrierBackend) Close() error                                        { return nil }

func (b *stopBarrierBackend) recordedMethods() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.methods...)
}

func TestComposerQueue_StopSuccessIsABarrierAgainstStartingANewTurn(t *testing.T) {
	r := newR7ComposerRig(t, true)
	turn := r7OpenNativeTurn(t, r)
	backend := newStopBarrierBackend()
	r.sk.registerBackend(r.local, "01a00339-a80e-72a0-966f-116427b6b9ce", backend)

	type result struct {
		code protocol.ErrorCode
		err  error
	}
	firstDone := make(chan result, 1)
	go func() {
		code, err := r.sendBare(turn, "first queued message", "devA:01JSTOPBARRIERFIRST0000")
		firstDone <- result{code: code, err: err}
	}()
	select {
	case <-backend.steerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first composer send never reached turn/steer")
	}

	secondDone := make(chan result, 1)
	go func() {
		code, err := r.sendBare(turn, "second queued message", "devA:01JSTOPBARRIERSECOND000")
		secondDone <- result{code: code, err: err}
	}()
	awaitTrue(t, 5*time.Second, "second send never entered the session queue", func() bool {
		lane := r.sk.composerLaneFor(r.local)
		lane.mu.Lock()
		defer lane.mu.Unlock()
		return lane.nextTicket >= 2 && lane.servingTicket == 0
	})

	if code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JSTOPBARRIERINTERRUPT0", protocol.TurnInterruptReq{
		Session: r.session, ExpectedTurn: turn,
	}); err != nil || code != "" {
		t.Fatalf("Stop did not succeed: code %q err %v", code, err)
	}
	r.sk.ingestBackendFrame(r.local, []byte(r7RecordedTurnCompletedFrame), time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)
	awaitTrue(t, 5*time.Second, "successful Stop never closed the daemon turn", func() bool {
		r.sk.itemMu.Lock()
		defer r.sk.itemMu.Unlock()
		return r.sk.turnIDs[r.local] == ""
	})
	close(backend.releaseSteer)

	for i, done := range []<-chan result{firstDone, secondDone} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("composer send %d remained stuck after Stop", i+1)
		}
	}

	methods := backend.recordedMethods()
	interruptAt := -1
	for i, method := range methods {
		if method == "turn/interrupt" {
			interruptAt = i
			break
		}
	}
	if interruptAt < 0 {
		t.Fatalf("backend calls = %v, want turn/interrupt", methods)
	}
	for _, method := range methods[interruptAt+1:] {
		if method == "turn/start" || method == "turn/steer" {
			t.Fatalf("backend calls = %v: a queued composer dispatch ran after Stop succeeded; Stop must be an ordering barrier, not an OK immediately followed by a new turn", methods)
		}
	}
}

func TestComposerIdempotency_OperationOutcomeKeepsOneSessionNamespace(t *testing.T) {
	r := newR7ComposerRig(t, true)
	const operationID = "devA:01JCOMPOSEROUTCOMENS0000"
	existed, err := r.sk.api.ClaimOperation(operationID, protocol.ActionComposerSend, r.session)
	if err != nil || existed {
		t.Fatalf("ClaimOperation = existed %v err %v, want fresh", existed, err)
	}
	if err := r.sk.api.CommitIdempotentOp(operationID, true); err != nil {
		t.Fatalf("CommitIdempotentOp: %v", err)
	}

	out, ok := r.sk.api.RemoteOperationOutcome(operationID)
	if !ok {
		t.Fatal("completed composer operation has no operation_status outcome")
	}
	if out.SessionID != r.session {
		t.Fatalf("composer operation outcome session = %q, want exactly %q (one endpoint namespace)", out.SessionID, r.session)
	}
}

func TestComposerIdempotency_FailedSendIsNeverReportedAppliedByOperationStatus(t *testing.T) {
	r := newR7ComposerRig(t, true)
	const operationID = "devA:01JCOMPOSERFAILEDSTATUS0"
	instance, ok := r.sk.sessionInstance(r.local)
	if !ok {
		t.Fatal("rig has no session instance")
	}
	if _, _, err := r.sk.api.ClaimComposerOperation(operationID, protocol.ActionComposerSend, r.local, instance, "failed-status-request"); err != nil {
		t.Fatalf("ClaimComposerOperation: %v", err)
	}
	if err := r.sk.api.CommitComposerOperation(operationID, []byte(`{"ok":false,"code":"input_busy"}`), false); err != nil {
		t.Fatalf("CommitComposerOperation: %v", err)
	}

	out, ok := r.sk.api.RemoteOperationOutcome(operationID)
	if !ok || out.State != "outcome_unknown" {
		t.Fatalf("failed composer operation status = ok %v %+v, want outcome_unknown and never applied", ok, out)
	}
}
