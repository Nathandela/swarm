// PB-PAIR-4, THE QUANTIFIER: "process death AT ANY TRANSITION ... resumes or fails closed --
// never a half-paired device", evidenced by "a kill/restart test AT EACH transition".
//
// The row names its transitions in a parenthetical, and the fence for it killed the phone at
// ONE of them (the SAS step, s16_pairing_test.go). A requirement quantified over a set is not
// met by a witness. This file enumerates the set, drives a kill at each member, and
// cross-checks the enumeration against the requirement's own text so a transition added to the
// row later fails HERE rather than slipping past.
//
// WHAT "PROCESS DEATH" IS, AND WHY App.Close() IS NOT IT. The existing fence says "Close is the
// closest a test can come to SIGKILL, and it is enough: everything that reached disk before it
// is what the next launch has." That is true at the SAS step and FALSE at the one transition
// that can actually half-pair. App.Close cancels the handshake and then WAITS for it
// (mobile/app.go: `a.pairingWG.Wait()`), so finish() runs to completion -- a Close during the
// durable pin lets the pin finish. Android's SIGKILL does not. And Close routes through
// Pairing.abandon, which WRITES the attempt record; a signal writes nothing, so a Close-based
// fence cannot tell a record that survived from one that abandon() supplied on the way out.
//
// So the phone here is a REAL second process, killed by a REAL SIGKILL, using the re-exec
// pattern this repository already uses for crash injection (internal/phonecore/
// processdeath_test.go). No Close is ever called on the victim.
//
// HOW A KILL IS AIMED. The phone is not instrumented -- it is the shipped facade. What is
// instrumented is the MACHINE, which holds the ceremony at a chosen frame so the phone is
// parked at exactly one transition when the signal arrives. That keeps the aiming in the
// FIXTURE and out of the product, and it makes each kill point deterministic rather than a
// race the parent has to win.
//
// THE PROPERTY, IN BOTH ORIENTATIONS. Each row declares what each leg must durably hold, so a
// row asserts a specific state rather than a disjunction anything satisfies:
//
//	FORBIDDEN, always: the machine holds a device while the phone holds no pin. That is the
//	orientation that spends the single-device slot, leaves remote control live against a
//	handset holding nothing, and is recovered only from the desktop.
//
//	PERMITTED, and asserted where it is expected: the phone holds a pin while the machine
//	holds nothing. Recovery is a re-pair, and the fence checks that the restarted phone is
//	COHERENT -- a pin is all-or-nothing -- rather than merely non-empty.
package conformance_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/enroll"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// pbPair4Authorities is the machine's durable coordinates for the reconcile record, at THIS
// fixture's epoch. Conservative zeros-plus-one, so no authority it publishes rewinds anything.
type pbPair4Authorities struct{}

func (pbPair4Authorities) InboundHighWater() (uint64, error) { return 0, nil }
func (pbPair4Authorities) ReplyCeiling() (uint64, error)     { return 0, nil }
func (pbPair4Authorities) GrantWatermark() (uint32, uint64, error) {
	return pbPair4Epoch, 1, nil
}

// ---- the enumeration --------------------------------------------------------------------

// pbPair4Hold names the point at which the MACHINE leg stops, which is what parks the phone at
// the transition being killed. The phone itself is uninstrumented.
type pbPair4Hold int

const (
	pbHoldNone           pbPair4Hold = iota
	pbHoldBeforeMsg2                 // msg1 is in; msg2 never leaves. The phone is parked awaiting it.
	pbHoldAfterMsg3                  // msg3 has arrived and is NOT processed: no machine SAS, no prompt.
	pbHoldAtConfirm                  // msg3 processed, both SAS derived, the desktop prompt is open.
	pbHoldBeforeDecision             // the consent is in; the accept/decline never leaves.
	pbHoldAfterAck                   // the acknowledgement has arrived and is NOT verified: no enrolment.
)

