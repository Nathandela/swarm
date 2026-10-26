package daemon

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
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
		if m.HermesNative {
			return errors.Join(errors.New("hermes native stop requested; completion remains unknown and the workspace is retained"), d.stopHermes(id))
		}
		return nil
	}
	var nativeStopErr error
	if m.HermesNative {
		nativeStopErr = d.stopHermes(id)
	}
	// Re-verify the shim identity before signalling: if (PID, start-time) no longer
	// matches, the shim exited or its PID was reused, so signalling its socket could
	// hit a rebound stranger. Resolve as lost instead of signalling (S3, F6).
	if !d.shimIdentityMatches(m) {
		d.markLost(id)
		if m.HermesNative {
			return errors.Join(errors.New("hermes viewer is unavailable; native job completion is unknown"), nativeStopErr)
		}
		return nil
	}
	if err := signalShim(shimSocketPath(d.cfg.StateDir, id), shimwire.SigTerm); err != nil {
		return fmt.Errorf("daemon: kill %s: %w", id, err)
	}
	if m.HermesNative {
		return errors.Join(errors.New("hermes native stop requested; completion remains unknown and the workspace is retained"), nativeStopErr)
	}
	return nil
}

// terminateForDeleteFn is the seam for Delete's termination step; a test overrides
// it to simulate a shim that cannot be confirmed dead (the mutate-nothing path).
var terminateForDeleteFn = (*Daemon).terminateForDelete

// deleteSessionDirFn permits a partial filesystem-removal failure in tests.
var deleteSessionDirFn = func(d *Daemon, id string) error { return d.store.Delete(id) }

// Delete removes a session (R-3). Termination is VERIFIED FIRST (R1.3.4): a
// running session's shim is confirmed dead before ANY registry/tombstone/hook/
// store mutation, so a session directory is never removed out from under a live
// agent. If a live, identity-matched shim cannot be terminated, Delete returns an
// error and mutates nothing (no orphaned live agent with a removed dir). Only once
// termination is confirmed does cleanup begin. Metadata stays available for a retry
// until the pre-delete hook, directory removal and deletion journal all succeed.
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
	var nativeStopErr error
	if ok && meta.HermesNative {
		nativeStopErr = d.stopHermes(id)
	}
	if ok && meta.Status.Process == status.ProcessRunning {
		if !terminateForDeleteFn(d, id, meta) {
			return fmt.Errorf("daemon: delete %s: shim %d still alive after termination attempt; session not removed", id, meta.ShimPID)
		}
	}
	if ok && meta.HermesNative && meta.ShimPID > 0 {
		// shortcut: cancellation is not a joined native proof, retain this scope until Hermes exposes one.
		return errors.Join(errors.New("hermes native job completion is unproven; session and workspace retained"), nativeStopErr)
	}

	// Termination can overlap; cleanup cannot, or two callers could tear down one worktree.
	d.deleteMu.Lock()
	defer d.deleteMu.Unlock()
	if d.isDeleted(id) {
		return nil
	}
	d.mu.Lock()
	sess, present := d.sessions[id]
	if present {
		meta = sess.meta
	}
	d.mu.Unlock()

	if present {
		if meta.Status.Process == status.ProcessRunning {
			d.reapOrphanBackend(id)
			exit, hasExit := readExitSideFile(d.sessionDir(id))
			d.finalizeTerminal(id, func(current persist.Meta) persist.Meta {
				if hasExit {
					return mergeExit(current, exit)
				}
				current.Status.Process = status.ProcessLost
				return current
			})
		}
		if d.cfg.OnSessionEnd != nil {
			d.cfg.OnSessionEnd(id)
		}
		meta, _ = d.Get(id)
	}

	if present && d.cfg.PreDelete != nil {
		if err := d.cfg.PreDelete(meta); err != nil {
			d.logf("delete %s: pre-delete hook: %v", id, err)
			return err
		}
	}

	// Hold the writer fence through disk removal and tombstoning to prevent resurrection.
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	d.mu.Lock()
	if current, exists := d.sessions[id]; exists {
		meta = current.meta
	}
	d.mu.Unlock()
	// WAL-first: a crash during directory removal must leave a durable deletion event.
	if _, jerr := d.journal.Append(journal.Record{SessionID: id, Type: journal.TypeDeleted}); jerr != nil {
		return jerr
	}
	if err := deleteSessionDirFn(d, id); err != nil {
		if present {
			if _, loadErr := d.store.Load(id); os.IsNotExist(loadErr) {
				err = errors.Join(err, d.store.Save(meta))
			}
			// Restore the remote row after the failed deletion, retaining its cleanup owner.
			_, stateErr := d.journal.Append(completeSessionRecord(meta, journal.TypeSessionState, nil))
			err = errors.Join(err, stateErr)
		}
		return err
	}
	d.mu.Lock()
	delete(d.sessions, id)
	d.tombstoneID(id)
	d.mu.Unlock()
	if present {
		close(sess.stop)
	}
	return nil
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
	// The shim this session recorded is gone (that is the only way here), so the backend
	// only that shim could contain may have outlived it. markLost is a THIRD shim-death
	// path beside handleShimExit and reconcile's orphan arm, and it needs the SAME sweep:
	// once this finalize persists LOST, the monitor's handleShimExit takes its
	// already-terminal early return BEFORE its own reap, and reconcile never revisits a
	// session not persisted running -- so an orphan skipped here is an orphan forever
	// (Wave R7, ADR-013 §R7.2c). A no-op for the overwhelmingly common session with no
	// backend record.
	d.reapOrphanBackend(id)
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
