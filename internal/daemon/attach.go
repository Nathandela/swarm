package daemon

import (
	"fmt"
	"net"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// DialSession opens a helloed connection to a running session's shim, ready for
// the Epic 6 protocol layer (protocol.FromDaemon) to drive the attach data plane
// over. The caller owns the returned connection and must Close it.
//
// This is the integration seam that lets the client-facing protocol Server reach
// a real shim's snapshot/stream/input/resize without duplicating the daemon's
// private per-session socket-path and G2 hello knowledge.
func (d *Daemon) DialSession(id string) (net.Conn, error) {
	d.mu.Lock()
	s, ok := d.sessions[id]
	var m persist.Meta
	if ok {
		m = s.meta
	}
	running := ok && m.Status.Process == status.ProcessRunning
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("daemon: unknown session %q", id)
	}
	if !running {
		return nil, fmt.Errorf("daemon: session %q is not running", id)
	}
	// Re-verify the recorded shim identity (PID, start-time) BEFORE dialing, as
	// Kill/Delete do: if it no longer matches, the shim exited or its PID was
	// reused, and dialing its socket could reach a rebound stranger. Refuse rather
	// than dial (S3, F6).
	if !d.shimIdentityMatches(m) {
		return nil, fmt.Errorf("daemon: session %q shim identity no longer matches; not dialing", id)
	}
	return dialShimHello(shimSocketPath(d.cfg.StateDir, id))
}
