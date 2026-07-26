package relay

// Tests for the SPLIT in deliverPush: the caller waits only for the provider's first
// verdict (pushVerdictWait), and a delivery still grinding through the sender's retries
// finishes on a background goroutine instead of holding the connection's request loop.
//
// Nothing in pushseam_test.go can exercise this, because every sink there answers
// instantly — which is exactly why the stall was invisible until a real sender existed.
//
// SCOPE HONESTY: the sink here is a fake that blocks on a channel. Nothing contacts FCM,
// nothing models delivery, and PB-E2E-5 (real provider, real handset) stays DEFERRED.

import (
	"context"
	"crypto/ed25519"
	"sync"
	"testing"
	"time"
)

// blockingPushSink parks every Push until the test releases it, so a test can observe what
// the relay does WHILE a delivery is still in flight — the state a real 5xx retry sits in
// for seconds at a time.
type blockingPushSink struct {
	release chan struct{}

	mu       sync.Mutex
	attempts []string // tokens, in call order
	err      error    // the verdict returned once released
}

func newBlockingPushSink(err error) *blockingPushSink {
	return &blockingPushSink{release: make(chan struct{}), err: err}
}

func (b *blockingPushSink) Push(ctx context.Context, token string, _ PushPayload) error {
	b.mu.Lock()
	b.attempts = append(b.attempts, token)
	b.mu.Unlock()
	select {
	case <-b.release:
		return b.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *blockingPushSink) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.attempts)
}

// waitUntil polls cond until it holds or the deadline elapses.
func waitUntil(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", within, what)
}

// tokenFor reads the relay's live token for rid.
func (s *Server) tokenFor(rid string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[rid]
}

// TestPushDelivery_SlowProviderDoesNotHoldTheRequestLoop is the property the split exists
// for. handlePushTrigger runs on the connection's request loop and serveConn dispatches
// serially, so every second spent inside a push is a second the relay is not reading the
// NEXT frame from that machine — and the machine's connection is the one the gateway
// re-registers mailbox_wait on to collect the phone's keystrokes (PB-NET-5, p50 150ms).
//
// So: with a sink that has not answered at all, push_trigger must still come back.
func TestPushDelivery_SlowProviderDoesNotHoldTheRequestLoop(t *testing.T) {
	sink := newBlockingPushSink(nil)
	srv, clk := startTestRelayWithSink(t, sink)
	defer close(sink.release) // let the parked delivery finish before Close joins it

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "fcm-token-slow"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))

	start := time.Now()
	if err := machine.PushTrigger(testCtx(t), RoutingID(dPub), sp.sealMailbox(t, 1, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger against a slow provider: %v", err)
	}
	elapsed := time.Since(start)

	// The sink was reached but has NOT answered -- so the reply came back while the
	// delivery was still in flight, which is the whole point.
	if got := sink.count(); got != 1 {
		t.Fatalf("sink attempts: got %d, want 1 (the delivery must have started)", got)
	}
	// Bounded by the verdict wait, not by the sender's full retry budget. The generous
	// upper bound keeps this from being a latency benchmark: what is under test is that
	// pushVerdictWait -- not pushDeliveryBudget -- is what the caller pays.
	if elapsed >= pushDeliveryBudget {
		t.Fatalf("push_trigger took %s against an unanswering provider; the caller waited the whole "+
			"delivery budget (%s) instead of just the verdict wait (%s), so the machine's request "+
			"loop is still hostage to the provider", elapsed, pushDeliveryBudget, pushVerdictWait)
	}
	// And the machine's connection is genuinely usable again: the next op is served.
	if _, err := machine.Presence(testCtx(t), RoutingID(dPub)); err != nil {
		t.Fatalf("the machine's next op after a slow push: %v -- the request loop is still blocked", err)
	}
}