// pbPair4Transition is one row of PB-PAIR-4's parenthetical.
type pbPair4Transition struct {
	// name is the transition EXACTLY as the requirement row names it. The enumeration test
	// below compares these against the row itself.
	name string

	// unreachable, when non-empty, is why no kill can be AIMED at this transition. It is a
	// stated result, not a skip: a row that cannot be reached must say why rather than carry a
	// test that passes because there is nothing there.
	unreachable string

	// hold is where the machine stops so the phone is parked at this transition.
	hold pbPair4Hold
	// until is how far the phone helper drives before it parks: "join", "sas" or "confirm".
	until string
	// needsSASOnScreen makes the parent wait for the phone to have SURFACED the SAS, which is
	// what separates the SAS-display row from the msg3 row (the phone's park is the same; the
	// machine's state and the screen's are not).
	needsSASOnScreen bool
	// afterPairing drives the row AFTER the ceremony completes: the machine enrols and appends
	// the bootstrap grant, and the phone dies before it can ever drain one.
	afterPairing bool
	// releaseAfterKill lets the machine finish the frame it was holding ONCE THE PHONE IS DEAD,
	// so the row measures what the phone had durably written at the instant the frame left it.
	releaseAfterKill bool

	// machineBoundsItsOwnWait is true where the machine's held call sits on a context the
	// machine itself bounds (the acknowledgement's window), so its leg settles without the
	// test cancelling anything.
	machineBoundsItsOwnWait bool

	// wantPhonePinned / wantMachineHoldsDevice are what each leg must durably hold once the
	// dust settles. Declaring both is what keeps a row from passing on "nothing happened".
	wantPhonePinned        bool
	wantMachineHoldsDevice bool

	// wantAttemptRecorded requires the restarted phone to report an outstanding attempt, so the
	// next launch can say "the machine may already hold this device" instead of offering a
	// scanner that cannot work.
	wantAttemptRecorded bool
}

// pbPair4Transitions is the enumeration. It is compared against PB-PAIR-4's own row, so it
// cannot drift from the requirement in either direction.
var pbPair4Transitions = []pbPair4Transition{
	{
		name:                "Noise msg1",
		hold:                pbHoldBeforeMsg2,
		until:               "join",
		wantAttemptRecorded: true,
	},
	{
		name: "Noise msg2",
		unreachable: "Between msg2 arriving and msg3 leaving the phone performs pure computation -- " +
			"decode, VerifyMachine, encode, send -- with no durable write and no call it can be " +
			"parked at. There is no state to reach: a kill landing in that window leaves exactly " +
			"what the msg1 row leaves (nothing sent that the machine kept, nothing pinned) or " +
			"exactly what the msg3 row leaves. Both bounding states ARE driven, by their own rows. " +
			"Aiming at the gap between them would be a test whose kill point is a race, reported " +
			"as a transition.",
	},
	{
		name:                "Noise msg3",
		hold:                pbHoldAfterMsg3,
		until:               "join",
		wantAttemptRecorded: true,
	},
	{
		name:                "SAS display",
		hold:                pbHoldAtConfirm,
		until:               "sas",
		needsSASOnScreen:    true,
		wantAttemptRecorded: true,
	},
	{
		name:                "machine decision wait",
		hold:                pbHoldBeforeDecision,
		until:               "confirm",
		wantAttemptRecorded: true,
	},
	{
		// THE ONLY TRANSITION AT WHICH A KILL CAN PRODUCE A GENUINE DISAGREEMENT, and the row
		// that discriminates the ordering rather than merely surviving it.
		//
		// The gate takes the acknowledgement off the wire and withholds it, the phone is killed
		// at that instant, and the gate is THEN released so the machine processes it and
		// enrols. The kill therefore lands in the window between the acknowledgement leaving
		// the phone and the machine acting on it -- and what the phone durably holds at that
		// instant is decided entirely by which side of the frame its commit sits on.
		//
		// It is not a race. The commit precedes the send in PROGRAM ORDER, and the gate
		// observes the send's arrival, so a phone that commits first has provably already
		// committed when the signal is delivered. A phone that pins afterwards provably has
		// not -- and then the machine enrols a handset holding nothing, which is the forbidden
		// orientation reached with no relay misbehaviour and no attacker.
		name:                    "local pin commit",
		hold:                    pbHoldAfterAck,
		until:                   "confirm",
		releaseAfterKill:        true,
		machineBoundsItsOwnWait: true,
		wantPhonePinned:         true,
		wantMachineHoldsDevice:  true,
	},
	{
		// The ceremony is over and both legs agree. What dies is the delivery of the epoch key,
		// which the phone must be able to obtain again on the next launch.
		name:                   "grant bootstrap",
		hold:                   pbHoldNone,
		until:                  "confirm",
		afterPairing:           true,
		wantPhonePinned:        true,
		wantMachineHoldsDevice: true,
	},
}

