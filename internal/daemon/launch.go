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
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// ErrMaxSessions is returned by Launch when the daemon is at its configured
// concurrent-session cap; the message names the cap value (S-7).
var ErrMaxSessions = errors.New("daemon: max sessions reached")

// ErrLaunchOpConsumed refuses a REPLAYED signed launch whose operation already
// applied definitively (the idempotency record reached PhaseCompleted) and whose
// session is no longer usable -- deliberately DELETED (round 3, review MAJOR 2) or
// since gone LOST (round 4, review LOW 4). Without the terminal phase, resolveReplay
// could not tell either apart from a crash that left no usable session (W1) and
// re-drove -- spawning a SECOND agent under a signature that stays valid for the whole
// command-validity window, on a redelivery the gateway is PINNED to perform. A
// completed record is the machine's own proof the launch happened; what became of the
// session afterwards is a separate fact, and the one re-driver must not undo it.
var ErrLaunchOpConsumed = errors.New("daemon: launch operation already applied; its session is no longer usable")

// ErrLaunchOutcomeUnknown is the UNDECIDABLE answer to a replayed signed launch whose
// operation is still IN FLIGHT under another driver (round 4, review MAJOR 2): the
// record's session exists but is a phase-1 reservation with no recorded process
// (ShimPID == 0), which the winner can still roll back (newHookToken, spawnShim,
// procStartTimeFn, the second saveMeta) or fail at waitShimServing.
//
// The loser must be answered neither "applied" (round 3 fixed exactly that lie for the
// status READ and left it on the primary launch reply, where the phone renders it as
// "the session was created on the machine") nor by RE-DRIVING (a second live process
// under one signed operation). Undecidable is the true answer, and ADR-017 T9 already
// has a name for it: outcome_unknown.
var ErrLaunchOutcomeUnknown = errors.New("daemon: launch operation is still in flight; its outcome cannot be decided yet")

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
	// HookSocketPath is the per-session shim-owned hook UDS (playbook §6.1). "" is the
	// pre-R6 compat default the shim reads as "bind no hook listener at all".
	HookSocketPath string `json:"hook_socket_path"`
	// HookDrainToken gates that socket's DRAIN verb. It is DELIBERATELY not in Env: the
	// agent (and every hook script it spawns) holds the POST token by necessity, while
	// DRAIN is destructive (FoldSeq compacts on the caller's say-so) and reads every
	// spooled body -- including the POST token itself. Persisting it here, in the same
	// 0600 file reconcile already re-reads the POST token from, is what lets a RESTARTED
	// daemon still drain the shims that outlived it.
	HookDrainToken string `json:"hook_drain_token"`
	// The SESSION BACKEND (Wave R7, ADR-013 §R7.2b). An ABSENT backend_socket_path is the
	// pre-R7 session and every non-Codex session forever -- HookSocketPath's exact
	// unset-means-disabled convention, which is what keeps those sessions byte-for-byte
	// unchanged. The plan is PERSISTED rather than re-derived because shims outlive daemons
	// and a restarted daemon recovers the whole session from this 0600 file; BackendProgram
	// in particular is the RESOLVED absolute path, so a restart never re-resolves a bare
	// name through a PATH that may now point at a different CLI version.
	BackendProgram    string   `json:"backend_program,omitempty"`
	BackendArgs       []string `json:"backend_args,omitempty"`
	BackendAgentArgs  []string `json:"backend_agent_args,omitempty"`
	BackendSocketPath string   `json:"backend_socket_path,omitempty"`
	BackendEnv        []string `json:"backend_env,omitempty"`
}

// HookChannel names one session's structured-capture channel: the shim-owned hook
// socket, the dedicated secret that gates its DRAIN verb, and the path the daemon-side
// fold cursor lives at. It is recovered from the 0600 shim-launch.json, so a daemon
// that restarted under a still-running shim finds exactly what it minted at launch.
type HookChannel struct {
	SocketPath string
	DrainToken string
	CursorPath string
	// SpoolPath is the shim's own durable log, read DIRECTLY by the daemon once no live
	// shim is left to serve a DRAIN (R6 review fix-pack round 2, BLOCKER 2). The shim's
	// hook server shuts down with the agent it reaped, so without this every event acked
	// inside the last drain interval of a session was unreachable forever while its
	// bytes sat on disk.
	SpoolPath string
}

