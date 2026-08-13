package skeleton

// Slice S19 -- PB-E2E-1, the exit demonstration.
//
//	"A Go end-to-end test with NO FAKES and NO phonesim SEAM: real relay, real client, real
//	 façade, real gateway, real daemon -- pair -> observe -> launch -> take_control -> type
//	 -> revoke. Passes under -race. Explicitly forbids the injected mailbox seam."
//
// THE HARD PART IS THE NEGATIVE, so the seam inventory is stated here rather than left to be
// reconstructed from the code. Every component in the chain, and what it really is:
//
//	relay      REAL   internal/remote/relay.Server over a real localhost WebSocket, its own
//	                  BoltDB store. Both parties reach it by URL; nothing is short-circuited.
//	client     REAL   internal/remote/relay.Client on BOTH sides -- the phone's is the one
//	                  mobile/relay.go dials from the phone's own relay-auth key, the machine's
//	                  is the one cmd/swarm-remote dials from the machine identity.
//	façade     REAL   the bound swarmmobile.App: durable phonecore.Core underneath it, the
//	                  durable send-seq reservation, the durable receive transaction, the lease
//	                  gate, the input coalescer, the reconcile gate, the op queue and the
//	                  undelivered ledger -- none of which internal/phonesim has ever run,
//	                  because phonesim never constructs a phonecore.Core at all.
//	gateway    REAL   the cmd/swarm-remote BINARY, spawned as the separate supervised process
//	                  production runs, resolving its OWN gatewayParams from the machine's state
//	                  directory and delivering its OWN epoch grant. Nothing here assembles a
//	                  remotegw.ServiceConfig by hand: that assembly (resolveGatewayParams) is
//	                  package-main code, and an exit demonstration that re-implemented it would
//	                  be testing a copy of the gateway rather than the gateway.
//	daemon     REAL   the full skeleton.Serve assembly with the remote tier, its shim binary,
//	                  its PTYs and a real fake-agent process on the other end of one.
//	pairing    REAL   the daemon hosts pairing.Machine over the production relay rendezvous
//	                  adapter loadPairingConfig wires; the phone runs the production
//	                  BeginPairing / ConfirmOrigin / SAS / Confirm flow. No memRendezvous.
//
// THE THREE THINGS THAT ARE NOT REAL, each with its justification:
//
//  1. KEY CUSTODY (s19Custody). swarmmobile.NewApp requires a KeyCustody, which on a handset
//     is the Android Keystore. A Go test on a build machine has no Keystore, so this is two
//     software AES keys behind the same interface. It models the CONTRACT and no hardware
//     property whatsoever -- PB-E2E-5 (deferred, S21) is the gate that asserts real backing.
//     mobile/conformance's own harness makes the identical substitution for the identical
//     reason.
//  2. `swarm remote init`. The machine's provisioning is written HERE (machineid.Generate +
//     Save, relay.json, remote-policy.json) instead of by running the CLI, because
//     runRemoteInit ALSO installs and starts a launchd/systemd unit on the developer's
//     machine. The bytes on disk are identical and are read back by the production loaders
//     (loadPairingConfig, resolveGatewayParams); only the supervision half is skipped, and
//     that half is S4's, evidenced there.
//  3. THE FAKE AGENT. `fake` is the reserved dev/test agent the whole repository launches
//     sessions with; it is a real subprocess on a real PTY, and its scripted `ask` is what
//     makes a keystroke's arrival at the PTY observable at all.
//
// WHY THE MACHINE'S RENDEZVOUS CREATE IS OBSERVED. The daemon returns the pair_start reply --
// and therefore the QR -- BEFORE its background goroutine has created the rendezvous at the
// relay, and the phone's production Claim does NOT retry: a claim that beats the create fails
// TERMINALLY and surfaces five seconds later as "the phone never derived a SAS", cause
// discarded. s19CreateObserver wraps the PRODUCTION transport and forwards every call; it
// invents nothing and substitutes nothing. It exists so this test's failures are its subject's.

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// s19Deadline bounds every "eventually" in this file. It is generous because the chain
// crosses two processes, a WebSocket and a PTY, and because a failure here must be a real
// one rather than a slow machine.
const s19Deadline = 45 * time.Second

// ---------------------------------------------------------------------------
// PB-E2E-1
// ---------------------------------------------------------------------------

