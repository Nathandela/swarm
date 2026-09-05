// agents-tracker-ksvb.1 (FAILING FIRST, GG-5): the MACHINE's human name reaches the phone, and
// the phone stores it beside the endpoint id rather than instead of it.
//
// THE SEAM IS THE ONE mobile/pairing.go ALREADY RESERVED. `DeviceName`'s doc says of the name
// the machine holds: "that is a different verb carrying a fact the wire actually delivers". The
// wire delivers it already -- `pairing.MachinePayload.Hostname` is msg2's first field, populated
// from `machineid.Identity.Hostname()` by internal/skeleton's pairingConfig -- and `pin()` was
// throwing it away. This is that verb.
//
// THE ENDPOINT ID REMAINS THE IDENTITY. `State.Machine` is what `OpenStore` filters the durable
// blob on and what `crypto.Command.Canonical` refuses to sign without, so the hostname is a
// SECOND coordinate and never a replacement: this test asserts both survive one pairing, because
// a change that overwrote the id with the name would satisfy any assertion about the name alone
// and brick the handset on its next process start (the S9 defect State.Disowned's doc records).
//
// THE ABSENT CASE IS THE OTHER HALF. A machine whose identity carries no hostname publishes an
// empty one, and the phone must hold nothing rather than invent something -- ADR-007 B135. The
// render sites fall back to the endpoint id, which is a fact.
package conformance_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// ksvb1Offer starts one machine responder publishing the given hostname and returns the QR for
// it. It is b54's rig with the field this bead is about made a parameter; b54's own offer is
// left alone because its subject is the relay pin and a widened signature there would put this
// bead's variable into that bead's assertions.
func ksvb1Offer(t *testing.T, m *b54Machine, hostname string) string {
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
			Hostname:            hostname,
			MachineRoutingID:    []byte(relay.RoutingID(m.authPub)),
			MachineRelayAuthPub: m.authPub,
			RecipientPub:        m.id.RecipientPublic(),
			MachineSignPub:      m.signPub,
			MachineEndpointID:   testMachineID,
			EpochID:             testEpochID,
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
		RelayURL: m.relayURL, RendezvousID: rid, PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	return qr
}

// ksvb1PersistedName reads the machine name out of the phone's DURABLE state, not out of the
// live App: the name has to survive the process death Android inflicts routinely, and a field
// held only in memory would satisfy an in-process read and come back empty on the next launch.
func ksvb1PersistedName(t *testing.T, dir string, custody *testCustody) (name, endpoint string) {
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
	st := store.Load()
	return st.MachineName, st.Machine
}

func TestKSVB1_APairingCarriesTheMachinesHumanName(t *testing.T) {
	_, relayURL := s16FreshRelay(t)
	machine := newB54Machine(t, relayURL)

	dir := t.TempDir()
	custody := newTestCustody(t)
	app := s16UnpairedApp(t, dir, relayURL, custody)

	b54PairOnce(t, app, ksvb1Offer(t, machine, "nathans-mbp"))

	// Waited for rather than read instantly, for b54's documented reason: finish() publishes
	// the "paired" label before pin() writes, so an immediate read races the durable write.
	eventually(t, "the phone never stored the hostname its machine published in the pairing "+
		"payload. `pairing.MachinePayload.Hostname` is msg2's first field and it reaches "+
		"`DeviceOutcome.Machine.Hostname`; App.pin() is the only place it can be persisted, and "+
		"without it every screen keeps rendering `ep-` plus a hash for a machine that told the "+
		"phone its name", func() bool {
		name, _ := ksvb1PersistedName(t, dir, custody)
		return name == "nathans-mbp"
	})

	// The endpoint id is UNTOUCHED. It is the identity every signed command carries and the
	// key OpenStore filters this very blob on; the hostname is display only.
	if _, endpoint := ksvb1PersistedName(t, dir, custody); endpoint != testMachineID {
		t.Errorf("State.Machine = %q after the pairing; want %q. The hostname is a SECOND "+
			"coordinate -- overwriting the endpoint id with it makes the next process start "+
			"discard this blob wholesale", endpoint, testMachineID)
	}

	// And the live App answers with the same string, so a screen has a verb to read rather
	// than a durable field it cannot reach.
	got, err := app.MachineName()
	if err != nil {
		t.Fatalf("App.MachineName: %v", err)
	}
	if got != "nathans-mbp" {
		t.Errorf("App.MachineName() = %q; want nathans-mbp -- the name the machine published", got)
	}
}

func TestKSVB1_AMachineThatPublishesNoNameLeavesThePhoneWithNone(t *testing.T) {
	_, relayURL := s16FreshRelay(t)
	machine := newB54Machine(t, relayURL)

	dir := t.TempDir()
	custody := newTestCustody(t)
	app := s16UnpairedApp(t, dir, relayURL, custody)

	b54PairOnce(t, app, ksvb1Offer(t, machine, ""))

	// The pairing must have LANDED before the absence means anything, or this passes on a
	// handset that paired with nothing at all.
	eventually(t, "the pairing never reached the durable state, so the empty name below would "+
		"be measuring a pairing that did not happen", func() bool {
		_, endpoint := ksvb1PersistedName(t, dir, custody)
		return endpoint == testMachineID
	})

	if name, _ := ksvb1PersistedName(t, dir, custody); name != "" {
		t.Errorf("State.MachineName = %q for a machine that published none; want the empty "+
			"string. ADR-007 B135: the phone renders the endpoint id when there is no name, and "+
			"a fabricated one is indistinguishable on screen from a real one", name)
	}
	got, err := app.MachineName()
	if err != nil {
		t.Fatalf("App.MachineName: %v", err)
	}
	if got != "" {
		t.Errorf("App.MachineName() = %q for a machine that published none; want the empty string", got)
	}
}

// TestKSVB1_AnUnpairedPhoneNamesNoMachine is the third state, and it is the one a screen is
// most likely to render by accident: an app that has never paired.
//
// IT ANSWERS EMPTY RATHER THAN REFUSING, which is this verb reading durable state honestly
// rather than asserting a gate it does not have. `App.ready()` checks only that the receiver
// exists and is not closed -- `PairedDeviceName` one screen up is gated by exactly the same
// call, so a refusal here would be a pairing check invented for one verb and absent from its
// neighbour. What is left is the fact: a phone bound to no machine holds no machine name, and
// "" is what "nobody has told this phone" means everywhere else in this seam.
func TestKSVB1_AnUnpairedPhoneNamesNoMachine(t *testing.T) {
	_, relayURL := s16FreshRelay(t)
	app := s16UnpairedApp(t, t.TempDir(), relayURL, newTestCustody(t))

	got, err := app.MachineName()
	if err != nil {
		t.Fatalf("App.MachineName on an unpaired phone: %v", err)
	}
	if got != "" {
		t.Errorf("App.MachineName() = %q on a phone that has never paired; want the empty string. "+
			"A name here came from nowhere the wire reached", got)
	}
}

var _ = swarmmobile.DeviceName // this file's subject is the counterpart to that constant
