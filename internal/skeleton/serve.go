// Package skeleton is the Epic 8 daemon ASSEMBLY: the wiring that turns the three
// independently-built layers into the running walking skeleton (GG-1). It composes
//
//   - internal/daemon    — the lifecycle authority (flock singleton, crash-safe
//     launch, reconnect-on-restart) that OWNS the client socket;
//   - internal/protocol  — the client-facing RPC + attach data plane, served on
//     the daemon's own socket via the daemon's ConnHandler knob (no second socket);
//   - internal/engine     — the status-detection authority, driven by the fallback
//     poll and fed hook callbacks demuxed off that same socket.
//
// It cannot live in internal/daemon: protocol imports daemon, so an assembly there
// would be an import cycle. skeleton imports all three and is what `swarm daemon`
// (cmd/swarm.runDaemon) runs.
//
// SOCKET OWNERSHIP: the daemon binds and owns the singleton socket (flock-before-
// bind, stale-socket reclaim under the lock — S12 all stay in daemon). Its accept
// loop hands each connection to this package's ConnHandler, which DEMUXES the one
// socket on an EXPLICIT first byte (see conn.go): a version probe leads with
// daemon.VersionProbeTag ('V'), a hook post with '{', and a wire frame with 0x00
// (its length MSB). The three are disjoint, so a single first-byte read routes each
// connection immediately — no timing window, no change to the hook or frame wire.
package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/worktree"
)

// Config parameterizes the assembly. The socket/lock/log/state paths and shim
// binary are the daemon's; PollInterval/StalenessThreshold tune the engine;
// FakeAgentBin is the dev/test-only resolver for the reserved agent "fake".
type Config struct {
	StateDir, SocketPath, LockPath, LogPath, ShimBinary string
	MaxSessions                                         int
	PollInterval                                        time.Duration // engine fallback-poll cadence (E10.8); 0 = no cadence
	StalenessThreshold                                  time.Duration
	FakeAgentBin                                        string // DEV/TEST ONLY: resolves the reserved agent "fake"
	// RemoteSocketPath, when non-empty, stands up the dedicated REMOTE-tier UDS the
	// gateway dials (R-GW.8 / amendment D.0-A1), distinct from the owner-trusted main
	// SocketPath. Every connection on it is unconditionally remote-origin, so every
	// mutating op is authorized against the pinned device registry (R-POL.9) before any
	// action. Empty => no remote socket. cmd/swarm fills it from gatewaySocket(), the same
	// definition the gateway's supervision unit dials, and leaves it empty on a machine
	// that was never provisioned for remote (ADR-007 B15).
	RemoteSocketPath string
	// ItemClock is the append floor's clock seam (remotegw.ItemAdmissionConfig.Now),
	// nil => time.Now, which is every production caller.
	//
	// It exists because ADR-010 §7's merge window is WALL-CLOCK. Whether two increments
	// of one item fold into one lossless append (IS-DELTA-1/-2) or ship as two records
	// therefore depends on how much real time passed between two Offer calls -- so a test
	// that must prove the MERGED item is admitted cannot get that from a real clock: a
	// scheduler stall of one window between the two offers is enough to release the first
	// increment alone, and CI's parallelism produces exactly that (docs/verification/
	// r0-flake-rootcause.md). Pinning the clock is what makes "these two are inside one
	// window" a fact of the test rather than a coincidence of the machine.
	ItemClock func() time.Time
}

