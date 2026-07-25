package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for the round-8 review's IsRunning finding.
//
// App.IsRunning answered from a.sess alone, and a.sess is cleared by Stop. The drain
// goroutine has one path that ends WITHOUT a Stop: crypto.ErrKeyInvalidated at the dial,
// where run() sets repair_required and RETURNS, because the relay-auth key is destroyed and
// every retry is a handshake spent re-proving it. The field stays non-nil forever after, so
// IsRunning kept answering "the drain is live" on a handset whose drain had ended -- and a
// UI gating its re-pair affordance on !IsRunning() would never show it, on exactly the device
// that has nothing else it can do.

import (
	"testing"
	"time"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// TestS10_R8_IsRunningIsFalseOnceTheDrainHasTerminated drives the same terminal state
// TestS14_APermanentCustodyRefusalIsTerminalAndStopsRetrying does, and asks the lifecycle
// question that one does not: what does the app now say about itself.
func TestS10_R8_IsRunningIsFalseOnceTheDrainHasTerminated(t *testing.T) {
	h := newHarness(t)

	if got, ok := awaitConnState(t, h.App, "online", 5*time.Second); !ok {
		t.Fatalf("precondition: the phone never connected (state %q)", got)
	}
	// NON-VACUITY: the answer must be true while the drain really is live, or "false after
	// the terminal state" is satisfied by a method that always says false.
	if running, err := h.App.IsRunning(); err != nil || !running {
		t.Fatalf("precondition: IsRunning = %v, %v while the drain is online; want true, nil", running, err)
	}

	// The Keystore key is destroyed -- a biometric enrollment change, a restored image.
	h.Custody.Refuse("wake", swarmmobile.KeyCustodyKeyInvalidated)
	if err := h.App.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}
	if err := h.App.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}
	if got, ok := awaitConnState(t, h.App, "repair_required", 5*time.Second); !ok {
		t.Fatalf("precondition: the phone is %q, not repair_required; the terminal path was never "+
			"reached, so this test measures nothing", got)
	}

	// The connection state flips just BEFORE run() returns, so poll rather than sample: the
	// property is "the goroutine has returned", not "it returns within one scheduling quantum".
	deadline := time.Now().Add(5 * time.Second)
	for {
		running, err := h.App.IsRunning()
		if err != nil {
			t.Fatalf("App.IsRunning: %v", err)
		}
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("IsRunning = true with the app in repair_required and the drain goroutine " +
				"returned. It reports a.sess, which only Stop clears, and the terminal custody path " +
				"never calls Stop -- so the one screen that must appear here, 'pair this device " +
				"again', is gated on a condition that can no longer become true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