// TestPBE2E1_PairObserveLaunchTakeControlTypeRevoke is the exit demonstration.
//
// ONE RIG, INTERLEAVED. The refusal half (input before a lease) and the success half (input
// after one) run against the SAME phone, the SAME gateway and the SAME session, seconds
// apart. Two rigs would let the refusal hold for a reason that has nothing to do with the
// lease -- a phone in a different mode, a gateway that never connected -- and both halves
// would read green on a system where the gate does not exist.
//
// EVERY LEG ASSERTS POSITIVELY. `err == nil` is never the assertion: a pairing must PIN the
// machine, an observe must SHOW the session the daemon launched, a launch must produce a
// session on the machine, a take_control must resolve as a LEASE, a keystroke must reach the
// PTY and come back rendered, and a revoke must empty the daemon's registry AND stop the
// gateway process. A chain of nil errors is satisfied by a system that seals nothing.
func TestPBE2E1_PairObserveLaunchTakeControlTypeRevoke(t *testing.T) {
	rig := newS19Rig(t)

	// ---- pair --------------------------------------------------------------
	rig.Pair()

	// The pairing PINNED the machine: MachineRelayAuthPub is the one coordinate that says how
	// to reach it, and only a completed handshake writes it (mobile/app.go StateSummary).
	if sum := rig.Summary(); !sum.Restored {
		t.Fatal("after a completed pairing the phone reports nothing restored; " +
			"pin() did not write the machine's relay-auth key, so the phone knows who the " +
			"machine is and not how to reach it")
	}

	// THE PHONE MUST KNOW WHICH MACHINE IT PAIRED WITH. Every mutating verb signs a tuple
	// carrying the machine endpoint id (crypto.Command.Canonical REFUSES an empty one), so a
	// phone that has completed a real pairing and cannot name its machine can author nothing:
	// launch, take_control, kill and the revoke panic button all fail before a byte is sealed.
	//
	// Nothing in this test supplies it, and that is the point. Config.MachineID is what the
	// Android app passes, and PhoneRuntime.construct passes "" -- deliberately, so a resume
	// does not overwrite the coordinate it just restored. pairing.MachinePayload carries no
	// endpoint id, so pin() has nothing to write; Core.Reconcile receives the machine's own
	// name on an authenticated record and does not adopt it either.
	if got := rig.Summary().Machine; got != rig.MachineEndpointID() {
		t.Fatalf("after a real pairing the phone's machine endpoint id is %q, want %q; "+
			"the phone has no production source for this value -- Config.MachineID is \"\" on a "+
			"handset, the pairing payload carries no endpoint id, and nothing adopts the one the "+
			"machine publishes on its reconcile record -- so crypto.Command.Canonical refuses "+
			"every signed command this phone will ever author", got, rig.MachineEndpointID())
	}

	// ---- the gateway, started after pairing (PB-LIFE-3: nothing to serve before) ----
	rig.StartGateway()

	// The gateway published the machine's rollback authorities, so the phone's fail-closed
	// refusal of mutating ops has been lifted by a record it ADOPTED (PB-SYNC-7), not by a
	// timer. Asserted before the first mutating verb so a later refusal cannot be mistaken
	// for this gate.
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	// ---- observe -----------------------------------------------------------
	// A session the MACHINE launched, so "observe" observes something the phone did not cause.
	localID := rig.LaunchOnMachine("print S19_LOCAL\nidle 600s\n")
	rig.Eventually("the phone's roster shows the session the machine launched", func() bool {
		return rig.RosterHas(localID)
	})

	// ---- launch ------------------------------------------------------------
	remoteID := rig.LaunchFromPhone("print S19_REMOTE\nask s19> \nidle 600s\n", localID)

	// ---- the lease gate, refusal half (PB-INPUT-2) -------------------------
	// The SAME phone, the SAME session, before any take_control. If this succeeds, every
	// assertion below about the lease is vacuous -- the phone would be typing at a machine
	// that granted it nothing, and the gateway drops those frames silently.
	if err := rig.App().SendInput(remoteID, []byte("must-not-reach-the-pty\r")); err == nil {
		t.Fatal("SendInput was ACCEPTED for a session this phone holds no confirmed lease on; " +
			"PB-INPUT-2 requires the refusal, and without it the take_control assertions below " +
			"prove nothing")
	}

	// ---- take_control ------------------------------------------------------
	op, err := rig.App().TakeControl(remoteID)
	if err != nil {
		t.Fatalf("App.TakeControl(%q): %v", remoteID, err)
	}
	rig.Eventually("the take_control resolved as a daemon-granted lease", func() bool {
		out, err := rig.App().Outcome(op.OperationID)
		if err != nil || !out.Resolved {
			return false
		}
		if out.Code != protocol.OpLease {
			t.Fatalf("the machine refused the phone's take_control: code=%q message=%q%s",
				out.Code, out.Message, rig.gatewayTail())
		}
		return true
	})
	// The gate opened: the phone will now accept keystrokes for this session.
	rig.Eventually("the phone adopted the lease confirmation", func() bool {
		return rig.App().SendInput(remoteID, nil) == nil
	})

	// ---- type --------------------------------------------------------------
	// The peek is opened FIRST so the snapshot stream is live when the keystrokes land.
	if err := rig.App().TerminalWatch(remoteID); err != nil {
		t.Fatalf("App.TerminalWatch(%q): %v", remoteID, err)
	}
	rig.Eventually("the server-rendered peek reached the phone", func() bool {
		snap, err := rig.App().Peek(remoteID)
		return err == nil && strings.Contains(snap.Text, "S19_REMOTE")
	})
	if err := rig.App().SendInput(remoteID, []byte("S19MARK\r")); err != nil {
		t.Fatalf("App.SendInput on a leased session: %v", err)
	}
	// The fake agent echoes `got: <line>` only after its `ask` has CONSUMED the line from the
	// PTY, so this string on the daemon's own render is proof the keystroke reached the PTY --
	// phone -> relay -> gateway -> lease conn -> daemon -> PTY -> render -> relay -> phone.
	rig.Eventually("the typed line reached the PTY and came back rendered", func() bool {
		snap, err := rig.App().Peek(remoteID)
		return err == nil && strings.Contains(snap.Text, "got: S19MARK")
	})

	// ---- revoke ------------------------------------------------------------
	if _, err := rig.App().RevokeThisDevice(); err != nil {
		t.Fatalf("App.RevokeThisDevice: %v", err)
	}
	rig.Eventually("the daemon's device registry is empty", func() bool {
		devs, err := rig.Owner().ListDevices()
		return err == nil && len(devs) == 0
	})
	// The real sidecar noticed and stopped ITSELF. A gateway that kept running would go on
	// resealing epoch frames to a revoked device's mailbox.
	rig.AwaitGatewayExit()
}