// Daemon is the assembled, running walking skeleton: the core lifecycle daemon,
// the protocol server bound to its socket, the status engine, and the roster
// event source, with one Close that tears all four down cleanly.
type Daemon struct {
	core       *daemon.Daemon
	srv        *protocol.Server
	remoteSrv  *protocol.Server // the dedicated remote-tier listener (R-GW.8); nil unless configured
	api        *coreAPI
	eng        *engine.Engine
	socketPath string
	stateDir   string // for reading a session's transcript tail (conversation-id capture)

	cancel context.CancelFunc // stops engine.Run

	ready   chan struct{} // closed once the assembly is wired; gates the ConnHandler
	closing chan struct{} // closed by Close; aborts a connection still waiting on ready

	// Grid-tap sampling state (FIX 7): each running session is sampled in its own
	// goroutine so one busy shim never stalls another session's cadence (L1, no
	// head-of-line blocking); sampling dedups per session (at most one in-flight
	// sample each), and sampleWG lets Close drain in-flight samples. sampleFn is the
	// per-session sample op — d.sampleGrid in production, overridable in tests.
	sampleMu sync.Mutex
	sampling map[string]struct{}
	sampleWG sync.WaitGroup // in-flight per-session grid samples
	tapWG    sync.WaitGroup // the tapGrids loop (the sole sampleWG/captureWG Adder)
	sampleFn func(id string)
	// controlled reports whether a session has a live controller lease; the tap
	// skips such a session so its stream is not stolen every poll (R1.3.7). It is
	// d.srv.IsControlled in production, overridable in tests.
	controlled func(id string) bool

	// captureMu/capturing/captureWG dispatch conversation-id capture in its OWN
	// per-session goroutine, the same dedup mechanism as sampleGridAsync (R2.1.2):
	// a slow disk read for one uncaptured session must not delay the tap loop
	// reaching the next session. Unlike grid sampling, capture is attach-
	// independent, so it runs for EVERY running session regardless of controlled
	// status (C1/R1.3.7 gates only the grid sample). captureFn is
	// d.captureConversationID in production (lazily defaulted so a test Daemon
	// literal need not set it), overridable in tests.
	captureMu sync.Mutex
	capturing map[string]struct{}
	captureWG sync.WaitGroup
	captureFn func(id string)

	// convScanMu/convScan back the growth-gated re-read in captureConversationID
	// (R2.1.3): a session's transcript tail is re-read only when the file's size
	// has changed (grown, or shrunk/rotated) since the last scan.
	convScanMu sync.Mutex
	convScan   map[string]convScanState

	// The INTERACTION PRODUCER's state (ADR-009 / ADR-010; interaction.go). items is
	// ADR-010 §7's append floor -- ONE per machine, because IS-DELTA-2a's ceiling is per
	// TARGET across every session and kind. itemIDs maps a CLI's own interaction id to the
	// minted item_id so successive records of one item fold under it (IS-ENV-2), and turnIDs
	// holds each session's open turn (IS-ENV-1); both are cleared by endSession. adapterFor
	// resolves an agent type to its adapter -- registry.New in production, overridable in
	// tests, the sampleFn/captureFn precedent above.
	//
	// The APPROVAL LIFECYCLE's state rides the same mutex (approval.go): approvals holds each
	// session's ONE unresolved approval_request and its ADR-007 D7 binding tuple (IS-LIFE-4),
	// openItems every item journalled `in_progress` and not yet closed (IS-ST-2's sweep), and
	// interacted the last status.Interaction seen per session -- the TRANSITION out of it is
	// IS-LIFE-2's answered_locally signal. All three are cleared by endSession.
	itemMu    sync.Mutex
	items     *remotegw.ItemAdmission
	itemClock func() time.Time // Config.ItemClock; nil => time.Now (the floor's own default)
	itemIDs   map[string]string
	turnIDs   map[string]string
	// nativeTurns holds the CLI'S OWN id for each session's OPEN turn, when its adapter
	// sources one (adapter.Interaction.TurnRef). It is the machine-side twin of turnIDs and
	// never reaches the wire: turnIDs is what a phone names in expected_turn, nativeTurns is
	// what a turn/steer or turn/interrupt must name on the provider's own socket. Cleared
	// with turnIDs, by the same rule, in the same place (turnIDLocked / forgetInteractions).
	nativeTurns map[string]string
	// closedTurns holds the CLI's own id for the LAST turn this daemon saw CLOSE, per session.
	// It exists only to bound the mid-turn rejoin adoption in turnIDLocked: a frame naming a
	// turn this daemon already completed must not reopen it, or the session looks busy forever
	// and no new turn can ever be started. Cleared with turnIDs, in the same places.
	closedTurns map[string]string
	approvals   map[string]*pendingApproval
	openItems   map[string]map[string]openItem
	interacted  map[string]status.Interaction
	adapterFor  func(agentType string) (adapter.Adapter, bool)
	// detectProviderVersion is the DETECTED CLI version seam (ADR-017 T2 / playbook:280:
	// the honest header names the provider's detected version). It is a daemon seam
	// rather than an inline probe so a test never execs, and so the record carries a fact
	// about this host rather than a guess. Wired in Serve; nil answers "" (unknown).
	detectProviderVersion func(agentType string) string

	// The COMPLETE-CHAT state (Wave R6, chat.go), riding itemMu with its siblings:
	// pendingSends holds each session's accepted composer injections awaiting their
	// UserPromptSubmit echo (M2.4's injection-time attribution), and details/detailOrder/
	// detailBytes are M3.3's byte-bounded capture-time retention of full pre-truncation
	// bodies (one 64 MiB store, oldest-first eviction). A session's entries are cleared by
	// endSession.
	pendingSends map[string][]pendingSend
	details      map[string]map[string][]byte
	detailOrder  []detailKey
	detailBytes  int

	// hookSeq is ingestHookBytes's own idempotency guard (hookdrain.go, R6 review
	// fix-pack BLOCKER 1): the bounded, durable SET of hook callback Sequences fully
	// ingested per session -- a membership test, not a high-water gate, because a hook
	// sequence carries no causal order (internal/engine, agents-tracker-707). Rides
	// itemMu like every other per-session ingest-side map above.
	hookSeq map[string]*hookSeen

	// capStore is the daemon-authored per-session capability record store (ADR-017 T2 /
	// playbook §6.2; capability.go).
	capStore sessionCapabilityStore

	// sup is the passive handoff supervisor (ADR-010 Amendment 3 C2; supervision.go):
	// armed from registerSession, signalled from emitStatus and endSession, closed by
	// Close. nil in a test Daemon literal, so every use is nil-guarded like d.eng/d.api.
	sup *supervisor

	// drains holds the per-session hook-spool drain loops (hookdrainloop.go): the
	// production caller of HookDrainer, started from registerSession and stopped by
	// endSession/Close.
	drains hookDrainState

	// The SESSION BACKEND's state (Wave R7, backend.go): `backend` is the per-session
	// app-server connection registry plus the outstanding server-request table, and `pump`
	// is the producer-edge delta batcher. They ride their OWN mutexes rather than itemMu,
	// with the lock order stated in backend.go: pumpMu -> itemMu, and backendMu taken alone.
	backend backendState
	pump    backendPump
	// backendReady overrides backendReadyDeadline. Test-only, and unset in production: the
	// degrade paths are only reachable after a dial gives up, and waiting 45 s per case is a
	// test suite nobody runs.
	backendReady time.Duration

	// tapFailures counts grid-tap attach/snapshot failures so a tap that can no longer
	// read a session's snapshot is OBSERVABLE rather than a silent heuristic death
	// (R1.2.6 — the pre-1.2 oversized-snapshot bug failed exactly here). tapLastLog
	// rate-limits the accompanying log line to tapLogInterval.
	tapFailures atomic.Uint64
	tapLastLog  atomic.Int64

	closeOnce sync.Once
}

