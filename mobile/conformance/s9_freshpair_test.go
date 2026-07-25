package conformance_test

// Slice S9 -- PB-NET-1, the SECOND consequence of the fresh-install machine-id defect, at the
// facade.
//
// TestPBNET1_TheFacadeDrivesTheRealClientFromPairingThroughAppend catches the first one at
// runtime: an empty State.Machine makes crypto.Command.Canonical refuse every mutating verb,
// so the phone visibly cannot do anything. This is the one that is SILENT. The empty value is
// PERSISTED, and OpenStore's load-time filter compares it against the configured machine id on
// the next process start and discards the entire durable blob -- pairing, epoch, sealed content
// key, relay cursor and send-seq ceilings. On Android a process death is routine, so the first
// one after a successful pairing takes the pairing with it, and nothing anywhere says so.
//
// It is fenced separately rather than folded into the test above because the two fail in
// different places for different reasons, and a fix that only silenced Canonical() -- say, by
// stamping the id at the command author -- would leave this one shipping.
//
// NOTHING HERE IS SEEDED. newHarness is deliberately not used: it writes State.Machine
// directly (harness_test.go seedState), which is the fixture family that hid this defect for
// the whole of S8, and a fence built on it would pass with the defect intact. The state
// directory is empty until a real pairing handshake writes to it.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	swarmmobile "github.com/Nathandela/swarm/mobile"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// s9RestartSession is the session the destination probe names. It is never opened on either
// side: what the probe reads is whether the phone can RESOLVE a mailbox to append to, which
// fails before the wire is touched.
const s9RestartSession = testMachineID + "/sess-s9-restart"

// noDestination is the substring of swarmmobile's errNoDestination, which is unexported. It is
// the phone saying it has no pinned MachineRelayAuthPub -- the exact loss this test fences.
const noDestination = "no machine relay destination in durable state"

// createdRendezvous gates the device on the machine having CREATED the rendezvous.
//
// relay.handleRendezvousClaim refuses an id the server has never seen (codeRendezvousTTL) and
// pairing.RunDevice does not retry its Claim, so a BeginPairing that beats the machine's Create
// fails the handshake terminally. The machine side necessarily runs on its own goroutine --
// Pair blocks until the device has spoken -- so without this gate the ordering is the
// scheduler's to decide, and it decides differently under load. (Measured: with the gate
// removed this test failed 2 runs in 5 while other agents were building, always at the SAS.)
type createdRendezvous struct {
	pairing.RendezvousTransport
	once    sync.Once
	created chan struct{}
}

func (c *createdRendezvous) Create(ctx context.Context, id string) error {
	err := c.RendezvousTransport.Create(ctx, id)
	c.once.Do(func() { close(c.created) })
	return err
}