// ---------------------------------------------------------------------------
// the rig
// ---------------------------------------------------------------------------

// s19Rig is the whole chain: one relay, one machine state directory, one daemon, one phone
// and one gateway process. It is deliberately a single object -- the refusal and success
// halves of every property in this file run against it, interleaved.
type s19Rig struct {
	t *testing.T

	relayURL string

	stateDir   string
	remoteSock string
	sk         *Daemon
	owner      *protocol.Client

	phoneDir string
	custody  *s19Custody
	app      *swarmmobile.App

	// created is closed once the daemon's PRODUCTION rendezvous transport has returned from
	// Create, which is the fact the phone's non-retrying Claim needs to be true.
	created chan struct{}

	gwBin  string
	gwCmd  *exec.Cmd
	gwWait chan error
	gwLog  *s19Buffer
}

func newS19Rig(t *testing.T) *s19Rig {
	t.Helper()
	buildBinaries(t)

	srv := startPairingRelay(t)

	// A short state-directory path: the daemon's UDS must fit in sun_path (104 bytes).
	stateDir, err := os.MkdirTemp("/tmp", "s19")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })

	// What `swarm remote init --relay-url` persists, minus the supervision-unit install.
	writeTestIdentity(t, stateDir, "s19-machine.local")
	writeRelayURL(t, stateDir, srv.URL())
	// R-POL.7: remote launches are confined to configured cwd roots and fail CLOSED with
	// none. The phone's launch below runs in a t.TempDir(), which lives under this root.
	tmpRoot, terr := filepath.EvalSymlinks(os.TempDir())
	if terr != nil {
		tmpRoot = filepath.Clean(os.TempDir())
	}
	if err := writeRemoteLaunchPolicy(stateDir, []string{tmpRoot}); err != nil {
		t.Fatalf("seed remote launch policy: %v", err)
	}

	remoteSock := filepath.Join(stateDir, "r.sock")
	sk, err := Serve(Config{
		StateDir:           stateDir,
		SocketPath:         filepath.Join(stateDir, "d.sock"),
		LockPath:           filepath.Join(stateDir, "d.lock"),
		LogPath:            filepath.Join(stateDir, "d.log"),
		ShimBinary:         swarmBin,
		MaxSessions:        16,
		PollInterval:       50 * time.Millisecond,
		StalenessThreshold: 2 * time.Second,
		FakeAgentBin:       fakeAgentBin,
		RemoteSocketPath:   remoteSock,
	})
	if err != nil {
		t.Fatalf("skeleton.Serve: %v", err)
	}
	t.Cleanup(func() { _ = sk.Close() })

	r := &s19Rig{
		t: t, relayURL: srv.URL(),
		stateDir: stateDir, remoteSock: remoteSock, sk: sk,
		phoneDir: t.TempDir(), custody: newS19Custody(t),
		created: make(chan struct{}),
		gwBin:   s19GatewayBinary(t),
	}
	r.observeRendezvousCreate()

	owner, err := protocol.Dial(sk.SocketPath(), []string{protocol.CapPairing})
	if err != nil {
		t.Fatalf("owner protocol.Dial: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	r.owner = owner

	// The phone as a FRESH INSTALL: no relay URL yet, because the handset learns it from the
	// QR it is about to scan, and no machine id, because PhoneRuntime.construct passes "".
	r.app = r.openApp("")
	return r
}

func (r *s19Rig) App() *swarmmobile.App     { return r.app }
func (r *s19Rig) Owner() *protocol.Client   { return r.owner }
func (r *s19Rig) MachineEndpointID() string { return r.sk.api.endpointID }

// openApp constructs the facade over the phone's state directory, exactly as
// PhoneRuntime.construct does: the same custody, and MachineID always "".
func (r *s19Rig) openApp(relayURL string) *swarmmobile.App {
	r.t.Helper()
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir:  r.phoneDir,
		RelayURL:  relayURL,
		MachineID: "",
	}, r.custody)
	if err != nil {
		r.t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	r.t.Cleanup(func() { _ = app.Close() })
	return app
}

