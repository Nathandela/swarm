package skeleton

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// TestAuthRecycleComposerUncertaintyUsesTheAuthoritativeTuple pins the safety
// predicate the auth watcher and supervisor share. Merely finding an existing
// lane is not unsafe, and an old uncertainty bit must not wedge recycling after
// the folded provider state has made progress. With the exact tuple unchanged,
// however, the provider effect remains unknown and an automatic recycle must
// defer.
func TestAuthRecycleComposerUncertaintyUsesTheAuthoritativeTuple(t *testing.T) {
	d := &Daemon{}
	const local = "session"
	if d.composerOutcomeUnresolved(local) {
		t.Fatal("a session with no composer lane reports an unresolved outcome")
	}
	if _, created := d.composerLanes.Load(local); created {
		t.Fatal("the read-only unresolved predicate created a composer lane")
	}

	lane := d.composerLaneFor(local)
	d.withComposerInteractionState(lane, func() {
		d.initInteractionsLocked()
		d.turnIDs[local] = "turn-a"
		d.closedTurns[local] = "closed-before-a"
		lane.uncertain = true
		lane.uncertainTurn = "turn-a"
		lane.uncertainClosed = "closed-before-a"
		lane.uncertainProgress = lane.progress.Load()
	})
	if !d.composerOutcomeUnresolved(local) {
		t.Fatal("the unchanged uncertainty tuple did not block auth recycling")
	}

	// A folded item is authoritative progress. The safety read must consume that
	// evidence itself; requiring another user send to lazily clear uncertainty
	// would leave an otherwise idle stale-auth session unrecyclable forever.
	lane.progress.Add(1)
	if d.composerOutcomeUnresolved(local) {
		t.Fatal("authoritative composer progress did not clear the old uncertainty tuple")
	}
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.uncertain {
		t.Fatal("resolved composer uncertainty remained latched in the lane")
	}
}

