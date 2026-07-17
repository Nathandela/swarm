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
	// demuxReadTimeout bounds reading the single discriminator byte from a fresh
	// connection, so a peer that connects and then stalls (or sends nothing) cannot
	// leak its handler goroutine or wedge anything. Every real client writes its
	// first byte immediately on connect, so this only ever bites a broken/idle peer.
	// It is a plain safety bound, NOT a classification window: routing happens the
	// instant the byte arrives (F2 — no 250ms timing demux).
	demuxReadTimeout = 3 * time.Second
	// versionPayloadLen is the daemon liveness handshake's payload after its tag: a
	// 4-byte big-endian version, answered with the daemon's own version (Epic 5/D-8).
	versionPayloadLen = 4
)

// handleConn is the daemon's ConnHandler for the assembled socket: one socket
// carries THREE kinds of first message, demuxed by an EXPLICIT first byte with no
// timing window (F2):
//
//   - the daemon's version handshake (daemon.Dial, used by Restart / EnsureDaemon /
//     liveness probes) — a leading daemon.VersionProbeTag ('V', 0x56) then a 4-byte
//     version;
//   - a raw-JSON hook post (hookclient.Post) — first byte '{' (0x7B), the JSON
//     object's opening brace;
//   - a framed protocol client (protocol.Dial) — a wire frame whose 4-byte length
//     prefix always begins 0x00 (the max frame is 2^20, so the length's most-
//     significant byte is 0x00).
//
// The three leading bytes are disjoint ('V' vs '{' vs 0x00), so a single first-byte
// read routes every connection deterministically and immediately. Only the version
// probe carries a dedicated tag: its payload (a bare 4-byte int) would otherwise
// also begin 0x00 and collide with a protocol frame — the sole ambiguity the old
// 250ms classify window existed to resolve. The hook's '{' and the frame's 0x00 are
// their own guaranteed first bytes and are routed directly. An unexpected first byte
// is rejected cleanly.
func (d *Daemon) handleConn(conn net.Conn) {
	// Serve nothing until the assembly is fully wired.
	select {
	case <-d.ready:
	case <-d.closing:
		conn.Close()
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(demuxReadTimeout))
	var tag [1]byte
	if _, err := io.ReadFull(conn, tag[:]); err != nil {
		conn.Close() // empty / stalled / partial: bounded, never wedges the accept loop
		return
	}

	switch tag[0] {
	case daemon.VersionProbeTag:
		serveVersionHandshake(conn)
	case '{':
		// The '{' is the hook JSON's first byte: replay it, then decode.
		d.serveHook(conn, tag[0])
	case 0x00:
		// The 0x00 is the wire frame's length MSB: replay it, hand a deadline-free
		// connection to the Server's per-connection loop (blocks until it ends).
		_ = conn.SetReadDeadline(time.Time{})
		d.srv.ServeConn(prefixConn(conn, tag[0]))
	default:
		conn.Close() // not one of the three known first bytes
	}
}

// serveHook decodes one raw-JSON hook callback (replaying the leading brace) and
// routes it to the status engine, which authenticates it (S6/G5). A rejected or
// malformed callback is dropped; the point is that a hook post never corrupts the
// shared socket (this connection is the hook's alone and is closed here), so a
// client can still use the socket afterward.
func (d *Daemon) serveHook(conn net.Conn, brace byte) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(demuxReadTimeout))
	r := io.MultiReader(bytes.NewReader([]byte{brace}), conn)
	cb, err := hookclient.Decode(r)
	if err != nil {
		return
	}
	_ = d.eng.HandleCallback(cb) // engine authenticates; rejection is expected pre-Epic-11
}

// serveVersionHandshake answers the daemon's version handshake with the daemon's
// protocol version and closes (Epic 5 / D-8): the caller (daemon.Dial, used by
// Restart / EnsureDaemon) classifies a mismatch as skew. The leading tag byte is
// already consumed; this reads the 4-byte version payload (draining the client's
// write) and replies with the 4-byte version. Preserving it on the assembled socket
// keeps those daemon-internal probes working now that the socket speaks the full
// client protocol.
func serveVersionHandshake(conn net.Conn) {
	defer conn.Close()
	var payload [versionPayloadLen]byte
	if _, err := io.ReadFull(conn, payload[:]); err != nil {
		return
	}
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], uint32(daemon.ProtocolVersion))
	_ = conn.SetWriteDeadline(time.Now().Add(demuxReadTimeout))
	_, _ = conn.Write(out[:])
}

// prefixedConn is a net.Conn whose Read replays bytes already consumed from the
// underlying connection (the demux prefix), delegating everything else. It lets
// the protocol Server read the connection from its true first byte.
type prefixedConn struct {
	net.Conn
	r io.Reader
}

// prefixConn wraps conn so its first reads yield the already-consumed prefix bytes,
// then the rest of conn.
func prefixConn(conn net.Conn, prefix ...byte) net.Conn {
	return &prefixedConn{
		Conn: conn,
		r:    io.MultiReader(bytes.NewReader(prefix), conn),
	}
}

func (p *prefixedConn) Read(b []byte) (int, error) { return p.r.Read(b) }
