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
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
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

	cancel context.CancelFunc // stops engine.Run

	ready   chan struct{} // closed once the assembly is wired; gates the ConnHandler
	closing chan struct{} // closed by Close; aborts a connection still waiting on ready

	closeOnce sync.Once
}

// Serve performs the full assembly and begins serving on cfg.SocketPath. On
// success the daemon is live: clients can Dial the socket, launch/list/attach, and
// hook posts route to the engine. The caller owns the returned *Daemon and closes
// it with Close.
func Serve(cfg Config) (*Daemon, error) {
	d := &Daemon{
		socketPath: cfg.SocketPath,
		ready:      make(chan struct{}),
		closing:    make(chan struct{}),
	}

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
	d.api = newCoreAPI(core, cfg.FakeAgentBin)

	d.eng = engine.New(engine.Config{
		StalenessThreshold: cfg.StalenessThreshold,
		PollInterval:       cfg.PollInterval,
		Emit:               d.api.emitStatus, // engine status changes reach subscribers
	})
	// The daemon is one federation endpoint with a STABLE id derived from its
	// persistent home, so a session's namespaced id is identical for every client
	// and unchanged across restarts (a session launched by one client is the same
	// id a later client — or the same daemon after a kill/restart — lists).
	d.srv = protocol.NewServer(d.api, endpointID(cfg.StateDir))

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go d.eng.Run(ctx)  // the ONLY periodic driver (E10.8); idle when PollInterval<=0
	go d.tapGrids(ctx) // the shim->engine output tap (seam b)

	close(d.ready) // assembly complete: the ConnHandler may now serve
	return d, nil
}

// registerSession is the daemon's OnSessionStart hook (Epic 11 seam a): it
// registers a freshly-launched session with the status engine under its
// per-session hook token, so an authenticated callback (S6) can drive the
// session's status. The token arrives here privately from the daemon launch path
// and is never persisted. Signal sources are left nil: the generic grid heuristic
// (seam b) and typed hooks need none; a real adapter (Epic 11) supplies them.
func (d *Daemon) registerSession(m persist.Meta, token string) {
	if d.eng != nil {
		d.eng.RegisterSession(m.ID, token, m.ShimPID, nil)
	}
}

// endSession is the daemon's OnSessionEnd hook: it retires an ended session's
// engine registration and token (S6). Ending an unregistered session (e.g. one
// adopted by reconcile, never registered) is a harmless no-op.
func (d *Daemon) endSession(id string) {
	if d.eng != nil {
		d.eng.EndSession(id)
	}
}

// gridPoll is how often the assembly samples each running session's shim grid and
// feeds it to the engine's grid heuristic (seam b). It is well within the L1 <=1s
// bound and mirrors the roster event cadence.
const gridPoll = 200 * time.Millisecond

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
			for _, m := range d.core.List() {
				if m.Status.Process == status.ProcessRunning {
					d.sampleGrid(m.ID)
				}
			}
		}
	}
}

// sampleGrid grabs one session's current shim grid and feeds it to the engine's
// grid heuristic. The attach is closed IMMEDIATELY after the snapshot is read, so
// it holds the shim's single serve slot only for the few milliseconds of the
// snapshot round-trip — a racing client attach simply waits that long rather than
// failing. Every failure is a silent skip: a session a client is actively attached
// to holds the shim (so the sample's dial times out and is ignored), a gone shim
// or undecodable snapshot is retried next poll, and a session not registered with
// the engine makes OnOutput a no-op.
func (d *Daemon) sampleGrid(id string) {
	stream, err := d.api.Attach(id)
	if err != nil {
		return
	}
	snapBytes := stream.Snapshot()
	_ = stream.Close()
	snap, err := vt.DecodeSnapshot(snapBytes)
	if err != nil {
		return
	}
	d.eng.OnOutput(id, snap)
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
		d.cancel()
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