// Serve performs the full assembly and begins serving on cfg.SocketPath. On
// success the daemon is live: clients can Dial the socket, launch/list/attach, and
// hook posts route to the engine. The caller owns the returned *Daemon and closes
// it with Close.
func Serve(cfg Config) (*Daemon, error) {
	// The daemon is one federation endpoint with a STABLE id derived from its
	// persistent home, so a session's namespaced id is identical for every client
	// and unchanged across restarts (a session launched by one client is the same
	// id a later client — or the same daemon after a kill/restart — lists). The
	// coreAPI needs it to validate a resume request's source endpoint (R-2).
	epID := endpointID(cfg.StateDir)
	d := &Daemon{
		socketPath: cfg.SocketPath,
		stateDir:   cfg.StateDir,
		ready:      make(chan struct{}),
		closing:    make(chan struct{}),
		sampling:   make(map[string]struct{}),
		capturing:  make(map[string]struct{}),
	}
	d.sampleFn = d.sampleGrid // the per-session grid sample (overridable in tests)
	d.itemClock = cfg.ItemClock
	d.initInteractions() // the ADR-010 §7 append floor + the adapter resolver (interaction.go)
	// The capability store's durable home: one 0600 record per session dir, so an
	// ADR-017 T2 rule 2 degrade outlives the incarnation that authored it
	// (capability.go). Set before anything can register a record.
	d.capStore.dir = cfg.StateDir

	// Build the status engine BEFORE opening the core: daemon.Open runs reconcile
	// synchronously and, for every reconnected running session, fires OnSessionStart
	// (registerSession) to RE-REGISTER it with the engine so typed hooks + the grid
	// tap keep driving status across a restart (L2). So the engine must already
	// exist when Open runs. Emit is the late-bound d.emitStatus because the engine's
	// sink (the coreAPI) is not built until after Open returns the core — and no emit
	// can fire in that window (reconcile's RegisterSession installs sessions at the
	// humble unknown baseline and emits nothing; hook/tap emits are gated on d.ready).
	d.eng = engine.New(engine.Config{
		StalenessThreshold: cfg.StalenessThreshold,
		PollInterval:       cfg.PollInterval,
		Emit:               d.emitStatus,
	})

	// The core owns the socket but delegates connection serving to d.handleConn,
	// and runs the worktree isolation hooks gated on the per-launch worktree flag
	// (Epic 12 toggle wiring). handleConn blocks on d.ready, so nothing is served
	// until the assembly below is fully wired.
	core, err := daemon.Open(daemon.Config{
		StateDir:       cfg.StateDir,
		SocketPath:     cfg.SocketPath,
		LockPath:       cfg.LockPath,
		LogPath:        cfg.LogPath,
		ShimBinary:     cfg.ShimBinary,
		MaxSessions:    cfg.MaxSessions,
		ConnHandler:    d.handleConn,
		PreLaunch:      preLaunchWorktree,
		PreDelete:      preDeleteWorktree,
		OnSessionStart: d.registerSession,
		OnSessionEnd:   d.endSession,
		// Wave R7 (backendconnect.go): the core asks the ASSEMBLY whether a session needs a
		// side process, because only the assembly may touch an adapter and only the core
		// knows the session dir the plan is contained against.
		BackendPlanner: d.planSessionBackend,
	})
	if err != nil {
		return nil, err
	}
	d.core = core
	d.api = newCoreAPI(core, cfg.FakeAgentBin, epID)
	// ADR-017 T2-a's three assembly hooks: author at launch (api.go), author on the
	// re-attach of a session dir that has no record (sessiontap.go), and the PURE READ
	// the remote-tier capability gate consults. Wired here and nowhere else, so there is
	// exactly one place that decides a bare coreAPI authors nothing.
	d.api.onLaunched = d.authorLaunchedSessionCapabilities
	d.api.sessionCaps = d.sessionCapabilities
	d.api.tap.onSubscribe = d.authorAttachedSessionCapabilities
	if d.detectProviderVersion == nil {
		d.detectProviderVersion = detectProviderVersion
	}
	// Round-7 re-audit (codex/opus/sonnet consensus): newCoreAPI already started the
	// coreAPI.watch() roster poller, so EVERY assembly error return past this point must tear it
	// down (and the core) or that goroutine + its fd leak. Harmless in production (a Serve error is
	// a fatal startup -> the process exits and the OS reclaims), but wrong for tests / any
	// in-process Serve retry. A defer'd cleanup-unless-success covers all error paths uniformly and
	// panic-safely; the explicit per-path core.Close() calls below are now redundant (removed).
	// coreAPI.close() is idempotent (stopOnce) and never touches core, so the later Daemon.Close()
	// stays a no-op double-call.
	assembled := false
	defer func() {
		if !assembled {
			if d.sup != nil {
				d.sup.close() // its delivery goroutine, once started, must not outlive a failed assembly
			}
			d.api.close()
			_ = core.Close()
		}
	}()
	// Open the pinned-device registry that backs R-POL.9 remote-command authorization.
	// A corrupt registry fails assembly (fail-closed): the daemon must not start unable
	// to authorize -- or worse, silently unable to enumerate -- its paired devices.
	devReg, err := device.Open(filepath.Join(cfg.StateDir, "devices"))
	if err != nil {
		return nil, err
	}
	// Finding 5 (re-audit): when pairing is configured, clear any device whose sealed grant
	// sidecar is absent -- a crash between AddSole and grant.Save (pairing.go) leaves such a
	// device registered with no deliverable bootstrap grant, holding the single-device slot yet
	// inert. Gated on the machine identity's presence: that crash can only occur under a
	// configured grant-based pairing flow, so an unconfigured daemon (no grant delivery at all)
	// never spuriously clears a record. Reconcile on load so the slot frees and re-pairing works.
	if _, statErr := os.Stat(filepath.Join(cfg.StateDir, "remote", remoteIdentityFile)); statErr == nil {
		// Finding 2 (round-6): fail CLOSED. If a confirmed-stale device cannot be reconciled away,
		// ABORT assembly rather than open remote.sock still serving it.
		if err := reconcilePairedDevices(devReg, cfg.StateDir); err != nil {
			return nil, err // defer'd cleanup tears down d.api + core
		}
	}
	d.api.devices = devReg
	// R-KS.1: the coreAPI mirrors its device-derived remote-control kill-switch state to a
	// durable remote-state.json under the state dir. Wire the dir so the switch (default OFF
	// until a device is paired) has somewhere to persist each transition.
	d.api.stateDir = cfg.StateDir
	// A4: restore the durable manual override (`swarm remote off`/`on`) so an owner who
	// severed remote control stays severed across a restart. Absent file => not overridden;
	// a corrupt file fails closed (loadRemoteState returns ManualOff=true).
	if st, _ := loadRemoteState(cfg.StateDir); st.ManualOff {
		d.api.manualOff.Store(true)
	}
	// R-POL.3/.7: load the machine-configured remote launch policy (allowed cwd roots) and
	// attach it to the coreAPI so the remote-tier Server confines remote launches. ALWAYS
	// wired: a missing/malformed config yields a deny-all policy (fail-closed by default),
	// and the error is advisory only (the returned policy is always safe).
	launchPolicy, _ := loadRemoteLaunchPolicy(cfg.StateDir)
	d.api.launchPolicy = launchPolicy
	// Load the machine's pairing identity (provisioned by `swarm remote init`) and wire
	// it onto the coreAPI. TRI-STATE fail-closed, unlike the launch policy above: a
	// MISSING identity simply leaves pairing unsupported (nil cfg -- BeginPairing
	// already fails closed on that), but a CORRUPT identity aborts assembly entirely --
	// the daemon must not start with pairing silently broken (machine key custody).
	pc, err := loadPairingConfig(cfg.StateDir)
	if err != nil {
		return nil, err // defer'd cleanup tears down d.api + core
	}
	d.api.pairing = pc
	// IS-LIFE-4: the coreAPI is what the protocol Server holds, and the approval lifecycle
	// lives on the OUTER Daemon (approval.go's binding tuples ride d.itemMu). Handing the
	// method across here is the same shape as sampleFn/captureFn -- the alternative, a
	// back-pointer to the Daemon on the coreAPI, would let any coreAPI method reach the whole
	// assembly. Left nil, ApproveInteraction refuses rather than pretending: an approve
	// answered OK by a daemon that applied nothing dismisses the card on every surface
	// (IS-LIFE-2) while the CLI stays blocked.
	d.api.approve = d.approveInteraction
	// Wave R6 (chat.go): the complete-chat seams, handed across on the approve pattern.
	// Left nil, each coreAPI method refuses rather than pretending.
	d.api.composer = d.composerSend
	d.api.interrupt = d.interruptTurn
	d.api.history = d.interactionHistory
	d.api.detail = d.interactionDetail
	d.srv = protocol.NewServer(d.api, epID)
	d.controlled = d.srv.IsControlled // grid tap skips a session with a live controller (R1.3.7)

	// R-GW.8: the dedicated remote-tier listener the gateway dials. It binds its own
	// socket and accept loop (independent of the demuxed main UDS), and every connection
	// is remote-origin -- so mutating ops are authorized against the device registry via
	// coreAPI's DeviceAuthenticator (R-POL.9). Assembled AFTER the registry is wired so
	// the very first remote connection is already fail-closed.
	if cfg.RemoteSocketPath != "" {
		// Unlink a leftover socket from a crashed prior daemon before binding, as
		// daemon.bindSocket does for the main UDS (S12). Without it a stale remote.sock would
		// fail the bind and abort assembly below -- and since ADR-007 B15 every PROVISIONED
		// machine opens this socket, so one crash would stop the daemon starting at all: "swarm
		// is broken" rather than "remote is broken", a far worse failure than the one B15 fixes.
		//
		// Confined to paths that ARE sockets. The singleton flock taken by daemon.Open above
		// makes the reclaim safe, but that lock is on <stateDir>/daemon.lock while
		// RemoteSocketPath may be configured anywhere (SWARM_DAEMON_REMOTE_SOCK), so an
		// unconditional remove would reach past it -- destroying a regular file an operator
		// pointed at by mistake, and letting two daemons with different state dirs share one
		// override path, the second silently stealing the first's LIVE socket instead of
		// failing to bind. Crash debris is always a socket, so nothing is given up.
		if fi, lerr := os.Lstat(cfg.RemoteSocketPath); lerr == nil && fi.Mode()&os.ModeSocket != 0 {
			_ = os.Remove(cfg.RemoteSocketPath)
		}
		rs, rerr := protocol.ServeRemoteWithID(d.api, cfg.RemoteSocketPath, epID)
		if rerr != nil {
			return nil, rerr // defer'd cleanup tears down d.api + core
		}
		d.remoteSrv = rs
		// C2a: `swarm remote off` (or removing the last device) must proactively SEVER every live
		// remote control lease + terminal peek on the remote Server, not merely pause per-keystroke
		// input. Wire the coreAPI kill-switch setter to the remote Server's teardown seam. Set
		// before close(d.ready) below, so no served op can read the observer before it is wired.
		d.api.SetRemoteControlObserver(rs.SeverAllRemoteControl)
		// nx44.7: the owner's roster shows which sessions a PAIRED DEVICE is driving.
		// The lease lives on the remote-tier Server (a phone's take_control registers
		// through its attach path), so both readers source it from rs, not d.srv --
		// d.srv's own leases are the owner's attaches, which are never "remote".
		// Two readers because they answer different questions: the protocol Server
		// stamps the value onto every SessionView it hands out, while the roster poller
		// needs it in its diff key or a control flip -- which changes nothing the core
		// persists -- would fan out no event at all.
		d.srv.SetRemoteControlledFunc(rs.IsControlled)
		d.api.SetRemoteControlledFunc(rs.IsControlled)
	}

	// ADR-010 Amendment 3 C2..C4: the passive supervisor, over the owner-tier Server's
	// SendInput (C3's serialized write seam) and every controller lease, owner or remote
	// (a human at the source's controls is never interrupted). Constructed AFTER both
	// Servers exist -- its delivery goroutine may read d.remoteSrv at once -- so the
	// records a prior incarnation left are the reconcile-time arming: registerSession
	// fired for the reconnected sessions during daemon.Open above, when d.sup was still
	// nil, and the durable record is what a re-arm would have kept anyway. A record dir
	// that cannot be opened aborts assembly like every other component's store.
	sup, err := newSupervisor(epID, filepath.Join(cfg.StateDir, "supervision"), supervisionRetry,
		d.core.Get, d.anyControlled, d.srv.SendInput)
	if err != nil {
		return nil, err // defer'd cleanup tears down d.api + core
	}
	d.sup = sup
	// C5: both readers of the live pending flag, for the same reason both remote-control
	// readers exist above -- the Server stamps SessionView.SupervisionPending, the roster
	// poller needs it in its diff key or a flip (which changes no persisted meta) would
	// fan out no event.
	d.srv.SetSupervisionPendingFunc(d.sup.pending)
	d.api.SetSupervisionPendingFunc(d.sup.pending)

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go d.eng.Run(ctx) // the ONLY periodic driver (E10.8); idle when PollInterval<=0
	// tapGrids is the sole caller of sampleGridAsync (the only sampleWG Adder), so
	// Close waits for it to RETURN before draining sampleWG — an Add must never race
	// a Wait (F7).
	d.tapWG.Add(1)
	go func() { defer d.tapWG.Done(); d.tapGrids(ctx) }() // shim->engine output tap (seam b)
	go d.releaseInteractions(ctx)                         // ADR-010 §7's append floor clock (interaction.go)

	// Every session reconcile reconnected got its engine registration during
	// daemon.Open, before d.core existed to read its hook channel from; start their
	// drain loops now (hookdrainloop.go).
	d.startHookDrainsForRunning()
	// ...and their SESSION BACKENDS, for exactly the same reason and at exactly the same
	// point (Wave R7 review BLOCKING 4, backendconnect.go). registerSession's own
	// connectSessionBackend cannot do it: it ran during daemon.Open, before d.core existed.
	d.connectBackendsForRunning()
	// ...and their CAPABILITY RECORDS (ADR-017 T2-a / D-NIL path 3), at this point and
	// not in registerSession, for the third time and the same reason: registerSession ran
	// during daemon.Open with d.core still nil, so it could not read whether a reconnected
	// session was launched with a structured backend plane -- and authoring the record
	// there would have pinned every reconnected Codex session at structured_chat=false
	// permanently, because T2 rule 2 makes that degrade one-way.
	d.authorCapabilitiesForRunning()

	assembled = true // success: the defer'd cleanup-unless-success must NOT tear anything down
	close(d.ready)   // assembly complete: the ConnHandler may now serve
	return d, nil
}

