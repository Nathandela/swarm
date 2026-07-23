package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// ErrMaxSessions is returned by Launch when the daemon is at its configured
// concurrent-session cap; the message names the cap value (S-7).
var ErrMaxSessions = errors.New("daemon: max sessions reached")

// procStartTimeFn is the seam for reading a just-spawned shim's process-start-time
// in launch; tests override it to inject a post-spawn identity-read failure (F2).
var procStartTimeFn = processStartTime

// killSpawnedShim tears down a just-spawned shim whose launch is aborting before
// its supervisor started (F2/N2). It SIGTERMs the shim: the shim's own signal
// handler runs the agent's process-group TERM->grace->KILL before exiting (Fix A in
// internal/shim, armed BEFORE the agent is spawned), so the shim exiting implies the
// agent group was killed first — no socket dependency and no startup/acceptLoop-
// window race. We wait bounded for the shim to exit; only if it does not do we
// SIGKILL it as a last resort (the uncatchable residual) and report that containment
// was not confirmed, so the caller can log/escalate rather than silently orphan.
func (d *Daemon) killSpawnedShim(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	termErr := syscall.Kill(pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }() // reap in all paths
	if termErr != nil {
		<-done // already gone (ESRCH): its own exit path contained the agent
		return nil
	}
	select {
	case <-done:
		return nil // shim exited ⇒ its handler killed the agent group first
	case <-time.After(deleteWait):
		_ = cmd.Process.Kill() // last resort: uncatchable SIGKILL of the shim itself
		<-done
		return fmt.Errorf("daemon: shim %d did not exit on SIGTERM within %s; SIGKILLed as last resort — agent containment not confirmed", pid, deleteWait)
	}
}

// launchPhase marks a two-phase-launch boundary for the crash-injection seam.
type launchPhase int

const (
	phaseReserved  launchPhase = iota // reservation meta persisted; no shim yet
	phaseSpawned                      // shim spawned; identity recorded
	phaseConfirmed                    // shim confirmed serving
)

// launchProbe is invoked at each phase boundary (test seam E5.4/S11). A non-nil
// error models a daemon CRASH: launch aborts and returns it WITHOUT any cleanup,
// exactly as a kill -9 would — the spawned shim (if any) keeps running, and
// reconciliation on the next Open resolves the orphan/phantom.
type launchProbe func(phase launchPhase, m persist.Meta) error

// shimLaunchConfigFile is the per-session `swarm shim --config` JSON the daemon
// writes at spawn (0600). Besides the argv/env/socket it carries the per-session
// hook token (in Env), which reconcile re-reads to re-register a reconnected
// session with the engine across a daemon restart (L2, ADR-004).
const shimLaunchConfigFile = "shim-launch.json"

// shimGrace is the TERM->KILL grace window handed to each spawned shim.
const shimGrace = 2 * time.Second

// launchConfirmTimeout bounds phase 3 (waiting for the shim to serve its socket).
const launchConfirmTimeout = 15 * time.Second

// shimSpawnConfig is the `swarm shim --config` JSON schema (mirrors cmd/swarm's
// contract). The daemon is the writer; the shim decodes it.
type shimSpawnConfig struct {
	SessionID  string   `json:"session_id"`
	Argv       []string `json:"argv"`
	Cwd        string   `json:"cwd"`
	Env        []string `json:"env"`
	SocketPath string   `json:"socket_path"`
	SessionDir string   `json:"session_dir"`
	Cols       int      `json:"cols"`
	Rows       int      `json:"rows"`
	GraceMS    int      `json:"grace_ms"`
}

// Launch starts a new session (Launch == launch(spec, nil)).
func (d *Daemon) Launch(spec LaunchSpec) (persist.Meta, error) {
	return d.launch(spec, nil)
}

// ClaimOperation claims operationID as single-use through the durable two-phase
// idempotency store (slice A5-c), for a remote op that — unlike launch — has NO
// re-drivable side effect. It Prepares the record (fsync'd before the caller acts) and
// surfaces whether the key ALREADY existed; a true `existed` is a REPLAY the caller must
// refuse. The record is left `prepared` deliberately — it is the durable "this
// operation_id was consumed" marker, and the launch-only stale-record sweep
// (resolveStaleLaunches) ignores non-launch actions, so no terminal transition (Begin/
// Complete) is needed for a take_control claim.
func (d *Daemon) ClaimOperation(operationID, action, session string) (bool, error) {
	_, existed, err := d.idem.Prepare(operationID, action, session)
	return existed, err
}

