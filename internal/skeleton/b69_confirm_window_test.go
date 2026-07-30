package skeleton

// ADR-007 B69(3), SECOND LEG -- B64 BOUNDED THE TRANSPORT AND LEFT THE HUMAN PROMPT
// UNBOUNDED, SO B64'S OWN CONSEQUENCE CHAIN IS STILL REACHABLE.
//
// Machine.Pair observes its ctx in exactly one kind of call: the transport ones (Recv, Send,
// Complete). The third leg of the handshake is not a transport call at all -- it is
// p.Confirm, the desktop SAS gate -- and the skeleton adapter took the pairing ctx and threw
// it away:
//
//	Confirm: func(_ context.Context, sas [6]string, deviceName string) (bool, error) {
//	        return confirm(sas[:], deviceName)
//	}
//
// The closure it forwards to (internal/protocol/server.go) selects on the pairing session ctx
// built as context.WithCancel(context.Background()) -- the CONNECTION lifetime, with no
// deadline anywhere in it. So the one leg with a human on the end of it was the one leg the
// announced window did not reach.
//
// The scenario needs no attacker and no broken phone. The phone is PERFECT here: it matches
// the SAS the instant it sees it and sends its consent. The owner simply does not answer the
// desktop prompt -- they walked away, or the terminal is on another desk. Past the announced
// expiry the daemon owes a terminal pair_result and a reusable slot, and before this fence it
// owed both forever: no result, and every later pair_start on the connection refused "pairing
// already in progress", which is verbatim the chain B64 claimed to close.
//
// WHY THE PROMPT IS CANCELLED RATHER THAN LEFT STANDING WITH THE SLOT RELEASED. The general
// rule that a human decision must not be killed by a clock does not apply to this prompt,
// because an affirmative answer past the window cannot be honoured by anything: the relay has
// purged the rendezvous at its own slot TTL (which pairWindow clamps to), the handset gave up
// at its 60 s pairingTTL, and the daemon told the operator the expiry itself. Releasing the
// slot while leaving the prompt standing would be worse than either -- a second pairing could
// start on the same connection while the first Machine.Pair is still live behind a prompt
// that, if answered, walks into a dead rendezvous and a second terminal result for one
// pair_start. Bounding the prompt IS what releases the slot: clearPairing runs only from
// result, and result runs only when the handshake goroutine returns.

import (
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// TestB69_AnUnansweredDesktopConfirmIsBoundedByTheAnnouncedWindow drives the third leg. It
// shares the silent-phone fence's discipline: production is handed no clock, and the only
// instant appealed to is the ExpiresAt the daemon announced in its own pair_start reply.
func TestB69_AnUnansweredDesktopConfirmIsBoundedByTheAnnouncedWindow(t *testing.T) {
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

	// A phone doing everything right: a nil DeviceSAS is the operator matching the code the
	// instant it appears, so the consent is on the wire before the desktop prompt is answered.
	// NOTHING is wrong on this leg, which is what makes the defect an ordinary one.
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	devCtx, cancelDev := context.WithCancel(context.Background())
	t.Cleanup(cancelDev)
	dEnd := recvDeviceEnd(t, deviceEnds)
	devDone := runDeviceLeg(devCtx, ks, dEnd, qp)

	// The desktop SAS prompt reaches the owner...
	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
		t.Fatalf("pair_pending missing the 6-word SAS gate: %+v", pending.Pairing)
	}
	// ...AND IS NEVER ANSWERED. No pair_confirm is written by this test, ever.

	// The isolation check. The phone has said everything it will say and is parked on the
	// machine's decision; if its leg has resolved, something other than the window ended this
	// pairing and the test would be measuring that instead.
	select {
	case r := <-devDone:
		t.Fatalf("the phone leg RESOLVED (outcome=%v err=%v) before the window elapsed; this test is "+
			"no longer isolating the confirm leg's deadline", r.outcome, r.err)
	case <-time.After(250 * time.Millisecond):
	}

	res, ok := awaitControlBy(t, rc, protocol.OpPairResult, expires.Add(silentSlack))
	if !ok {
		t.Fatalf("no pair_result %s past the announced expiry %s, with the desktop prompt unanswered.\n"+
			"  Machine.Pair observes its ctx only in transport calls; the SAS confirm is p.Confirm, and\n"+
			"  internal/skeleton/pairing.go adapts it as func(_ context.Context, ...) -- the pairing ctx\n"+
			"  is DISCARDED, and the closure behind it selects on the connection ctx, which carries no\n"+
			"  deadline. The window is enforced on two legs of three.",
			silentSlack, expires.Format(time.RFC3339Nano))
	}
	if res.Pairing != nil && res.Pairing.DeviceID != "" {
		t.Fatalf("pair_result carried DeviceID %q for a pairing whose SAS gate was never answered; "+
			"an unanswered prompt must fail CLOSED (R-PAIR.5)", res.Pairing.DeviceID)
	}
	if got := sk.api.devices.List(); len(got) != 0 {
		t.Fatalf("registry has %d devices after a pairing the owner never confirmed; want 0", len(got))
	}

	// And the slot: the operator retries, which is the only thing the UI offers them.
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
			t.Fatalf("the retried pair_start was REFUSED (%q): the connection's pairing slot is still held "+
				"by a handshake parked on a prompt nobody will ever answer. There is no pair_cancel op, so "+
				"only dropping the owner connection escapes.", c.Error)
		case protocol.OpPairResult:
			// A late terminal result; keep reading for the reply.
		default:
			t.Fatalf("unexpected op %q while waiting for the retried pair_start's answer", c.Op)
		}
	}
	t.Fatal("the retried pair_start produced neither a reply nor a refusal within the frame budget")
}
