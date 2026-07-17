package daemon

import (
	"fmt"
	"net"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

// dialShimHello dials a shim's per-session socket and completes the G2 hello
// handshake: it sends a hello and reads the shim's hello reply. The returned
// connection is past the shim's helloed gate, so an attach/resize/signal sent on
// it will be honored. The caller owns the connection and must close it.
func dialShimHello(sock string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", sock, dialTimeout)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(helloIO))

	hello, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := wire.WriteFrame(conn, wire.TControl, hello); err != nil {
		conn.Close()
		return nil, err
	}
	typ, payload, err := wire.ReadFrame(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if typ != wire.TControl {
		conn.Close()
		return nil, fmt.Errorf("shim handshake: got frame type %d, want control", typ)
	}
	ctrl, err := shimwire.Decode(payload)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if ctrl.Type != shimwire.TypeHello {
		conn.Close()
		return nil, fmt.Errorf("shim handshake: got %q, want hello", ctrl.Type)
	}
	// The reconnect hello must compare WireVersion, not merely the reply type: a
	// shim advertising an incompatible wire version is rejected here (not adopted),
	// so reconcile marks it lost rather than driving it over a mismatched protocol
	// (F9). The full compat matrix is E14.3; this is the single-version gate.
	if ctrl.WireVersion != shimwire.Version {
		conn.Close()
		return nil, fmt.Errorf("shim handshake: wire version %d, want %d", ctrl.WireVersion, shimwire.Version)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// confirmShimServing reports whether a shim is live and answering the G2 hello at
// sock. A stale socket file with no listener, or a shim that has torn its socket
// down, yields false — the signal the daemon uses to distinguish a reconnectable
// shim from an orphan/phantom.
func confirmShimServing(sock string) bool {
	conn, err := dialShimHello(sock)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// signalShim routes a termination signal (SigTerm|SigKill) to a shim over its
// socket; the shim terminates the agent's whole process group. It handshakes
// first (the shim ignores ops before hello), sends the signal, then briefly reads
// so the shim consumes the frame before the connection closes.
func signalShim(sock, sig string) error {
	conn, err := dialShimHello(sock)
	if err != nil {
		return err
	}
	defer conn.Close()

	body, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSignal, Sig: sig})
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(helloIO))
	if err := wire.WriteFrame(conn, wire.TControl, body); err != nil {
		return err
	}
	// Give the shim a moment to consume the signal before we close: it returns
	// when the shim tears the connection down (agent gone) or the short deadline
	// elapses. The signal is already committed to the socket either way.
	_ = conn.SetReadDeadline(time.Now().Add(helloIO))
	_, _, _ = wire.ReadFrame(conn)
	return nil
}
