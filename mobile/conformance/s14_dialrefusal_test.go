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
//   - ErrKeyAuthRequired: an endless "reconnecting" with no prompt and, after ADR-007 B133, no
//     prompt to give -- the key demands an authentication this product no longer performs.
//   - ErrKeyInvalidated: the same silent loop, forever, against a key that no longer exists.
//
// Both tests below fail against that `continue`: the state stays "reconnecting" in the first
// and the dial count keeps climbing in the second.
//
// THE TWO SENTINELS NOW SHARE AN OUTCOME, NOT A TEST (ADR-007 B133). They arrive by
// different routes -- one is a destroyed key, one is a key gated on an authentication that no
// longer exists -- and mobile/relay.go's dial switch deliberately lands both on
// connRepairRequired. Keeping a test per identity is what makes that a decision rather than a
// coincidence: if a later change split the arm, one of these would fail rather than neither.
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

// TestS14_AnAuthGatedCustodyRefusalTellsTheUserToRePairRatherThanSpinning.
//
// PB-KEY-6's other sentinel, RE-PREMISED by ADR-007 B133 rather than retired. It used to assert
// a prompt: crypto.ErrKeyAuthRequired meant "authenticate and it will connect", so the state had
// to be one the UI could turn into a biometric prompt, the loop had to keep dialing so that
// satisfying it was noticed, and the KEK answering again had to reconnect. B133 removes every
// phone-side user authentication, so there is no prompt to offer and nothing for the loop to
// wait for -- all three of those assertions now describe a product that does not exist.
//
// WHAT SURVIVES UNCHANGED IS WHY THIS FILE WAS WRITTEN: the dial error must not be discarded,
// leaving the user on a spinner for a condition nothing can clear. Only the REMEDY moved, from
// "authenticate" to "pair this device again" -- and for the one population that still raises
// this verdict that is a fix they can carry out. An install provisioned BEFORE B133 keeps its
// AUTH_BIOMETRIC_STRONG content KEK, because KeystoreCustodyBootstrap.ensure returns early when
// the alias exists and does not re-request the spec on upgrade; a re-pair discards the alias and
// the next provision writes one that asks for no authenticator.
// dev.swarm.phone.PhoneRuntime.routeCustodyVerdict puts the Kotlin exception in the same arm,
// and mobile/error_taxonomy.tsv classifies the sentinel with crypto.ErrKeyInvalidated.
func TestS14_AnAuthGatedCustodyRefusalTellsTheUserToRePairRatherThanSpinning(t *testing.T) {
	h := newHarness(t)

	if got, ok := awaitConnState(t, h.App, "online", 5*time.Second); !ok {
		t.Fatalf("precondition: the phone never connected (state %q); every assertion below "+
			"would be about a phone that was never online", got)
	}

	// The Keystore starts refusing the WAKE tier -- the tier RelayAuth lives in (ADR-007 B9) --
	// with the auth-gated verdict, and the link is cycled so the next dial has to sign.
	h.Custody.Refuse("wake", swarmmobile.KeyCustodyAuthRequired)
	if err := h.App.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}
	if err := h.App.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}

	got, ok := awaitConnState(t, h.App, "repair_required", 5*time.Second)
	if !ok {
		t.Fatalf("PB-KEY-6: an auth-gated custody refusal (crypto.ErrKeyAuthRequired) left the "+
			"phone reporting %q. The dial error is being discarded, so the user sees a spinner "+
			"for a condition nothing on this handset can clear and is never told what would. "+
			"The state must be one the UI can turn into 'pair this device again'.", got)
	}

	// TERMINAL, and this is the assertion that CHANGED SIGN. It used to require the loop to keep
	// dialing, because a biometric could be satisfied at any moment and the retry was what
	// noticed. There is no such moment left, so every further dial is a websocket handshake
	// spent re-proving an unusable key -- on a battery, against the relay's per-source budget.
	//
	// Two observation windows rather than one, as in the permanent case below: the first lets a
	// dial already in flight when the state flipped finish, so this measures the steady state
	// and not a race.
	time.Sleep(300 * time.Millisecond)
	before := h.Custody.Unwraps("wake")
	time.Sleep(1 * time.Second)
	if after := h.Custody.Unwraps("wake"); after != before {
		t.Errorf("PB-KEY-6: the phone made %d further dial attempts after an auth-gated custody "+
			"refusal. After ADR-007 B133 nothing the user can do makes that key answer, so the "+
			"retry is spending the battery and the relay budget to re-learn the same verdict",
			after-before)
	}

	// AND THE VERDICT STICKS. The old test ended by clearing the refusal and requiring the phone
	// back online, which was the way out a prompt promised. There is no such way out now, so the
	// same stimulus asserts the opposite: a KEK that starts answering again is not a user acting,
	// and a phone that silently returned to online would have hidden a custody verdict the owner
	// was told to re-pair for. The intended re-arm is a PAIRING (mobile/relay.go's
	// rearmAfterPairing), which this does not perform.
	h.Custody.Refuse("wake", "")
	if got, ok := awaitConnState(t, h.App, "online", 1*time.Second); ok {
		t.Errorf("PB-KEY-6: the phone returned to %q on its own once the KEK answered again. "+
			"No user action occurred, so the terminal custody verdict was cleared by the "+
			"platform changing its mind -- the screen telling the user to pair again vanishes "+
			"under them", got)
	}
	if state, err := h.App.ConnectionState(); err != nil {
		t.Fatalf("App.ConnectionState: %v", err)
	} else if state != "repair_required" {
		t.Errorf("PB-KEY-6: the terminal custody state was overwritten with %q. A phone whose "+
			"relay-auth key is unusable must not report a transport condition", state)
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
