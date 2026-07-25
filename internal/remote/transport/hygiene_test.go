// PB-NET-7 (FAILING FIRST): hygiene -- timeouts on every call, cancellation
// honoured, no goroutine leaks across repeated connect/disconnect cycles.
//
// These are the failures that do not look like failures. A call with no timeout
// against a relay that accepts the TCP connection and then says nothing hangs the
// UI thread forever with a "connected" indicator; a goroutine leaked per reconnect
// is invisible until a phone that has roamed between cells all day is killed by the
// OS mid-session.
package transport_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/transport"
)

// TestNonWaitRequestTimeoutIsTheCommitteeBudget pins §6.0's 10 s non-wait bound.
func TestNonWaitRequestTimeoutIsTheCommitteeBudget(t *testing.T) {
	if transport.RequestTimeout != 10*time.Second {
		t.Fatalf("RequestTimeout = %v, want 10s (§6.0)", transport.RequestTimeout)
	}
}

// TestEveryCallTimesOutAgainstASilentRelay asserts the default bound is actually
// applied, not merely declared: a relay that authenticates and then never answers
// must not be able to park a caller indefinitely, on any call.
func TestEveryCallTimesOutAgainstASilentRelay(t *testing.T) {
	h := newHostileRelay(t)
	pub, priv := newRelayAuthKey(t)
	p := peers{machineRID: "machine-rid", deviceRID: "device-rid", party: newSealParty(t), deviceAuth: authFor(pub, priv)}
	sleep := &recordingSleep{}
	s := devSession(t, h.URL(), p, func(o *transport.Options) {
		o.RequestTimeout = 200 * time.Millisecond
		o.Sleep = sleep.fn
	})
	h.setSilent(true)

	calls := map[string]func() error{
		"SendOp":   func() error { return s.SendOp(context.Background(), p.machineRID, []byte("op")) },
		"SendLive": func() error { return s.SendLive(context.Background(), p.machineRID, []byte("live")) },
		"Drain": func() error {
			_, err := s.Drain(context.Background(), func(relay.Item) error { return nil })
			return err
		},
	}
	for name, call := range calls {
		done := make(chan error, 1)
		start := time.Now()
		go func() { done <- call() }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("%s returned nil against a relay that never answered", name)
			}
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("%s took %v to time out with a 200ms bound configured", name, elapsed)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never returned: the call has no timeout", name)
		}
	}
}

// TestContextCancellationIsHonoured asserts the caller's context wins over the
// session's own bound: cancelling returns promptly, and with the caller's error.
func TestContextCancellationIsHonoured(t *testing.T) {
	h := newHostileRelay(t)
	pub, priv := newRelayAuthKey(t)
	p := peers{machineRID: "machine-rid", deviceRID: "device-rid", party: newSealParty(t), deviceAuth: authFor(pub, priv)}
	sleep := &recordingSleep{}
	s := devSession(t, h.URL(), p, func(o *transport.Options) {
		o.RequestTimeout = 30 * time.Second
		o.Sleep = sleep.fn
	})
	h.setSilent(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.SendOp(ctx, p.machineRID, []byte("op")) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled SendOp: got %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("SendOp ignored context cancellation and is still waiting on its own 30s bound")
	}
}

// TestDialHonoursCallerContext asserts the same for the connect path, which is the
// one a phone hits on a dead cell: Dial must not outlive its context.
func TestDialHonoursCallerContext(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())
	tap.Refuse(true)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sleep := &recordingSleep{}
	start := time.Now()
	s, err := transport.Dial(ctx, transport.Options{
		URL:      tap.URL(),
		Auth:     p.deviceAuth,
		Security: relay.Security{AllowLoopbackCleartext: true},
		Sleep:    sleep.fn,
	})
	if err == nil {
		_ = s.Close()
		t.Fatalf("Dial succeeded against a relay refusing every connection")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Dial took %v to honour a 300ms context", elapsed)
	}
}

// TestCallsAfterCloseFailCleanly asserts shutdown is a state, not a crash: Close is
// idempotent and every later call is a clean typed refusal.
func TestCallsAfterCloseFailCleanly(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	s := devSession(t, srv.URL(), p, nil)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v (Close must be idempotent)", err)
	}
	if got := s.State(); got != transport.StateClosed {
		t.Fatalf("state after Close = %q, want %q", got, transport.StateClosed)
	}
	if err := s.SendOp(testCtx(t), p.machineRID, []byte("op")); !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("SendOp after Close: got %v, want ErrClosed", err)
	}
	if err := s.SendLive(testCtx(t), p.machineRID, []byte("live")); !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("SendLive after Close: got %v, want ErrClosed", err)
	}
	if _, err := s.Drain(testCtx(t), func(relay.Item) error { return nil }); !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("Drain after Close: got %v, want ErrClosed", err)
	}
}

// TestNoGoroutineLeakAcrossConnectDisconnectCycles is the -race + leak assertion
// PB-NET-7 names. It cycles the full lifecycle, including a mid-session flap that
// forces the reconnect machinery to start and stop, and requires the goroutine
// count to return to its baseline.
func TestNoGoroutineLeakAcrossConnectDisconnectCycles(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())

	cycle := func() {
		sleep := &recordingSleep{}
		s, err := transport.Dial(testCtx(t), transport.Options{
			URL:      tap.URL(),
			Auth:     p.deviceAuth,
			Security: relay.Security{AllowLoopbackCleartext: true},
			Sleep:    sleep.fn,
		})
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		if err := s.SendOp(testCtx(t), p.machineRID, []byte("cycle-op")); err != nil {
			t.Fatalf("SendOp: %v", err)
		}
		tap.Cut()
		waitState(t, s, transport.StateConnected)
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	// One warm-up cycle so the baseline includes whatever the relay and the
	// websocket library keep alive per connection.
	cycle()
	baseline := settledGoroutines()

	for i := 0; i < 20; i++ {
		cycle()
	}
	assertNoLeak(t, baseline)
}