// ClaimIdempotentOp is the durable backing of protocol.IdempotentExecutor for replay-safe
// remote kill/delete (slice DHI-3). A fresh op Prepares (existed=false) and the caller then
// executes + CommitIdempotentOp; a replay returns the ORIGINAL attempt's cached outcome:
// completed => priorOK=true, failed => priorOK=false (a cached failure, never a false
// success). A record still prepared/executing means a crash struck mid-op — kill/delete are
// self-idempotent, so it is reported as not-existed and safe to re-run.
func (d *Daemon) ClaimIdempotentOp(op, action, session string) (existed, priorOK bool, err error) {
	rec, existed, err := d.idem.Prepare(op, action, session)
	if err != nil || !existed {
		return existed, false, err
	}
	switch rec.Phase {
	case idempotency.PhaseCompleted:
		return true, true, nil
	case idempotency.PhaseFailed:
		return true, false, nil
	default:
		return false, false, nil // prepared/executing (crash mid-op): safe to re-run
	}
}

// CommitIdempotentOp records the terminal outcome of a claimed kill/delete durably: a
// success transitions the record -> completed, a failure -> failed, so a later replay
// surfaces that exact outcome via ClaimIdempotentOp.
func (d *Daemon) CommitIdempotentOp(op string, ok bool) error {
	if ok {
		return d.idem.Complete(op, nil)
	}
	return d.idem.Fail(op, nil)
}

