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

// blockingStartBackend parks the first provider call after it has reached the backend. That
// gives a second composerSend time to enqueue behind it without relying on scheduler timing.
type blockingStartBackend struct {
	base    *r7FakeBackend
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingStartBackend) Call(ctx context.Context, method string, params, out any) error {
	if method == "turn/start" {
		b.once.Do(func() {
			close(b.entered)
			<-b.release
		})
	}
	return b.base.Call(ctx, method, params, out)
}

func (b *blockingStartBackend) Respond(ctx context.Context, id json.RawMessage, result any) error {
	return b.base.Respond(ctx, id, result)
}

func (b *blockingStartBackend) Close() error { return b.base.Close() }

type echoBeforeErrorBackend struct {
	clientRef chan string
	release   chan struct{}
}

func (b *echoBeforeErrorBackend) Call(ctx context.Context, method string, params, _ any) error {
	if method != "turn/start" {
		return nil
	}
	var in struct {
		ClientRef string `json:"clientUserMessageId"`
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	b.clientRef <- in.ClientRef
	select {
	case <-b.release:
		return errors.New("provider reply transport lost")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *echoBeforeErrorBackend) Respond(context.Context, json.RawMessage, any) error { return nil }
func (b *echoBeforeErrorBackend) Close() error                                        { return nil }

// TestComposerFIFO_ConcurrentArrivalsDispatchInAcceptedOrder proves the coordinator is an
// explicit queue, not merely two sends that happened not to overlap. The first start is held
// in provider I/O while the second obtains the next ticket; after release the provider sees
// start(first), steer(second), preserving dialog order and creating only one turn.
func TestComposerFIFO_ConcurrentArrivalsDispatchInAcceptedOrder(t *testing.T) {
	r := newR7ComposerRig(t, true)
	base := newR7FakeBackend()
	base.reply["turn/start"] = json.RawMessage(
		`{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","status":"inProgress"}}`)
	wrapped := &blockingStartBackend{base: base, entered: make(chan struct{}), release: make(chan struct{})}
	r.sk.registerBackend(r.local, "01a00339-a80e-72a0-966f-116427b6b9ce", wrapped)

	type outcome struct {
		code string
		err  error
	}
	firstDone := make(chan outcome, 1)
	go func() {
		code, err := r.sendBare("old-render-a", "first", "devA:01JFIFOCONCURRENTFIRST00")
		firstDone <- outcome{code: string(code), err: err}
	}()
	select {
	case <-wrapped.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first send never reached the blocked turn/start")
	}

	secondDone := make(chan outcome, 1)
	go func() {
		code, err := r.sendBare("old-render-b", "second", "devA:01JFIFOCONCURRENTSECOND0")
		secondDone <- outcome{code: string(code), err: err}
	}()
	// Wait until the second arrival has actually received ticket 1. A sleep would only
	// prove that the goroutine was scheduled, not that it entered the product queue.
	awaitTrue(t, 5*time.Second, "second send never entered the per-session FIFO", func() bool {
		lane := r.sk.composerLaneFor(r.local)
		lane.mu.Lock()
		defer lane.mu.Unlock()
		return lane.nextTicket >= 2 && lane.servingTicket == 0
	})
	close(wrapped.release)

	for i, done := range []<-chan outcome{firstDone, secondDone} {
		select {
		case got := <-done:
			if got.err != nil || got.code != "" {
				t.Fatalf("send %d refused: code %q err %v", i+1, got.code, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("send %d did not finish", i+1)
		}
	}

	calls := base.recorded()
	if len(calls) != 2 || calls[0].Method != "turn/start" || calls[1].Method != "turn/steer" {
		t.Fatalf("provider calls = %v, want turn/start then turn/steer", methodsOf(base))
	}
	for i, want := range []string{"first", "second"} {
		var params struct {
			Input []struct {
				Text string `json:"text"`
			} `json:"input"`
		}
		if err := json.Unmarshal(calls[i].Params, &params); err != nil {
			t.Fatalf("decode call %d: %v", i, err)
		}
		if len(params.Input) != 1 || params.Input[0].Text != want {
			t.Fatalf("provider call %d input = %+v, want %q", i, params.Input, want)
		}
	}
}

// TestComposerFIFO_QueuedDurableOperationStaysPreparedUntilProviderBoundary pins the crash
// distinction: waiting for an older message is not delivery. A process death in this window
// must leave the operation prepared and therefore safe to replay. Only the queue head, after
// all live preflight checks, may fsync executing immediately before provider I/O.
func TestComposerFIFO_QueuedDurableOperationStaysPreparedUntilProviderBoundary(t *testing.T) {
	r := newR7ComposerRig(t, true)
	base := newR7FakeBackend()
	base.reply["turn/start"] = json.RawMessage(
		`{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","status":"inProgress"}}`)
	wrapped := &blockingStartBackend{base: base, entered: make(chan struct{}), release: make(chan struct{})}
	r.sk.registerBackend(r.local, "01a00339-a80e-72a0-966f-116427b6b9ce", wrapped)

	firstDone := make(chan error, 1)
	go func() {
		_, err := r.sendBare("rendered-a", "first", "devA:01JPREPAREDBLOCKER00000")
		firstDone <- err
	}()
	select {
	case <-wrapped.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first send never reached the blocked provider boundary")
	}

	instance, ok := r.sk.sessionInstance(r.local)
	if !ok || instance == "" {
		t.Fatal("rig has no current session instance")
	}
	const opID = "devA:01JPREPAREDWHILEQUEUED0"
	phase, _, err := r.sk.core.ClaimComposerOperation(opID, protocol.ActionComposerSend, r.local, instance, "crash-request")
	if err != nil || phase != "prepared" {
		t.Fatalf("initial durable claim = phase %q err %v, want prepared", phase, err)
	}
	beginCalled := make(chan struct{}, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := r.sk.api.ComposerSendTransactional(r.sk.api.endpointID, opID, protocol.ComposerSendReq{
			Session: r.session, SessionInstance: instance, ExpectedTurn: "rendered-b", Text: "second",
		}, func() error {
			beginCalled <- struct{}{}
			return r.sk.core.BeginComposerOperation(opID)
		})
		secondDone <- err
	}()
	awaitTrue(t, 5*time.Second, "durable send never entered the per-session FIFO", func() bool {
		lane := r.sk.composerLaneFor(r.local)
		lane.mu.Lock()
		defer lane.mu.Unlock()
		return lane.nextTicket >= 2 && lane.servingTicket == 0
	})
	select {
	case <-beginCalled:
		t.Fatal("queued operation entered executing before reaching the provider boundary")
	default:
	}
	phase, _, err = r.sk.core.ClaimComposerOperation(opID, protocol.ActionComposerSend, r.local, instance, "crash-request")
	if err != nil || phase != "prepared" {
		t.Fatalf("queued durable operation = phase %q err %v, want replayable prepared", phase, err)
	}

	close(wrapped.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("durable queued send: %v", err)
	}
	select {
	case <-beginCalled:
	default:
		t.Fatal("queue head reached provider without crossing the durable executing boundary")
	}
	if got := len(base.recorded()); got != 2 {
		t.Fatalf("provider received %d calls, want exactly first+second with no replay duplicate", got)
	}
}

func TestComposerFIFO_ReplacedSessionIsRefusedAtLaneHeadBeforeBegin(t *testing.T) {
	r := newR7ComposerRig(t, true)
	beginCalled := false
	code, err := r.sk.api.ComposerSendTransactional(r.sk.api.endpointID, "devA:01JSTALEINSTANCE0000000", protocol.ComposerSendReq{
		Session: r.session, SessionInstance: "instance-that-was-replaced", Text: "must not cross",
	}, func() error {
		beginCalled = true
		return nil
	})
	if code != protocol.CodeStaleInstance || err == nil {
		t.Fatalf("replaced session = code %q err %v, want stale_instance", code, err)
	}
	if beginCalled {
		t.Fatal("replaced session crossed durable executing boundary")
	}
	if got := len(r.backend.recorded()); got != 0 {
		t.Fatalf("replaced session made %d provider calls, want zero", got)
	}
}

func TestComposerFIFO_MatchingEchoBeforeTransportErrorProvesDelivery(t *testing.T) {
	r := newR7ComposerRig(t, true)
	backend := &echoBeforeErrorBackend{clientRef: make(chan string, 1), release: make(chan struct{})}
	r.sk.registerBackend(r.local, "01a00339-a80e-72a0-966f-116427b6b9ce", backend)

	type outcome struct {
		code protocol.ErrorCode
		err  error
	}
	done := make(chan outcome, 1)
	const opID = "devA:01JECHOPROVESDELIVERY00"
	const message = "echo arrived before the lost reply"
	go func() {
		code, err := r.sendBare("rendered-idle", message, opID)
		done <- outcome{code: code, err: err}
	}()
	var clientRef string
	select {
	case clientRef = <-backend.clientRef:
	case <-time.After(5 * time.Second):
		t.Fatal("composer never reached provider")
	}
	if clientRef == "" {
		t.Fatal("provider call carried no client correlation id")
	}
	fields := map[string]any{}
	r.sk.itemMu.Lock()
	r.sk.stampComposerEchoLocked(r.local, message, clientRef, fields)
	r.sk.itemMu.Unlock()
	if fields["operation_id"] != opID {
		t.Fatalf("matching echo attribution = %+v, want operation %q", fields, opID)
	}
	close(backend.release)
	select {
	case got := <-done:
		if got.err != nil || got.code != "" {
			t.Fatalf("echo-proven delivery returned code %q err %v, want success", got.code, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("composer remained stuck after provider error")
	}
	lane := r.sk.composerLaneFor(r.local)
	lane.mu.Lock()
	uncertain := lane.uncertain
	lane.mu.Unlock()
	if uncertain {
		t.Fatal("echo-proven delivery latched the session uncertain")
	}
}

// TestComposerFIFO_ACompletedNativeTurnBetweenSelectionAndSteerRetriesAsNextTurn drives the
// provider-side half of the race. The daemon selected an active native turn, but turn/steer
// reports its typed precondition failure before the event pump folds completion. The message
// must remain accepted queue work and start the next turn with the same client id.
func TestComposerFIFO_ACompletedNativeTurnBetweenSelectionAndSteerRetriesAsNextTurn(t *testing.T) {
	r := newR7ComposerRig(t, true)
	r7OpenNativeTurn(t, r)
	r.backend.callErr["turn/steer"] = &appserver.RPCError{Code: -32600, Message: "no active turn to steer"}

	code, err := r.send(t, "rendered-old-turn", "next thought", "devA:01JLATESTEERRETRY0000000")
	if err != nil || code != "" {
		t.Fatalf("late native-turn completion refused queued message: code %q err %v", code, err)
	}
	calls := r.backend.recorded()
	if len(calls) != 2 || calls[0].Method != "turn/steer" || calls[1].Method != "turn/start" {
		t.Fatalf("provider calls = %v, want refused turn/steer then turn/start", methodsOf(r.backend))
	}
	var first, second struct {
		ClientID string `json:"clientUserMessageId"`
	}
	if err := json.Unmarshal(calls[0].Params, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(calls[1].Params, &second); err != nil {
		t.Fatal(err)
	}
	if first.ClientID == "" || second.ClientID != first.ClientID {
		t.Fatalf("retry client ids = %q then %q, want one stable non-empty id", first.ClientID, second.ClientID)
	}
}
