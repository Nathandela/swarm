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
}

// Daemon is the assembled, running walking skeleton: the core lifecycle daemon,
// the protocol server bound to its socket, the status engine, and the roster
// event source, with one Close that tears all four down cleanly.
type Daemon struct {
	core       *daemon.Daemon
	srv        *protocol.Server
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
	})
	if err != nil {
		return nil, err
	}
	d.core = core
	d.api = newCoreAPI(core, cfg.FakeAgentBin, epID)
	d.srv = protocol.NewServer(d.api, epID)
	d.controlled = d.srv.IsControlled // grid tap skips a session with a live controller (R1.3.7)

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go d.eng.Run(ctx) // the ONLY periodic driver (E10.8); idle when PollInterval<=0
	// tapGrids is the sole caller of sampleGridAsync (the only sampleWG Adder), so
	// Close waits for it to RETURN before draining sampleWG — an Add must never race
	// a Wait (F7).
	d.tapWG.Add(1)
	go func() { defer d.tapWG.Done(); d.tapGrids(ctx) }() // shim->engine output tap (seam b)

	close(d.ready) // assembly complete: the ConnHandler may now serve
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
	d.eng.RegisterSession(m.ID, token, m.ShimPID, sources, m.Status)
}

// emitStatus is the engine's late-bound emission sink (see Serve): it forwards an
// engine-derived status change to the coreAPI, which persists it through the
// daemon's sole meta writer (G6) and fans it out to subscribers (Epic 6). It is
// nil-guarded because the engine is constructed before the coreAPI exists; no emit
// fires in that window.
func (d *Daemon) emitStatus(id string, s status.Status) {
	if d.api != nil {
		d.api.emitStatus(id, s)
	}
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
	d.captureConversationIDFinal(id)
	d.convScanMu.Lock()
	delete(d.convScan, id) // bound convScan to running sessions (R2.1.3 hygiene)
	d.convScanMu.Unlock()
	if d.eng != nil {
		d.eng.EndSession(id)
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
// grid heuristic. The attach is closed IMMEDIATELY after the snapshot is read. The
// shim serves connections concurrently, so this brief tap attach coexists with any
// other connection; tapOnce only calls sampleGrid for a session with NO live
// controller (R1.3.7), so the tap never supersedes a controller's subscriber.
// A failed attach (a gone shim, or — before item 1.2 — an oversized snapshot the
// shim could not send in one frame) or an undecodable snapshot is retried next poll,
// but it is COUNTED and rate-limit-logged via noteTapFailure so the heuristic can no
// longer die silently (R1.2.6). A session not registered with the engine makes
// OnOutput a no-op.
func (d *Daemon) sampleGrid(id string) {
	stream, err := d.api.Attach(id)
	if err != nil {
		d.noteTapFailure(id, err)
		return
	}
	snapBytes := stream.Snapshot()
	_ = stream.Close()
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
	defer f.Close()
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
		d.cancel()         // stops tapGrids + engine.Run: no NEW grid samples/captures start
		d.tapWG.Wait()     // tapGrids returned: no more sampleWG/captureWG.Add can race the Wait (F7)
		d.sampleWG.Wait()  // drain in-flight grid samples (bounded by shim timeouts)
		d.captureWG.Wait() // drain in-flight conversation-id captures
		_ = d.core.Close() // stops accepting new connections; releases the lock
		_ = d.srv.Close()  // disconnects clients; drains the per-connection loops
		d.api.close()      // stops the roster poller
	})
	return nil
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
