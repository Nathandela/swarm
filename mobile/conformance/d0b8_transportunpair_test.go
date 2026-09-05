package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-d0b8's remaining half: the revoke that
// arrives OVER THE TRANSPORT has to survive a process death, and pairing again has to end it.
//
// WHAT THE LIVE READING ALREADY DOES, so this file's subject is precise. `App.StateSummary.Paired`
// consults the connection state, so a phone that observes `relay.ErrRevoked` reads as unpaired
// from that instant and the presentation gate sends it to the pairing screen. That covers the
// owner-side revoke while the app is RUNNING.
//
// WHAT IT DOES NOT DO. Android SIGKILLs this app as routine behaviour, and a connection state is
// process memory. The handset comes back, cannot reach the relay -- no signal, aeroplane mode, a
// relay that is down -- and nothing re-derives the verdict: it reads as paired again, in the
// four-tab shell, holding a registration the machine deleted. The recovery is not lost (the
// settings screen still offers "Replace this computer", which ends the registration durably), but
// it is a recovery the user has to know to perform, over an app that has stopped telling them
// anything is wrong. So the verdict is written down at the moment it is first observed.
//
// WHAT IS DELIBERATELY NOT WRITTEN DOWN WITH IT: the keys. `PurgeKeys` destroys both tiers
// irreversibly, and its trigger is the OWNER acting on this handset (ADR-007 B133). Doing it on
// `relay.ErrRevoked` would let the relay -- a party this design trusts with no plaintext and no
// authority -- destroy a user's cached content, and doing it on `connRepairRequired` would destroy
// content over a platform fault that is not a revocation at all. The unpair is recorded; the purge
// stays the owner's.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/relay"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// d0b8Stranded is a handset the owner has revoked FROM THE MACHINE: real relay, real ban, and the
// pairing record a real stranded phone holds. It is the fixture
// TestPBSTATE10_ThePostPairingGraceWindowSurvivesADialThatLosesTheRace builds, factored out to the
// point where the two files' subjects diverge.
type d0b8Stranded struct {
	dir        string
	relayURL   string
	custody    *testCustody
	provision  *phonecore.Core
	machine    *relay.Client
	machinePub ed25519.PublicKey
	phonePub   ed25519.PublicKey
	ctx        context.Context
}

func d0b8NewStranded(t *testing.T) *d0b8Stranded {
	t.Helper()
	_, relayURL := s16FreshRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	dir := t.TempDir()
	custody := newTestCustody(t)
	coreDir := pairedNamespace(t, dir, testMachineID)
	provision, err := phonecore.Resume(phonecore.Config{
		Dir: coreDir, Machine: testMachineID,
		WakeSealer: custody.wakeSealer(), ContentSealer: custody.contentSealer(),
	})
	if err != nil {
		t.Fatalf("provision the phone's keystore: %v", err)
	}
	phonePub := provision.KeyStore().RelayAuthPublic()

	// ONE MACHINE IDENTITY FOR BOTH HALVES, because the ban records its placer (ADR-007 B24) and
	// a scoped verdict is asked for by naming the peer (B49): the authorize that could lift it
	// has to come from the connection that placed it.
	mPub, mPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	machine, err := relay.Dial(ctx, relayURL, relay.ClientAuth{
		RelayAuthPub: mPub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(mPriv, c), nil },
	})
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = machine.Close() })
	if err := machine.AuthorizeDevice(ctx, phonePub, consentFrom(t, provision.KeyStore(), relay.RoutingID(mPub))); err != nil {
		t.Fatalf("machine authorize phone: %v", err)
	}

	// `swarm remote revoke <device-id>`, which is the documented way to remove a device and the
	// only mitigation ADR-007 B133 leaves for a lost handset. NOTHING ON THE PHONE RUNS FOR IT.
	if err := machine.DeviceRevoke(ctx, relay.RoutingID(phonePub)); err != nil {
		t.Fatalf("the owner's revoke, from the machine: %v", err)
	}

	// The pairing record the stranded handset actually holds -- a scoped ban is only answered to
	// a phone that names its machine, and this is the coordinate a restart restores.
	store, err := phonecore.OpenStore(filepath.Join(coreDir, phonecore.StateFileName), testMachineID,
		custody.wakeSealer(), custody.contentSealer())
	if err != nil {
		t.Fatalf("open the stranded phone's state: %v", err)
	}
	if err := store.Save(phonecore.State{
		Machine:             testMachineID,
		MachineRelayAuthPub: mPub,
		OperatorNamespace:   "owner",
		RoutingID:           relay.RoutingID(phonePub),
	}); err != nil {
		t.Fatalf("seed the stranded phone's pairing record: %v", err)
	}

	return &d0b8Stranded{
		dir: dir, relayURL: relayURL, custody: custody, provision: provision,
		machine: machine, machinePub: mPub, phonePub: phonePub, ctx: ctx,
	}
}

// open builds a facade over the stranded directory. started=false is a handset that has come back
// with no way to reach the relay, which is the case this file exists for: nothing dials, so
// nothing can re-derive a verdict held in memory.
func (s *d0b8Stranded) open(t *testing.T, started bool) (*swarmmobile.App, *s18bConnLog) {
	t.Helper()
	log := &s18bConnLog{}
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: s.dir, RelayURL: s.relayURL, MachineID: testMachineID,
	}, s.custody)
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.SetEventListener(log); err != nil {
		t.Fatalf("SetEventListener: %v", err)
	}
	if started {
		if err := app.Start(); err != nil {
			t.Fatalf("App.Start: %v", err)
		}
	}
	return app, log
}