// TestAuthRecycleFenceEmbargoesQueuedComposerUntilTheOldLaneIsRetired models
// daemon.Kill's real contract: success means SIGTERM was delivered, not that
// Core has already recorded ProcessExited. A composer accepted behind that
// successful fence must therefore be refused before Begin/provider bytes even
// while the stale Core row still says Running.
func TestAuthRecycleFenceEmbargoesQueuedComposerUntilTheOldLaneIsRetired(t *testing.T) {
	r := newR7ComposerRig(t, true)
	if err := r.att.Detach(); err != nil {
		t.Fatalf("detach owner so only recycle state can be unsafe: %v", err)
	}
	if m, ok := r.sk.core.Get(r.local); !ok || m.Status.Process != status.ProcessRunning {
		t.Fatalf("fixture process = %#v/%v, want Running", m.Status, ok)
	}
	if r.sk.authw.sessionUnsafe(r.local) {
		t.Fatal("clean unattended fixture is unsafe before recycle")
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	fenceDone := make(chan error, 1)
	go func() {
		fenceDone <- r.sk.withAuthRecycleFence(r.local, func() error {
			close(entered)
			<-release
			return nil // the asynchronous kill signal was accepted
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("auth recycle never acquired the composer lane")
	}
	if !r.sk.composerRecycleInFlight(r.local) {
		t.Fatal("recycle queue head did not publish its transient embargo before the kill attempt")
	}
	if r.sk.authw.sessionUnsafe(r.local) {
		t.Fatal("auth watcher's final safety callback sees and self-refuses its own transient embargo")
	}

	type outcome struct {
		code protocol.ErrorCode
		err  error
	}
	sendDone := make(chan outcome, 1)
	go func() {
		code, err := r.sendBare("", "must not reach the dying provider", "devA:01JAUTHRECYCLEQUEUED00")
		sendDone <- outcome{code: code, err: err}
	}()
	awaitTrue(t, 5*time.Second, "composer never queued behind the recycle fence", func() bool {
		lane := r.sk.composerLaneFor(r.local)
		lane.mu.Lock()
		defer lane.mu.Unlock()
		return lane.nextTicket >= 2 && lane.servingTicket == 0
	})
	close(release)
	if err := <-fenceDone; err != nil {
		t.Fatalf("successful recycle fence: %v", err)
	}
	if !r.sk.authw.sessionUnsafe(r.local) {
		t.Fatal("production auth-watch unsafe source omitted the successful recycle embargo")
	}
	if err := r.sk.srv.SendInput(r.local, protocol.SendInputReq{Text: "must not be typed", Submit: true}); err == nil || !strings.Contains(err.Error(), "recycl") {
		t.Fatalf("owner typed-input gate did not refuse during recycle: %v", err)
	}
	select {
	case got := <-sendDone:
		if got.code != protocol.CodeInputBusy || got.err == nil || !strings.Contains(got.err.Error(), "recycl") {
			t.Fatalf("queued send = code %q err %v, want explicit input_busy recycle refusal", got.code, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued composer did not settle after the recycle fence")
	}
	if calls := r.backend.recorded(); len(calls) != 0 {
		t.Fatalf("queued composer wrote %d provider calls after successful recycle", len(calls))
	}
}

func TestFailedAuthRecycleFenceDoesNotEmbargoComposer(t *testing.T) {
	r := newR7ComposerRig(t, true)
	wantErr := errors.New("kill refused")
	if err := r.sk.withAuthRecycleFence(r.local, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("failed recycle fence = %v, want %v", err, wantErr)
	}
	code, err := r.sendBare("", "still live", "devA:01JAUTHRECYCLEFAILED000")
	if err != nil || code != "" {
		t.Fatalf("composer after failed recycle = code %q err %v, want success", code, err)
	}
	if calls := r.backend.recorded(); len(calls) != 1 {
		t.Fatalf("provider calls after failed recycle = %d, want 1", len(calls))
	}
}

func TestSuccessfulAuthRecycleEmbargoCannotBeClearedByALaterAttempt(t *testing.T) {
	r := newR7ComposerRig(t, true)
	if err := r.sk.withAuthRecycleFence(r.local, func() error { return nil }); err != nil {
		t.Fatalf("successful recycle fence: %v", err)
	}
	called := false
	err := r.sk.withAuthRecycleFence(r.local, func() error {
		called = true
		return errors.New("duplicate kill failed")
	})
	if !errors.Is(err, errAuthRecycleUnsafe) {
		t.Fatalf("second recycle fence = %v, want the package unsafe sentinel", err)
	}
	if called {
		t.Fatal("an already-embargoed lane invoked a second kill attempt")
	}
	if !r.sk.composerRecycleInFlight(r.local) {
		t.Fatal("a later recycle attempt cleared the successful embargo")
	}
	if calls := r.backend.recorded(); len(calls) != 0 {
		t.Fatalf("duplicate recycle produced %d provider calls", len(calls))
	}
}

func TestComposerRecycleEmbargoSurvivesProcessRetirementUntilClaimClears(t *testing.T) {
	d := &Daemon{}
	const local = "retired"
	if err := d.withAuthRecycleFence(local, func() error { return nil }); err != nil {
		t.Fatalf("successful recycle fence: %v", err)
	}
	if !d.composerRecycleInFlight(local) {
		t.Fatal("successful recycle did not publish an embargo")
	}
	d.forgetInteractions(local)
	if !d.composerRecycleInFlight(local) {
		t.Fatal("process retirement dropped a live recycle obligation")
	}
	d.clearAuthRecycle(local)
	if lane := d.composerLaneFor(local); lane.recyclingNow() {
		t.Fatal("a resolved recycle claim leaked into a reused local session id")
	}
}

// TestAuthRecycleFencesADirectSendThatAlreadyPassedItsEarlyGate is the exact
// direct-input TOCTOU: send_input has observed a safe gate but is parked on the
// session input locks; recycle then begins. The recycler must publish its
// transient embargo before waiting for those same locks, and sendMessage must
// recheck after acquiring them, so the stale message writes zero bytes whether
// the parked send or recycler obtains the lock next.
func TestAuthRecycleFencesADirectSendThatAlreadyPassedItsEarlyGate(t *testing.T) {
	r := newR7ComposerRig(t, true)
	if err := r.att.Detach(); err != nil {
		t.Fatalf("detach owner before direct-input race: %v", err)
	}

	firstGate := make(chan struct{})
	var gateOnce sync.Once
	r.sk.srv.SetInputGateFunc(func(local string) error {
		if r.sk.composerRecycleInFlight(local) {
			return errors.New("session is recycling; nothing was typed")
		}
		gateOnce.Do(func() { close(firstGate) })
		return nil
	})

	holderEntered := make(chan struct{})
	releaseHolder := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- r.sk.srv.WithInputFence(r.local, func() error {
			close(holderEntered)
			<-releaseHolder
			return nil
		})
	}()
	select {
	case <-holderEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("test holder never acquired the direct-input fence")
	}

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- r.sk.srv.SendInput(r.local, protocol.SendInputReq{
			Text: "must-not-cross-recycle", Submit: true,
		})
	}()
	select {
	case <-firstGate: // the early gate passed; send is now parked on the held locks
	case <-time.After(5 * time.Second):
		t.Fatal("direct SendInput never passed its early gate")
	}

	recycleDone := make(chan error, 1)
	go func() {
		recycleDone <- r.sk.withAuthRecycleFence(r.local, func() error { return nil })
	}()
	awaitTrue(t, 5*time.Second, "recycle did not publish its transient embargo before the input fence", func() bool {
		return r.sk.composerRecycleInFlight(r.local)
	})
	close(releaseHolder)
	if err := <-holderDone; err != nil {
		t.Fatalf("release test input fence: %v", err)
	}
	select {
	case err := <-sendDone:
		if err == nil || !strings.Contains(err.Error(), "recycl") {
			t.Fatalf("parked direct send = %v, want recycle refusal before bytes", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parked direct send did not settle")
	}
	select {
	case err := <-recycleDone:
		if err != nil {
			t.Fatalf("recycle fence: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recycle did not acquire the released input fence")
	}

	// Resolve the synthetic auth claim after retiring its process interaction so
	// this still-running test fixture becomes attachable for inspection. A
	// forbidden send would be reported by the fake CLI here.
	r.sk.forgetInteractions(r.local)
	r.sk.clearAuthRecycle(r.local)
	oc := dialClient(t, r.sk)
	att, err := oc.Attach(r.session)
	if err != nil {
		t.Fatalf("attach after retiring test lane: %v", err)
	}
	t.Cleanup(func() { _ = att.Detach() })
	r.att = att
	r.assertPTYUntouched(t)
}

func TestComposerRefusesANonRunningSessionBeforeProviderBytes(t *testing.T) {
	r := newR7ComposerRig(t, true)
	if err := r.sk.core.Kill(r.local); err != nil {
		t.Fatalf("kill fixture session: %v", err)
	}
	awaitTrue(t, 5*time.Second, "fixture session never became non-running", func() bool {
		m, ok := r.sk.core.Get(r.local)
		return ok && m.Status.Process != status.ProcessRunning
	})

	code, err := r.sendBare("", "must not reach an ended provider", "devA:01JNONRUNNINGCOMPOSER00")
	if code != protocol.CodeInvalidField || err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("non-running composer = code %q err %v, want invalid_field/not running", code, err)
	}
	if calls := r.backend.recorded(); len(calls) != 0 {
		t.Fatalf("non-running composer wrote %d provider calls", len(calls))
	}
}

// Stop is deliberately a priority operation rather than another FIFO message,
// but it still shares the recycle embargo's lane.mu transaction. Once recycle
// has reached the queue head, Stop must refuse without publishing a barrier or
// calling the provider; otherwise turn/interrupt can race the final auth check
// and kill of the same process.
func TestAuthRecycleEmbargoRefusesPriorityStopBeforeProviderBytes(t *testing.T) {
	r := newR7ComposerRig(t, true)
	turn := r7OpenNativeTurn(t, r)

	entered := make(chan struct{})
	release := make(chan struct{})
	recycleDone := make(chan error, 1)
	go func() {
		recycleDone <- r.sk.withAuthRecycleFence(r.local, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recycle never reached the queue head")
	}

	lane := r.sk.composerLaneFor(r.local)
	lane.mu.Lock()
	barrierBefore, stopsBefore := lane.barrier, lane.stopsInFlight
	lane.mu.Unlock()
	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JRECYCLESTOP000000000",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: turn})
	if code != protocol.CodeInputBusy || err == nil || !strings.Contains(err.Error(), "recycl") {
		close(release)
		<-recycleDone
		t.Fatalf("Stop during recycle = code %q err %v, want explicit input_busy recycle refusal", code, err)
	}
	lane.mu.Lock()
	barrierAfter, stopsAfter := lane.barrier, lane.stopsInFlight
	lane.mu.Unlock()
	if barrierAfter != barrierBefore || stopsAfter != stopsBefore {
		t.Fatalf("refused Stop mutated lane barrier/stops from %d/%d to %d/%d",
			barrierBefore, stopsBefore, barrierAfter, stopsAfter)
	}
	for _, call := range r.backend.recorded() {
		if call.Method == "turn/interrupt" {
			t.Fatalf("refused Stop still wrote provider call %q", call.Method)
		}
	}
	close(release)
	if err := <-recycleDone; err != nil {
		t.Fatalf("recycle fence: %v", err)
	}
}

// Approval writes are provider input too. They must take their FIFO position
// before re-reading the pending card, then honor a recycle embargo published
// while they waited. This test occupies the lane first so an implementation
// that validates/applies outside the lane cannot pass by timing accident.
func TestApprovalJoinsComposerLaneAndRecycleRefusesBeforePTYBytes(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, claudeApproval("req-recycle-approval"))
	req := approveFor(t, r.sk, r.local, r.item, "allow")
	lane := r.sk.composerLaneFor(r.local)
	lane.enter()

	type outcome struct {
		code protocol.ErrorCode
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-recycle-approval", req)
		done <- outcome{code: code, err: err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		lane.mu.Lock()
		queued := lane.nextTicket >= 2 && lane.servingTicket == 0
		lane.mu.Unlock()
		if queued {
			break
		}
		select {
		case got := <-done:
			lane.leave()
			t.Fatalf("approval bypassed the composer lane: code %q err %v", got.code, got.err)
		default:
		}
		if time.Now().After(deadline) {
			lane.leave()
			t.Fatal("approval did not take a composer-lane ticket")
		}
		time.Sleep(time.Millisecond)
	}
	lane.mu.Lock()
	lane.recycling = true
	lane.mu.Unlock()
	lane.leave()

	select {
	case got := <-done:
		if got.code != protocol.CodeInputBusy || got.err == nil || !strings.Contains(got.err.Error(), "recycl") {
			t.Fatalf("approval during recycle = code %q err %v, want explicit input_busy recycle refusal", got.code, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not settle after reaching the lane head")
	}
	// Resolve the synthetic recycle authority so the test can type its own
	// observation newline. Production keeps this gate until authwatch resolves
	// the durable claim; attached input is correctly blocked during that window.
	r.sk.clearAuthRecycle(r.local)
	r.assertNothingWasTyped(t)
}
