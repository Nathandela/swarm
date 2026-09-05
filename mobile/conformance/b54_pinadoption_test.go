// ADR-007 B54 (FAILING FIRST): a completed pairing adopts the machine's published payload
// VERBATIM, including the absence of a relay pin.
//
// THE LOOP THIS CLOSES, which is reachable by an ordinary operator action and has no exit.
// `swarm remote init --relay-pin` is optional, so "publishes no pin" is the common case. A
// phone that once learned a pin used to KEEP it when a later pairing published none, on the
// reasoning that overwriting a known pin with nothing would silently downgrade a handset that
// had one. That reasoning ignores where the phone ends up:
//
//	the machine moves to a relay it has no pin for, and re-inits without --relay-pin
//	  -> every dial fails ErrPinMismatch against the pin the phone still holds
//	  -> the phone reports relay_untrusted, whose remedy is "pair this phone again"
//	  -> the user pairs again; same machine, so nothing refuses it
//	  -> the machine publishes no pin, the phone keeps the stale one
//	  -> back to the first line, forever, with no on-device way out
//
// So the state the old rule protected against -- a downgrade -- is recoverable the moment the
// operator supplies a pin, and the state it created is not recoverable at all.
//
// WHY ADOPTION IS THE RIGHT RULE AND NOT MERELY THE LESSER EVIL: a completed pairing is
// authenticated by the Noise handshake and confirmed by two operators comparing a SAS. It is
// the machine's own statement about its own relay, made over the one channel the design trusts
// for exactly this. Treating "no pin" as an accident second-guesses that statement.
//
// THE RIG REUSES ONE MACHINE IDENTITY ACROSS BOTH PAIRINGS, deliberately. App.differentMachine
// keys on MachineStatic and refuses the whole pairing before pin() runs, so a rig that minted a
// fresh identity per pairing would land in `different_machine` and prove nothing about the pin.
// The reachable loop is the SAME machine re-pairing, and that is what this drives.
package conformance_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// b54Machine is one machine identity, reusable across pairings, so the phone sees the SAME
// machine twice and differentMachine stays silent.
type b54Machine struct {
	id       *crypto.Identity
	signPub  ed25519.PublicKey
	authPub  ed25519.PublicKey
	relayURL string
}

func newB54Machine(t *testing.T, relayURL string) *b54Machine {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	signPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine sign key: %v", err)
	}
	authPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	return &b54Machine{id: id, signPub: signPub, authPub: authPub, relayURL: relayURL}
}

// offer starts one responder publishing pin (nil for "this machine has no pin") and returns
// the QR for it.
func (m *b54Machine) offer(t *testing.T, pin []byte) string {
	t.Helper()
	return m.offerAt(t, pin, m.relayURL)
}