// registerSession is the daemon's OnSessionStart hook (Epic 11 seam a): it
// registers a launched session with the status engine under its per-session hook
// token, so an authenticated callback (S6) can drive its status. It fires at fresh
// launch (token from the launch path) and on reconcile after a restart (token
// re-read from the 0600 shim-launch.json, L2). The session's declared SignalSources
// come from its agent's registry adapter — that is how a real hook's event is
// normalized to a status dimension (the mapping bridge, seam c). The reserved dev
// "fake" agent has no adapter, so its sources are nil and only explicit-dimension
// callbacks / the grid heuristic drive it.
func (d *Daemon) registerSession(m persist.Meta, token string) {
	if d.eng == nil {
		return
	}
	var sources []adapter.SignalSource
	if ad, ok := registry.New(m.AgentType); ok {
		sources = ad.SignalSources()
	}
	// Register WITH the session's persisted status in ONE atomic op (C2/S7): at fresh
	// launch m.Status is the humble launch baseline; on reconcile after a restart it
	// is the last-persisted status, so the engine believes a persisted turn=active and
	// the staleness guard can downgrade a now-idle session. Folding the status into
	// RegisterSession closes the register->seed gap an early hook could fall into.
	// SINGLE-WRITER, ENFORCED (Wave R7, ADR-013 §R7.3). A session with a BACKEND has its
	// typed status driven by the in-process pump (Engine.ApplyTypedEvent), and registering it
	// with a hook token as well would give one high-water namespace two producers -- whose
	// failure mode is a SILENT DROP, not a warning. It is registered with NO token instead,
	// which makes HandleCallback refuse every callback for it outright (engine.go's empty-token
	// check) rather than leaving the exclusion to convention.
	//
	// It costs Codex nothing: it posts no hooks at all, and its typed rows have never fired
	// through that path.
	//
	// d.core IS NIL during reconcile: daemon.Open runs it synchronously and fires this
	// callback before Serve has assigned the core (see Serve's own comment on building the
	// engine first). A session adopted at reconcile therefore keeps its token here, and
	// connectSessionBackend below returns immediately for the same reason -- both are
	// disclosed as the "rejoin is not distinguished from a fresh join" residual in
	// docs/verification/r7-green/README.md rather than papered over with a nil-tolerant
	// lookup that would silently do the wrong thing.
	if d.core != nil {
		if _, hasBackend := d.core.SessionBackend(m.ID); hasBackend {
			token = ""
		}
	}
	d.eng.RegisterSession(m.ID, token, m.ShimPID, sources, m.Status)
	// ADR-017 T2-a / D-NIL: THE CORE'S OWN SESSION-START HOOK AUTHORS THE RECORD.
	// This fires for every session the core starts, whichever client asked -- so it is the
	// one place that covers a launch, a resume and a reconcile alike. The d.core guard is
	// the same one registerSession's own comment explains three paragraphs up: during
	// daemon.Open's reconcile this callback runs BEFORE d.core is assigned, and a record
	// authored there could not read whether a reconnected session has a structured backend
	// plane. authorCapabilitiesForRunning re-runs for exactly those sessions once the
	// assembly is complete.
	if d.core != nil {
		d.authorSessionCapabilitiesWhenDecided(m.ID, m.AgentType, m.ShimPID)
	}
	if d.sup != nil {
		d.sup.arm(m) // a passive handoff child gets its supervision record (ADR-010 Amendment 3 C2)
	}
	// The structured-capture channel's daemon half (playbook §6.1, hookdrainloop.go):
	// one drain loop per session with a shim-owned hook spool. Started here rather than
	// only at launch so a session whose shim OUTLIVED this daemon -- the case the whole
	// spool exists for -- is drained from the moment reconcile adopts it.
	d.startHookDrain(m.ID)
	// The SESSION BACKEND's join (Wave R7, backendconnect.go). Started here rather than only
	// at launch for the same reason the hook drain is: a session whose shim OUTLIVED this
	// daemon is exactly the case ADR-001 exists for, and it must be rejoined the moment
	// reconcile adopts it. A session with no backend returns immediately.
	d.connectSessionBackend(m.ID)
}

