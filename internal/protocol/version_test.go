package protocol

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/version"
	"github.com/Nathandela/swarm/internal/wire"
)

// E13.2 — the daemon's build version is ADDITIVE to the hello handshake: it
// rides alongside ProtocolVersion (the wire skew gate, unchanged) so a client
// can tell it is talking to a different-build daemon even when the wire
// protocol still matches. Neither test touches ProtocolVersion/D-8 skew
// behavior, which TestHandshake_* in handshake_test.go continues to cover.

// TestHandshake_ServerHelloRepliesBuildVersion drives the wire level directly
// (no Client): the server's hello reply Control carries BuildVersion set to
// this build's internal/version.Version.
func TestHandshake_ServerHelloRepliesBuildVersion(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	r := rawDial(t, sock)
	reply := r.hello(Version, nil)
	if reply.BuildVersion != version.Version {
		t.Fatalf("hello reply BuildVersion = %q, want %q (internal/version.Version)", reply.BuildVersion, version.Version)
	}
}

// TestHandshake_ClientExposesDaemonBuildVersion drives the CLIENT side: after a
// successful Dial, Client.BuildVersion() reports the value the daemon replied
// with — the surface a caller uses to detect a different-build daemon and
// nudge `swarm daemon restart`.
func TestHandshake_ClientExposesDaemonBuildVersion(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	c := dialClient(t, sock, nil)
	if c.BuildVersion() != version.Version {
		t.Fatalf("Client.BuildVersion() = %q, want %q (internal/version.Version)", c.BuildVersion(), version.Version)
	}
}

// ---------------------------------------------------------------------------
// Cross-version compatibility (audit-012 F7). BuildVersion is additive/
// omitempty on both sides, so it must not break a peer from before it
// existed, in either direction, and a Client must degrade to "" — never an
// error — when the OTHER side is the one missing it.
// ---------------------------------------------------------------------------

// TestHandshake_OldClientHelloWithoutBuildVersionStillWorks drives an OLD
// client's wire shape at a real server: a hello whose JSON has no
// "build_version" key at all (not merely an empty string — the pre-E13.2 wire
// shape never had the key). The server must still complete the handshake and
// reply with ITS OWN build version; the old client simply doesn't understand
// that field and can ignore it.
func TestHandshake_OldClientHelloWithoutBuildVersionStillWorks(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	r := rawDial(t, sock)
	oldHello := []byte(`{"op":"hello","protocol_version":` + strconv.Itoa(Version) + `}`)
	r.writeFrame(wire.TControl, oldHello)
	reply := r.readControl()
	if reply.Op != OpHello {
		t.Fatalf("old-client (no build_version key) hello reply op = %q, want %q", reply.Op, OpHello)
	}
	if reply.BuildVersion != version.Version {
		t.Fatalf("server hello reply BuildVersion = %q, want %q (server reports its own, regardless of what the client sent)", reply.BuildVersion, version.Version)
	}
}

// TestDecodeControl_BuildVersionAlongsideAnUnknownFutureFieldDecodesCleanly
// drives the forward-compat half: a payload carrying build_version PLUS a
// field that doesn't exist in today's Control schema (standing in for
// whatever a FUTURE writer might add) still decodes cleanly — an old-style
// reader tolerates unknown fields it wasn't built to know about, and
// build_version itself decodes correctly alongside them.
func TestDecodeControl_BuildVersionAlongsideAnUnknownFutureFieldDecodesCleanly(t *testing.T) {
	raw := []byte(`{"op":"hello","protocol_version":1,"build_version":"v9.9.9","some_future_field":{"nested":true}}`)
	c, err := DecodeControl(raw)
	if err != nil {
		t.Fatalf("DecodeControl(build_version + unknown future field) = %v, want no error", err)
	}
	if c.BuildVersion != "v9.9.9" {
		t.Fatalf("BuildVersion = %q, want %q", c.BuildVersion, "v9.9.9")
	}
}

// TestHandshake_ClientBuildVersionEmptyWhenServerOmitsIt drives an OLD
// server's wire shape at a real Client: a hello reply with no "build_version"
// key (the pre-E13.2 daemon shape). Dial must still succeed (BuildVersion is
// never fatal to the handshake), and Client.BuildVersion() must report "" —
// never panic, never fabricate a value.
func TestHandshake_ClientBuildVersionEmptyWhenServerOmitsIt(t *testing.T) {
	sock := tmpSock(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(netTimeout))
		if _, _, rerr := wire.ReadFrame(conn); rerr != nil {
			return
		}
		oldReply := []byte(`{"op":"hello","endpoint_id":"srv","protocol_version":` + strconv.Itoa(Version) + `}`)
		_ = wire.WriteFrame(conn, wire.TControl, oldReply)
	}()

	c, err := Dial(sock, nil)
	if err != nil {
		t.Fatalf("Dial against an old-style server omitting build_version: %v, want success", err)
	}
	defer c.Close()
	if c.BuildVersion() != "" {
		t.Fatalf("Client.BuildVersion() = %q, want %q (server never reported one)", c.BuildVersion(), "")
	}
}
