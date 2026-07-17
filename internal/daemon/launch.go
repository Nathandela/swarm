package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
)

// ErrMaxSessions is returned by Launch when the daemon is at its configured
// concurrent-session cap; the message names the cap value (S-7).
var ErrMaxSessions = errors.New("daemon: max sessions reached")

// procStartTimeFn is the seam for reading a just-spawned shim's process-start-time
// in launch; tests override it to inject a post-spawn identity-read failure (F2).
var procStartTimeFn = processStartTime

// killSpawnedShim tears down a just-spawned shim whose launch is aborting before
// its supervisor started (F2/N2). The AGENT runs in its OWN process group (the
// shim setsids it — Epic 4), so a bare kill(-shimPID) never reaches it and would
// orphan the agent. Instead the shim is told to KILL its agent's group over the
// socket; only if the socket is not serving yet (the agent has not been spawned)
// do we fall back to a best-effort group kill of the shim. Finally the shim
// process itself is killed and reaped (no supervisor is running yet).
func (d *Daemon) killSpawnedShim(cmd *exec.Cmd, id string, pid int) {
	if err := signalShim(shimSocketPath(d.cfg.StateDir, id), shimwire.SigKill); err != nil && pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
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
		Status:        status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
	}
	s := &session{meta: m, stop: make(chan struct{})}
	d.sessions[id] = s // reserve the slot so a concurrent launch counts it against the cap
	d.mu.Unlock()

	dir := d.sessionDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.dropReserved(id)
		return persist.Meta{}, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		d.dropReserved(id)
		return persist.Meta{}, err
	}

	// Phase 1 — reserve: persist the running meta before any shim exists.
	if err := d.saveMeta(m); err != nil {
		d.dropReserved(id)
		return persist.Meta{}, err
	}
	if probe != nil {
		if err := probe(phaseReserved, m); err != nil {
			return m, err // crash: no cleanup — reconcile resolves the reserved phantom
		}
	}

	// Phase 2 — spawn: launch the shim with the deterministic socket + filtered env.
	sock := shimSocketPath(d.cfg.StateDir, id)
	cmd, err := d.spawnShim(id, spec, sock, dir)
	if err != nil {
		d.dropReserved(id)
		return persist.Meta{}, err
	}
	m.ShimPID = cmd.Process.Pid
	st, sterr := procStartTimeFn(m.ShimPID)
	if sterr != nil {
		// A shim whose start-time we cannot record is un-trackable: reconcile matches
		// by (PID, start-time), so persisting ShimStartTime=0 would let a later Open
		// mark this LIVE shim lost. This is a setup failure, not a crash, so we DO
		// clean up: kill the just-spawned shim (and its agent, over the socket) and
		// abort (F2/N2). No supervisor is running yet, so we reap it here.
		d.killSpawnedShim(cmd, id, m.ShimPID)
		d.dropReserved(id)
		return persist.Meta{}, fmt.Errorf("daemon: record shim identity for %s: %w", id, sterr)
	}
	m.ShimStartTime = st
	if err := d.saveMeta(m); err != nil {
		d.killSpawnedShim(cmd, id, m.ShimPID)
		d.dropReserved(id)
		return persist.Meta{}, fmt.Errorf("daemon: persist shim identity for %s: %w", id, err)
	}
	d.wg.Add(1)
	go d.superviseLaunched(id, cmd, s.stop)

	// Wait for the shim to actually serve its socket before declaring the spawn
	// phase reached, so a crash injected at this boundary deterministically leaves
	// a live, SERVING shim for reconcile to adopt (the S11 spawn window) — never a
	// half-started process the test would race. We never kill on failure here
	// (crash-safe): a shim that fails to serve is left for reconcile to reap.
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
func (d *Daemon) spawnShim(id string, spec LaunchSpec, sock, dir string) (*exec.Cmd, error) {
	lc := shimSpawnConfig{
		SessionID:  id,
		Argv:       spec.Argv,
		Cwd:        spec.Cwd,
		Env:        persist.FilterEnv(spec.ClientEnv),
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
	cfgPath := filepath.Join(dir, "shim-launch.json")
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