// authorCapabilitiesForRunning authors (or re-authors) the capability record of every
// session this daemon reconnected -- ADR-017 T2-a / D-NIL's third session-creation path.
//
// IT ADOPTS THE INSTANCE, IT DOES NOT MINT ONE. The shim did not restart, the PTY is the
// same PTY, and re-minting would show the phone an epoch reset on every daemon upgrade.
// A session that predates this ruling has no instance file at all and gets one now, which
// is the only moment the daemon can honestly say "this incarnation begins here".
//
// registerSessionCapabilities' merge is what makes this safe to run on every start: a
// prior incarnation's degrade -- and the terminal_control it withdrew -- survives.
func (d *Daemon) authorCapabilitiesForRunning() {
	if d.core == nil {
		return
	}
	for _, m := range d.core.List() {
		if m.Status.Process != status.ProcessRunning {
			continue
		}
		inst, ad, version, ok := d.sessionCapabilityInputs(m.ID, m.AgentType, m.ShimPID)
		if !ok {
			continue // no bindable instance: T2-a's honest status card
		}
		live, decided := d.backendPlaneDecided(m.ID, ad)
		if !decided {
			continue // the rejoin is still dialling; backend.go authors either outcome
		}
		if _, err := d.authorSessionCapabilities(m.ID, inst, m.AgentType, ad, version, adapterRevision, live); err != nil {
			log.Printf("skeleton: author capability record for reconciled session %s: %v", m.ID, err)
		}
	}
}

// registryAdapter is the production adapter resolver behind d.adapterFor: the ONE table
// mapping an agent name to its adapter (T-5/T-7), the same lookup registerSession and
// captureConversationIDGated make. It is a method rather than registry.New itself so the seam
// has one shape whether or not a test replaced it.
func (d *Daemon) registryAdapter(agentType string) (adapter.Adapter, bool) {
	return registry.New(agentType)
}

// emitStatus is the engine's late-bound emission sink (see Serve): it forwards an
// engine-derived status change to the coreAPI, which persists it through the
// daemon's sole meta writer (G6) and fans it out to subscribers (Epic 6). It is
// nil-guarded because the engine is constructed before the coreAPI exists; no emit
// fires in that window.
func (d *Daemon) emitStatus(id string, s status.Status) {
	// IS-LIFE-2's answered_locally: a session LEAVING the waiting interaction state with an
	// approval still pending is the owner having answered it at the machine, and the transition
	// is the only observation the daemon gets (approval.go).
	d.noteInteractionStatus(id, s.Interaction)
	if d.api != nil {
		d.api.emitStatus(id, s)
	}
	// After the persist above, so the supervisor's evaluation reads the CURRENT meta
	// (ADR-010 Amendment 3 C2). Cheap and synchronous; delivery is its own goroutine.
	if d.sup != nil {
		d.sup.signal(id)
	}
}

// anyControlled reports whether ANY controller lease -- an owner attach or a phone
// take_control -- is held on local: the supervisor never types into a session someone is
// driving (ADR-010 Amendment 3 C3). The remote Server may be absent (no remote listener).
func (d *Daemon) anyControlled(local string) bool {
	if d.srv.IsControlled(local) {
		return true
	}
	return d.remoteSrv != nil && d.remoteSrv.IsControlled(local)
}

// endSession is the daemon's OnSessionEnd hook: it retires an ended session's
// engine registration and token (S6). Ending an unregistered session (e.g. one
// adopted by reconcile, never registered) is a harmless no-op.
func (d *Daemon) endSession(id string) {
	// A final conversation-id capture before the engine retires the session: a
	// session attached-until-exit (the grid tap never sampled it) or a very
	// short-lived one still gets its id from the transcript tail on disk (C1). This
	// is sequential with the daemon's terminal write — finalizeTerminal has already
	// released writeMu before firing OnSessionEnd — so SetConversationID's writeMu is
	// never nested (no deadlock). Uses the Final variant (not captureConversationID):
	// this call always sees an already-terminal status, so the tap path's
	// Running-gate would silently no-op it every time (HIGH regression, C2 review).
	d.stopHookDrain(id) // the session's spool has no more producer (hookdrainloop.go)
	// The backend's frames have no more producer either: release whatever prose the fold is
	// holding (a turn's last words must not die in the pump), then drop the connection and
	// the pump state (backend.go).
	d.flushBackendFrames(id)
	d.forgetBackendPump(id)
	d.forgetBackend(id)
	d.captureConversationIDFinal(id)
	d.convScanMu.Lock()
	delete(d.convScan, id) // bound convScan to running sessions (R2.1.3 hygiene)
	d.convScanMu.Unlock()
	if d.eng != nil {
		d.eng.EndSession(id)
	}
	// IS-ST-2 / IS-LIFE-2: the agent instance is gone, so every item still `in_progress` is
	// closed `failed` before the session's terminal session_status, and a pending approval still
	// reaches its one resolution (approval.go). It runs BEFORE forgetInteractions, which drops
	// the state it sweeps.
	d.sweepSessionInteractions(id)
	// The interaction fold keys and the open turn name a CLI that is gone (interaction.go).
	d.forgetInteractions(id)
	// A child that ended is `completed` for its supervisor; a source that ended leaves its
	// children's events pending (ADR-010 Amendment 3 C4: no re-parenting).
	if d.sup != nil {
		d.sup.signal(id)
	}
}

