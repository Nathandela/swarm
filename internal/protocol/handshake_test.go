package protocol

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

// E6.3 — hello handshake: version + capability negotiation, endpoint-id
// assignment, and the D-8 incompatible-version UX (the error names `swarm daemon
// restart` AND states the restart is safe / loses no live sessions).

// TestHandshake_MatchingVersionSucceeds asserts a client at the same version
// completes the handshake and receives an endpoint id.
func TestHandshake_MatchingVersionSucceeds(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	c := dialClient(t, sock, []string{"attach", "subscribe"})
	if c.EndpointID() == "" {
		t.Fatalf("EndpointID after successful Dial is empty")
	}
}

// TestHandshake_EndpointIDsUnique asserts each connection is assigned a distinct
// endpoint id (F-1: multi-endpoint namespacing needs unique endpoints).
func TestHandshake_EndpointIDsUnique(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		c := dialClient(t, sock, nil)
		ep := c.EndpointID()
		if ep == "" {
			t.Fatalf("connection %d got empty endpoint id", i)
		}
		if seen[ep] {
			t.Fatalf("endpoint id %q assigned twice", ep)
		}
		seen[ep] = true
	}
}

// TestHandshake_CapabilityIntersection asserts the negotiated capability set is
// the intersection of what the client offered and what the server supports — a
// capability the client does not offer is not returned, and vice versa.
func TestHandshake_CapabilityIntersection(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	r := rawDial(t, sock)
	// Offer a real capability plus one the server cannot possibly support.
	reply := r.hello(Version, []string{"attach", "totally-made-up-capability"})
	if reply.Op != OpHello {
		t.Fatalf("hello reply op = %q, want hello", reply.Op)
	}
	for _, cap := range reply.Capabilities {
		if cap == "totally-made-up-capability" {
			t.Fatalf("negotiated caps %v include one the client offered but the server does not support", reply.Capabilities)
		}
	}
	// The intersection must not exceed what the client offered either.
	for _, cap := range reply.Capabilities {
		if cap != "attach" {
			t.Fatalf("negotiated cap %q was not in the client's offer {attach, ...}", cap)
		}
	}
}

// TestHandshake_ServerVersionMismatchReturnsD8Error drives the SERVER side of
// D-8: a client speaking an incompatible version gets an error control whose
// message names `swarm daemon restart` AND asserts the restart is safe.
func TestHandshake_ServerVersionMismatchReturnsD8Error(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	r := rawDial(t, sock)
	reply := r.hello(Version+1, nil)
	if reply.Op != OpError {
		t.Fatalf("version-mismatch hello reply op = %q, want %q", reply.Op, OpError)
	}
	assertD8Message(t, reply.Error)
}

// TestHandshake_ClientDialReturnsIncompatibleVersion drives the CLIENT side of
// D-8: dialing a server that answers with a different version returns an error
// that is ErrIncompatibleVersion AND carries the safe-restart message. A tiny
// raw listener stands in for a skewed daemon.
func TestHandshake_ClientDialReturnsIncompatibleVersion(t *testing.T) {
	sock := tmpSock(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(netTimeout))
		// Read the client's hello, then answer hello at a DIFFERENT version.
		if _, _, err := wire.ReadFrame(conn); err != nil {
			return
		}
		body, _ := EncodeControl(Control{Op: OpHello, EndpointID: "srv", ProtocolVersion: Version + 1})
		_ = wire.WriteFrame(conn, wire.TControl, body)
	}()

	_, err = Dial(sock, nil)
	if err == nil {
		t.Fatalf("Dial against a skewed server: err = nil, want ErrIncompatibleVersion")
	}
	if !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("Dial error = %v, want errors.Is(ErrIncompatibleVersion)", err)
	}
	assertD8Message(t, err.Error())
}

// assertD8Message asserts a version-skew message satisfies D-8: it names the fix
// command AND states the restart is safe (loses no live sessions).
func assertD8Message(t *testing.T, msg string) {
	t.Helper()
	if !strings.Contains(msg, "swarm daemon restart") {
		t.Errorf("D-8 message %q does not name `swarm daemon restart`", msg)
	}
	low := strings.ToLower(msg)
	if !strings.Contains(low, "safe") && !strings.Contains(low, "no live sessions") && !strings.Contains(low, "keep running") {
		t.Errorf("D-8 message %q does not state the restart is safe / loses no live sessions", msg)
	}
}