// launch is the two-phase, crash-safe launch (E5.4/S11): reserve a running meta,
// spawn the shim with a deterministic socket and filtered env, then confirm it is
// serving. The probe (if any) fires at each boundary and its error aborts WITHOUT
// cleanup, modelling a crash whose orphan/phantom reconcile later resolves.
func (d *Daemon) launch(spec LaunchSpec, probe launchProbe) (persist.Meta, error) {
	// Cap check + id reservation, atomically, BEFORE any spawn (S-7): the rejected
	// launch must grow nothing and spawn nothing.
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return persist.Meta{}, errors.New("daemon: closed")
	}
	if d.liveCountLocked() >= d.cfg.MaxSessions {
		d.mu.Unlock()
		return persist.Meta{}, fmt.Errorf("%w: at capacity (max %d sessions)", ErrMaxSessions, d.cfg.MaxSessions)
	}
	id := d.freshIDLocked()
	now := time.Now()
	m := persist.Meta{
		ID:            id,
		AgentType:     spec.AgentType,
		Cwd:           spec.Cwd,
		LaunchOptions: spec.Options,
		Env:           persist.FilterEnv(spec.ClientEnv),
		CreatedAt:     now,
		LastActivity:  now,
		ResumedFrom:   spec.ResumedFrom, // link a resume-as-new-session launch (R-2)
		Status:        status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
	}
	s := &session{meta: m, stop: make(chan struct{})}
	d.sessions[id] = s // reserve the slot so a concurrent launch counts it against the cap
	d.mu.Unlock()

	// Remote launch idempotency (R-IDP.2/.3, A3): persist the operation_id as part of
	// the reservation so a replayed launch reuses the reserved session and spawns
	// nothing. Prepare is mutex-guarded, so a concurrent double-launch has exactly one
	// winner; the loser drops its fresh reservation and returns the cached session. The
	// reservation has touched only d.sessions (no disk yet), so dropReserved is a clean
	// abort here.
	if spec.OperationID != "" {
		_, existed, perr := d.idem.Prepare(spec.OperationID, "launch", id)
		if perr != nil {
			d.dropReserved(id)
			return persist.Meta{}, fmt.Errorf("daemon: idempotency prepare for %s: %w", id, perr)
		}
		if existed {
			// Replay of a known operation_id. The signal is LIVENESS, not phase: return
			// the recorded session only if it is still usable; a MISSING (W1) or LOST
			// (W3) session means the prior attempt crashed mid-launch and left no usable
			// session, so re-point the key at THIS fresh reservation and re-drive rather
			// than poison the key (W1) or return the dead corpse as success (W3).
			redrive, cached, rerr := d.resolveReplay(spec.OperationID, id)
			if rerr != nil {
				d.dropReserved(id)
				return persist.Meta{}, rerr
			}
			if !redrive {
				d.dropReserved(id)
				return cached, nil
			}
			// ponytail: the re-drive spawns a fresh session under the same operation_id.
			// SAFETY CEILING (window W4, NOT "no worse" than before): if the lost session
			// were actually a LIVE orphan shim (reconcile marked it LOST only because it
			// could not match the orphan's identity, meta ShimPID=0), this re-drive spawns
			// a SECOND live agent while the orphan keeps running — two code-editing agents
			// racing on one cwd, and unbounded under repeated crash+replay (each cycle can
			// leave another unreapable orphan). For the code-editing threat model that is
			// arguably WORSE than the pre-fix corpse+one-orphan, not neutral. Closing it
			// needs orphan-process tracking (persist the shim PID before/around cmd.Start,
			// then SIGTERM the prior attempt on re-drive — collapsing W4 into W3); tracked
			// as follow-up 4c and by the skipped TestLaunchCrashReplay_W4_LiveOrphanAgent_TODO.
			// Fall through and re-drive with our reservation `id`, now the operation_id's session.
		}
	}

	// Epic 12: an optional pre-launch hook (e.g. worktree isolation) may override
	// the AGENT's working directory. m.Cwd above already captured the caller's
	// spec.Cwd, so overriding spec.Cwd here reaches only the later spawnShim call,
	// not the persisted meta. Nothing has touched disk yet, so on error dropping
	// the reservation is a clean abort — no orphan. preLaunchOK tracks whether the
	// hook actually ran and succeeded: every later rollback in this function must
	// compensate via PreDelete when it did (F2), since dropReserved erases the
	// meta and no future Delete() could otherwise ever reach this id again.
	preLaunchOK := false
	if d.cfg.PreLaunch != nil {
		cwd, err := d.cfg.PreLaunch(id, spec)
		if err != nil {
			d.dropReserved(id)
			return persist.Meta{}, fmt.Errorf("daemon: pre-launch hook for %s: %w", id, err)
		}
		preLaunchOK = true
		if cwd != "" {
			spec.Cwd = cwd
		}
	}

	dir := d.sessionDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.rollbackReserved(id, m, preLaunchOK)
		return persist.Meta{}, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		d.rollbackReserved(id, m, preLaunchOK)
		return persist.Meta{}, err
	}

	// Phase 1 — reserve: persist the running meta before any shim exists.
	if err := d.saveMeta(m); err != nil {
		d.rollbackReserved(id, m, preLaunchOK)
		return persist.Meta{}, err
	}
	if probe != nil {
		if err := probe(phaseReserved, m); err != nil {
			return m, err // crash: no cleanup — reconcile resolves the reserved phantom
		}
	}

	// Phase 2 — spawn: launch the shim with the deterministic socket + filtered env,
	// plus a fresh per-session hook token injected into the agent env (E10.1/G4).
	sock := shimSocketPath(d.cfg.StateDir, id)
	token, terr := newHookToken()
	if terr != nil {
		d.rollbackReserved(id, m, preLaunchOK)
		return persist.Meta{}, terr
	}
	cmd, err := d.spawnShim(id, spec, sock, dir, token)
	if err != nil {
		d.rollbackReserved(id, m, preLaunchOK)
		return persist.Meta{}, err
	}
	m.ShimPID = cmd.Process.Pid

	// Record the shim identity as EARLY as possible — before the shim spawns its
	// agent — so a daemon crash any time after the agent exists still leaves a
	// reconnectable meta (S1/L2: reconcile matches by (PID, start-time)). Deferring
	// this until after waitShimServing would open a window where a LIVE agent has no
	// persisted identity and is wrongly marked lost on the next Open.
	//
	// A read/persist failure makes the shim un-trackable (persisting ShimStartTime=0
	// would let a later Open mark this live shim lost), so we abort and clean up. The
	// cleanup is race-free even this early: killSpawnedShim SIGTERMs the shim, whose
	// own signal handler contains its agent group before exiting (F2/N2).
	st, sterr := procStartTimeFn(m.ShimPID)
	if sterr != nil {
		if kerr := d.killSpawnedShim(cmd); kerr != nil {
			d.logf("launch %s: abort cleanup: %v", id, kerr)
		}
		d.rollbackReserved(id, m, preLaunchOK)
		return persist.Meta{}, fmt.Errorf("daemon: record shim identity for %s: %w", id, sterr)
	}
	m.ShimStartTime = st
	if err := d.saveMeta(m); err != nil {
		if kerr := d.killSpawnedShim(cmd); kerr != nil {
			d.logf("launch %s: abort cleanup: %v", id, kerr)
		}
		d.rollbackReserved(id, m, preLaunchOK)
		return persist.Meta{}, fmt.Errorf("daemon: persist shim identity for %s: %w", id, err)
	}
	d.wg.Add(1)
	go d.superviseLaunched(id, cmd, s.stop)

	// Register the session with the status engine (Epic 11 seam a): the assembly's
	// OnSessionStart hook installs the session's per-session hook token so an
	// authenticated `swarm hook` callback can drive its status. The token is never
	// persisted, so this synchronous hand-off is the sole path by which the engine
	// learns it. Fired after the shim identity is persisted, so the meta carries the
	// shim PID the engine samples CPU from (S7).
	if d.cfg.OnSessionStart != nil {
		d.cfg.OnSessionStart(m, token)
	}

	// Wait for the shim to actually serve its socket before declaring the spawn phase
	// reached. We never kill on failure here (crash-safe): the identity is already
	// persisted, so a shim that fails to serve is left for reconcile to reconnect or
	// reap.
	if !d.waitShimServing(sock, launchConfirmTimeout) {
		return m, fmt.Errorf("daemon: shim for session %s did not confirm serving", id)
	}
	if probe != nil {
		if err := probe(phaseSpawned, m); err != nil {
			return m, err // crash: no cleanup — the shim keeps running and serving
		}
	}

	// Phase 3 — finalize: the session is fully launched and confirmed serving.
	if probe != nil {
		if err := probe(phaseConfirmed, m); err != nil {
			return m, err
		}
	}
	return m, nil
}

