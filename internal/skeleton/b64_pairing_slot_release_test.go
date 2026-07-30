package skeleton

// ADR-007 B64 -- A PAIRING THE PHONE ABORTS AFTER THE DESKTOP CONFIRM PARKS THE DAEMON FOREVER.
//
// These are the DAEMON-LEVEL fences: the property the operator can actually observe, driven
// over the owner-tier wire against the real coreAPI PairingHost, a real Noise handshake, and a
// real RunDevice. They are the tests whose absence let the defect ship -- every existing fence
// for msg4 lives inside internal/remote/pairing and hands Machine.Pair a deadline the test
// itself invented.
//
// THE ORDER IS THE ORDINARY ONE. No attacker, no adversarial build. The owner scans the QR,
// the desktop shows the SAS, the owner answers the prompt in front of them -- `y`, on the
// desktop, FIRST -- and only then turns to the phone. From that instant the machine is inside
// recvConsent (internal/remote/pairing/pairing.go:473), which has no clock:
//
//   - internal/protocol/server.go:2098 builds the pairing ctx as context.WithCancel(
//     context.Background()). It is the ONLY production construction, and it carries no deadline.
//   - pairWindow (pairing.go:288) is the ANNOUNCED ExpiresAt in the PairView and nothing else.
//     Nothing enforces it on the handshake goroutine.
//   - the phone's only abort paths -- RejectSAS, Cancel, the 60 s pairingTTL -- all cancel the
//     ctx RunDevice must then send the abort on, so the abort never reaches the wire (ADR-007
//     B64, fenced in internal/remote/pairing/b64_shipped_abort_test.go).
//
// So Machine.Pair never returns; BeginPairing's goroutine never calls result; clearPairing
// (internal/protocol/server.go:2191) runs only from result and from the BeginPairing error
// path, so cc.pair stays non-nil FOREVER and every later pair_start on that connection is
// refused "pairing already in progress" (:2102). There is no pair_cancel op. Dropping the
// whole owner connection is the only escape.
//
// NEITHER TEST BELOW HANDS PRODUCTION A DEADLINE. The pairing ctx is the daemon's own,
// derived from the connection. The only clock either test appeals to is the ExpiresAt the
// daemon ITSELF announced in the pair_start reply: a daemon that tells the operator "this
// pairing expires at T" and is still holding the slot long after T has broken its own promise.

import (
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// abortTTLSeconds is the pairing window these tests REQUEST on the wire, in the field
// PairStartReq already carries. It is a production input, not an injected safety property:
// pairWindow(2s) is 2s (under the relay's 60 s slot), and the daemon echoes it back as the
// ExpiresAt the assertions then hold it to.
const abortTTLSeconds = 2

// slotSlack is how long past the daemon's OWN announced expiry these tests keep waiting
// before calling the pairing permanently stuck. Generous on purpose: the assertion is
// "eventually, bounded by what was announced", not "to the millisecond".
const slotSlack = 8 * time.Second

// ctxFaithfulMem refuses a Send on a done ctx, as relay.Conn does -- writeFrame hands the
// caller's ctx to ws.Write, which fails outright once it is cancelled, and mobile's
// cancelHandshake() additionally CloseNow()s the socket. The bare memRendezvous selects on a
// BUFFERED channel against ctx.Done(), so on a dead ctx Go picks a ready case at random and
// delivers the frame about half the time; wrapping it keeps the phone's abort from
// succeeding in the harness in a way it never can in production.
type ctxFaithfulMem struct {
	*memRendezvous
}

var _ pairing.RendezvousTransport = (*ctxFaithfulMem)(nil)

func (c *ctxFaithfulMem) Send(ctx context.Context, m []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.memRendezvous.Send(ctx, m)
}

// startAbortingPhone runs the real RunDevice with mobile/pairing.go's DeviceSAS shape --
// which returns nil or ctx.Err() and nothing else -- over a ctx-faithful transport. The
// returned cancel is what RejectSAS, Cancel and the 60 s pairingTTL all reduce to; sasShown
// fires when the phone has the code on screen, which is the moment the owner turns to it.
func startAbortingPhone(t *testing.T, dEnd *memRendezvous, qp pairing.QRPayload) (sasShown <-chan struct{}, cancel context.CancelFunc, done chan devLegResult) {
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
	ctx, cancelFn := context.WithCancel(context.Background())
	t.Cleanup(cancelFn)

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
		DeviceSAS: func(ctx context.Context, _ [6]string) error {
			select {
			case shown <- struct{}{}:
			default:
			}
			<-ctx.Done() // this operator never presses "match"; only the abort ends the wait
			return ctx.Err()
		},
	}

	ch := make(chan devLegResult, 1)
	go func() {
		do, err := pairing.RunDevice(ctx, dp, &ctxFaithfulMem{memRendezvous: dEnd})
		ch <- devLegResult{outcome: do, err: err}
	}()
	return shown, cancelFn, ch
}