func d0b8Paired(t *testing.T, app *swarmmobile.App) bool {
	t.Helper()
	sum, err := app.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary: %v", err)
	}
	return sum.Paired
}

// TestD0B8_AnOwnerSideRevokeSurvivesTheProcessDeathThatFollowsIt.
//
// The sequence is the ordinary one on a handset: the owner revokes from the Mac, the phone learns
// on its next dial, and then Android kills the app -- which it does routinely, and which is
// certain here because the app has nothing left to do. What must not happen is the next launch
// coming up paired.
//
// THE RESTART IS DELIBERATELY OFFLINE (`started=false`). A restarted app that dials would observe
// the ban again and re-derive the verdict from the live connection, which would make this test
// pass over an implementation that wrote nothing down -- and would leave the real case, a handset
// with no signal, exactly as stranded as before. With no dial, the only thing that can answer is
// the durable state.
func TestD0B8_AnOwnerSideRevokeSurvivesTheProcessDeathThatFollowsIt(t *testing.T) {
	s := d0b8NewStranded(t)

	app, log := s.open(t, true)
	log.await(t, "the stranded handset never reported \"revoked\", so nothing was observed to survive",
		func(states []string) bool { return len(states) >= 1 && states[len(states)-1] == "revoked" })
	if d0b8Paired(t, app) {
		t.Fatal("agents-tracker-d0b8: the running phone still reads as paired after the owner's " +
			"revoke, so the live reading regressed and this test is measuring the wrong half")
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Android SIGKILLs the app, and the handset comes back somewhere with no signal.
	restarted, _ := s.open(t, false)
	if d0b8Paired(t, restarted) {
		t.Error("agents-tracker-d0b8: the next launch comes up believing the phone is paired. The " +
			"owner revoked it from the machine, the phone SAW that on its last dial, and the verdict " +
			"went no further than process memory -- so a handset that cannot reach the relay is back " +
			"in the four-tab shell holding a registration the machine deleted, with no signal that " +
			"anything is wrong and no reason to go looking for Replace this computer")
	}
	// Non-vacuity: the durable blob was READ, not discarded, and the pairing coordinates are all
	// still there -- the phone must remain able to pair again, which is the whole point.
	sum, err := restarted.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the process death: %v", err)
	}
	if sum.Machine == "" {
		t.Error("agents-tracker-d0b8: the reopened phone holds no machine endpoint id, so either the " +
			"durable blob was discarded at load (S9) or the unpair cleared the coordinate the blob is " +
			"filtered on")
	}
}

// TestD0B8_PairingAgainClearsATransportSideUnpair is the other direction, and it is what keeps the
// durable write from being a brick of its own.
//
// A flag the transport can set and only the local Replace press can clear would strand the handset
// the other way round: it would complete this ceremony and still be shown the pairing screen. The
// clear is `mobile/pairing.go`'s `pin`, which runs on EVERY successful pairing and does not ask
// what caused the unpair -- and this proves it over a real ceremony against a real relay, on a
// handset whose unpair was written by the transport rather than by a press.
func TestD0B8_PairingAgainClearsATransportSideUnpair(t *testing.T) {
	s := d0b8NewStranded(t)

	app, log := s.open(t, true)
	log.await(t, "the stranded handset never reported \"revoked\"",
		func(states []string) bool { return len(states) >= 1 && states[len(states)-1] == "revoked" })
	if d0b8Paired(t, app) {
		t.Fatal("the phone reads as paired before the ceremony, so this test cannot tell a recovery " +
			"from the state it started in")
	}

	// THE OWNER ACTS: a real pairing over the real rendezvous, under the machine identity that
	// placed the ban -- a fresh one would re-pin the handset onto a machine that has revoked
	// nobody and prove nothing about lifting this ban.
	mSignPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine sign key: %v", err)
	}
	mp := s16MachinePairingAs(t, s.relayURL, mSignPub, s.machinePub, testEpochID, true)
	p := s16BeginConfirmed(t, app, mp.QR)
	s16AwaitSAS(t, p)
	if err := p.Confirm(); err != nil {
		t.Fatalf("Pairing.Confirm: %v", err)
	}
	// A SECOND consent over the fresh ceremony (ADR-007 B47): the credential the revoke retired
	// authorizes nothing ever again, so replaying the pre-revoke bytes would be that attack rather
	// than this recovery.
	if err := s.machine.AuthorizeDevice(s.ctx, s.phonePub,
		consentFrom(t, s.provision.KeyStore(), relay.RoutingID(s.machinePub))); err != nil {
		t.Fatalf("machine authorize after the pairing: %v", err)
	}
	log.await(t, "the re-paired phone never came back online",
		func(states []string) bool { return len(states) > 0 && states[len(states)-1] == "online" })

	if !d0b8Paired(t, app) {
		t.Error("agents-tracker-d0b8: the phone pairs successfully and still reads as unpaired, so " +
			"the presentation gate keeps it on the pairing screen it has just come from. An unpair " +
			"the owner cannot clear by pairing is a worse brick than the one it was fixing")
	}
	// AND IT MUST STAY CLEARED ACROSS THE KILL. A clear that lived only in memory would put the
	// handset back on the pairing screen at the next launch, which is the same brick one process
	// later.
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	restarted, _ := s.open(t, false)
	if !d0b8Paired(t, restarted) {
		t.Error("agents-tracker-d0b8: the re-paired phone comes back UNPAIRED after a process death. " +
			"The pairing cleared the unpair in memory and not on disk, so the recovery lasts exactly " +
			"as long as the process that performed it")
	}
}
