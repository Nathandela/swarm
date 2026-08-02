// PB-PAIR-4 (FAILING FIRST) -- THE ACKNOWLEDGEMENT MUST ATTEST THE PHONE'S DURABLE COMMIT,
// NOT THE ARRIVAL OF THE ACCEPTANCE FRAME.
//
// The machine no longer commits on having SENT its acceptance; it waits to be told (ADR-007
// B81(2), B86(2)), and internal/skeleton enrols on that acknowledgement. So the whole of the
// machine's protection is what the acknowledgement MEANS.
//
// Today it means "the acceptance frame arrived". RunDevice sends it and returns; the phone's
// durable pin runs AFTERWARDS, in Pairing.finish -> App.pin. Between those two points sit a
// full disk, a read-only data directory, a Keystore refusal and process death -- and every one
// of them ends with the MACHINE ENROLLED, remote control live, the single-device slot spent,
// while the phone holds no durable pin at all and does not believe it is paired.
//
// THE SEND SITE'S OWN COMMENT IS THE FINDING IN MINIATURE. It reasons carefully about the
// two-generals residual and concludes that it "is the harmless orientation: re-pairing needs
// nothing from the desktop." That is true of the orientation it enumerates -- phone pinned,
// machine claiming nothing. It never considers the reverse, which is the one that needs a
// desktop revoke, and the reverse is what a failed pin produces DETERMINISTICALLY, with no
// relay misbehaviour and no attacker anywhere.
//
// WHY THE DISCRIMINATOR IS IN THE FIXTURE AND NOT IN AN ASSERTION. A fence that only watched
// the phone would pass on both implementations: the phone reports `failed` either way, because
// ADR-007 B60 already made a refused write not a pairing. What separates the two worlds is what
// the MACHINE ends up holding, so the rig captures the responder's own Pair result and the
// property is stated over it. Without that channel there is nothing here to fail.
//
// The injection is a read-only state directory, applied at the SAS gate -- one of the four
// causes named above, chosen because it needs no seam that does not already exist and it is
// independent of every custody tier. It is applied AFTER the SAS is derived, so the handshake,
// the relay-auth consent signature and both operators' comparison all happen normally; the
// first and only thing that cannot happen is the durable write.
package conformance_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// pbPair4Result is the machine responder's OWN answer to the ceremony: an outcome means it
// claimed the device, and internal/skeleton enrols on exactly that.
type pbPair4Result struct {
	out *pairing.MachineOutcome
	err error
}

// pbPair4Offer starts one responder over the real relay and returns the QR for it PLUS the
// channel carrying its result.
//
// The channel is the whole point. b54Machine.offer drops the responder's outcome on the floor
// (`go func() { _, _ = mach.Pair(...) }()`), which is right for the pin-adoption tests it was
// built for and blind to everything this file is about.
func pbPair4Offer(t *testing.T, m *b54Machine) (qr string, done <-chan pbPair4Result) {
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
			Hostname:            "pbpair4.local",
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
	results := make(chan pbPair4Result, 1)
	go func() {
		out, perr := mach.Pair(ctx, rz)
		results <- pbPair4Result{out: out, err: perr}
	}()
	select {
	case <-rz.created:
	case <-time.After(10 * time.Second):
		t.Fatalf("the machine never created the pairing rendezvous")
	}

	code, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL: m.relayURL, RendezvousID: rid, PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	return code, results
}

// pbPair4Phone is a handset that has never paired, over dir, and DELIBERATELY NOT STARTED: the
// transport loop would dial the relay on its own schedule and unwrap the wake tier for every
// dial, which is noise against a test whose only durable write of interest is the pairing's.
func pbPair4Phone(t *testing.T, dir, relayURL string) *swarmmobile.App {
	t.Helper()
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: dir, RelayURL: relayURL, MachineID: testMachineID,
	}, newTestCustody(t))
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// freezeStateDir makes the phone's state directory unwritable, which is what a phone with a
// full disk or a data directory the platform has revoked write access to looks like from
// phonecore: persistState writes through writeFileAtomic, whose temp file cannot be created.
//
// IT PROVES THE INJECTION LANDED RATHER THAN ASSUMING IT. A user who ignores directory
// permissions -- root in a container -- would sail through the chmod and turn every assertion
// below into a vacuous pass, which is precisely the shape of fence this audit keeps finding. So
// the probe runs, and a rig that cannot inject the fault says so and FAILS instead of skipping:
// a fence that quietly disarms itself on some hosts is not a fence.
func freezeStateDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod state dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	probe := filepath.Join(dir, "writability-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err == nil {
		_ = os.Remove(probe)
		t.Fatalf("the state directory is still writable after chmod 0500, so the durable write "+
			"this test must break will succeed and every assertion below is vacuous. This rig "+
			"cannot run as a user who ignores directory permissions (uid %d).", os.Geteuid())
	}
}

