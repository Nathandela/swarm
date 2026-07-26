package conformance_test

// Slice S10 -- PB-KEY-10: THE PHONE CAN NEVER OBTAIN AN EPOCH KEY, so every other
// requirement about sending, typing or opening a frame is unreachable in production.
//
// NOTHING HERE IS SEEDED, AND NOTHING HERE CALLS Install*Key. newHarness is deliberately
// not used: harness_test.go's seedState writes crypto.EpochKeys straight into the durable
// blob and says out loud that it seeds durable state "rather than running a pairing
// handshake" -- that is the fixture family that hides this defect, and a fence built on it
// passes with the defect intact. Even the PB-NET-1 real-wire test generates the epoch keys
// in-test and hands the content key to InstallContentKey: the no-fakes test performs BY
// HAND the exact step the facade is missing.
//
// So this file runs the whole first-run path with real parts:
//
//	real relay  ->  real Noise XXpsk0 pairing  ->  real enroll.Enroll  ->  the gateway's
//	own bootstrap delivery (grant.MarshalBootstrap + MailboxAppend, byte-for-byte what
//	cmd/swarm-remote/deliver.go does)  ->  the facade's own drain
//
// and then asks the only two questions that matter: can the phone SEND a signed command
// the machine opens, and can it OPEN an inbound frame the machine sealed. Both under the
// epoch key, neither with any test-supplied key.
//
// WHY IT FAILS TODAY. The bootstrap frame is TAGGED PLAINTEXT, not a ContentKey-sealed
// envelope -- deliberately, because it is what DELIVERS the ContentKey -- so
// MailboxRouter.AcceptCommit's ParseEnvelope refuses it, commits nothing and acks nothing.
// The relay read cursor never advances past it, so mobile.App.drain re-reads the same page
// forever, and State.Keys stays zero: resolveSend returns errNoContentKey for take_control,
// kill, launch, input, paste and resize alike.

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

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/enroll"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// s10BootstrapEpoch is the epoch the machine pairs the phone into. It is deliberately NOT
// testEpochID: nothing about this test may be satisfiable by a value some other fixture
// already wrote.
const s10BootstrapEpoch = uint32(11)

// s10MachineEndpointID is the endpoint id this machine publishes in its pairing payload
// (S19). ONE machine has ONE name: in production it is skeleton.endpointID(stateDir), and the
// pairing payload, the gateway's reconcile record (RelaySink.Machine) and the id the daemon
// verifies signatures against are all that same value. So this fixture must not invent a
// second one -- a machine whose handshake and whose reconcile record name different endpoints
// has the phone refuse every authority it publishes, and its blob discarded on the next load.
const s10MachineEndpointID = testMachineID

// s10Machine is the machine side of a first run, assembled from the real components: a
// pairing identity, a grant-signing key, an authenticated relay connection, and -- after
// the handshake -- the enrollment record and the sealed grant enroll.Enroll produced.
type s10Machine struct {
	t   *testing.T
	ctx context.Context

	relayURL string
	conn     *relay.Client

	signPub  ed25519.PublicKey
	signPriv ed25519.PrivateKey
	authPub  ed25519.PublicKey

	identity *crypto.Identity
	keys     crypto.EpochKeys

	// Filled in by Pair.
	Outcome *pairing.MachineOutcome
	Record  device.Record
	Grant   *crypto.EpochGrant
	sink    *remotegw.RelaySink

	mu     sync.Mutex
	recv   *crypto.MailboxReceiver
	cursor uint64
}

func newS10Machine(t *testing.T, ctx context.Context, relayURL string) *s10Machine {
	t.Helper()
	m := &s10Machine{t: t, ctx: ctx, relayURL: relayURL, recv: crypto.NewMailboxReceiver()}

	var err error
	if m.signPub, m.signPriv, err = ed25519.GenerateKey(rand.Reader); err != nil {
		t.Fatalf("machine grant-signing key: %v", err)
	}
	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	m.authPub = authPub
	if m.identity, err = crypto.GenerateIdentity(); err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	if m.keys, err = crypto.NewEpochKeys(); err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	if m.conn, err = relay.Dial(ctx, relayURL, relay.ClientAuth{
		RelayAuthPub: authPub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(authPriv, c), nil },
	}); err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = m.conn.Close() })
	return m
}

