package daemon

import (
	"fmt"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
)

// Kill routes a termination signal to a session's shim, which terminates the
// agent's whole process group (S-4). It returns once the signal is delivered; the
// session's supervisor observes the shim exit and persists the outcome (exited +
// exit code) from the side-files. Killing an already-terminal session is a no-op.
func (d *Daemon) Kill(id string) error {
	d.mu.Lock()
	s, ok := d.sessions[id]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("daemon: unknown session %q", id)
	}
	m := s.meta
	running := m.Status.Process == status.ProcessRunning
	d.mu.Unlock()
	if !running {
		return nil
	}
	// Re-verify the shim identity before signalling: if (PID, start-time) no longer
	// matches, the shim exited or its PID was reused, so signalling its socket could
	// hit a rebound stranger. Resolve as lost instead of signalling (S3, F6).
	if !d.shimIdentityMatches(m) {
		d.markLost(id)
		return nil
	}
	if err := signalShim(shimSocketPath(d.cfg.StateDir, id), shimwire.SigTerm); err != nil {
		return fmt.Errorf("daemon: kill %s: %w", id, err)
	}
	return nil
}

// terminateForDeleteFn is the seam for Delete's termination step; a test overrides
// it to simulate a shim that cannot be confirmed dead (the mutate-nothing path).
var terminateForDeleteFn = (*Daemon).terminateForDelete

// Delete removes a session (R-3). Termination is VERIFIED FIRST (R1.3.4): a
// running session's shim is confirmed dead before ANY registry/tombstone/hook/
// store mutation, so a session directory is never removed out from under a live
// agent. If a live, identity-matched shim cannot be terminated, Delete returns an
// error and mutates nothing (no orphaned live agent with a removed dir). Only once
// termination is confirmed (or the session was not running, or the shim is already
// gone) does the removal commit: registry removal + tombstone under d.mu (F3),
// then the monitor stop, the engine retire, the pre-delete hook, and store.Delete.
func (d *Daemon) Delete(id string) error {
	d.mu.Lock()
	s, ok := d.sessions[id]
	var meta persist.Meta
	if ok {
		meta = s.meta
	}
	d.mu.Unlock()

	// Phase 1 — TERMINATION FIRST. On failure (a live, identity-matched shim we
	// could not confirm dead) error out having mutated nothing.
	if ok && meta.Status.Process == status.ProcessRunning {
		if !terminateForDeleteFn(d, id, meta) {
			return fmt.Errorf("daemon: delete %s: shim %d still alive after termination attempt; session not removed", id, meta.ShimPID)
		}
	}

	// Phase 2 — COMMIT the removal. Tombstone within the same d.mu section as the
	// registry removal, so a concurrent exit-merge's putMem/saveMeta sees it and
	// cannot re-add or re-persist this id after the removal (F3).
	d.mu.Lock()
	if ok {
		delete(d.sessions, id)
	}
	d.tombstoneID(id)
	d.mu.Unlock()

	if ok {
		close(s.stop) // stop this session's monitor WITHOUT finalizing it
	}
	// The session is being removed: retire its engine registration and token (S6).
	if d.cfg.OnSessionEnd != nil {
		d.cfg.OnSessionEnd(id)
	}

	var preDeleteErr error
	if ok && d.cfg.PreDelete != nil {
		// Epic 12: an optional pre-delete hook (e.g. worktree teardown) runs before
		// the session directory is removed below. Its error is logged here and
		// returned below, but never skips the mandatory directory teardown (R-3).
		if preDeleteErr = d.cfg.PreDelete(meta); preDeleteErr != nil {
			d.logf("delete %s: pre-delete hook: %v", id, preDeleteErr)
		}
	}

	// Remove the session directory under writeMu, mutually exclusive with saveMeta's
	// tombstone-check + store.Save, so a merge racing this Delete cannot recreate the
	// directory after it is removed (F3).
	d.writeMu.Lock()
	err := d.store.Delete(id)
	d.writeMu.Unlock()
	if err != nil {
		return err
	}
	return preDeleteErr
}

// terminateForDelete confirms a running session's shim is dead before Delete
// removes anything (R1.3.4). It returns true once the shim is confirmed gone (or
// its identity no longer matches, so there is nothing live to signal, S3), and
// false only when a live, identity-matched shim could not be terminated. The
// primary path signals over the socket (the shim kills the agent's group and
// exits); when the socket is unreachable or the shim does not exit on it, it falls
// back to signalling the shim's process group directly — the shim's armed handler
// contains the agent's group before it exits — then re-verifies death (R1.3.5).
func (d *Daemon) terminateForDelete(id string, m persist.Meta) bool {
	if !d.shimIdentityMatches(m) {
		return true // shim exited or its PID was reused: nothing to signal (S3)
	}
	if err := signalShim(shimSocketPath(d.cfg.StateDir, id), shimwire.SigKill); err == nil {
		if d.awaitShimGone(m.ShimPID) {
			return true
		}
	}
	// Fallback (R1.3.5): re-verify identity, then SIGTERM the shim's process group
	// so its handler contains the agent's group before the shim exits.
	if d.shimIdentityMatches(m) {
		_ = syscall.Kill(-m.ShimPID, syscall.SIGTERM)
		return d.awaitShimGone(m.ShimPID)
	}
	return true // identity stopped matching mid-way: the shim is gone
}

// shimIdentityMatches reports whether the recorded shim (PID, start-time) still
// names the process the session was launched with. A mismatch means the shim
// exited or its PID was reused; the daemon must not signal that PID (S3). This is
// the cheap pre-signal recheck that closes the rebound-socket window (F6).
func (d *Daemon) shimIdentityMatches(m persist.Meta) bool {
	if m.ShimPID <= 0 || !pidAlive(m.ShimPID) {
		return false
	}
	st, err := processStartTime(m.ShimPID)
	return err == nil && st == m.ShimStartTime
}

// markLost reclassifies a still-running session as lost and persists it, sending
// no signal (S3). Used when a pre-signal identity recheck fails (F6).
func (d *Daemon) markLost(id string) {
	// The lost transition is applied atomically under writeMu and ONLY advances a
	// running session — a racing handleShimExit that recorded exited+code (rank 2)
	// is never regressed to lost (rank 1), regardless of ordering (S1).
	d.finalizeTerminal(id, func(cur persist.Meta) persist.Meta {
		m := cur
		m.Status.Process = status.ProcessLost
		return m
	})
	// The session has ended (lost): retire its engine registration and token (S6),
	// exactly as handleShimExit does for a clean exit — otherwise a lost session's
	// stale engine entry lingers and a late hook could still be accepted.
	if d.cfg.OnSessionEnd != nil {
		d.cfg.OnSessionEnd(id)
	}
}

// awaitShimGone waits (bounded by deleteWait) for a shim PID to be reaped, so
// Delete never removes a session directory while the shim is still alive/writing
// its side-files. It reports whether the shim is confirmed gone (R1.3.4): false
// means it was still alive at the deadline, so the caller must not remove the dir.
func (d *Daemon) awaitShimGone(pid int) bool {
	if pid <= 0 {
		return true // no shim to wait for: nothing can be orphaned
	}
	deadline := time.Now().Add(deleteWait)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(monitorPoll)
	}
	return false
}