func (r *s19Rig) Summary() *swarmmobile.StateSummary {
	r.t.Helper()
	sum, err := r.app.StateSummary()
	if err != nil {
		r.t.Fatalf("App.StateSummary: %v", err)
	}
	return sum
}

// Eventually polls fn until it reports true, or fails naming what never happened.
func (r *s19Rig) Eventually(what string, fn func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(s19Deadline)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	r.t.Fatalf("timed out after %s: %s%s", s19Deadline, what, r.gatewayTail())
}

// ---- pairing ---------------------------------------------------------------

// observeRendezvousCreate wraps the daemon's PRODUCTION rendezvous factory so the test can
// tell when the machine's Create has returned. It forwards every method; see the file header
// for why the phone cannot simply be told to retry.
func (r *s19Rig) observeRendezvousCreate() {
	r.t.Helper()
	r.sk.api.pairingMu.Lock()
	defer r.sk.api.pairingMu.Unlock()
	cfg := r.sk.api.pairing
	if cfg == nil || cfg.NewRendezvous == nil {
		r.t.Fatal("the daemon has no relay-backed rendezvous seam; loadPairingConfig did not " +
			"wire one from the machine identity + relay.json this rig wrote")
	}
	inner := cfg.NewRendezvous
	var once sync.Once
	cfg.NewRendezvous = func(ctx context.Context, id [16]byte) (pairing.RendezvousTransport, error) {
		rt, err := inner(ctx, id)
		if err != nil {
			return nil, err
		}
		return &s19CreateObserver{RendezvousTransport: rt, done: func() { once.Do(func() { close(r.created) }) }}, nil
	}
}

// s19CreateObserver forwards a real RendezvousTransport and signals when Create returns.
type s19CreateObserver struct {
	pairing.RendezvousTransport
	done func()
}