// gridPoll is how often the assembly samples each running session's shim grid and
// feeds it to the engine's grid heuristic (seam b). R2.1.1 (committee-ruled): 500ms,
// a 2.5x cut from the former 200ms — NOT the 1s that would spend the whole L1
// change->delivery<=1s budget on the poll cadence alone and leave no headroom for
// fan-out (see TestTapLatency_GridChangeReachesSubscriberWithin1s).
const gridPoll = 500 * time.Millisecond

// tapGrids is the shim->engine output tap (Epic 11 seam b): on a low-frequency
// cadence it samples each running session's current shim grid and feeds it to
// engine.OnOutput, so the CLI-agnostic grid heuristic runs even for a session that
// emits no typed hook signal (T-3). It stops when ctx is cancelled (Close).
func (d *Daemon) tapGrids(ctx context.Context) {
	t := time.NewTicker(gridPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.tapOnce(ctx)
		}
	}
}

// tapOnce samples every running session once: it SKIPS a session that has a live
// controller lease so the tap never steals its stream (R1.3.7 — the shim now serves
// connections concurrently, so a tap attach on a controlled session would supersede
// the controller's subscriber every poll). Conversation-id capture reads the
// transcript on disk (no shim attach), so it runs regardless of the controller
// (C1), dispatched in its own per-session goroutine so a slow disk read for one
// session cannot delay reaching the next (R2.1.2).
func (d *Daemon) tapOnce(ctx context.Context) {
	for _, m := range d.core.List() {
		if m.Status.Process != status.ProcessRunning {
			continue
		}
		if d.controlled == nil || !d.controlled(m.ID) {
			d.sampleGridAsync(ctx, m.ID)
		}
		d.captureConversationIDAsync(ctx, m.ID) // attach-independent id capture (C1)
	}
}

// sampleGridAsync samples one session's grid in its OWN goroutine, so a slow shim
// cannot stall the sampling CADENCE of other sessions (L1, no head-of-line
// blocking: the former serial loop blocked every later session behind a busy
// shim's dial/hello). tapOnce already skips a controlled session (R1.3.7), so this
// only ever runs for a session with no live controller. It is deduped per session
// via asyncOnce: at most one in-flight sample each, so a persistently slow shim
// never piles up a fresh goroutine every poll.
func (d *Daemon) sampleGridAsync(ctx context.Context, id string) {
	d.asyncOnce(ctx, &d.sampleMu, d.sampling, &d.sampleWG, id, d.sampleFn)
}

// captureConversationIDAsync dispatches captureConversationID for one session in
// its own goroutine via asyncOnce (R2.1.2): a slow disk read for one uncaptured
// session cannot delay the tap loop reaching the next session. captureFn defaults
// to the real captureConversationID when unset, so a test Daemon literal that
// never sets it (most existing tests) still gets correct behavior.
func (d *Daemon) captureConversationIDAsync(ctx context.Context, id string) {
	fn := d.captureFn
	if fn == nil {
		fn = d.captureConversationID
	}
	d.captureMu.Lock()
	if d.capturing == nil {
		d.capturing = make(map[string]struct{})
	}
	d.captureMu.Unlock()
	d.asyncOnce(ctx, &d.captureMu, d.capturing, &d.captureWG, id, fn)
}

// asyncOnce runs fn(id) in its own goroutine, deduped against inFlight (at most
// one fn per id in flight at a time) and tracked in wg so Close can drain it. It
// is the shared per-session async-dispatch mechanism behind both grid sampling and
// conversation-id capture: a slow shim or a slow disk read for one session must
// never delay dispatching the next session's op (L1, no head-of-line blocking).
// ctx cancellation before dispatch is a no-op — Close is already tearing things
// down and nothing new should start.
func (d *Daemon) asyncOnce(ctx context.Context, mu *sync.Mutex, inFlight map[string]struct{}, wg *sync.WaitGroup, id string, fn func(string)) {
	mu.Lock()
	if _, busy := inFlight[id]; busy {
		mu.Unlock()
		return // an op for this session is already in flight
	}
	select {
	case <-ctx.Done():
		mu.Unlock()
		return
	default:
	}
	inFlight[id] = struct{}{}
	mu.Unlock()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			mu.Lock()
			delete(inFlight, id)
			mu.Unlock()
		}()
		fn(id)
	}()
}

// sampleGrid grabs one session's current shim grid and feeds it to the engine's
// grid heuristic, via coreAPI.SampleSnapshot: a NON-SUBSCRIBING snapshot_req
// against a capability-advertising shim (which cannot supersede a controller's
// stream no matter how it races an attach — the C3 tap-steal fix), or the
// attach-based sample against an old shim. tapOnce still skips a session with a
// live controller (R1.3.7) — cheap, and the sole safeguard on the old-shim path.
// A failed attach (a gone shim, or — before item 1.2 — an oversized snapshot the
// shim could not send in one frame) or an undecodable snapshot is retried next poll,
// but it is COUNTED and rate-limit-logged via noteTapFailure so the heuristic can no
// longer die silently (R1.2.6). A session not registered with the engine makes
// OnOutput a no-op.
func (d *Daemon) sampleGrid(id string) {
	snapBytes, err := d.api.SampleSnapshot(id)
	if err != nil {
		d.noteTapFailure(id, err)
		return
	}
	snap, err := vt.DecodeSnapshot(snapBytes)
	if err != nil {
		d.noteTapFailure(id, err)
		return
	}
	d.eng.OnOutput(id, snap)
}

// tapLogInterval rate-limits the grid-tap snapshot-failure log so a persistently
// failing session cannot flood the daemon log; every failure is still counted.
const tapLogInterval = 30 * time.Second

// noteTapFailure records a grid-tap attach/snapshot failure: it bumps the observable
// counter and emits a rate-limited log line, so a tap that can no longer read a
// session's snapshot is never silent (R1.2.6). Safe for concurrent samplers.
func (d *Daemon) noteTapFailure(id string, err error) {
	n := d.tapFailures.Add(1)
	now := time.Now().UnixNano()
	last := d.tapLastLog.Load()
	if now-last >= int64(tapLogInterval) && d.tapLastLog.CompareAndSwap(last, now) {
		log.Printf("skeleton: grid-tap snapshot failed for session %s (%d total): %v", id, n, err)
	}
}

// convTailBytes bounds the transcript tail read for conversation-id extraction.
const convTailBytes = 64 << 10