// pbPair4RequirementRow locates PB-PAIR-4 in the requirements document.
var pbPair4RequirementRow = regexp.MustCompile(`(?m)^\| PB-PAIR-4 \|.*$`)

// pbPair4NamedTransitions extracts the transition names from PB-PAIR-4's own row: the contents
// of the first parenthetical, comma-separated, with a "a/b/c" group expanded onto its stem
// ("Noise msg1/2/3" is three transitions, and the row's own evidence column says "at each").
func pbPair4NamedTransitions(t *testing.T) []string {
	t.Helper()
	const path = "../../docs/specifications/remote-phaseB-requirements.md"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	row := pbPair4RequirementRow.Find(raw)
	if row == nil {
		t.Fatalf("PB-PAIR-4 has no row in %s; this enumeration has nothing to check itself "+
			"against and would silently become whatever it says it is", path)
	}
	open := strings.Index(string(row), "(")
	closeIdx := strings.Index(string(row), ")")
	if open < 0 || closeIdx < open {
		t.Fatalf("PB-PAIR-4's row names no parenthetical set of transitions:\n%s", row)
	}
	var names []string
	for _, part := range strings.Split(string(row)[open+1:closeIdx], ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		alts := strings.Split(part, "/")
		if len(alts) == 1 {
			names = append(names, part)
			continue
		}
		// "Noise msg1/2/3": the stem is the first alternative with its trailing digits removed.
		stem := strings.TrimRight(alts[0], "0123456789")
		names = append(names, alts[0])
		for _, a := range alts[1:] {
			names = append(names, stem+a)
		}
	}
	return names
}

// TestPBPAIR4_TheEnumerationCoversEveryTransitionTheRequirementNames is the fix-shape for a
// dropped quantifier: the table cannot fall behind the row, and the row cannot gain a
// transition the table ignores.
func TestPBPAIR4_TheEnumerationCoversEveryTransitionTheRequirementNames(t *testing.T) {
	want := pbPair4NamedTransitions(t)
	if len(want) == 0 {
		t.Fatal("PB-PAIR-4's row names no transitions, so this test asserts nothing")
	}
	got := make(map[string]bool, len(pbPair4Transitions))
	for _, tr := range pbPair4Transitions {
		if got[tr.name] {
			t.Errorf("the enumeration lists %q twice", tr.name)
		}
		got[tr.name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("PB-PAIR-4's row names the transition %q and the enumeration has no row for "+
				"it. A requirement quantified over a set is not met by a witness: add the row, or "+
				"record why no kill can be aimed at it", name)
		}
		delete(got, name)
	}
	for name := range got {
		t.Errorf("the enumeration carries a transition %q that PB-PAIR-4's row does not name; the "+
			"table and the requirement have drifted", name)
	}
}

// ---- the victim -------------------------------------------------------------------------

const (
	pbPair4HelperEnv = "SWARM_PBPAIR4_HELPER"
	pbPair4DirEnv    = "SWARM_PBPAIR4_DIR"
	pbPair4MarksEnv  = "SWARM_PBPAIR4_MARKS"
	pbPair4RelayEnv  = "SWARM_PBPAIR4_RELAY"
	pbPair4QREnv     = "SWARM_PBPAIR4_QR"
	pbPair4UntilEnv  = "SWARM_PBPAIR4_UNTIL"

	pbPair4Epoch = uint32(13)
)

