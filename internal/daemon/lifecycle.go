package daemon

import (
	"fmt"
	"time"

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
	running := s.meta.Status.Process == status.ProcessRunning
	d.mu.Unlock()
	if !running {
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
	d.mu.Unlock()

	if ok {
		close(s.stop) // stop this session's monitor WITHOUT finalizing it
		if s.meta.Status.Process == status.ProcessRunning {
			_ = signalShim(shimSocketPath(d.cfg.StateDir, id), shimwire.SigKill)
			d.awaitShimGone(s.meta.ShimPID)
		}
	}
	return d.store.Delete(id) // remove the session directory
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