func TestPBNET1_AFreshInstallsPairingSurvivesTheNextProcessStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// The state directory is the install. It outlives every App opened over it, which is what
	// makes the assertion after the restart a statement about what reached DISK.
	dir := t.TempDir()
	// ONE custody across both launches, deliberately. The Android Keystore survives a
	// process death; a fresh KEK per launch would model a factory reset, and the assertion
	// after the restart would then be about a directory the second App cannot open rather
	// than about what reached disk.
	custody := newTestCustody(t)
	open := func() *swarmmobile.App {
		t.Helper()
		app, err := swarmmobile.NewApp(&swarmmobile.Config{
			StateDir:  dir,
			RelayURL:  srv.URL(),
			MachineID: testMachineID,
		}, custody)
		if err != nil {
			t.Fatalf("swarmmobile.NewApp: %v", err)
		}
		if err := app.Start(); err != nil {
			t.Fatalf("App.Start: %v", err)
		}
		return app
	}

	app := open()
	t.Cleanup(func() { _ = app.Close() })

	// NON-VACUITY for the restore assertion at the end. Restored must be FALSE here, and it is
	// the reason this probe is not decoration: the naive repair for this defect -- stamping the
	// configured machine id onto the durable state at load time and leaving Restored derived
	// from it -- makes a phone that has never been paired report that it resumed a pairing. The
	// assertion at the bottom would then hold with the pairing deleted.
	sum, err := app.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary on a fresh install: %v", err)
	}
	if sum.Restored {
		t.Errorf("a phone whose state directory is EMPTY reports Restored=true. Restored is what "+
			"the debug screen and PB-BIND-3 read to tell a resumed install from a first launch, so "+
			"one that is true before any pairing has run cannot distinguish them at all "+
			"(machine=%q epoch=%d)", sum.Machine, sum.EpochID)
	}
	if sum.EpochID != 0 {
		t.Fatalf("a fresh install came up holding epoch %d; nothing has paired it", sum.EpochID)
	}
	// The other half of the non-vacuity, and the probe the restart assertion is measured
	// against. A phone with no pinned MachineRelayAuthPub cannot resolve a destination at all,
	// and every send verb says exactly that before it touches the wire. It must be true here
	// and false after the restart, or the restart proved nothing about what reached disk.
	if err := app.TerminalWatch(s9RestartSession); err == nil || !strings.Contains(err.Error(), noDestination) {
		t.Fatalf("an unpaired phone's TerminalWatch = %v, want the no-destination refusal. The "+
			"assertion after the restart reads that same refusal, so a build that never emits it "+
			"makes the restart check vacuous", err)
	}

	// ---- a real pairing handshake, over the real relay rendezvous ------------------
	mAuthPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	mSignPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine grant-signing key: %v", err)
	}
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}

	var secret [32]byte
	var rid [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(rid[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}

	var (
		mu         sync.Mutex
		machineSAS [6]string
		sasSeen    = make(chan struct{})
		pairDone   = make(chan struct{})
	)
	m := pairing.NewMachine(pairing.MachineParams{
		Static:       machineID.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		LocalConsole: true,
		Confirm: func(_ context.Context, got [6]string, _ string) (bool, error) {
			mu.Lock()
			machineSAS = got
			mu.Unlock()
			close(sasSeen)
			return true, nil
		},
		Payload: pairing.MachinePayload{
			Hostname:            "s9-restart.local",
			MachineRoutingID:    []byte(relay.RoutingID(mAuthPub)),
			MachineRelayAuthPub: mAuthPub,
			RecipientPub:        machineID.RecipientPublic(),
			MachineSignPub:      mSignPub,
			EpochID:             testEpochID,
		},
	})

	rzConn, err := relay.DialRaw(ctx, srv.URL())
	if err != nil {
		t.Fatalf("machine DialRaw for the rendezvous: %v", err)
	}
	t.Cleanup(func() { _ = rzConn.Close() })
	rz := &createdRendezvous{
		RendezvousTransport: &relayRendezvous{conn: rzConn, label: hex.EncodeToString(rid[:])},
		created:             make(chan struct{}),
	}
	go func() {
		defer close(pairDone)
		_, _ = m.Pair(ctx, rz)
	}()
	// The device may only join a rendezvous that EXISTS -- see createdRendezvous. Waiting here
	// is what makes this test's outcome a statement about the phone rather than about which
	// goroutine the scheduler ran first.
	select {
	case <-rz.created:
	case <-time.After(10 * time.Second):
		t.Fatalf("the machine never created the pairing rendezvous")
	}

	qr, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL:      srv.URL(),
		RendezvousID:  rid,
		PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	p, err := app.BeginPairing(qr)
	if err != nil {
		t.Fatalf("App.BeginPairing: %v", err)
	}
	// A handshake that has FAILED is reported as such rather than waited out: p.SAS() errors on
	// a terminal state, so polling it alone turns any pairing failure into "no SAS", five
	// seconds later, with the actual cause discarded.
	var phoneSAS string
	sasDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(sasDeadline) && phoneSAS == "" {
		s, serr := p.SAS()
		switch st, sterr := p.State(); {
		case serr == nil && s != "":
			phoneSAS = s
		case sterr != nil || (st != "pairing" && st != "confirming"):
			t.Fatalf("the pairing reached terminal state %q (%v) without deriving a SAS: %v", st, sterr, serr)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	if phoneSAS == "" {
		t.Fatalf("the phone never derived a SAS within 5s")
	}
	select {
	case <-sasSeen:
	case <-time.After(10 * time.Second):
		t.Fatalf("the machine never reached its SAS confirm gate")
	}
	mu.Lock()
	wantSAS := strings.Join(machineSAS[:], " ")
	mu.Unlock()
	if phoneSAS != wantSAS {
		t.Fatalf("the two ends derived different SAS strings (phone %q, machine %q)", phoneSAS, wantSAS)
	}
	if err := p.Confirm(); err != nil {
		t.Fatalf("Pairing.Confirm: %v", err)
	}
	eventually(t, "the pairing never reached its terminal paired state", func() bool {
		st, err := p.State()
		return err == nil && st == "paired"
	})
	select {
	case <-pairDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("the machine side of the pairing never returned")
	}

	// ---- PROCESS DEATH -----------------------------------------------------------
	//
	// Android SIGKILLs the app. Close is the closest this test can come, and it is enough:
	// pin() persisted through phonecore.Save before returning, so everything the pairing
	// learned is already at rest. What the next launch does with it is the whole question.
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}

	// ---- the next launch ---------------------------------------------------------
	//
	// Everything below reads the SECOND App, over the same directory. The durable blob is
	// deliberately not opened directly: it is sealed under the two tier KEKs now, so reading
	// it here would mean re-deriving the facade's own AEAD construction in a test, and a
	// second copy of that construction is a second thing to get wrong. The blob's contents --
	// content key,
	// send-seq ceilings, relay cursor -- are asserted where they belong, in
	// internal/phonecore's TestS9_AFreshInstallsFirstSaveSurvivesTheNextProcessStart.
	restarted := open()
	t.Cleanup(func() { _ = restarted.Close() })
	sum, err = restarted.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the restart: %v", err)
	}
	if sum.EpochID != int64(testEpochID) {
		t.Errorf("the durable blob a successful pairing wrote does not survive the next process "+
			"start: the phone comes back holding epoch %d, want %d. The pairing, the machine's "+
			"relay-auth pub, the sealed content key, the relay cursor and the send-seq ceilings go "+
			"with it -- the phone is unpaired, silently, on the first process death Android hands it.",
			sum.EpochID, testEpochID)
	}
	if sum.Machine != testMachineID {
		t.Errorf("after the restart the phone's machine endpoint id is %q, want %q", sum.Machine, testMachineID)
	}
	// The DESTINATION survived, read through the same refusal the fresh install emitted above.
	// This is the coordinate whose loss is silent -- phonecore.State's own doc: a phone without
	// it holds a valid content key, a valid send-seq and no destination, and nothing fails
	// loudly. Here it is made to fail loudly.
	if err := restarted.TerminalWatch(s9RestartSession); err != nil && strings.Contains(err.Error(), noDestination) {
		t.Errorf("after the restart the phone cannot resolve a destination: %v. The machine's "+
			"relay-auth pub is the only coordinate that says how to REACH the machine, so the phone "+
			"has come back knowing who the machine is and not where it is -- one re-pair per process "+
			"death, on a platform that kills processes routinely", err)
	}
	if !sum.Restored {
		t.Errorf("after the restart StateSummary reports Restored=false over a state directory a " +
			"real pairing wrote; a phone that silently starts from zero is requirements 4.3, restored")
	}
}
