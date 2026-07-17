package daemon

import (
	"fmt"
	"net"

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
	running := ok && s.meta.Status.Process == status.ProcessRunning
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("daemon: unknown session %q", id)
	}
	if !running {
		return nil, fmt.Errorf("daemon: session %q is not running", id)
	}
	return dialShimHello(shimSocketPath(d.cfg.StateDir, id))
}
