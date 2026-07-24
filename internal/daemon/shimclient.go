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
func dialShimHello(sock string) (net.Conn, shimwire.Caps, error) {
	conn, err := net.DialTimeout("unix", sock, dialTimeout)
	if err != nil {
		return nil, shimwire.Caps{}, err
	}
	_ = conn.SetDeadline(time.Now().Add(helloIO))

	// Advertise snapshot chunking (an OPTIONAL hello field; WireVersion stays 1) so a
	// chunking-capable shim chunks its snapshot on this connection — the daemon reader
	// (protocol.readSnapshot) reassembles it. An old shim ignores the field and sends a
	// single frame, which this daemon still reads on the single-frame path (G-D). The
	// signal/confirm paths also send it; it is harmless there (no attach follows).
	hello, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, SnapshotChunking: true})
	if err != nil {
		conn.Close()
		return nil, shimwire.Caps{}, err
	}
	if err := wire.WriteFrame(conn, wire.TControl, hello); err != nil {
		conn.Close()
		return nil, shimwire.Caps{}, err
	}
	typ, payload, err := wire.ReadFrame(conn)
	if err != nil {
		conn.Close()
		return nil, shimwire.Caps{}, err
	}
	if typ != wire.TControl {
		conn.Close()
		return nil, shimwire.Caps{}, fmt.Errorf("shim handshake: got frame type %d, want control", typ)
	}
	ctrl, err := shimwire.Decode(payload)
	if err != nil {
		conn.Close()
		return nil, shimwire.Caps{}, err
	}
	if ctrl.Type != shimwire.TypeHello {
		conn.Close()
		return nil, shimwire.Caps{}, fmt.Errorf("shim handshake: got %q, want hello", ctrl.Type)
	}
	// The reconnect hello must compare WireVersion, not merely the reply type: a
	// shim advertising an incompatible wire version is rejected here (not adopted),
	// so reconcile marks it lost rather than driving it over a mismatched protocol
	// (F9). The full compat matrix is E14.3; this is the single-version gate.
	if ctrl.WireVersion != shimwire.Version {
		conn.Close()
		return nil, shimwire.Caps{}, fmt.Errorf("shim handshake: wire version %d, want %d", ctrl.WireVersion, shimwire.Version)
	}
	_ = conn.SetDeadline(time.Time{})
	// Capture the shim's advertised capabilities from its hello reply so callers
	// can ENFORCE what was negotiated (R1.2.2) rather than trusting the peer's
	// later frames to imply it.
	return conn, ctrl.Caps(), nil
}

// confirmShimServing reports whether a shim is live and answering the G2 hello at
// sock. A stale socket file with no listener, or a shim that has torn its socket
// down, yields false — the signal the daemon uses to distinguish a reconnectable
// shim from an orphan/phantom.
func confirmShimServing(sock string) bool {
	conn, _, err := dialShimHello(sock)
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
	conn, _, err := dialShimHello(sock)
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