// pbPair4Custody is the Keystore stand-in the victim and the parent SHARE. The KEKs are
// constants because they cross a process boundary: a random pair could not reach the re-exec'd
// child, and the restarted phone would read its own directory as another device's (PB-KEY-9).
// What is under test is durable-coordinate survival across a real SIGKILL, not the KEK's
// secrecy, so a fixed pair is the honest fixture and the material stays genuinely sealed.
type pbPair4Custody struct{}

func (pbPair4Custody) WakeKEK() ([]byte, error)    { return pbPair4KEK(0x5a), nil }
func (pbPair4Custody) ContentKEK() ([]byte, error) { return pbPair4KEK(0xa5), nil }

var _ swarmmobile.KeyCustody = pbPair4Custody{}

func pbPair4KEK(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

// pbPair4Mark records that the victim reached a step, so the parent can aim the signal at a
// state the machine cannot observe (the screen having the SAS on it, for one).
func pbPair4Mark(dir, name string) {
	_ = os.WriteFile(filepath.Join(dir, name), []byte("1"), 0o600)
}

// TestHelperPBPAIR4Phone is the victim process: a real facade over a real state directory,
// driving the shipped pairing verbs, which runs until the OS takes it away. It exits non-zero
// on any failure so a helper that died of its own accord is never mistaken for one that was
// killed.
func TestHelperPBPAIR4Phone(t *testing.T) {
	if os.Getenv(pbPair4HelperEnv) != "1" {
		t.Skip("PB-PAIR-4 process-death victim; runs only when re-exec'd")
	}
	marks := os.Getenv(pbPair4MarksEnv)

	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir:  os.Getenv(pbPair4DirEnv),
		RelayURL:  os.Getenv(pbPair4RelayEnv),
		MachineID: testMachineID,
	}, pbPair4Custody{})
	if err != nil {
		os.Exit(10)
	}
	// DELIBERATELY NOT Started. The transport loop is what drains the epoch grant, and the
	// grant-bootstrap row needs a phone that dies before it can ever drain one; every other row
	// is about the ceremony, which needs no transport loop at all.
	p, err := app.BeginPairing(os.Getenv(pbPair4QREnv))
	if err != nil {
		os.Exit(11)
	}
	origin, err := p.Origin()
	if err != nil {
		os.Exit(12)
	}
	if err := p.ConfirmOrigin(origin); err != nil {
		os.Exit(13)
	}
	pbPair4Mark(marks, "joined")

	until := os.Getenv(pbPair4UntilEnv)
	if until != "join" {
		for {
			if s, serr := p.SAS(); serr == nil && s != "" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		pbPair4Mark(marks, "sas")
	}
	if until == "confirm" {
		if err := p.Confirm(); err != nil {
			os.Exit(14)
		}
		pbPair4Mark(marks, "confirmed")
		for {
			if st, serr := p.State(); serr == nil && st == "paired" {
				pbPair4Mark(marks, "paired")
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// Park. Nothing below is reached: the parent's signal is the only exit.
	for {
		time.Sleep(time.Second)
	}
}

// ---- the instrumented machine -------------------------------------------------------------

// pbPair4Gate holds the machine's rendezvous at one frame so the phone is parked at exactly one
// transition. Every other frame is forwarded verbatim.
type pbPair4Gate struct {
	pairing.RendezvousTransport

	hold        pbPair4Hold
	reached     chan struct{}
	once        sync.Once
	release     chan struct{}
	releaseOnce sync.Once

	mu           sync.Mutex
	sends, recvs int
}

func (g *pbPair4Gate) arrive() { g.once.Do(func() { close(g.reached) }) }

// releaseNow lets the held frame through. It is idempotent because the cleanup releases every
// gate unconditionally and one row releases its own on purpose.
func (g *pbPair4Gate) releaseNow() { g.releaseOnce.Do(func() { close(g.release) }) }

// park blocks until the caller's context dies or the test releases the gate. Which of those
// happens is the row's business: the acknowledgement's context is bounded by the machine, every
// other one by the pairing.
func (g *pbPair4Gate) park(ctx context.Context) error {
	g.arrive()
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Send: machine frame 1 is msg2, frame 2 is the accept/decline decision.
func (g *pbPair4Gate) Send(ctx context.Context, msg []byte) error {
	g.mu.Lock()
	g.sends++
	n := g.sends
	g.mu.Unlock()
	if (g.hold == pbHoldBeforeMsg2 && n == 1) || (g.hold == pbHoldBeforeDecision && n == 2) {
		if err := g.park(ctx); err != nil {
			return err
		}
	}
	return g.RendezvousTransport.Send(ctx, msg)
}

// Recv: machine frame 1 is msg1, 2 is msg3, 3 is the consent, 4 is the acknowledgement.
//
// The held frames are READ FIRST and then withheld from the machine, which is the distinction
// the two rows rest on: the bytes are on the wire -- so the phone provably got that far -- and
// the machine has not processed them, so it has derived no SAS and enrolled nothing.
func (g *pbPair4Gate) Recv(ctx context.Context) ([]byte, error) {
	g.mu.Lock()
	g.recvs++
	n := g.recvs
	g.mu.Unlock()
	held := (g.hold == pbHoldAfterMsg3 && n == 2) || (g.hold == pbHoldAfterAck && n == 4)
	frame, err := g.RendezvousTransport.Recv(ctx)
	if err != nil || !held {
		return frame, err
	}
	if perr := g.park(ctx); perr != nil {
		return nil, perr
	}
	return frame, nil
}

// pbPair4Machine is the machine leg: the real responder, the real relay client, and -- for the
// grant-bootstrap row -- the real enrolment and bootstrap append.
type pbPair4Machine struct {
	t        *testing.T
	ctx      context.Context
	cancel   context.CancelFunc
	relayURL string
	conn     *relay.Client

	signPub  ed25519.PublicKey
	signPriv ed25519.PrivateKey
	authPub  ed25519.PublicKey
	identity *crypto.Identity
	keys     crypto.EpochKeys

	gate *pbPair4Gate
	done chan struct{}

	mu      sync.Mutex
	outcome *pairing.MachineOutcome
	perr    error

	record device.Record
	grant  *crypto.EpochGrant
}

// newPBPair4Machine stands the machine up, starts its half of the ceremony behind the gate, and
// returns the QR the victim must scan.
func newPBPair4Machine(t *testing.T, relayURL string, hold pbPair4Hold) (*pbPair4Machine, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m := &pbPair4Machine{t: t, ctx: ctx, cancel: cancel, relayURL: relayURL, done: make(chan struct{})}
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

	var secret [32]byte
	var rid [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(rid[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}

	m.gate = &pbPair4Gate{hold: hold, reached: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(m.gate.releaseNow)

	responder := pairing.NewMachine(pairing.MachineParams{
		Static:       m.identity.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		LocalConsole: true,
		Confirm: func(cctx context.Context, _ [6]string, _ string) (bool, error) {
			if hold == pbHoldAtConfirm {
				if err := m.gate.park(cctx); err != nil {
					return false, err
				}
			}
			return true, nil
		},
		Payload: pairing.MachinePayload{
			Hostname:            "pbpair4-transitions.local",
			OperatorNamespace:   "owner",
			MachineRoutingID:    []byte(relay.RoutingID(m.authPub)),
			MachineRelayAuthPub: m.authPub,
			RecipientPub:        m.identity.RecipientPublic(),
			MachineSignPub:      m.signPub,
			MachineEndpointID:   testMachineID,
			EpochID:             pbPair4Epoch,
		},
	})

	rzConn, err := relay.DialRaw(ctx, relayURL)
	if err != nil {
		t.Fatalf("machine DialRaw for the rendezvous: %v", err)
	}
	t.Cleanup(func() { _ = rzConn.CloseNow() })

	created := &createdRendezvous{
		RendezvousTransport: &relayRendezvous{conn: rzConn, label: hex.EncodeToString(rid[:])},
		created:             make(chan struct{}),
	}
	m.gate.RendezvousTransport = created
	go func() {
		defer close(m.done)
		out, perr := responder.Pair(ctx, m.gate)
		m.mu.Lock()
		m.outcome, m.perr = out, perr
		m.mu.Unlock()
	}()
	select {
	case <-created.created:
	case <-time.After(10 * time.Second):
		t.Fatal("the machine never created the pairing rendezvous")
	}

	qr, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL: relayURL, RendezvousID: rid, PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	return m, qr
}

// settle returns the machine's own answer to the ceremony. Where its held call sits on a
// context it does not bound, the pairing is cancelled first -- the phone is dead, so the wait
// can only end in the machine's own deadline, and waiting it out proves nothing the cancel does
// not.
func (m *pbPair4Machine) settle(own bool) (*pairing.MachineOutcome, error) {
	m.t.Helper()
	if own {
		select {
		case <-m.done:
		case <-time.After(15 * time.Second):
			m.t.Fatal("the machine leg never settled inside its own acknowledgement window")
		}
	} else {
		m.cancel()
		select {
		case <-m.done:
		case <-time.After(15 * time.Second):
			m.t.Fatal("the machine leg never returned after its pairing context was cancelled")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcome, m.perr
}

// enrollAndDeliver is the machine's post-handshake half, in its production shape: enroll.Enroll
// mints the record and the sealed grant, the gateway authorizes the device's relay route, and
// the tagged bootstrap frame goes onto its mailbox. It is the only thing that ever puts an
// epoch key where a real phone can reach it.
func (m *pbPair4Machine) enrollAndDeliver(out *pairing.MachineOutcome) {
	m.t.Helper()
	res, err := enroll.Enroll(out, device.CapFull, m.signPriv, pbPair4Epoch, 1, m.keys, time.Now())
	if err != nil {
		m.t.Fatalf("enroll.Enroll: %v", err)
	}
	m.record, m.grant = res.Record, res.Grant
	if err := m.conn.AuthorizeDevice(m.ctx, ed25519.PublicKey(m.record.RelayAuthPub), m.record.ConsentSig); err != nil {
		m.t.Fatalf("machine authorize device: %v", err)
	}
	frame, err := grant.MarshalBootstrap(m.grant)
	if err != nil {
		m.t.Fatalf("grant.MarshalBootstrap: %v", err)
	}
	target := relay.RoutingID(ed25519.PublicKey(m.record.RelayAuthPub))
	if _, err := m.conn.MailboxAppend(m.ctx, target, frame); err != nil {
		m.t.Fatalf("append the bootstrap grant: %v", err)
	}

	// AND THE RECONCILE RECORD, which is not decoration: the phone fail-closes every mutating
	// op until the machine publishes its rollback authorities (PB-SYNC-7), so a rig that
	// delivered only the key would prove the key had NOT arrived whether it had or not.
	sink := remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender:       m.conn,
		Target:         target,
		Machine:        testMachineID,
		EpochID:        pbPair4Epoch,
		Key:            m.keys.ContentKey,
		RecipientKeyID: crypto.KeyID(m.record.RecipientPub),
		SenderKeyID:    crypto.KeyID(m.identity.RecipientPublic()),
		Authorities:    pbPair4Authorities{},
	})
	if err := sink.Reconcile(); err != nil {
		m.t.Fatalf("sink.Reconcile: %v", err)
	}
}

// ---- the kill ------------------------------------------------------------------------------

// pbPair4IsSIGKILL reports whether err is the exit error of a SIGKILL'ed child. A clean exit or
// any other signal means the victim died of its own accord and the row proved nothing.
func pbPair4IsSIGKILL(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

// pbPair4Await polls cond until it holds or the deadline passes.
func pbPair4Await(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func pbPair4Marked(marks, name string) bool {
	_, err := os.Stat(filepath.Join(marks, name))
	return err == nil
}

// pbPair4DurableState reads the phone's durable state the way the next launch does, without an
// App in the way. A pin that is not here is a pin the phone does not have.
func pbPair4DurableState(t *testing.T, dir string) phonecore.State {
	t.Helper()
	reg, err := phonecore.OpenMachineRegistry(dir)
	if err != nil {
		t.Fatalf("open the phone's machine registry: %v", err)
	}
	namespace := reg.BootstrapDir()
	for _, entry := range reg.Entries() {
		if entry.ID == testMachineID {
			namespace = reg.MachineDir(entry.ID)
			break
		}
	}
	store, err := phonecore.OpenStore(filepath.Join(namespace, phonecore.StateFileName), testMachineID,
		custodySealer{key: pbPair4KEK(0x5a)}, custodySealer{key: pbPair4KEK(0xa5)})
	if err != nil {
		t.Fatalf("open the phone's durable state: %v", err)
	}
	return store.Load()
}

// ---- the fence -------------------------------------------------------------------------------

// TestPBPAIR4_ProcessDeathAtEveryNamedTransition is the requirement's evidence column: a
// kill/restart at each transition, with both legs' durable state asserted.
func TestPBPAIR4_ProcessDeathAtEveryNamedTransition(t *testing.T) {
	if os.Getenv(pbPair4HelperEnv) == "1" {
		t.Skip("running as the process-death victim")
	}
	for _, tr := range pbPair4Transitions {
		t.Run(strings.ReplaceAll(tr.name, " ", "_"), func(t *testing.T) {
			if tr.unreachable != "" {
				t.Log("NO KILL IS AIMED AT THIS TRANSITION, and that is the result rather than a " +
					"gap: " + tr.unreachable)
				return
			}
			pbPair4RunTransition(t, tr)
		})
	}
}

func pbPair4RunTransition(t *testing.T, tr pbPair4Transition) {
	t.Helper()
	_, relayURL := s16FreshRelay(t)
	machine, qr := newPBPair4Machine(t, relayURL, tr.hold)

	dir, marks := t.TempDir(), t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPBPAIR4Phone$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		pbPair4HelperEnv+"=1",
		pbPair4DirEnv+"="+dir,
		pbPair4MarksEnv+"="+marks,
		pbPair4RelayEnv+"="+relayURL,
		pbPair4QREnv+"="+qr,
		pbPair4UntilEnv+"="+tr.until,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the victim: %v", err)
	}

	// THE WINDOW. Each row parks the phone at one transition and waits for it to be there
	// before the signal, so the kill is aimed rather than raced.
	ready := func() bool {
		if tr.needsSASOnScreen && !pbPair4Marked(marks, "sas") {
			return false
		}
		if tr.afterPairing {
			return pbPair4Marked(marks, "paired")
		}
		select {
		case <-machine.gate.reached:
			return true
		default:
			return false
		}
	}
	opened := pbPair4Await(ready, 45*time.Second)

	// The grant-bootstrap row is the one driven past the ceremony: the machine enrols and puts
	// the epoch key on the phone's mailbox, and the phone -- which never started its transport
	// loop -- dies before it can drain it.
	var settled *pairing.MachineOutcome
	if tr.afterPairing && opened {
		var err error
		settled, err = machine.settle(true)
		if err != nil || settled == nil {
			t.Fatalf("the machine leg did not complete the ceremony this row is driven past "+
				"(outcome=%v err=%v)", settled, err)
		}
		machine.enrollAndDeliver(settled)
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL the victim: %v", err)
	}
	// STRICTLY AFTER THE SIGNAL. Releasing before it would let the machine act on the held
	// frame while the phone was still running and still able to write, which is the one thing
	// that would stop this row discriminating anything.
	if tr.releaseAfterKill {
		machine.gate.releaseNow()
	}
	werr := cmd.Wait()
	if !opened {
		t.Fatalf("the phone never reached the %q transition within 45s (it exited with %v), so "+
			"the process-death window this row is about never opened", tr.name, werr)
	}
	if !pbPair4IsSIGKILL(werr) {
		t.Fatalf("the victim exited with %v; want death by SIGKILL. A clean exit means no process "+
			"death was tested, and App.Close -- which this deliberately never calls -- WAITS for "+
			"the handshake it tears down", werr)
	}

	if !tr.afterPairing {
		settled, _ = machine.settle(tr.machineBoundsItsOwnWait)
	}

	// ---- both legs, after the dust ----
	durable := pbPair4DurableState(t, dir)
	phonePinned := len(durable.MachineStatic) > 0
	machineHolds := settled != nil

	// THE FORBIDDEN ORIENTATION, asserted first and by name.
	if machineHolds && !phonePinned {
		t.Fatalf("HALF-PAIR (PB-PAIR-4) at the %q transition: the machine holds device %x and the "+
			"phone's durable state has NO pin.\n"+
			"  Remote control is live against a handset that knows nothing about it, the\n"+
			"  single-device slot is spent, every further pairing is refused, and the only exit\n"+
			"  is a desktop revoke the owner has no reason to know they need.", tr.name,
			settled.DeviceStatic)
	}
	if phonePinned != tr.wantPhonePinned {
		t.Errorf("at the %q transition the restarted phone %s a durable pin; want %v. A row that "+
			"does not reach the state it declares is not measuring that state", tr.name,
			map[bool]string{true: "HOLDS", false: "holds NO"}[phonePinned], tr.wantPhonePinned)
	}
	if machineHolds != tr.wantMachineHoldsDevice {
		t.Errorf("at the %q transition the machine %s a device; want %v", tr.name,
			map[bool]string{true: "HOLDS", false: "holds NO"}[machineHolds], tr.wantMachineHoldsDevice)
	}

	// A PIN IS ALL OR NOTHING. Restored is derived from MachineRelayAuthPub -- the one
	// coordinate that says how to REACH the machine -- so a phone holding a static with no
	// route, or an epoch with no static, is half-paired within its own state.
	if phonePinned && (len(durable.MachineRelayAuthPub) == 0 || durable.EpochID == 0) {
		t.Errorf("at the %q transition the restarted phone holds an INCOHERENT pin: static=%d "+
			"bytes relay-auth=%d bytes epoch=%d. One coordinate of a pairing survived and another "+
			"did not, which is a state neither a re-pair nor a resume can leave", tr.name,
			len(durable.MachineStatic), len(durable.MachineRelayAuthPub), durable.EpochID)
	}

	// ---- the restart ----
	restarted, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: dir, RelayURL: relayURL, MachineID: testMachineID,
	}, pbPair4Custody{})
	if err != nil {
		t.Fatalf("the phone could not be reopened after a process death at the %q transition: %v",
			tr.name, err)
	}
	t.Cleanup(func() { _ = restarted.Close() })

	sum, err := restarted.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the kill: %v", err)
	}
	if sum.Restored != (sum.EpochID != 0) {
		t.Errorf("at the %q transition the restarted phone is HALF PAIRED: restored=%v epoch=%d",
			tr.name, sum.Restored, sum.EpochID)
	}

	attempt, err := restarted.PairingState()
	if err != nil {
		t.Fatalf("PairingState after the kill: %v", err)
	}
	if tr.wantAttemptRecorded && attempt == "" {
		t.Errorf("after a process death at the %q transition the restarted phone reports NO "+
			"pairing attempt. The machine's half may have committed; a phone that has forgotten "+
			"the attempt offers the user a scanner that cannot work, and PB-STATE-10 fail-fasts "+
			"it while the machine still holds this device", tr.name)
	}

	// ---- the grant-bootstrap row's own clause: the key is obtainable again ----
	if tr.afterPairing {
		if err := restarted.Start(); err != nil {
			t.Fatalf("App.Start after the kill: %v", err)
		}
		eventually(t, "after a process death before the bootstrap grant was drained, the "+
			"restarted phone can still not author a command: the epoch key never reached it, and "+
			"the frame that carries it is delivered once per gateway session",
			func() bool {
				_, kerr := restarted.Kill(testMachineID + "/sess-pbpair4")
				return kerr == nil
			})
	}
}