// pbPair4AwaitPhoneFailed requires the phone to report the refused write as a FAILED pairing.
//
// It is a precondition, not the property: ADR-007 B60 already made a refused write not a
// pairing, so this reads the same on both implementations. What it buys is that a `paired` here
// means the fault never reached the durable write and the rig is measuring nothing.
func pbPair4AwaitPhoneFailed(t *testing.T, p *swarmmobile.Pairing) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		got, err := p.State()
		if err != nil {
			t.Fatalf("Pairing.State: %v", err)
		}
		last = got
		switch got {
		case "failed":
			return
		case "paired":
			t.Fatalf("the phone reported `paired` with its state directory unwritable. Either " +
				"the injection never reached the durable write -- in which case every assertion " +
				"in this test is vacuous -- or a refused write was published as a completed " +
				"pairing, which is the defect ADR-007 B60 closed.")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the pairing settled in %q within 10s; want `failed`, the state ADR-007 B60 gives a "+
		"refused durable write", last)
}

// TestPBPAIR4_TheMachineMustNotEnrolWhenThePhoneCannotDurablyPin is the requirement.
//
// Every frame of the ceremony is delivered intact in both directions, both operators consent,
// and the ONE thing that fails is the phone's durable write. The machine must therefore end the
// ceremony claiming NOTHING: an acknowledgement that can be sent by a phone which then fails to
// pin attests the arrival of a frame, and the machine's entire protection is that it attests a
// COMMIT.
func TestPBPAIR4_TheMachineMustNotEnrolWhenThePhoneCannotDurablyPin(t *testing.T) {
	_, relayURL := s16FreshRelay(t)
	machine := newB54Machine(t, relayURL)

	dir := t.TempDir()
	app := pbPair4Phone(t, dir, relayURL)

	qr, machineDone := pbPair4Offer(t, machine)
	p := s16BeginConfirmed(t, app, qr)
	s16AwaitSAS(t, p)

	// The fault is injected HERE: after the handshake, after the consent signature, after both
	// operators have something to compare -- and before the acceptance the phone must not
	// acknowledge without committing.
	freezeStateDir(t, dir)

	if err := p.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	pbPair4AwaitPhoneFailed(t, p)

	var res pbPair4Result
	select {
	case res = <-machineDone:
	case <-time.After(20 * time.Second):
		t.Fatal("the machine leg never resolved; it should either claim the device or give up " +
			"waiting for an acknowledgement within its own window")
	}

	if res.err == nil && res.out != nil {
		t.Fatalf("HALF-PAIR (PB-PAIR-4): the MACHINE claimed this device (%x) while the phone "+
			"holds no durable pin at all.\n"+
			"  Nothing was lost. Every frame arrived, both operators consented, and the only\n"+
			"  failure was the phone's own durable write -- a full disk, a read-only data\n"+
			"  directory, a Keystore refusal, or the process dying in that window.\n"+
			"  RunDevice sends the acknowledgement and RETURNS; App.pin runs afterwards in\n"+
			"  Pairing.finish. So the acknowledgement attests \"the acceptance frame arrived\",\n"+
			"  and internal/skeleton enrols on it -- remote control live, the single-device slot\n"+
			"  spent, every further pairing refused, and the only exit a desktop revoke the\n"+
			"  phone's owner has no reason to know they need.\n"+
			"  This is the ORIENTATION THE SEND SITE'S COMMENT NEVER CONSIDERS. It is right that\n"+
			"  the reverse is harmless; it does not follow that this one is.\n"+
			"  The acknowledgement must mean THE PHONE DURABLY COMMITTED.",
			res.out.DeviceStatic)
	}
}

// TestPBPAIR4_ControlAHealthyPairingStillEnrolsBothLegs is what forbids the lazy fix. A device
// that never acknowledged anything, or a machine that never claimed anything, satisfies the
// fence above completely.
func TestPBPAIR4_ControlAHealthyPairingStillEnrolsBothLegs(t *testing.T) {
	_, relayURL := s16FreshRelay(t)
	machine := newB54Machine(t, relayURL)

	dir := t.TempDir()
	app := pbPair4Phone(t, dir, relayURL)

	qr, machineDone := pbPair4Offer(t, machine)
	p := s16BeginConfirmed(t, app, qr)
	s16AwaitSAS(t, p)

	if err := p.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	s16AwaitState(t, p, "paired")

	var res pbPair4Result
	select {
	case res = <-machineDone:
	case <-time.After(20 * time.Second):
		t.Fatal("the machine leg never resolved on a healthy pairing")
	}
	if res.err != nil || res.out == nil {
		t.Fatalf("the machine claimed NO device on a pairing both operators allowed and whose "+
			"phone pinned successfully (err=%v). A remedy that stops the machine enroling is not "+
			"a remedy: it breaks every pairing there is", res.err)
	}
	if len(res.out.Device.ConsentSig) == 0 {
		t.Fatal("the machine completed the pairing holding no relay-route consent, so this " +
			"control did not drive the ceremony it claims to")
	}
}
