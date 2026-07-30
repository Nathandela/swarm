package skeleton

// ADR-007 B69(3) -- THE DEADLINE IS THE FENCE; THE ABORT IS A COURTESY, AND UNTIL THIS FILE
// NOTHING MEASURED THE DEADLINE.
//
// B64 shipped two mechanisms for the same scenario: an authoritative deadline on the machine
// handshake (internal/skeleton/pairing.go:244) and a best-effort abort frame the phone sends
// on a detached context (internal/remote/pairing/pairing.go abortConsent). Its commit message
// named the deadline as the fence. Both B64 suites nevertheless stayed green with the deadline
// DELETED -- context.WithTimeout(ctx, window) replaced by context.WithCancel(ctx) -- because
// every scenario they drive ends with the phone aborting, and the abort resolves them first.
//
// Residual 4.13: when two mechanisms both resolve a scenario, the faster one silently absorbs
// the test and the slower one is unfenced. Saying in prose which is load-bearing does not make
// the test measure it.
//
// THE SCENARIO THAT ISOLATES THE DEADLINE IS THE ONE THE ABORT CANNOT SERVE: a phone that
// sends NOTHING. It loses the network at its SAS screen, its operator walks away, or a hostile
// relay swallows the frame. No abort is produced, no context is cancelled, nothing arrives at
// the machine ever again -- and the machine is parked in recvConsent, which has no clock of its
// own. Only the daemon's deadline can unpark it, so only the deadline can make this test pass.
//
// The test hands production no clock. The pairing ctx is the daemon's own, derived from the
// owner connection (internal/protocol/server.go builds it as context.WithCancel(
// context.Background())). The only instant appealed to is the ExpiresAt the daemon ITSELF
// announced in its pair_start reply.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// silentTTLSeconds is the window this test REQUESTS on the wire, in the field PairStartReq
// already carries: a production input, not an injected safety property. pairWindow(2s) is 2s
// (under the relay slot), and the daemon echoes it back as the ExpiresAt asserted against.
const silentTTLSeconds = 2

// silentSlack is how long past the daemon's OWN announced expiry the test keeps waiting before
// calling the pairing permanently stuck. Generous: the claim is "eventually, bounded by what
// was announced", not "to the millisecond".
const silentSlack = 8 * time.Second

// errPhoneWentSilent is what the silent phone's SAS gate returns once the TEST is over. It is
// never reached while any assertion is live.
var errPhoneWentSilent = errors.New("phone went silent")

// startSilentPhone runs the real RunDevice through msg3 and then STOPS. Its SAS gate blocks on
// a channel released only at test cleanup, and the whole leg runs on context.Background(), so
// while the test is running this phone is structurally incapable of producing anything: no
// msg4, no abort frame, no cancellation the machine could observe. That is the difference
// between this fence and every B64 fence -- there is no second mechanism left to absorb it.
func startSilentPhone(t *testing.T, dEnd *memRendezvous, qp pairing.QRPayload) (sasShown <-chan struct{}, done chan devLegResult) {
	t.Helper()
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	static, err := ks.NoiseStatic()
	if err != nil {
		t.Fatalf("device noise static: %v", err)
	}

	shown := make(chan struct{}, 1)
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })

	dp := pairing.DeviceParams{
		Static:       static,
		Secret:       qp.PairingSecret,
		RendezvousID: qp.RendezvousID,
		Payload: pairing.DevicePayload{
			DeviceName:           "Test iPhone",
			DeviceRoutingID:      []byte("device-routing-id-0001"),
			DeviceRelayAuthPub:   ks.RelayAuthPublic(),
			RecipientPub:         ks.RecipientPublic(),
			DeviceCommandSignPub: ks.CommandSigningPublic(),
		},
		Consent: phoneConsentFor(ks, qp.RendezvousID),
		DeviceSAS: func(context.Context, [6]string) error {
			select {
			case shown <- struct{}{}:
			default:
			}
			<-released
			return errPhoneWentSilent
		},
	}

	ch := make(chan devLegResult, 1)
	go func() {
		do, err := pairing.RunDevice(context.Background(), dp, dEnd)
		ch <- devLegResult{outcome: do, err: err}
	}()
	return shown, ch
}