// convScanState is one session's transcript-scan bookkeeping for the growth-gated
// re-read in captureConversationID (R2.1.3).
type convScanState struct {
	size    int64 // transcript size as of the last scan
	errOnce bool  // a disk error has already been logged for this session (log-once)
}

// captureConversationID recovers a session's native conversation id from its output
// and persists it ONCE (Epic 11 / R-2, the id a later resume replays). It reads the
// session's TRANSCRIPT tail on disk (bounded) and feeds it to the session's adapter
// — INDEPENDENT of any live attach (C1). Because it reads the transcript file rather
// than attaching, it runs even for a session with a live controller (which the grid
// tap skips, R1.3.7) and for a session left attached until exit — both of which
// would otherwise never be sampled and end non-resumable.
//
// The tail is re-read only when the transcript's size has CHANGED since the last
// scan — grown, or shrunk (a rotation) — not on every poll (R2.1.3): a session that
// has gone quiet costs no further disk reads once its tail has already been
// scanned at its current size. A late-appearing id (the marker was not yet
// present at an earlier, smaller size) is still captured on the growth that
// introduces it — there is no permanent give-up while the session runs. A disk
// error (missing/unreadable transcript) is logged ONCE per session, never panics,
// and never wedges the loop: the next poll simply tries again. On a successful
// extraction it persists Meta.ConversationID through the daemon's sole meta writer
// (write-once, G6). Cheap no-op once captured, for an adapterless agent (the
// reserved fake), or when nothing extracts yet. SetConversationID takes writeMu,
// so it is never called nested inside another writeMu holder (finalizeTerminal has
// already released it before endSession runs).
//
// This is the tap-dispatched path (captureConversationIDAsync's default
// captureFn): it gates its convScan write on the session still being Running,
// which closes the LOW leak race where a delayed async write recreates
// convScan[id] after endSession already deleted it (agents-tracker-vyd). Use
// captureConversationIDFinal instead for endSession's OWN call — see there for
// why gating that one too silently disables the session-end capture net.
func (d *Daemon) captureConversationID(id string) {
	d.captureConversationIDGated(id, true)
}

// captureConversationIDFinal is endSession's session-end capture net (serve.go
// endSession, C1): it exists so a session that exits before any tap poll ever
// ran (e.g. one left attached until exit, or a very short-lived one) still gets
// its conversation id. Unlike captureConversationID, it does NOT gate its
// convScan write on Running status: production always commits a session's
// terminal status BEFORE firing OnSessionEnd, so that gate would ALWAYS see a
// terminal status here too and silently skip the write on every call — which is
// exactly the HIGH regression a C2 review caught (TestEndSession_
// CapturesConversationIDForShortLivedSession pins it). This is still leak-safe:
// endSession deletes convScan[id] immediately after this call returns (same
// goroutine, sequential, serve.go:239-241), and it is captureConversationID's
// OWN gate — evaluated at ITS write, against a status that is by then already
// terminal regardless of interleaving — that keeps a stale async write from
// recreating the entry afterward.
func (d *Daemon) captureConversationIDFinal(id string) {
	d.captureConversationIDGated(id, false)
}

// captureConversationIDGated is the shared body: gateRunning selects whether
// the convScan write additionally requires the session to still be Running
// (the tap path) or always proceeds (endSession's final call).
func (d *Daemon) captureConversationIDGated(id string, gateRunning bool) {
	m, ok := d.core.Get(id)
	if !ok || m.ConversationID != "" {
		return
	}
	ad, ok := registry.New(m.AgentType)
	if !ok {
		return
	}
	path := filepath.Join(d.stateDir, id, shim.TranscriptFile)
	fi, err := os.Stat(path)
	if err != nil {
		d.noteConvScanError(id)
		return
	}
	size := fi.Size()

	d.convScanMu.Lock()
	if d.convScan == nil {
		d.convScan = make(map[string]convScanState)
	}
	if d.convScan[id].size == size {
		d.convScanMu.Unlock()
		return // unchanged since the last scan: nothing new to extract
	}
	d.convScanMu.Unlock()

	tail := readTail(path, convTailBytes)

	d.convScanMu.Lock()
	if gateRunning {
		// This read can finish after endSession already deleted convScan[id]
		// (R2.1.3 hygiene); a stale write here would resurrect the entry
		// forever — nothing polls an ended session again (LOW leak race,
		// agents-tracker-vyd).
		if m, ok := d.core.Get(id); !ok || m.Status.Process != status.ProcessRunning {
			d.convScanMu.Unlock()
			return
		}
	}
	d.convScan[id] = convScanState{size: size} // also clears errOnce: the error cleared
	d.convScanMu.Unlock()

	if len(tail) == 0 {
		return
	}
	convID, ok := ad.ExtractConversationID(nil, tail)
	if !ok || convID == "" {
		return
	}
	_ = d.core.SetConversationID(id, convID)
}

// noteConvScanError log-once's a transcript-scan disk error for a session
// (R2.1.3): repeated failures stay silent after the first so a persistently
// unreadable transcript cannot flood the log. The next poll retries regardless —
// scan state is left untouched, so a later successful stat is still treated as a
// fresh change.
func (d *Daemon) noteConvScanError(id string) {
	d.convScanMu.Lock()
	defer d.convScanMu.Unlock()
	if d.convScan == nil {
		d.convScan = make(map[string]convScanState)
	}
	st := d.convScan[id]
	if st.errOnce {
		return
	}
	st.errOnce = true
	d.convScan[id] = st
	log.Printf("skeleton: conversation-id transcript scan failed for session %s", id)
}

// readTail is the transcript-tail reader captureConversationID calls on a growth-
// gated re-read; production points it at readTranscriptTail, tests substitute a
// call-counting wrapper to verify an unchanged transcript size skips the read
// entirely (R2.1.3).
var readTail = readTranscriptTail

// readTranscriptTail returns up to the last n bytes of the file at path — the raw
// agent output the adapter's ExtractConversationID scans. A missing/short file
// yields what is there; any error yields nil.
func readTranscriptTail(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	off := int64(0)
	if fi.Size() > n {
		off = fi.Size() - n
	}
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil
	}
	return buf
}

// SocketPath is the path clients dial (the daemon's singleton socket).
func (d *Daemon) SocketPath() string { return d.socketPath }

// Core exposes the underlying lifecycle authority — the walking-skeleton launch
// seam the in-process tests drive directly.
func (d *Daemon) Core() *daemon.Daemon { return d.core }