// TestPushDelivery_UnregisteredVerdictAfterTheWaitStillPrunes covers the case the split
// could plausibly have broken, and the one that made me not prune on first-attempt
// verdicts only.
//
// UNREGISTERED does not always arrive on the first attempt: a 503 followed by a dead
// token, or an OAuth blip then UNREGISTERED, both surface it later -- after the caller has
// already been released. The prune must still happen. A design that only honoured a
// verdict that beat the wait would silently stop pruning for exactly those cases, and the
// symptom (quota burned against dead handsets) is invisible from the relay.
func TestPushDelivery_UnregisteredVerdictAfterTheWaitStillPrunes(t *testing.T) {
	sink := newBlockingPushSink(ErrPushUnregistered)
	srv, clk := startTestRelayWithSink(t, sink)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "fcm-token-dead-slowly"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))

	if err := machine.PushTrigger(testCtx(t), devRID, sp.sealMailbox(t, 1, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}
	// The caller is back before any verdict exists, so nothing can have been pruned yet.
	// This is the control: without it the assertion below would also pass on a relay that
	// pruned synchronously, and would prove nothing about the background path.
	if got := srv.tokenFor(devRID); got != "fcm-token-dead-slowly" {
		t.Fatalf("token was already pruned (%q) before the sink answered: this test would not be "+
			"exercising the background verdict path at all", got)
	}

	close(sink.release) // the provider finally answers UNREGISTERED
	waitUntil(t, 5*time.Second, "the background UNREGISTERED verdict to prune the token", func() bool {
		return srv.tokenFor(devRID) == ""
	})

	// Behaviourally: the dead token is gone, so a later trigger reaches no sink at all.
	before := sink.count()
	if err := machine.PushTrigger(testCtx(t), devRID, sp.sealMailbox(t, 2, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger(2): %v", err)
	}
	if got := sink.count(); got != before {
		t.Fatalf("sink attempts after the pruning verdict: got %d, want the earlier %d -- the dead "+
			"token was not pruned", got, before)
	}
}

// TestPushDelivery_CloseJoinsInFlightDeliveries pins the hazard the split introduced.
// A background delivery that ends in UNREGISTERED WRITES to the bbolt store to prune, so
// Close must join it before closing the handle -- the same ordering CR-3 already
// established for the sweep goroutine. Without it a shutdown racing a slow provider
// touches a closed store.
//
// Under -race this also covers the WaitGroup itself: an Add concurrent with Wait is a
// data race, which is why deliverPush registers under the closing flag.
func TestPushDelivery_CloseJoinsInFlightDeliveries(t *testing.T) {
	sink := newBlockingPushSink(ErrPushUnregistered)
	srv, clk := startTestRelayWithSink(t, sink)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "fcm-token-inflight"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	if err := machine.PushTrigger(testCtx(t), RoutingID(dPub), sp.sealMailbox(t, 1, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}
	if got := sink.count(); got != 1 {
		t.Fatalf("sink attempts: got %d, want 1 (a delivery must be in flight for this test to mean anything)", got)
	}

	// Close with the delivery still parked. baseCancel cancels its context, so the sink's
	// ctx.Done wins and Close returns rather than hanging for the whole budget.
	done := make(chan error, 1)
	go func() { done <- srv.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close with an in-flight delivery: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung with a delivery in flight: it must cancel them, not wait out the budget")
	}
	close(sink.release)
}

// slowVerdictSink answers after a delay that is short against pushVerdictWait but long
// against a loopback reply. That gap is what makes "the caller waited for the verdict"
// distinguishable from "the caller did not".
type slowVerdictSink struct {
	delay time.Duration
	err   error

	mu       sync.Mutex
	attempts int
}

func (s *slowVerdictSink) Push(ctx context.Context, _ string, _ PushPayload) error {
	s.mu.Lock()
	s.attempts++
	s.mu.Unlock()
	t := time.NewTimer(s.delay)
	defer t.Stop()
	select {
	case <-t.C:
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestPushDelivery_CallerWaitsForTheVerdictSoThePruneIsNotARace guards the half of the
// split that a pure fire-and-forget would silently delete.
//
// pushseam_test.go's pruning test cannot catch that. Its sink answers instantly, so even
// fire-and-forget usually prunes before the NEXT trigger completes its network round trip
// -- it passes on luck, and would flake rather than fail. Here the sink is deliberately
// slower than the reply path, so a caller that does not wait for the verdict provably has
// not pruned when it returns.
//
// Why it matters: the relay reads the token, decides whether to push, and replies, all on
// one pass. If the prune is not ordered before the reply, a burst of triggers keeps
// spending quota on a handset the provider has already declared gone.
func TestPushDelivery_CallerWaitsForTheVerdictSoThePruneIsNotARace(t *testing.T) {
	// Well under pushVerdictWait, far over a loopback round trip.
	sink := &slowVerdictSink{delay: 100 * time.Millisecond, err: ErrPushUnregistered}
	srv, clk := startTestRelayWithSink(t, sink)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "fcm-token-verdict"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))

	// POSITIVE CONTROL: the token is there to begin with, so "gone" below means pruned and
	// not never-registered.
	if got := srv.tokenFor(devRID); got != "fcm-token-verdict" {
		t.Fatalf("token before the trigger = %q, want the registered one", got)
	}

	if err := machine.PushTrigger(testCtx(t), devRID, sp.sealMailbox(t, 1, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}
	if got := srv.tokenFor(devRID); got != "" {
		t.Fatalf("push_trigger returned with the dead token still registered (%q): the caller did not "+
			"wait for the provider's verdict, so the prune is ordered after the reply rather than "+
			"before it and a burst of triggers keeps spending quota on a handset the provider has "+
			"already declared gone", got)
	}
	if sink.attempts != 1 {
		t.Fatalf("sink attempts: got %d, want 1", sink.attempts)
	}
}