// spawnShim writes the shim launch config and starts a detached `swarm shim
// --config` process. It sets no process group, so the shim setsids in place (a
// stable PID that reconcile can match) and detaches itself; the shim's stdio goes
// to the daemon log while the AGENT's env is the filtered set in the config.
func (d *Daemon) spawnShim(id string, spec LaunchSpec, sock, dir, token string) (*exec.Cmd, error) {
	lc := shimSpawnConfig{
		SessionID:  id,
		Argv:       spec.Argv,
		Cwd:        spec.Cwd,
		Env:        injectHookEnv(persist.FilterEnv(spec.ClientEnv), id, token, d.cfg.SocketPath, hookSeqFilePath(dir)),
		SocketPath: sock,
		SessionDir: dir,
		Cols:       spec.Cols,
		Rows:       spec.Rows,
		GraceMS:    int(shimGrace / time.Millisecond),
	}
	data, err := json.Marshal(lc)
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dir, shimLaunchConfigFile)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		return nil, err
	}

	cmd := exec.Command(d.cfg.ShimBinary, "shim", "--config", cfgPath)
	cmd.Env = os.Environ() // the shim PROCESS env; the agent env is lc.Env (filtered)
	logf, err := openDaemonLog(d.cfg.LogPath)
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	startErr := cmd.Start()
	logf.Close() // the shim holds its own dup of the fd
	if startErr != nil {
		return nil, startErr
	}
	return cmd, nil
}

// hookSeqFilePath is the per-session monotonic counter file injected as
// SWARM_HOOK_SEQ_FILE; each `swarm hook` invocation atomically increments it for a
// strictly increasing callback sequence (G5).
func hookSeqFilePath(dir string) string {
	return filepath.Join(dir, "hook.seq")
}

// newHookToken mints a fresh per-session hook-authentication token (crypto/rand).
// It is injected into the agent env and (Epic 8) registered with the engine, so a
// callback bearing it authenticates. It is never written to meta.json or the
// transcript; it transits only the 0600 shim-launch config and the agent's
// environment, which is ADR-004's 0600 threat model — a local process that cannot
// read the owner-only session dir cannot spoof the session's hooks.
func newHookToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("daemon: generate hook token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// injectHookEnv appends the four per-session hook variables — session id, token,
// daemon socket, and monotonic counter file — to the already allowlist-filtered
// agent env (E10.1/G4). They are added POST-filter deliberately: FilterEnv (S-2)
// would strip them, but the agent's `swarm hook` needs them to reach and
// authenticate to the daemon.
func injectHookEnv(filtered []string, id, token, sock, seqFile string) []string {
	out := make([]string, 0, len(filtered)+4)
	out = append(out, filtered...)
	out = append(out,
		hookclient.EnvSessionID+"="+id,
		hookclient.EnvToken+"="+token,
		hookclient.EnvSocket+"="+sock,
		hookclient.EnvSequenceFile+"="+seqFile,
	)
	return out
}

// superviseLaunched reaps the shim child and finalizes the session when it exits
// on its own (or via Kill). A stop signal — Close/abandon (d.stopCh) or Delete
// (the session stop) — makes it return WITHOUT finalizing, while the detached
// reaper keeps running so the child never lingers as a zombie.
func (d *Daemon) superviseLaunched(id string, cmd *exec.Cmd, stop chan struct{}) {
	defer d.wg.Done()
	waitCh := make(chan struct{}, 1)
	go func() {
		_ = cmd.Wait()
		waitCh <- struct{}{}
	}()
	select {
	case <-d.stopCh:
		return // clean shutdown / kill -9 model: do not finalize; the shim survives
	case <-stop:
		return // Delete: do not finalize; Delete owns the teardown
	case <-waitCh:
		d.handleShimExit(id) // exited on its own or via Kill: finalize from side-files
	}
}

// waitShimServing polls until the shim answers the G2 hello at sock, or the
// timeout / a daemon stop intervenes.
func (d *Daemon) waitShimServing(sock string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-d.stopCh:
			return false
		default:
		}
		if confirmShimServing(sock) {
			return true
		}
		time.Sleep(monitorPoll)
	}
	return false
}