// Close tears the assembly down cleanly: stop the engine, stop the core (which
// closes the socket and releases the singleton lock so a fresh daemon can take
// over), disconnect clients, and stop the roster poller. Running shims are
// independent and survive (S1). It is idempotent.
func (d *Daemon) Close() error {
	d.closeOnce.Do(func() {
		close(d.closing)
		d.cancel()                   // stops tapGrids + engine.Run: no NEW grid samples/captures start
		d.tapWG.Wait()               // tapGrids returned: no more sampleWG/captureWG.Add can race the Wait (F7)
		d.sampleWG.Wait()            // drain in-flight grid samples (bounded by shim timeouts)
		d.captureWG.Wait()           // drain in-flight conversation-id captures
		d.stopHookDrains()           // join every hook-spool drain loop BEFORE the core it applies through goes away
		d.drainPendingInteractions() // flush anything the append floor is still holding (below)
		if d.sup != nil {
			d.sup.close() // no supervision send may start once the Server below is closing
		}
		_ = d.core.Close() // stops accepting new connections; releases the lock
		_ = d.srv.Close()  // disconnects clients; drains the per-connection loops
		if d.remoteSrv != nil {
			_ = d.remoteSrv.Close() // tears down the remote-tier listener + its connections
		}
		d.api.close() // stops the roster poller
	})
	return nil
}

// drainPendingInteractionsTimeout bounds drainPendingInteractions so a wedged Append
// (e.g. a stalled disk) cannot hang Close forever.
const drainPendingInteractionsTimeout = 5 * time.Second

// drainPendingInteractions flushes every item the append floor (ADR-010 §7,
// interaction.go) is still holding when the daemon closes. releaseInteractions' own
// ticker is what normally drives this, but it is cancelled (with the rest of Close's
// ctx-scoped work) just above, and Flush releases at most one item per call, spaced
// by the floor's own window -- so without this, a session's already-ingested item
// offered less than one window before Close would be silently lost. Playbook §6.1's
// "daemon unavailability neither fails a provider hook nor loses an accepted item"
// applies exactly as much to a graceful shutdown as to a crash, and
// TestHookDrainer_RestartMidDrain_ResumesExactlyOnceWithNoDuplicateOrLoss
// (r6_hookdrain_test.go) pins exactly this: a daemon incarnation that Closes with
// items still in flight must not lose them out from under the NEXT incarnation
// reading the same on-disk journal.
//
// A deadline that passes with items still pending is logged (not silently dropped):
// an operator can see a shutdown that genuinely could not flush everything in time,
// rather than the loss being indistinguishable from a clean exit.
func (d *Daemon) drainPendingInteractions() {
	if d.items == nil {
		return
	}
	deadline := time.Now().Add(drainPendingInteractionsTimeout)
	for d.items.Pending() > 0 && time.Now().Before(deadline) {
		if err := d.items.Flush(); err != nil {
			log.Printf("skeleton: flush pending interaction item at close: %v", err)
		}
		time.Sleep(remotegw.DefaultAppendWindow)
	}
	if n := d.items.Pending(); n > 0 {
		log.Printf("skeleton: close deadline reached with %d pending interaction item(s) still unflushed", n)
	}
}

// reconcilePairedDevices clears, at startup (single-threaded, before close(d.ready), so no pairing
// races it), any device that a crash left in an incoherent state:
//
//   - Finding 5 (round-4): a device whose sealed grant sidecar is ABSENT was never fully paired --
//     a crash between devices.AddSole and grant.Save (pairing.go) left it registered with no
//     deliverable bootstrap grant, holding the single-device slot yet unrecoverable except by
//     revoke. Removing it on load frees the slot.
//   - Finding 3 (round-5, codex#1): a device whose GrantedEpoch != the CURRENT machine epoch was
//     granted under a dead epoch -- a crash after rotateEpoch persisted N+1 but before Remove
//     persisted leaves the revoked device (GrantedEpoch==N) registered. Left in place, remote
//     control re-enables and a still-running old-epoch gateway resumes sealing under N to the
//     revoked phone. Clearing it on load closes that residual confidentiality window.
//
// Fail-safe throughout: a sidecar-load ERROR (corrupt, unreadable) leaves the device untouched, and
// the epoch check only fires on a CONFIRMED mismatch -- an unreadable machine identity (epochOK
// false) leaves every device's epoch untouched, mirroring the missing-grant reconcile's fail-safe
// gate. Only a definitively absent sidecar or a definitively stale epoch clears a slot.
//
// Finding 2 (round-6, codex#2): the stale-epoch removal must fail CLOSED. Registry.Remove restores
// the stale device in memory on a persistence failure, so discarding its (removed, err) would let
// Serve open remote.sock still serving a CONFIRMED-stale record -- defeating the crash-confidentiality
// reconcile. When a stale-epoch device cannot be removed (removed==false), return an error so Serve
// ABORTS assembly. A committed post-rename dir-fsync error (removed==true) has already dropped the
// device from the live set, so it is tolerated like the missing-grant path below.
func reconcilePairedDevices(reg *device.Registry, stateDir string) error {
	registryDir := filepath.Join(stateDir, "devices")
	curEpoch, epochOK := currentMachineEpoch(stateDir)
	for _, rec := range reg.List() {
		if epochOK && rec.GrantedEpoch != curEpoch {
			if removed, err := reg.Remove(rec.DeviceID); !removed { // stale epoch: the revoked device's content key is dead
				return fmt.Errorf("reconcile: stale-epoch device %q could not be removed (still registered): %w", rec.DeviceID, err)
			}
			continue
		}
		g, err := grant.Load(registryDir, rec.DeviceID)
		if err != nil {
			continue // ambiguous read: leave the device alone (fail-safe)
		}
		if g == nil {
			_, _ = reg.Remove(rec.DeviceID) // no sidecar => not fully paired: free the slot
		}
	}
	return nil
}

// currentMachineEpoch loads the current epoch id from the machine identity (Finding 3). It reports
// ok=false on ANY read/parse error so the caller leaves devices untouched (fail-safe): a stale-epoch
// removal must fire only on a CONFIRMED mismatch, never on an unreadable identity.
func currentMachineEpoch(stateDir string) (uint32, bool) {
	id, err := machineid.Load(filepath.Join(stateDir, "remote", remoteIdentityFile))
	if err != nil {
		return 0, false
	}
	return id.EpochID(), true
}

// endpointID derives the daemon's stable federation endpoint id from its state
// dir: deterministic (unchanged across restarts of the same daemon) and distinct
// per daemon (distinct state dirs). The short hash keeps namespaced ids compact.
func endpointID(stateDir string) string {
	sum := sha256.Sum256([]byte(stateDir))
	return "ep-" + hex.EncodeToString(sum[:4])
}

// preLaunchWorktree creates an isolated git worktree for a session that opted into
// isolation via the worktree flag (Epic 12), returning it as the agent's working
// directory. A session without the flag is untouched. The gate keeps the hook a
// generic no-op for every non-worktree launch.
func preLaunchWorktree(id string, spec daemon.LaunchSpec) (string, error) {
	if spec.Options[protocol.OptionWorktree] != "true" {
		return "", nil
	}
	return worktree.Create(spec.Cwd, id)
}

// preDeleteWorktree tears down a worktree-isolated session's worktree on delete.
// m.Cwd is the original launch cwd (the repo), not the overridden agent cwd.
func preDeleteWorktree(m persist.Meta) error {
	if m.LaunchOptions[protocol.OptionWorktree] != "true" {
		return nil
	}
	return worktree.Remove(m.Cwd, m.ID)
}