func (o *s19CreateObserver) Create(ctx context.Context, id string) error {
	err := o.RendezvousTransport.Create(ctx, id)
	if err == nil {
		o.done()
	}
	return err
}

// Pair runs the whole ceremony: owner-tier pair_start on the daemon, the phone's production
// BeginPairing / ConfirmOrigin / SAS / Confirm, both SAS gates answered, and the phone
// REOPENED over the relay URL it learned from the QR -- which is what a handset does, since
// PhoneRuntime remembers the URL and the bound facade has no verb to re-target a live App.
func (r *s19Rig) Pair() {
	t := r.t
	t.Helper()

	sess, err := r.owner.StartPairing(protocol.PairStartReq{Capability: "full"})
	if err != nil {
		t.Fatalf("owner StartPairing: %v", err)
	}
	t.Cleanup(sess.Close)
	if sess.QR == "" {
		t.Fatal("pair_start returned no QR")
	}

	select {
	case <-r.created:
	case <-time.After(s19Deadline):
		t.Fatal("the machine never created its rendezvous at the relay; the phone's claim " +
			"would fail terminally and report itself as a missing SAS")
	}

	p, err := r.app.BeginPairing(sess.QR)
	if err != nil {
		t.Fatalf("App.BeginPairing: %v", err)
	}
	origin, err := p.Origin()
	if err != nil {
		t.Fatalf("Pairing.Origin: %v", err)
	}
	if origin != r.relayURL {
		t.Fatalf("the QR names %q, want the machine's configured relay %q", origin, r.relayURL)
	}
	if err := p.ConfirmOrigin(origin); err != nil {
		t.Fatalf("Pairing.ConfirmOrigin: %v", err)
	}

	// Both SAS displays, then compare. PB-SAS-1: the two ends must show the same six words,
	// and comparing them is the whole anti-MITM check.
	var pending protocol.PairingPending
	select {
	case pending = <-sess.Pending():
	case <-time.After(s19Deadline):
		t.Fatalf("the machine never reached its SAS gate (phone pairing state %q)%s",
			r.pairState(p), r.gatewayTail())
	}
	phoneSAS := r.awaitPhoneSAS(p)
	if machineSAS := strings.Join(pending.SAS, " "); machineSAS != phoneSAS {
		t.Fatalf("SAS mismatch: machine shows %q, phone shows %q", machineSAS, phoneSAS)
	}

	if err := p.Confirm(); err != nil {
		t.Fatalf("Pairing.Confirm (phone): %v", err)
	}
	if err := sess.Confirm(true); err != nil {
		t.Fatalf("pair_confirm (owner): %v", err)
	}

	var res protocol.PairingResult
	select {
	case res = <-sess.Result():
	case <-time.After(s19Deadline):
		t.Fatal("the daemon never produced a pair_result")
	}
	if !res.Paired || res.DeviceID == "" {
		t.Fatalf("pair_result = %+v; want an enrolled device", res)
	}
	r.Eventually("the phone's pairing reached the paired state", func() bool {
		return r.pairState(p) == "paired"
	})

	// The handset's own restart: PhoneRuntime.rememberRelay records the URL and the NEXT App
	// construction dials it.
	qr, err := swarmmobile.DecodeQR(sess.QR)
	if err != nil {
		t.Fatalf("swarmmobile.DecodeQR: %v", err)
	}
	if err := r.app.Close(); err != nil {
		t.Fatalf("App.Close before the reopen: %v", err)
	}
	r.app = r.openApp(qr.RelayURL)
	if err := r.app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}
}

// awaitPhoneSAS polls the phone's SAS and FAILS FAST on a terminal pairing state, so a
// handshake that died is reported as what it was rather than as a five-second silence.
func (r *s19Rig) awaitPhoneSAS(p *swarmmobile.Pairing) string {
	r.t.Helper()
	deadline := time.Now().Add(s19Deadline)
	for time.Now().Before(deadline) {
		if sas, err := p.SAS(); err == nil && sas != "" {
			return sas
		}
		switch st := r.pairState(p); st {
		case "confirm_destination", "pairing", "confirming":
		default:
			r.t.Fatalf("the phone's pairing reached terminal state %q before deriving a SAS", st)
		}
		time.Sleep(25 * time.Millisecond)
	}
	r.t.Fatalf("the phone never derived a SAS (state %q)", r.pairState(p))
	return ""
}

