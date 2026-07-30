package skeleton

// THE THIRD CALLER OF THE UNBOUNDED DIAL, and the worst of the three -- fenced at the
// consequence that is its own, which "the dial returns" does not show.
//
// relayRendezvousFactory (pairing_rendezvous.go) dials relay.DialRawSecure inside the closure
// BeginPairing calls at pairing.go's `cfg.NewRendezvous(ctx, id)`. That ctx is the pair_start
// handler's -- internal/protocol/server.go's context.WithCancel(context.Background()), the
// OWNER CONNECTION's lifetime context. No deadline, like the phone's and the sidecar's.
//
// WHAT MAKES IT DIFFERENT FROM THE OTHER TWO. The dial runs BEFORE pairing.go builds
// `pairCtx, cancelPair := context.WithTimeout(ctx, window)` -- ADR-007 B64's fix -- so it sits
// INSIDE the pairing slot and OUTSIDE the window B64 exists to close. The slot is already
// claimed (`cc.pair = ps`, server.go) by the time BeginPairing is called, and it is released on
// exactly two paths: `result` (which only a finished handshake reaches) and BeginPairing's own
// error return. A dial that never returns takes neither. B64's own comment states the price:
// "every later pair_start on it is refused 'pairing already in progress'. There is no
// pair_cancel op; dropping the owner connection was the only exit."
//
// So the phone's cost is a spinner and the sidecar's is a silent non-start, but this one BURNS
// THE OWNER CONNECTION'S PAIRING SLOT, permanently, against a relay that has only to accept a
// TCP connection and go quiet. B64 fixed the handshake's missing clock; the dial that PRECEDES
// the handshake is the one step of the ceremony B64 did not reach.
//
// AND IT IS AN ARGUMENT FOR BOUNDING AT dialConn RATHER THAN HERE. The ctx this closure
// receives has DUAL DUTY: it bounds the dial AND owns the connection's lifetime, through the
// `go func(){ <-ctx.Done(); _ = conn.Close() }()` watcher three lines below the dial. A
// caller-side `defer cancel()` on it would close the connection the factory just returned. The
// boundary bound has no such hazard, at this site or the other two.
//
// THE ASSERTION IS THE SLOT, not the dial. The dial returning is fenced where the bound lives
// (internal/remote/relay/dialdeadline_test.go). What is fenced here is the daemon-level
// property an operator can observe: after a pairing whose rendezvous dial hit a silent relay,
// THE OWNER CAN PAIR AGAIN ON THE SAME CONNECTION.

import (
	"crypto/ed25519"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// stalledDialAnswerBound is how long these assertions wait for the daemon to answer a
// pair_start whose rendezvous dial reaches a silent relay. It is far above the bound under test
// and far below "forever", and it transcribes no production constant (ADR-007 B113).
const stalledDialAnswerBound = 60 * time.Second

// newSilentRelayListener accepts every TCP connection and then says nothing, ever: no TLS
// ServerHello, no HTTP response, no upgrade. Nothing is closed or reset, so there is no event
// for the dialling side to observe and nothing for the OS to time out. It returns the ws:// URL
// production would have been configured with.
func newSilentRelayListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c) // accepted and held open, unanswered
		}
	}()
	return "ws://" + ln.Addr().String()
}

// injectRelayPairing is injectPairing's sibling with the ONE difference this file is about: the
// rendezvous comes from the REAL production closure, relayRendezvousFactory, under the machine's
// own transport policy, rather than from an in-memory pair. Everything a stalled dial does to
// the pairing slot happens inside that closure, so a test that substitutes it measures nothing.
func injectRelayPairing(t *testing.T, sk *Daemon, relayURL string) {
	t.Helper()
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	signPub, signPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("machine grant-signing key: %v", err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	sk.api.pairing = &pairingConfig{
		Static:        machineID.NoiseStatic(),
		RecipientPub:  machineID.RecipientPublic(),
		SignPub:       signPub,
		SignPriv:      signPriv,
		EpochID:       1,
		GrantSeq:      1,
		EpochKeys:     keys,
		Hostname:      "test-machine.local",
		RoutingID:     []byte("machine-routing-id-0001"),
		RelayAuthPub:  make([]byte, 32),
		RelayURL:      relayURL,
		NewRendezvous: relayRendezvousFactory(relayURL, relay.MachineSecurity()),
	}
}

// TestPBNET7_AStalledRendezvousDialDoesNotBurnThePairingSlot.
//
// Two pair_starts on ONE owner connection, against a relay that accepts and answers nothing.
// The first must be ANSWERED rather than parked; the second must be ACCEPTED rather than
// refused, which is the only way to observe that clearPairing ran.
//
// No wall-clock duration is asserted. Both waits are generous, and the substantive claims are
// on WHICH frame comes back.
func TestPBNET7_AStalledRendezvousDialDoesNotBurnThePairingSlot(t *testing.T) {
	sk := assemble(t)
	injectRelayPairing(t, sk, newSilentRelayListener(t))

	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	start := protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full", TTLSeconds: abortTTLSeconds}}

	// ---- the first pair_start: its rendezvous dial reaches the silent relay -------------
	rc.write(start)
	first, err := rc.readTry(stalledDialAnswerBound)
	if err != nil {
		t.Fatalf("no answer to pair_start within %v: %v.\n"+
			"The daemon is parked in relayRendezvousFactory's relay.DialRawSecure against a peer "+
			"that accepted the TCP connection and went quiet. That dial runs BEFORE pairing.go's "+
			"pairCtx, so ADR-007 B64's window is not yet in force, and the pairing slot claimed by "+
			"the pair_start handler is held with nothing left to release it",
			stalledDialAnswerBound, err)
	}
	if first.Op != protocol.OpError {
		t.Fatalf("the first pair_start answered with %q against a relay that never replies; "+
			"want an error naming the failed rendezvous open", first.Op)
	}

	// ---- the second, on the SAME connection: the slot must be free ----------------------
	rc.write(start)
	for i := 0; i < 8; i++ {
		c, err := rc.readTry(stalledDialAnswerBound)
		if err != nil {
			t.Fatalf("no answer to the second pair_start within %v: %v",
				stalledDialAnswerBound, err)
		}
		switch c.Op {
		case protocol.OpPairStart:
			if c.Pairing == nil || c.Pairing.QR == "" {
				t.Fatalf("the second pair_start replied without a QR: %+v", c.Pairing)
			}
			return // the slot was released: the owner can pair again
		case protocol.OpError:
			if isPairingInProgress(c.Error) {
				t.Fatalf("the second pair_start was REFUSED (%q).\n"+
					"The connection's pairing slot is still held by the first pairing, whose "+
					"rendezvous dial never returned, so BeginPairing never returned either and "+
					"neither of clearPairing's two callers ran. There is no pair_cancel op: every "+
					"later pair_start on this connection is refused the same way, and only "+
					"dropping the owner connection escapes (ADR-007 B64)", c.Error)
			}
			// The second dial reaching the same silent relay and failing the same way is the
			// SLOT being free, which is what this test is about.
			return
		default:
			t.Fatalf("unexpected op %q while waiting for the second pair_start's answer", c.Op)
		}
	}
	t.Fatal("the second pair_start produced neither a reply nor a refusal within the frame budget")
}

// isPairingInProgress recognises the refusal the held-slot defect produces, so a test cannot
// mistake it for the ordinary dial failure the second attempt is expected to hit.
func isPairingInProgress(msg string) bool {
	return strings.Contains(msg, "pairing already in progress")
}