// hookSocketPath is the per-session hook UDS path, deterministic exactly like
// shimSocketPath so spawn and any later reader agree without bookkeeping.
func hookSocketPath(stateDir, id string) string {
	return filepath.Join(stateDir, id, "hook.sock")
}

// hookFoldCursorPath is where the DAEMON-side fold cursor for a session lives: in the
// session dir, beside the shim's own spool, so it is retired with the session.
func hookFoldCursorPath(dir string) string { return filepath.Join(dir, "hook.fold") }

// newHookDrainToken mints a session's DRAIN secret (crypto/rand), the twin of
// newHookToken. Two distinct secrets rather than one: see shimSpawnConfig's own field
// doc for why the POST side's token must not open the DRAIN side.
func newHookDrainToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("daemon: generate hook drain token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// SessionHookChannel recovers id's hook channel from its persisted launch config. It
// reports false for a session launched before the channel existed (no hook_socket_path
// key at all) rather than fabricating one -- the same "unset means disabled" convention
// the shim and hookclient.PostSmart already follow.
func (d *Daemon) SessionHookChannel(id string) (HookChannel, bool) {
	dir := d.sessionDir(id)
	data, err := os.ReadFile(filepath.Join(dir, shimLaunchConfigFile))
	if err != nil {
		return HookChannel{}, false
	}
	var lc shimSpawnConfig
	if json.Unmarshal(data, &lc) != nil || lc.HookSocketPath == "" {
		return HookChannel{}, false
	}
	return HookChannel{
		SocketPath: lc.HookSocketPath,
		DrainToken: lc.HookDrainToken,
		CursorPath: hookFoldCursorPath(dir),
		SpoolPath:  filepath.Join(dir, shim.HookSpoolFile),
	}, true
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
	// The launch ENVIRONMENT is resolved ONCE here, so the persisted meta and the env
	// the shim actually execs the agent with cannot disagree (ADR-007 D8's daemon-policy
	// half; see PolicyEnv). spec is a value copy, so this is local to this launch.
	spec.ClientEnv = PolicyEnv(spec.ClientEnv)

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
		ID:             id,
		AgentType:      spec.AgentType,
		ConversationID: spec.ConversationID,
		Name:           spec.Name, // user-provided label (P2); "" falls back to the agent name at display
		NameSetAt:      now,       // the newest-wins clock starts at launch (ADR-022)
		Cwd:            spec.Cwd,
		LaunchOptions:  spec.Options,
		Env:            PolicyEnv(spec.ClientEnv), // already resolved above; idempotent
		CreatedAt:      now,
		GroupEnteredAt: now,
		LastActivity:   now,
		ResumedFrom:    spec.ResumedFrom, // link a resume-as-new-session launch (R-2)
		SpawnedFrom:    spec.SpawnedFrom, // link an agent-initiated spawn to its source (ADR-010 D4)
		SpawnIntent:    spec.SpawnIntent,
		Supervision:    spec.Supervision, // how the source follows a handoff child (ADR-010 Amendment 3 C1)
		Status:         status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
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
			redrive, stale, staleMeta, cached, rerr := d.resolveReplay(spec.OperationID, id)
			if rerr != nil {
				d.dropReserved(id)
				return persist.Meta{}, rerr
			}
			if !redrive {
				d.dropReserved(id)
				return cached, nil
			}
			if stale != "" {
				// R5: the prior attempt's PHANTOM reservation (LOST, ShimPID==0 -- a
				// reserve that never recorded a process) is retired now that its OWN
				// operation re-drives past it. Left in place it would sit in every
				// roster beside the re-driven session forever -- two rows for one
				// signed operation, one of them a corpse ("at most one process" must
				// also read as "one session per operation" to the lists both tiers
				// converge on). Restricted to ShimPID==0: a lost session that once
				// recorded a real shim keeps its row (and its session dir) as the
				// operator-visible evidence reconcile left.
				//
				// Retired through rollbackReserved, NOT a bare dropReserved (round-2
				// review, BLOCKER 1): the phantom's meta was persisted AFTER the prior
				// run's PreLaunch succeeded (this function orders PreLaunch before the
				// phase-1 saveMeta), so its side effect -- Epic 12's git worktree --
				// exists and must be compensated by PreDelete over the PERSISTED meta
				// before the row is erased forever; rollbackReserved's own doc states
				// the rule. preLaunchOK is inferred from the CURRENT config: the hook
				// set is the daemon assembly's and does not change across the restart
				// that created the phantom.
				d.rollbackReserved(stale, staleMeta, d.cfg.PreLaunch != nil)
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
	// not the persisted meta -- the override is recorded ALONGSIDE it as m.AgentCwd
	// instead. Nothing has touched disk yet, so on error dropping
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
			// This is the ONLY moment the resolved agent cwd exists. Without stamping it
			// here it is dropped when this function returns, and the directory a worktree
			// session's agent actually ran in -- the one a provider files its history
			// under -- becomes uncomputable by anyone, this daemon included. Meta.Cwd is
			// deliberately left alone: rollbackReserved and Delete both anchor worktree
			// teardown on it (PreDelete's worktree.Remove(m.Cwd, m.ID) is run -C the REPO).
			m.AgentCwd = cwd
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
	// Terminal record (round 3, review MAJOR 2): the launch definitively applied, so
	// the operation's idempotency record is completed durably. This is what lets a
	// replay arriving AFTER the session was deliberately deleted refuse
	// (ErrLaunchOpConsumed) instead of re-driving a second process, and what lets
	// OperationOutcome answer that shape authoritatively. A failure to record it only
	// degrades to the pre-round-3 replay semantics, so it is logged, never fatal to
	// the live launch it describes. A crash BEFORE this line leaves the record
	// prepared -- the existing W1/W3 replay semantics, disclosed in r5-launch.md.
	if spec.OperationID != "" {
		if cerr := d.idem.Complete(spec.OperationID, nil); cerr != nil {
			d.logf("launch %s: record applied outcome for %s: %v", id, spec.OperationID, cerr)
		}
	}
	return m, nil
}

// spawnShim writes the shim launch config and starts a detached `swarm shim
// --config` process. It sets no process group, so the shim setsids in place (a
// stable PID that reconcile can match) and detaches itself; the shim's stdio goes
// to the daemon log while the AGENT's env is the filtered set in the config.
func (d *Daemon) spawnShim(id string, spec LaunchSpec, sock, dir, token string) (*exec.Cmd, error) {
	// The structured-capture channel (playbook §6.1): the shim binds its own hook
	// socket beside its control socket, the agent's `swarm hook` is pointed at it, and
	// the DRAIN verb is gated by a secret minted here and persisted only in this 0600
	// launch config -- never in the agent's env. A restarted daemon recovers all three
	// from that file (SessionHookChannel), because shims outlive daemons.
	hookSock := hookSocketPath(d.cfg.StateDir, id)
	drainToken, err := newHookDrainToken()
	if err != nil {
		return nil, err
	}
	lc := shimSpawnConfig{
		SessionID:      id,
		Argv:           spec.Argv,
		Cwd:            spec.Cwd,
		Env:            injectHookEnv(PolicyEnv(spec.ClientEnv), id, token, d.cfg.SocketPath, hookSeqFilePath(dir), hookSock, spec.CaptureEvents),
		SocketPath:     sock,
		SessionDir:     dir,
		Cols:           spec.Cols,
		Rows:           spec.Rows,
		GraceMS:        int(shimGrace / time.Millisecond),
		HookSocketPath: hookSock,
		HookDrainToken: drainToken,
	}
	// The plan is either CARRIED on the spec (a caller that already resolved it) or asked
	// for HERE, at the first moment the session id -- and therefore the session dir and the
	// backend socket path -- exist. Either way this package never derives it: it holds no
	// adapter and could not.
	backendSock := backendSocketPath(d.cfg.StateDir, id)
	if spec.Backend == nil && d.cfg.BackendPlanner != nil {
		plan, perr := d.cfg.BackendPlanner(spec.AgentType, dir, backendSock, spec.ClientEnv)
		if perr != nil {
			// A backend failure is a failure for the BACKEND only (playbook §6.1's posture):
			// the session still launches, degraded, exactly as a pre-R7 session of the same
			// CLI does. The honest structured_gap that accompanies it is the assembly's.
			d.logf("launch %s: no session backend planned: %v", id, perr)
		} else {
			spec.Backend = plan
		}
	}
	if spec.Backend != nil {
		lc.BackendProgram = spec.Backend.Program
		lc.BackendArgs = append([]string(nil), spec.Backend.Args...)
		lc.BackendAgentArgs = append([]string(nil), spec.Backend.AgentArgs...)
		lc.BackendSocketPath = backendSock
		lc.BackendEnv = append([]string(nil), spec.Backend.Env...)
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
	_ = logf.Close() // the shim holds its own dup of the fd
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

// injectHookEnv appends the five per-session hook variables — session id, token,
// daemon socket, monotonic counter file, and the adapter's capture=raw event rows
// — to the already allowlist-filtered agent env (E10.1/G4, ADR-010 §6). They are
// added POST-filter deliberately: FilterEnv (S-2) would strip them, but the agent's
// `swarm hook` needs them to reach and authenticate to the daemon, and to know
// which of its events carry a body worth keeping.
func injectHookEnv(filtered []string, id, token, sock, seqFile, hookSock string, capture []string) []string {
	out := make([]string, 0, len(filtered)+6)
	out = append(out, filtered...)
	out = append(out,
		hookclient.EnvSessionID+"="+id,
		hookclient.EnvToken+"="+token,
		hookclient.EnvSocket+"="+sock,
		hookclient.EnvSequenceFile+"="+seqFile,
		// The shim's own hook socket (playbook §6.1). hookclient.PostSmart treats an
		// EMPTY value exactly as an unset one -- straight to the daemon, no dial
		// attempt -- so injecting it unconditionally keeps one contract rather than two.
		hookclient.EnvHookSocket+"="+hookSock,
		// Injected even when empty: "no capture rows" is the ordinary state of every
		// adapter that implements no capture extension, and one contract ("read the
		// list") is simpler for the hook than two ("read it, or infer from its absence").
		hookclient.EnvCapture+"="+hookclient.CaptureEnv(capture),
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

// resolveReplay decides a replayed launch (Prepare returned existed) under d.mu, in
// four outcomes:
//
//   - IDEMPOTENT SUCCESS (redrive=false, err=nil): the recorded session is present, not
//     lost, and has a RECORDED PROCESS (ShimPID != 0). Its meta is returned.
//   - UNDECIDABLE (ErrLaunchOutcomeUnknown, round 4): the recorded session is present
//     and not lost but has NO recorded process — the winner's phase-1 reservation, seen
//     by a concurrent driver. Neither an authoritative success nor a re-drive is true.
//   - CONSUMED (ErrLaunchOpConsumed, round 3 + round 4): the record is COMPLETED and its
//     session is gone (deleted) or LOST. The launch definitively applied; re-driving
//     would spawn a second process for it.
//   - RE-DRIVE (redrive=true): the record is mid-flight (W1 missing / W3 lost) with no
//     terminal phase. The record is re-pointed at freshID (this call's reservation) so
//     the caller drives a fresh spawn under the SAME operation_id — never poisoning the
//     key or returning a corpse.
//
// Re-reading the record under d.mu makes concurrent re-drivers converge on one winner.
// Reads d.sessions directly (not d.Get) since d.mu is already held.
//
// stale (R5) names the prior LOST reservation the re-drive supersedes, but only when
// it is a PHANTOM — ShimPID==0, a reserve that never recorded a process — so the
// caller can retire the corpse row its own operation replaced. A lost session that
// once held a real shim is never named here (its row and session dir stay).
// staleMeta is the phantom's PERSISTED meta, returned so the caller can compensate
// the phantom's PreLaunch side effect (round-2 BLOCKER 1): once the row is dropped
// no Delete() can ever reach this meta again, so the worktree teardown needs it NOW.
func (d *Daemon) resolveReplay(opID, freshID string) (redrive bool, stale string, staleMeta, cached persist.Meta, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, _ := d.idem.Get(opID)
	if s, ok := d.sessions[rec.SessionID]; ok {
		if s.meta.Status.Process != status.ProcessLost {
			// ROUND 4, review MAJOR 2: the SAME phantom rule the status read applies
			// (OperationOutcome's ShimPID!=0 clause), now on the primary launch reply.
			// A present, Running session with NO recorded process is the winner's phase-1
			// reservation, and this call is the concurrent loser (or the gateway's PINNED
			// crash-shaped redelivery racing the in-flight original). Returning that meta
			// is an AUTHORITATIVE success -- the wire replies OpSessionLaunch, which the
			// phone renders as "the session was created on the machine" -- for a launch
			// the winner can still roll back (newHookToken, spawnShim, procStartTimeFn,
			// the second saveMeta) or fail at waitShimServing. Re-driving instead would
			// put a SECOND live process under one signed operation. Neither: the honest
			// answer is undecidable, and the caller's replay of the operation once the
			// winner has settled resolves it either way.
			if s.meta.ShimPID == 0 {
				return false, "", persist.Meta{}, persist.Meta{},
					fmt.Errorf("%w: operation %q reserved session %s", ErrLaunchOutcomeUnknown, opID, rec.SessionID)
			}
			// Adoption completes the record too (round 3): a replay that returns a
			// PROVEN-SPAWNED session has resolved the operation as definitively as a
			// confirmed launch, so its later deliberate Delete must also refuse the
			// re-drive below rather than reopen the second-process window.
			if rec.Phase != idempotency.PhaseCompleted {
				if cerr := d.idem.Complete(opID, nil); cerr != nil {
					d.logf("replay %s: record applied outcome: %v", opID, cerr)
				}
			}
			return false, "", persist.Meta{}, s.meta, nil // live (or already-exited) session: idempotent success
		}
		// ROUND 4, review LOW 4: the terminal-record rule applies to the LOST branch
		// exactly as it does to the row-MISSING branch below. A COMPLETED record is the
		// machine's own proof that the launch applied; the session going LOST afterwards
		// is a later fact about the PROCESS (reconcile could not re-identify its shim),
		// never evidence the launch did not happen. Re-driving it spawns a second agent
		// for an operation that already applied -- under a signature valid for the whole
		// command-validity window, on a redelivery the gateway is pinned to perform.
		// The W3 crash re-driver is untouched: a launch that died mid-flight never
		// reached PhaseCompleted (resolveStaleLaunches fails those records on Open), so
		// prepared/executing records pointing at LOST sessions still re-drive below.
		if rec.Phase == idempotency.PhaseCompleted {
			return false, "", persist.Meta{}, persist.Meta{},
				fmt.Errorf("%w: operation %q launched session %s", ErrLaunchOpConsumed, opID, rec.SessionID)
		}
		if s.meta.ShimPID == 0 {
			stale = rec.SessionID // phantom reservation: retire once the re-drive is committed
			staleMeta = s.meta
		}
	} else if rec.Phase == idempotency.PhaseCompleted {
		// The launch APPLIED (terminal record) and its session row is gone: the only
		// path that removes a row without a crash is Delete, a deliberate user verb.
		// Re-driving here is the review's proven second-process window (MAJOR 2);
		// refuse instead, naming the operation and the session it launched.
		return false, "", persist.Meta{}, persist.Meta{},
			fmt.Errorf("%w: operation %q launched session %s", ErrLaunchOpConsumed, opID, rec.SessionID)
	}
	if _, rerr := d.idem.Redrive(opID, "launch", freshID); rerr != nil {
		return false, "", persist.Meta{}, persist.Meta{}, fmt.Errorf("daemon: idempotent launch %q: redrive: %w", opID, rerr)
	}
	return true, stale, staleMeta, persist.Meta{}, nil
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
