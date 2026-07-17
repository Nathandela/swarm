package daemon

import (
	"fmt"
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

// Delete removes a session (R-3): if it is running it is killed first, then its
// directory is removed and it leaves the registry. It is removed from the registry
// up front so the session's monitor becomes a no-op, avoiding a meta write racing
// the directory removal.
func (d *Daemon) Delete(id string) error {
	d.mu.Lock()
	s, ok := d.sessions[id]
	if ok {
		delete(d.sessions, id)
	}
	// Tombstone within the same d.mu section as the registry removal, so a concurrent
	// exit-merge's putMem/saveMeta sees it and cannot re-add or re-persist this id
	// after the removal below (F3).
	d.tombstoneID(id)
	d.mu.Unlock()

	// The session is being removed: retire its engine registration and token (S6).
	if d.cfg.OnSessionEnd != nil {
		d.cfg.OnSessionEnd(id)
	}

	var preDeleteErr error
	if ok {
		close(s.stop) // stop this session's monitor WITHOUT finalizing it
		// Only signal if the recorded shim identity still matches; otherwise the shim
		// is gone or its PID was reused and must not be signalled (S3, F6).
		if s.meta.Status.Process == status.ProcessRunning && d.shimIdentityMatches(s.meta) {
			_ = signalShim(shimSocketPath(d.cfg.StateDir, id), shimwire.SigKill)
			d.awaitShimGone(s.meta.ShimPID)
		}
		// Epic 12: an optional pre-delete hook (e.g. worktree teardown) runs before
		// the session directory is removed below. Its error is logged here and
		// returned below, but never skips the mandatory directory teardown (R-3).
		if d.cfg.PreDelete != nil {
			if preDeleteErr = d.cfg.PreDelete(s.meta); preDeleteErr != nil {
				d.logf("delete %s: pre-delete hook: %v", id, preDeleteErr)
			}
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

// awaitShimGone waits (bounded) for a shim PID to be reaped, so Delete never
// removes a session directory while the shim is still writing its side-files.
func (d *Daemon) awaitShimGone(pid int) {
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(deleteWait)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(monitorPoll)
	}
}