// offerAt is offer with the URL the QR names stated separately from the one the MACHINE dials.
// They differ whenever the phone reaches the relay through a TLS terminator and the machine
// speaks to the plain relay behind it, which is the topology a pin is meaningful in at all.
func (m *b54Machine) offerAt(t *testing.T, pin []byte, qrURL string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var secret [32]byte
	var rid [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(rid[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}

	mach := pairing.NewMachine(pairing.MachineParams{
		Static:       m.id.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		LocalConsole: true,
		Confirm:      func(context.Context, [6]string, string) (bool, error) { return true, nil },
		Payload: pairing.MachinePayload{
			Hostname:            "b54.local",
			OperatorNamespace:   "owner",
			MachineRoutingID:    []byte(relay.RoutingID(m.authPub)),
			MachineRelayAuthPub: m.authPub,
			RecipientPub:        m.id.RecipientPublic(),
			MachineSignPub:      m.signPub,
			MachineEndpointID:   testMachineID,
			EpochID:             testEpochID,
			RelaySPKIPin:        pin,
		},
	})

	conn, err := relay.DialRaw(ctx, m.relayURL)
	if err != nil {
		t.Fatalf("machine DialRaw: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	rz := &createdRendezvous{
		RendezvousTransport: &relayRendezvous{conn: conn, label: hex.EncodeToString(rid[:])},
		created:             make(chan struct{}),
	}
	go func() { _, _ = mach.Pair(ctx, rz) }()
	select {
	case <-rz.created:
	case <-time.After(10 * time.Second):
		t.Fatalf("the machine never created the pairing rendezvous")
	}

	qr, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL: qrURL, RendezvousID: rid, PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	return qr
}

// b54PairOnce drives the phone through one complete pairing and requires it to land paired.
func b54PairOnce(t *testing.T, app *swarmmobile.App, qr string) {
	t.Helper()
	p := s16BeginConfirmed(t, app, qr)
	s16AwaitSAS(t, p)
	if err := p.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	s16AwaitState(t, p, "paired")
}

// b54PersistedPin reads the pin out of the phone's durable state, which is the only place the
// dial site reads it from (App.handsetSecurity).
func b54PersistedPin(t *testing.T, dir string, custody *testCustody) []byte {
	t.Helper()
	reg, err := phonecore.OpenMachineRegistry(dir)
	if err != nil {
		t.Fatalf("open machine registry: %v", err)
	}
	namespace := reg.BootstrapDir()
	for _, entry := range reg.Entries() {
		if entry.ID == testMachineID {
			namespace = reg.MachineDir(entry.ID)
			break
		}
	}
	store, err := phonecore.OpenStore(namespace+"/"+phonecore.StateFileName, testMachineID,
		custody.wakeSealer(), custody.contentSealer())
	if err != nil {
		t.Fatalf("open phone state: %v", err)
	}
	return store.Load().RelaySPKIPin
}

// TestB54_ARePairingWithNoPinClearsTheOneThePhoneHeld is the loop, driven end to end through
// two real pairings against one machine.
//
// The first pairing is the CONTROL and it runs first: without it, "the pin is empty at the end"
// is satisfied by a rig that never delivered one.
func TestB54_ARePairingWithNoPinClearsTheOneThePhoneHeld(t *testing.T) {
	_, relayURL := s16FreshRelay(t)
	machine := newB54Machine(t, relayURL)

	dir := t.TempDir()
	custody := newTestCustody(t)
	app := s16UnpairedApp(t, dir, relayURL, custody)

	// ---- control: the machine publishes a pin and the phone adopts it ----
	//
	// WAITED FOR, NOT READ INSTANTLY, and the reason is a happens-before this fence originally
	// got wrong. Pairing.finish() sets the "paired" label under p.mu, UNLOCKS, and only then
	// calls App.pin() -- so s16AwaitState(..., "paired") synchronises with the state machine
	// and NOT with the durable write. Reading immediately after the label is a race the write
	// normally wins on an idle machine and loses under `go test ./...`, which is exactly the
	// 2-in-3 flake this fence showed. Proven rather than surmised: inserting a 200ms sleep
	// between that unlock and pin() makes the instantaneous read fail every time.
	//
	// The wait is bounded and the property is unchanged: a product that never writes the pin
	// times out here and fails.
	want := sha256.Sum256([]byte("the relay this machine used to be behind"))
	b54PairOnce(t, app, machine.offer(t, want[:]))
	eventually(t, "the phone never adopted the pin its machine published -- the rig delivered "+
		"none, so the assertion below would be vacuous", func() bool {
		return string(b54PersistedPin(t, dir, custody)) == string(want[:])
	})

	// ---- the fence: the SAME machine re-pairs publishing none ----
	// This is `swarm remote init --relay-url X` without --relay-pin: the common case, since
	// the flag is optional.
	b54PairOnce(t, app, machine.offer(t, nil))

	eventually(t, "the phone still holds the pin it learned earlier, after re-pairing with a "+
		"machine that published none.\nThat is ADR-007 B54's loop and it has no exit: every dial "+
		"now fails ErrPinMismatch, the phone reports relay_untrusted, its remedy is \"pair this "+
		"phone again\", and pairing again lands right back here. A completed pairing is "+
		"SAS-confirmed and authenticated -- the machine's payload is its own statement about its "+
		"own relay, and an absent pin is part of that statement", func() bool {
		return len(b54PersistedPin(t, dir, custody)) == 0
	})
}