func (r *s19Rig) pairState(p *swarmmobile.Pairing) string {
	st, err := p.State()
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return st
}

// ---- the gateway process ---------------------------------------------------

var (
	s19GatewayOnce sync.Once
	s19GatewayPath string
	s19GatewayErr  error
)

// s19GatewayBinary builds cmd/swarm-remote -- the actual sidecar, not a re-implementation of
// it -- carrying the race detector exactly when this test binary does.
func s19GatewayBinary(t *testing.T) string {
	t.Helper()
	s19GatewayOnce.Do(func() {
		dir, err := os.MkdirTemp("", "s19-gw")
		if err != nil {
			s19GatewayErr = err
			return
		}
		s19GatewayPath = filepath.Join(dir, "swarm-remote")
		args := []string{"build"}
		if s19RaceEnabled {
			args = append(args, "-race")
		}
		args = append(args, "-o", s19GatewayPath, "github.com/Nathandela/swarm/cmd/swarm-remote")
		cmd := exec.Command("go", args...)
		cmd.Stderr = os.Stderr
		s19GatewayErr = cmd.Run()
	})
	if s19GatewayErr != nil {
		t.Fatalf("cannot build the gateway sidecar: %v", s19GatewayErr)
	}
	return s19GatewayPath
}

// StartGateway spawns the real sidecar over the machine's state directory. It resolves its
// own params, delivers its own epoch grant and runs its own Service; nothing here hands it a
// ServiceConfig.
func (r *s19Rig) StartGateway() {
	t := r.t
	t.Helper()
	r.gwLog = &s19Buffer{}
	cmd := exec.Command(r.gwBin)
	cmd.Env = append(os.Environ(),
		daemon.EnvStateDir+"="+r.stateDir,
		daemon.EnvRemoteSocket+"="+r.remoteSock,
	)
	cmd.Stdout = r.gwLog
	cmd.Stderr = r.gwLog
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the gateway sidecar: %v", err)
	}
	r.gwCmd = cmd
	r.gwWait = make(chan error, 1)
	go func() { r.gwWait <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-r.gwWait:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
}

// AwaitGatewayExit requires the sidecar to have stopped BY ITSELF after the revoke, and to
// say why. Its exit STATUS is deliberately not the assertion: a graceful SIGTERM also exits
// zero, so a status check would pass on a gateway this test killed at cleanup.
func (r *s19Rig) AwaitGatewayExit() {
	t := r.t
	t.Helper()
	select {
	case <-r.gwWait:
	case <-time.After(s19Deadline):
		t.Fatalf("the gateway sidecar was still running %s after the phone revoked itself; "+
			"it must stop rather than keep resealing epoch frames to a revoked mailbox%s",
			s19Deadline, r.gatewayTail())
	}
	if out := r.gwLog.String(); !strings.Contains(out, "revoked") {
		t.Fatalf("the gateway sidecar exited without reporting the revocation; output was:\n%s", out)
	}
}

// gatewayTail is what the machine has to say, appended to every timeout message. A hop in
// this chain that stops answering is otherwise pure silence: the sidecar's own output names
// the refusal, and the machine's session list says whether the daemon ever acted.
func (r *s19Rig) gatewayTail() string {
	var b strings.Builder
	if r.gwLog != nil {
		if out := strings.TrimSpace(r.gwLog.String()); out != "" {
			b.WriteString("\ngateway sidecar output:\n" + out)
		}
	}
	if views, err := r.owner.List(); err == nil {
		b.WriteString("\nmachine sessions:")
		for _, v := range views {
			fmt.Fprintf(&b, "\n  %s group=%s", v.ID, v.Group)
		}
		if len(views) == 0 {
			b.WriteString(" (none)")
		}
	}
	return b.String()
}

// s19Buffer is a concurrency-safe sink for the sidecar's stdout/stderr: os/exec writes from
// its own goroutines while the test reads on a timeout.
type s19Buffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *s19Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *s19Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// ---- sessions --------------------------------------------------------------

// LaunchOnMachine launches a scripted fake-agent session through the OWNER-tier client, so
// the roster the phone observes was populated by something the phone did not do.
func (r *s19Rig) LaunchOnMachine(script string) string {
	r.t.Helper()
	return r.LaunchOnMachineSized(script, 80, 24)
}