// beginAbortedPairing drives one pairing to the exact state the defect lives in: the desktop
// operator has CONFIRMED, and the phone has since aborted. It returns the owner connection and
// the expiry the daemon announced for that pairing.
func beginAbortedPairing(t *testing.T, sk *Daemon, deviceEnds chan *memRendezvous) (*rawRemote, time.Time) {
	t.Helper()

	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full", TTLSeconds: abortTTLSeconds}})

	reply := awaitControl(t, rc, protocol.OpPairStart)
	if reply.Pairing == nil || reply.Pairing.QR == "" {
		t.Fatalf("pair_start reply missing QR: %+v", reply.Pairing)
	}
	if reply.Pairing.ExpiresAt == nil {
		t.Fatal("pair_start announced no expiry, so there is no promise to hold the daemon to")
	}
	expires := *reply.Pairing.ExpiresAt
	if window := time.Until(expires); window > 30*time.Second {
		t.Fatalf("the daemon announced a %s window for a pair_start that requested %ds; these tests "+
			"hold it to what it announced and cannot wait that long", window, abortTTLSeconds)
	}
	qp, err := pairing.DecodeQR(reply.Pairing.QR)
	if err != nil {
		t.Fatalf("pair_start QR undecodable: %v", err)
	}

	dEnd := recvDeviceEnd(t, deviceEnds)
	sasShown, abort, devDone := startAbortingPhone(t, dEnd, qp)

	// The desktop reaches the SAS gate...
	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
		t.Fatalf("pair_pending missing the 6-word SAS gate: %+v", pending.Pairing)
	}
	// ...and the owner answers the prompt in front of them, affirmatively, FIRST.
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: true}})

	// Only then do they turn to the phone, which is holding the code up, and abort it.
	select {
	case <-sasShown:
	case <-time.After(10 * time.Second):
		t.Fatal("the phone never displayed its SAS; the pairing did not reach the state under test")
	}
	abort()

	select {
	case r := <-devDone:
		if r.err == nil {
			t.Fatalf("the phone leg returned success (%v) after aborting; it must fail closed", r.outcome)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the phone leg never resolved after its abort")
	}
	return rc, expires
}

// awaitControlBy reads control frames until one carries op or the instant `by` passes,
// returning ok=false on expiry rather than failing, so a caller can phrase the failure.
func awaitControlBy(t *testing.T, rc *rawRemote, op string, by time.Time) (protocol.Control, bool) {
	t.Helper()
	for {
		remaining := time.Until(by)
		if remaining <= 0 {
			return protocol.Control{}, false
		}
		c, err := rc.readTry(remaining)
		if err != nil {
			return protocol.Control{}, false
		}
		if c.Op == op {
			return c, true
		}
	}
}

// TestB64_APairingWhosePhoneAbortsAfterTheConfirmStillReportsAResult: the owner's desktop is
// sitting at "pairing..." with nothing left to answer. The daemon told it when this pairing
// expires; past that instant it owes the operator a terminal pair_result, success or failure.
// Today it owes one forever, because Machine.Pair is parked on a msg4 that the phone's abort
// path structurally cannot send.
func TestB64_APairingWhosePhoneAbortsAfterTheConfirmStillReportsAResult(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)

	rc, expires := beginAbortedPairing(t, sk, deviceEnds)

	res, ok := awaitControlBy(t, rc, protocol.OpPairResult, expires.Add(slotSlack))
	if !ok {
		t.Fatalf("no pair_result by %s after the announced expiry %s.\n"+
			"  The desktop operator confirmed, the phone then aborted, and the handshake goroutine is "+
			"parked in recvConsent with no deadline: internal/protocol/server.go:2098 builds the pairing "+
			"ctx as context.WithCancel(context.Background()), and pairWindow is only the expiry it ANNOUNCED.",
			slotSlack, expires.Format(time.RFC3339Nano))
	}
	if res.Pairing != nil && res.Pairing.DeviceID != "" {
		t.Fatalf("pair_result carried DeviceID %q for a pairing the PHONE aborted; want a failure",
			res.Pairing.DeviceID)
	}
	// The security half: an aborted pairing enrolls nothing and spends no device slot.
	if got := sk.api.devices.List(); len(got) != 0 {
		t.Fatalf("registry has %d devices after a pairing the phone ABORTED; want 0", len(got))
	}
}

// TestB64_AConnectionCanPairAgainAfterAPairingThePhoneAborted is the consequence that makes
// the hang permanent rather than merely slow, and it is the property whose absence let this
// ship. The owner retries -- the obvious thing to do, and the only thing the UI offers -- and
// pair_start is refused "pairing already in progress" on a pairing that ended minutes ago and
// will never end. Every subsequent attempt on that connection is refused the same way; there
// is no pair_cancel op, so only dropping the whole owner connection clears it.
func TestB64_AConnectionCanPairAgainAfterAPairingThePhoneAborted(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)

	rc, expires := beginAbortedPairing(t, sk, deviceEnds)

	// Wait out the window the daemon itself announced, then do what the operator does.
	if d := time.Until(expires.Add(slotSlack)); d > 0 {
		time.Sleep(d)
	}
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full", TTLSeconds: abortTTLSeconds}})

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
			return // the slot was released and the owner can pair again
		case protocol.OpError:
			t.Fatalf("the retried pair_start was REFUSED (%q). The connection's pairing slot is held by a "+
				"handshake that ended when the phone aborted and whose goroutine will never return, so "+
				"clearPairing never runs and cc.pair stays non-nil. There is no pair_cancel op: every "+
				"later pair_start on this connection is refused the same way, and only dropping the "+
				"owner connection escapes.", c.Error)
		case protocol.OpPairResult:
			// The first pairing's terminal result, arriving late. Keep reading for the reply.
		default:
			t.Fatalf("unexpected op %q while waiting for the retried pair_start's answer", c.Op)
		}
	}
	t.Fatal("the retried pair_start produced neither a reply nor a refusal within the frame budget")
}