// TestB69_ASilentPhoneIsUnparkedByTheDeadlineAlone: the owner confirmed on the desktop and the
// phone then went dark without a word. The daemon promised an expiry; past it the operator is
// owed a terminal pair_result and a pairing slot they can use again, and the ONLY mechanism
// that can supply either is the deadline on the handshake.
func TestB69_ASilentPhoneIsUnparkedByTheDeadlineAlone(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)

	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full", TTLSeconds: silentTTLSeconds}})

	reply := awaitControl(t, rc, protocol.OpPairStart)
	if reply.Pairing == nil || reply.Pairing.QR == "" {
		t.Fatalf("pair_start reply missing QR: %+v", reply.Pairing)
	}
	if reply.Pairing.ExpiresAt == nil {
		t.Fatal("pair_start announced no expiry, so there is no promise to hold the daemon to")
	}
	expires := *reply.Pairing.ExpiresAt
	if window := time.Until(expires); window > 30*time.Second {
		t.Fatalf("the daemon announced a %s window for a pair_start that requested %ds; this test "+
			"holds it to what it announced and cannot wait that long", window, silentTTLSeconds)
	}
	qp, err := pairing.DecodeQR(reply.Pairing.QR)
	if err != nil {
		t.Fatalf("pair_start QR undecodable: %v", err)
	}

	dEnd := recvDeviceEnd(t, deviceEnds)
	sasShown, devDone := startSilentPhone(t, dEnd, qp)

	// The desktop reaches the SAS gate and the owner answers the prompt in front of them.
	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
		t.Fatalf("pair_pending missing the 6-word SAS gate: %+v", pending.Pairing)
	}
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: true}})

	select {
	case <-sasShown:
	case <-time.After(10 * time.Second):
		t.Fatal("the phone never displayed its SAS; the pairing did not reach the state under test")
	}

	// The isolation check, and the whole point of this file. From here the phone is holding
	// the code up and will never act again. If its leg has resolved, something ended it --
	// and whatever that is would resolve the scenario instead of the deadline, which is
	// precisely how B64's fences went vacuous.
	select {
	case r := <-devDone:
		t.Fatalf("the phone leg RESOLVED (outcome=%v err=%v). It was supposed to stay silent, so this "+
			"test is no longer isolating the deadline from the abort", r.outcome, r.err)
	case <-time.After(250 * time.Millisecond):
	}

	res, ok := awaitControlBy(t, rc, protocol.OpPairResult, expires.Add(silentSlack))
	if !ok {
		t.Fatalf("no pair_result %s past the announced expiry %s, from a phone that sent NOTHING.\n"+
			"  There is no abort frame in this scenario and never will be, so the deadline on the\n"+
			"  handshake (internal/skeleton/pairing.go, context.WithTimeout(ctx, window)) is the only\n"+
			"  thing that can unpark recvConsent. THE DEADLINE IS THE FENCE; THE ABORT IS A COURTESY.",
			silentSlack, expires.Format(time.RFC3339Nano))
	}
	if res.Pairing != nil && res.Pairing.DeviceID != "" {
		t.Fatalf("pair_result carried DeviceID %q for a pairing that never produced a consent; want a failure",
			res.Pairing.DeviceID)
	}
	if got := sk.api.devices.List(); len(got) != 0 {
		t.Fatalf("registry has %d devices after a pairing the phone never answered; want 0", len(got))
	}

	// And the consequence that made B64 severe rather than slow: the slot is released, so the
	// operator can do the only thing the UI offers them -- retry.
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full", TTLSeconds: silentTTLSeconds}})
	for i := 0; i < 8; i++ {
		c, err := rc.readTry(5 * time.Second)
		if err != nil {
			t.Fatalf("no answer to the retried pair_start: %v", err)
		}
		switch c.Op {
		case protocol.OpPairStart:
			if c.Pairing == nil || c.Pairing.QR == "" {
				t.Fatalf("the retried pair_start replied without a QR: %+v", c.Pairing)
			}
			return
		case protocol.OpError:
			t.Fatalf("the retried pair_start was REFUSED (%q): the connection's pairing slot is still "+
				"held by the handshake the silent phone abandoned. There is no pair_cancel op, so only "+
				"dropping the owner connection escapes.", c.Error)
		case protocol.OpPairResult:
			// A late terminal result; keep reading for the reply.
		default:
			t.Fatalf("unexpected op %q while waiting for the retried pair_start's answer", c.Op)
		}
	}
	t.Fatal("the retried pair_start produced neither a reply nor a refusal within the frame budget")
}