// freshIDLocked returns a generated id not already in the registry. Caller holds
// d.mu.
func (d *Daemon) freshIDLocked() string {
	for {
		id := generateID()
		if _, ok := d.sessions[id]; !ok {
			return id
		}
	}
}

// dropReserved rolls back a reservation that failed BEFORE any shim was spawned:
// it removes the registry slot and the reserved meta from disk. It is never used
// on a probe-injected crash (those leave everything for reconcile).
func (d *Daemon) dropReserved(id string) {
	d.mu.Lock()
	delete(d.sessions, id)
	d.mu.Unlock()
	_ = d.store.Delete(id)
}

// rollbackReserved is dropReserved plus a compensating PreDelete when a
// successful PreLaunch may have created something to undo (Epic 12 F2). Once
// dropReserved erases the meta, no future Delete() can ever look this id up
// again, so any hook side effect (e.g. a git worktree) must be undone HERE or
// it leaks permanently. preLaunchOK is false when PreLaunch was never called or
// itself failed, in which case it created nothing and there is nothing to
// compensate — this degrades to a plain dropReserved.
func (d *Daemon) rollbackReserved(id string, m persist.Meta, preLaunchOK bool) {
	if preLaunchOK && d.cfg.PreDelete != nil {
		if err := d.cfg.PreDelete(m); err != nil {
			d.logf("launch %s: rollback pre-delete hook: %v", id, err)
		}
	}
	d.dropReserved(id)
}

// resolveReplay decides a replayed launch (Prepare returned existed) under d.mu.
// If the operation_id's recorded session is present and NOT lost, the prior launch
// left a usable session and its meta is returned (redrive=false) for an idempotent
// success. If that session is MISSING (W1) or LOST (W3), the record is re-pointed
// at freshID (this call's reservation) and redrive=true is returned, so the caller
// drives a fresh spawn under the SAME operation_id — never poisoning the key or
// returning a corpse. Re-reading the record under d.mu makes concurrent re-drivers
// converge on one winner: the loser observes the winner's reservation (Running) and
// returns it instead of spawning again. Reads d.sessions directly (not d.Get) since
// d.mu is already held.
func (d *Daemon) resolveReplay(opID, freshID string) (redrive bool, cached persist.Meta, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, _ := d.idem.Get(opID)
	if s, ok := d.sessions[rec.SessionID]; ok && s.meta.Status.Process != status.ProcessLost {
		return false, s.meta, nil // live (or already-exited) session: idempotent success
	}
	if _, rerr := d.idem.Redrive(opID, "launch", freshID); rerr != nil {
		return false, persist.Meta{}, fmt.Errorf("daemon: idempotent launch %q: redrive: %w", opID, rerr)
	}
	return true, persist.Meta{}, nil
}

// resolveStaleLaunches sweeps launch idempotency records still in flight
// (prepared/executing) whose reserved session did not survive the restart — MISSING
// (W1) or reconcile-LOST (W3) — and fails them, so the operation_id is re-drivable
// on the next replay instead of lingering as a poison/corpse pointing at a dead
// session (fix-pack 4a, DCR-1/DCR-2). Runs in Open AFTER reconcile, so d.sessions
// already reflects the reconnected/lost world; a record pointing at a live (or
// already-exited) session is left untouched.
func (d *Daemon) resolveStaleLaunches() {
	for _, rec := range d.idem.List() {
		if rec.Action != "launch" {
			continue
		}
		if rec.Phase != idempotency.PhasePrepared && rec.Phase != idempotency.PhaseExecuting {
			continue
		}
		if m, ok := d.Get(rec.SessionID); ok && m.Status.Process != status.ProcessLost {
			continue // a usable session survived: leave the record alone
		}
		if err := d.idem.Fail(rec.OperationID, nil); err != nil {
			d.logf("resolve stale launch %s: %v", rec.OperationID, err)
		}
	}
}