// pairWith drives the machine side of the handshake against the phone's BeginPairing, and
// returns the QR the phone must scan. The caller runs the device half.
func (m *s10Machine) pairWith(secret [32]byte, rid [16]byte) (qr string, sasSeen chan struct{}, machineSAS *[6]string, done chan struct{}) {
	m.t.Helper()

	sasSeen = make(chan struct{})
	done = make(chan struct{})
	machineSAS = new([6]string)
	var mu sync.Mutex

	p := pairing.NewMachine(pairing.MachineParams{
		Static:       m.identity.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		LocalConsole: true,
		Confirm: func(_ context.Context, got [6]string, _ string) (bool, error) {
			mu.Lock()
			*machineSAS = got
			mu.Unlock()
			close(sasSeen)
			return true, nil
		},
		Payload: pairing.MachinePayload{
			Hostname:            "s10-bootstrap.local",
			MachineRoutingID:    []byte(relay.RoutingID(m.authPub)),
			MachineRelayAuthPub: m.authPub,
			RecipientPub:        m.identity.RecipientPublic(),
			MachineSignPub:      m.signPub,
			MachineEndpointID:   s10MachineEndpointID,
			EpochID:             s10BootstrapEpoch,
		},
	})

	rzConn, err := relay.DialRaw(m.ctx, m.relayURL)
	if err != nil {
		m.t.Fatalf("machine DialRaw for the rendezvous: %v", err)
	}
	m.t.Cleanup(func() { _ = rzConn.Close() })
	rz := &createdRendezvous{
		RendezvousTransport: &relayRendezvous{conn: rzConn, label: hex.EncodeToString(rid[:])},
		created:             make(chan struct{}),
	}
	go func() {
		defer close(done)
		out, perr := p.Pair(m.ctx, rz)
		if perr != nil {
			return
		}
		// Under m.mu: this goroutine outlives pairWith, and enrollAndDeliver reads Outcome
		// from the TEST goroutine. Sequencing them by wall-clock order is not a
		// happens-before, and -race reports it as the data race it is.
		m.mu.Lock()
		m.Outcome = out
		m.mu.Unlock()
	}()
	select {
	case <-rz.created:
	case <-time.After(10 * time.Second):
		m.t.Fatalf("the machine never created the pairing rendezvous")
	}

	qr, err = pairing.EncodeQR(pairing.QRPayload{
		RelayURL: m.relayURL, RendezvousID: rid, PairingSecret: secret,
	})
	if err != nil {
		m.t.Fatalf("EncodeQR: %v", err)
	}
	return qr, sasSeen, machineSAS, done
}

// enrollAndDeliver is the machine's post-handshake half, and it is the PRODUCTION shape:
// enroll.Enroll mints the record and the sealed grant, then the gateway authorizes the
// device's relay route and appends the tagged bootstrap frame to its mailbox -- exactly
// cmd/swarm-remote/deliver.go's deliverEpochGrant, which is the only thing that ever puts
// an epoch key where a real phone can reach it.
func (m *s10Machine) enrollAndDeliver() {
	m.t.Helper()
	m.mu.Lock()
	outcome := m.Outcome
	m.mu.Unlock()
	if outcome == nil {
		m.t.Fatalf("the machine side of the pairing produced no outcome; nothing can be enrolled")
	}
	res, err := enroll.Enroll(outcome, device.CapFull, m.signPriv,
		s10BootstrapEpoch, 1, m.keys, time.Now())
	if err != nil {
		m.t.Fatalf("enroll.Enroll: %v", err)
	}
	m.Record, m.Grant = res.Record, res.Grant

	if err := m.conn.AuthorizeDevice(m.ctx, ed25519.PublicKey(m.Record.RelayAuthPub), m.Record.ConsentSig); err != nil {
		m.t.Fatalf("machine authorize device: %v", err)
	}
	frame, err := grant.MarshalBootstrap(m.Grant)
	if err != nil {
		m.t.Fatalf("grant.MarshalBootstrap: %v", err)
	}
	if _, err := m.conn.MailboxAppend(m.ctx, m.phoneTarget(), frame); err != nil {
		m.t.Fatalf("append the bootstrap grant: %v", err)
	}

	m.sink = remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender:       m.conn,
		Target:         m.phoneTarget(),
		Machine:        testMachineID,
		EpochID:        s10BootstrapEpoch,
		Key:            m.keys.ContentKey,
		RecipientKeyID: crypto.KeyID(m.Record.RecipientPub),
		SenderKeyID:    crypto.KeyID(m.identity.RecipientPublic()),
		Authorities:    s10Authorities{},
	})
}

func (m *s10Machine) phoneTarget() string {
	return relay.RoutingID(ed25519.PublicKey(m.Record.RelayAuthPub))
}

