package conformance_test

// Slice S14 -- PB-KEY-6 at the transport edge, over a REAL relay and a REAL handshake.
//
// THE DEFECT THESE WERE WRITTEN AGAINST. mobile/relay.go discarded its dial error outright:
//
//	cl, err := a.dial(ctx)
//	if err != nil {
//		continue
//	}
//
// and mobile/relay.go wires `Sign: ks.SignRelayAuth`, so the phone is the ONLY production
// caller of relay.ClientAuth.Sign that can refuse (cmd/swarm-remote/config.go records that the
// machine identity never does). While the shipped app ran on the software keystore that
// refusal was unreachable, which is the only reason the bare `continue` was not a stop-ship.
// It went LIVE the moment PB-KEY-9's Keystore-backed KEK landed -- in this same slice -- and
// left two failures with no user-visible difference between them and no way out:
//
//   - ErrKeyAuthRequired (RECOVERABLE): an endless "reconnecting" with no prompt. The user is
//     never told that authenticating is what fixes it.
//   - ErrKeyInvalidated (PERMANENT): the same silent loop, forever, against a key that no
//     longer exists.
//
// Both tests below fail against that `continue`: the state stays "reconnecting" in the first
// and the dial count keeps climbing in the second.
//
// The refusal is injected at the KEK, not at the signature, deliberately. That is where a real
// handset's refusal originates -- Keystore declining to unwrap -- and it makes the assertion a
// statement about the whole chain: KeyCustody -> custodySealer -> sealedKeyStore.SignRelayAuth
// -> relay.ClientAuth.Sign -> relay.Dial -> the transport loop. A fake ClientAuth.Sign would
// have tested the last two links and skipped the ones this slice built.