// LaunchOnMachineSized is LaunchOnMachine at an explicit terminal size, for a test whose script
// paints a recorded grid: a 100x30 capture replayed into an 80x24 PTY wraps, and a wrapped box
// rule is not a box rule.
func (r *s19Rig) LaunchOnMachineSized(script string, cols, rows int) string {
	r.t.Helper()
	id, _, err := r.owner.Launch(protocol.LaunchReq{
		Agent:   "fake",
		Cwd:     r.t.TempDir(),
		Options: map[string]string{"script": r.script(script)},
		Cols:    cols,
		Rows:    rows,
	})
	if err != nil {
		r.t.Fatalf("owner Launch: %v", err)
	}
	return id
}

// LaunchFromPhone drives PB-APP-6's remote launch and returns the namespaced id of the
// session it created, identified as the roster row that is not `existing`.
func (r *s19Rig) LaunchFromPhone(script string, existing ...string) string {
	r.t.Helper()
	op, err := r.app.Launch(&swarmmobile.LaunchSpec{
		Agent:   "fake",
		Cwd:     r.t.TempDir(),
		Options: "script=" + r.script(script),
	})
	if err != nil {
		r.t.Fatalf("App.Launch: %v", err)
	}
	// PB-SYNC-2: the verdict is claimed BY OPERATION ID, so a reply the machine sends without
	// one is unattributable -- phonecore drops it from the durable model and the op stays in
	// flight for the life of the process. A launch that appears on the machine while this
	// times out is that defect rather than a slow relay, which is why the machine's session
	// list rides the failure.
	r.Eventually("the phone's launch was answered by the machine", func() bool {
		out, err := r.app.Outcome(op.OperationID)
		if err != nil || !out.Resolved {
			return false
		}
		if out.Code != protocol.OpLaunch {
			r.t.Fatalf("the machine refused the phone's launch: code=%q message=%q%s",
				out.Code, out.Message, r.gatewayTail())
		}
		return true
	})

	known := map[string]bool{}
	for _, id := range existing {
		known[id] = true
	}
	var launched string
	r.Eventually("the launched session appeared on the phone's roster", func() bool {
		for _, id := range r.roster() {
			if !known[id] {
				launched = id
				return true
			}
		}
		return false
	})
	return launched
}

// script writes a fake-agent script and returns its path.
func (r *s19Rig) script(body string) string {
	r.t.Helper()
	path := filepath.Join(r.t.TempDir(), "script.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		r.t.Fatalf("write fake-agent script: %v", err)
	}
	return path
}

func (r *s19Rig) roster() []string {
	r.t.Helper()
	list, err := r.app.Roster()
	if err != nil {
		return nil
	}
	n, err := list.Count()
	if err != nil {
		return nil
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		s, err := list.At(i)
		if err != nil {
			continue
		}
		out = append(out, s.ID)
	}
	return out
}

func (r *s19Rig) RosterHas(id string) bool {
	for _, got := range r.roster() {
		if got == id {
			return true
		}
	}
	return false
}

// ---- custody ---------------------------------------------------------------

// s19Custody is the Android Keystore stand-in (PB-KEY-9). It models the CONTRACT -- a
// per-tier data key the platform unwraps and can refuse -- and no hardware property at all.
// Real backing, real biometrics and real attestation are PB-E2E-5, which is deferred to S21
// and cannot be asserted on a build machine.
type s19Custody struct {
	wake    []byte
	content []byte
}

func newS19Custody(t *testing.T) *s19Custody {
	t.Helper()
	c := &s19Custody{wake: make([]byte, 32), content: make([]byte, 32)}
	for _, k := range [][]byte{c.wake, c.content} {
		if _, err := rand.Read(k); err != nil {
			t.Fatalf("generating a Keystore stand-in KEK: %v", err)
		}
	}
	return c
}

// The facade zeroizes what it is handed, so every call returns a COPY.
func (c *s19Custody) WakeKEK() ([]byte, error) {
	return append([]byte(nil), c.wake...), nil
}

func (c *s19Custody) ContentKEK() ([]byte, error) {
	return append([]byte(nil), c.content...), nil
}

var _ swarmmobile.KeyCustody = (*s19Custody)(nil)