// drainPhoneCommands opens every frame the phone has appended to the machine's mailbox,
// through the REAL gateway opener and the REAL shared inbound seq guard.
func (m *s10Machine) drainPhoneCommands() []schema.RemoteCommand {
	m.t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := m.conn.MailboxRead(m.ctx, m.cursor)
	if err != nil {
		m.t.Fatalf("machine mailbox read: %v", err)
	}
	var out []schema.RemoteCommand
	for _, it := range items {
		if it.Cursor > m.cursor {
			m.cursor = it.Cursor
		}
		fr, ferr := remotegw.OpenMailboxFrame(m.recv, m.keys.ContentKey, it.Envelope)
		if ferr != nil {
			// LOGGED, not silent. The cursor has already advanced, so a frame that will not
			// open is lost for good and every later poll returns nothing -- which reads to
			// the caller as "the phone stopped sending", the wrong diagnosis with no way to
			// tell it from the right one. A bare `continue` here is what left the
			// `different_machine` failure unattributable between "never appended",
			// "appended elsewhere" and "appended but unopenable". The cursor still
			// advances: not advancing would spin forever on a genuinely bad frame.
			m.t.Logf("drainPhoneCommands: mailbox item at cursor %d would not open: %v", it.Cursor, ferr)
			continue
		}
		if fr.Kind == remotegw.FrameInput {
			continue
		}
		out = append(out, fr.Command)
	}
	return out
}

// s10Authorities is a machine whose durable coordinates are known and conservative, so no
// authority the reconcile record carries can rewind anything.
type s10Authorities struct{}

func (s10Authorities) InboundHighWater() (uint64, error) { return 0, nil }
func (s10Authorities) ReplyCeiling() (uint64, error)     { return 0, nil }
func (s10Authorities) GrantWatermark() (uint32, uint64, error) {
	return s10BootstrapEpoch, 1, nil
}

// s10FreshInstall stands up a relay and an EMPTY state directory, opens the facade over it
// and returns both. The directory outlives the App, so an assertion after a restart is a
// statement about what reached disk.
func s10FreshInstall(t *testing.T) (ctx context.Context, relayURL, dir string, open func() *swarmmobile.App) {
	t.Helper()
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

	dir = t.TempDir()
	relayURL = srv.URL()
	// ONE custody for every reopen of this state dir (S14, PB-KEY-9): the KEKs are the
	// Android Keystore stand-in, so a fresh one per open would be a phone whose hardware
	// keys changed between process starts -- the sealed state would not unseal and the
	// restart assertions below would fail for a reason that has nothing to do with grants.
	custody := newTestCustody(t)
	open = func() *swarmmobile.App {
		t.Helper()
		app, aerr := swarmmobile.NewApp(&swarmmobile.Config{
			StateDir: dir, RelayURL: relayURL, MachineID: testMachineID,
		}, custody)
		if aerr != nil {
			t.Fatalf("swarmmobile.NewApp: %v", aerr)
		}
		t.Cleanup(func() { _ = app.Close() })
		if serr := app.Start(); serr != nil {
			t.Fatalf("App.Start: %v", serr)
		}
		return app
	}
	return ctx, relayURL, dir, open
}