import (
	"testing"
	"time"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// awaitConnState polls App.ConnectionState until it reads want, and reports what it saw.
func awaitConnState(t *testing.T, app *swarmmobile.App, want string, within time.Duration) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	last := ""
	for {
		got, err := app.ConnectionState()
		if err != nil {
			t.Fatalf("App.ConnectionState: %v", err)
		}
		last = got
		if got == want {
			return got, true
		}
		if !time.Now().Before(deadline) {
			return last, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestS14_ARecoverableCustodyRefusalAsksForTheBiometricRatherThanSpinning.
//
// PB-KEY-6's recoverable half. The state the user sees must say what to DO, and "reconnecting"
// says the opposite -- it says the app is working on it and the user should wait, which is a
// wait that never ends because nothing but a biometric will end it.
//
// It also asserts the state is not a dead end: once the KEK answers again, the very next retry
// connects. A re-prompt state that could only be left by restarting the app would be a
// different defect with the same screen.
func TestS14_ARecoverableCustodyRefusalAsksForTheBiometricRatherThanSpinning(t *testing.T) {
	h := newHarness(t)

	if got, ok := awaitConnState(t, h.App, "online", 5*time.Second); !ok {
		t.Fatalf("precondition: the phone never connected (state %q); every assertion below "+
			"would be about a phone that was never online", got)
	}

	// The Keystore starts refusing the WAKE tier -- the tier RelayAuth lives in (ADR-007 B9) --
	// with the recoverable verdict, and the link is cycled so the next dial has to sign.
	h.Custody.Refuse("wake", swarmmobile.KeyCustodyAuthRequired)
	if err := h.App.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}
	if err := h.App.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}

	got, ok := awaitConnState(t, h.App, "reauth_required", 5*time.Second)
	if !ok {
		t.Fatalf("PB-KEY-6: a RECOVERABLE custody refusal (crypto.ErrKeyAuthRequired) left the "+
			"phone reporting %q. The dial error is being discarded, so the user sees a spinner "+
			"for a condition only a biometric prompt can clear and is never told so. The state "+
			"must be one the UI can turn into a prompt.", got)
	}

	// It must keep TRYING while it says so -- a recoverable refusal is recoverable at any
	// moment, and the retry is what notices. A state that stopped the loop would need an
	// explicit resume verb the facade does not have.
	before := h.Custody.Unwraps("wake")
	time.Sleep(1 * time.Second)
	if after := h.Custody.Unwraps("wake"); after <= before {
		t.Errorf("PB-KEY-6: the phone stopped dialing while in reauth_required (%d -> %d wake "+
			"unwraps). A RECOVERABLE refusal must keep retrying, or satisfying the biometric "+
			"changes nothing until the app is restarted", before, after)
	}

	// And the way out works. Without this the assertion above is satisfied by a phone that can
	// never connect again, which is the vacuous form of "it asked for the biometric".
	h.Custody.Refuse("wake", "")
	if got, ok := awaitConnState(t, h.App, "online", 10*time.Second); !ok {
		t.Errorf("PB-KEY-6: the KEK answers again and the phone is still %q. reauth_required is "+
			"a dead end rather than a prompt: authenticating fixes nothing", got)
	}
}

// TestS14_APermanentCustodyRefusalIsTerminalAndStopsRetrying.
//
// PB-KEY-6's permanent half, and the one with a cost attached. crypto.ErrKeyInvalidated means
// the Keystore key is destroyed -- a biometric enrollment change, a cleared credential, a
// restored image -- and nothing on-device brings it back. Retrying is not merely useless: it is
// a websocket handshake every 250 ms, forever, on a battery-powered device, against the relay's
// per-source ops budget, while the screen shows a spinner that will never resolve.
//
// So the assertion is TWO things, and the second is the one the old `continue` could never
// satisfy: a state the UI can turn into "pair this device again", and a loop that has actually
// stopped.
func TestS14_APermanentCustodyRefusalIsTerminalAndStopsRetrying(t *testing.T) {
	h := newHarness(t)

	if got, ok := awaitConnState(t, h.App, "online", 5*time.Second); !ok {
		t.Fatalf("precondition: the phone never connected (state %q)", got)
	}

	h.Custody.Refuse("wake", swarmmobile.KeyCustodyKeyInvalidated)
	if err := h.App.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}
	if err := h.App.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}

	got, ok := awaitConnState(t, h.App, "repair_required", 5*time.Second)
	if !ok {
		t.Fatalf("PB-KEY-6: a PERMANENT custody refusal (crypto.ErrKeyInvalidated) left the phone "+
			"reporting %q. The relay-auth key is gone; the phone must say the device has to pair "+
			"again, not present a transport state the user could mistake for a bad network.", got)
	}

	// TERMINAL. Two observation windows rather than one: the first lets any dial already in
	// flight when the state flipped finish, so this measures the loop's steady state and not a
	// race.
	//
	// THE MARGIN DOES NOT REST ON THE RETRY CADENCE AT ALL, which is why this test survived
	// PB-NET-4 replacing a fixed 250 ms delay with section 6.0's growing backoff (ADR-007 B118).
	// The guarantee is that App.run RETURNS on ErrKeyInvalidated rather than continuing: the
	// goroutine exits, so there is no loop left whose timing could matter. The old comment here
	// counted retries in the window, which was already reasoning about the wrong thing before
	// the cadence changed underneath it.
	time.Sleep(300 * time.Millisecond)
	before := h.Custody.Unwraps("wake")
	time.Sleep(1 * time.Second)
	if after := h.Custody.Unwraps("wake"); after != before {
		t.Errorf("PB-KEY-6: the phone made %d further dial attempts after a PERMANENT custody "+
			"refusal. The key is destroyed -- every one of those is a websocket handshake spent "+
			"re-proving it, on a battery, against the relay's per-source budget", after-before)
	}

	// And the state STICKS. The failure this catches is a loop that exits through its normal
	// bottom and overwrites the verdict with "offline", which reads as an ordinary disconnect
	// and loses the only signal telling the user to re-pair.
	if state, err := h.App.ConnectionState(); err != nil {
		t.Fatalf("App.ConnectionState: %v", err)
	} else if state != "repair_required" {
		t.Errorf("PB-KEY-6: the terminal custody state was overwritten with %q. A phone whose "+
			"device key is gone must not report a transport condition", state)
	}
}
