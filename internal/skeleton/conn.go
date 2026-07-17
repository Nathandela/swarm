package skeleton

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/hookclient"
)

const (
	// classifyTimeout bounds reading the demux prefix from a fresh connection. A
	// protocol client and a hook post each write their whole first message at once,
	// so their prefix bytes are available immediately; only the daemon's 4-byte
	// version handshake sends exactly four bytes and then waits, so it is this
	// window (not a hang) that identifies it. It must stay under the version
	// client's own handshake deadline (daemon.helloIO, 3s).
	classifyTimeout = 250 * time.Millisecond
	// versionHandshakeLen is the daemon's minimal liveness handshake: a 4-byte
	// big-endian version, answered with the daemon's version (Epic 5 / D-8). A
	// protocol frame is longer — a 4-byte length prefix followed by at least the
	// type byte — so a fifth byte's presence tells the two apart.
	versionHandshakeLen = 4
)

// handleConn is the daemon's ConnHandler for the assembled socket: one socket
// carries THREE kinds of first message, demuxed by their opening bytes:
//
//   - a raw-JSON hook post (hookclient.Post) — first byte '{' (0x7B);
//   - the daemon's 4-byte version handshake (daemon.Dial, used by Restart /
//     EnsureDaemon / liveness probes) — exactly four bytes, then the peer waits;
//   - a framed protocol client (protocol.Dial) — a wire frame whose 4-byte length
//     prefix is followed at once by the type byte and payload (wire.WriteFrame
//     writes the whole frame in one Write, so the fifth byte co-arrives).
//
// The demux is unambiguous: a hook is set apart by '{' (every wire length's
// most-significant byte is 0x00, since the max frame is 2^20); a version handshake
// is the one first message that stops at four bytes. This keeps the hook wire
// format (raw JSON) and the version handshake (Epic 5) both intact on the socket
// protocol.Server now owns.
func (d *Daemon) handleConn(conn net.Conn) {
	// Serve nothing until the assembly is fully wired.
	select {
	case <-d.ready:
	case <-d.closing:
		conn.Close()
		return
	}

	prefix, err := readPrefix(conn)
	if err != nil {
		conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{}) // hand off a deadline-free connection

	switch {
	case prefix[0] == '{':
		d.serveHook(conn, prefix)
	case len(prefix) == versionHandshakeLen:
		serveVersionHandshake(conn)
	default:
		// A framed protocol client: replay the prefix and run the Server's per-
		// connection loop (blocks until the connection ends).
		d.srv.ServeConn(prefixConn(conn, prefix))
	}
}

// readPrefix reads the demux prefix under classifyTimeout: up to five bytes, which
// is enough to tell a version handshake (exactly four bytes, then a wait that trips
// the deadline) from a protocol frame (a fifth byte co-arrives) or a hook ('{'
// first). It returns the bytes actually read (>=1), or an error if fewer than the
// four-byte minimum arrived.
func readPrefix(conn net.Conn) ([]byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(classifyTimeout))
	buf := make([]byte, versionHandshakeLen+1)
	n, err := io.ReadFull(conn, buf)
	if n >= versionHandshakeLen {
		return buf[:n], nil // full handshake, or a frame's prefix (n == 5)
	}
	if n >= 1 && buf[0] == '{' {
		return buf[:n], nil // a short hook post is still a hook
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}

// serveHook decodes one raw-JSON hook callback (replaying the demux prefix) and
// routes it to the status engine, which authenticates it (S6/G5). A rejected or
// malformed callback is dropped; the point is that a hook post never corrupts the
// shared socket (this connection is the hook's alone and is closed here), so a
// client can still use the socket afterward.
func (d *Daemon) serveHook(conn net.Conn, prefix []byte) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(classifyTimeout))
	r := io.MultiReader(bytes.NewReader(prefix), conn)
	cb, err := hookclient.Decode(r)
	if err != nil {
		return
	}
	_ = d.eng.HandleCallback(cb) // engine authenticates; rejection is expected pre-Epic-11
}

// serveVersionHandshake answers the daemon's 4-byte liveness handshake with the
// daemon's protocol version and closes (Epic 5 / D-8): the caller (daemon.Dial,
// used by Restart / EnsureDaemon) classifies a mismatch as skew. Preserving it on
// the assembled socket keeps those daemon-internal probes working now that the
// socket speaks the full client protocol.
func serveVersionHandshake(conn net.Conn) {
	defer conn.Close()
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], uint32(daemon.ProtocolVersion))
	_ = conn.SetWriteDeadline(time.Now().Add(classifyTimeout))
	_, _ = conn.Write(out[:])
}

// prefixedConn is a net.Conn whose Read replays bytes already consumed from the
// underlying connection (the demux prefix), delegating everything else. It lets
// the protocol Server read the connection from its true first byte.
type prefixedConn struct {
	net.Conn
	r io.Reader
}

// prefixConn wraps conn so its first reads yield prefix, then the rest of conn.
func prefixConn(conn net.Conn, prefix []byte) net.Conn {
	return &prefixedConn{
		Conn: conn,
		r:    io.MultiReader(bytes.NewReader(prefix), conn),
	}
}

func (p *prefixedConn) Read(b []byte) (int, error) { return p.r.Read(b) }