// runPairing drives the phone half of the handshake to its terminal paired state.
func runPairing(t *testing.T, app *swarmmobile.App, m *s10Machine) {
	t.Helper()

	var secret [32]byte
	var rid [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(rid[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	qr, sasSeen, machineSAS, done := m.pairWith(secret, rid)

	p, err := app.BeginPairing(qr)
	if err != nil {
		t.Fatalf("App.BeginPairing: %v", err)
	}
	// PB-PAIR-6 (S16): BeginPairing joins nothing now. The destination is displayed and
	// confirmed first, and ConfirmOrigin is what dials.
	s16PassOriginGate(t, p)
	var phoneSAS string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && phoneSAS == "" {
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
	if want := strings.Join(machineSAS[:], " "); phoneSAS != want {
		t.Fatalf("the two ends derived different SAS strings (phone %q, machine %q)", phoneSAS, want)
	}
	if err := p.Confirm(); err != nil {
		t.Fatalf("Pairing.Confirm: %v", err)
	}
	eventually(t, "the pairing never reached its terminal paired state", func() bool {
		st, serr := p.State()
		return serr == nil && st == "paired"
	})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the machine side of the pairing never returned")
	}
}

// ---------------------------------------------------------------------------

// TestPBKEY10_AFreshPairingObtainsTheEpochKeyWithoutAnyInstallCall is the headline. The
// assertions are the product's two verbs: send, and receive.
func TestPBKEY10_AFreshPairingObtainsTheEpochKeyWithoutAnyInstallCall(t *testing.T) {
	ctx, relayURL, _, open := s10FreshInstall(t)
	app := open()
	m := newS10Machine(t, ctx, relayURL)

	// NON-VACUITY. Before the grant the phone must be unable to send: if it could, the
	// assertions below would hold for reasons that have nothing to do with the bootstrap.
	if _, err := app.Kill(testMachineID + "/sess-s10"); err == nil {
		t.Fatalf("an unpaired phone's Kill succeeded; every assertion below is then vacuous")
	}

	runPairing(t, app, m)
	m.enrollAndDeliver()

	// (1) RECEIVE. The gateway seals a journal roster + the reconcile record under the epoch
	// content key. The phone can open them only if the grant delivered that key.
	if err := m.sink.Snapshot([]schema.JournalRecord{
		{SessionID: testMachineID + "/sess-s10", Type: "roster"},
	}, 0); err != nil {
		t.Fatalf("sink.Snapshot: %v", err)
	}
	eventually(t, "the phone never opened a single machine-sealed frame after pairing. The "+
		"epoch key never arrived: State.Keys is written only by InstallWakeKey/InstallContentKey, "+
		"which are inbound verbs from Kotlin, and nothing supplies those bytes. The bootstrap "+
		"grant the machine appended is not an envelope, so AcceptCommit refuses it, the relay "+
		"cursor never advances past it, and the drain re-reads the same page forever",
		func() bool {
			list, lerr := app.Roster()
			if lerr != nil {
				return false
			}
			n, _ := list.Count()
			return n > 0
		})

	// (2) SEND. A signed command, opened by the REAL gateway opener under the REAL shared
	// inbound seq guard. Nothing in this test ever handed the phone a key.
	var op *swarmmobile.Op
	eventually(t, "the phone could never author a signed command; it holds no content key",
		func() bool {
			var kerr error
			op, kerr = app.Kill(testMachineID + "/sess-s10")
			return kerr == nil
		})
	eventually(t, "the signed command the phone sealed never reached the machine", func() bool {
		for _, c := range m.drainPhoneCommands() {
			if c.Action == schema.ActionKill && c.OperationID == op.OperationID {
				return true
			}
		}
		return false
	})
}

// TestPBKEY10_TheBootstrapFrameIsCompactedRatherThanRePolledForever is the delivery half,
// fenced separately because it fails for a different reason than the key never arriving:
// AcceptCommit acks only on crypto.ErrStaleSeq or crypto.ErrStaleAge, and an unopened
// bootstrap frame is neither -- so it is never compacted from the relay mailbox, and the
// phone's drain is pinned to the page containing it for the whole 7-day retention window.
func TestPBKEY10_TheBootstrapFrameIsCompactedRatherThanRePolledForever(t *testing.T) {
	ctx, relayURL, _, open := s10FreshInstall(t)
	app := open()
	m := newS10Machine(t, ctx, relayURL)
	runPairing(t, app, m)
	m.enrollAndDeliver()

	eventually(t, "the relay read cursor never advanced past the bootstrap grant frame. Every "+
		"later frame -- journal, terminal, reply, reconcile -- sits behind it unreachable, and "+
		"mobile.App.drain only polls immediately when the cursor MOVED, so the phone re-reads "+
		"one page at the idle cadence and makes no progress ever",
		func() bool {
			sum, err := app.StateSummary()
			return err == nil && sum.RelayCursor > 0
		})
}

// TestPBKEY10_TheGrantSurvivesTheFirstProcessDeath. Android SIGKILLs the app as routine
// behaviour, and the bootstrap frame is delivered ONCE per gateway session -- so a key held
// only in memory is a key the phone loses before it has done anything with it, with the
// relay mailbox already compacted behind it.
func TestPBKEY10_TheGrantSurvivesTheFirstProcessDeath(t *testing.T) {
	ctx, relayURL, _, open := s10FreshInstall(t)
	app := open()
	m := newS10Machine(t, ctx, relayURL)
	runPairing(t, app, m)
	m.enrollAndDeliver()

	if err := m.sink.Reconcile(); err != nil {
		t.Fatalf("sink.Reconcile: %v", err)
	}
	eventually(t, "the phone never adopted the epoch key before the restart", func() bool {
		sum, err := app.StateSummary()
		return err == nil && sum.EpochID == int64(s10BootstrapEpoch) && sum.RelayCursor > 0
	})

	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}
	restarted := open()

	// The proof is behavioural, not a field read: the restarted phone can still SEAL. A
	// content key that did not reach disk makes resolveSend return errNoContentKey, and the
	// bootstrap frame that carried it is gone from the mailbox.
	eventually(t, "after the first process death the phone can no longer author a command: the "+
		"epoch key the bootstrap grant delivered did not reach disk, and the frame that carried "+
		"it was delivered once",
		func() bool {
			_, err := restarted.Kill(testMachineID + "/sess-s10")
			return err == nil
		})
}
