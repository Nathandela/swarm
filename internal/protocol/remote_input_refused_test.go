package protocol

// FAILING-FIRST fail-closed tests for interactive control on the REMOTE tier
// (review finding HIGH-2). The remote-tier Server must refuse the interactive-
// control path — OpAttach, OpResize, and raw wire.TDataIn input frames — because
// there is no signed take_control gate yet. Without this, a compromised gateway
// dialing the remote socket could establish a lease and inject keystrokes into any
// session with NO device signature (handleAttach/handleResize/handleDataIn today
// dispatch identically on the owner and remote tiers).
//
// The security property under test is the daemon-side SIDE EFFECT, not error prose:
//   - a remote OpAttach opens no upstream stream / establishes no lease;
//   - a remote OpResize never reaches the session's shim (no forwardResize);
//   - a remote raw input frame never reaches the session's shim (no forwardInput).
// The owner (local) tier MUST keep working unchanged (R-POL.1 local exemption):
// attach/resize/input are normal interactive control there — pinned by the
// regression guard below so the fix stays tier-scoped.

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/wire"
)

// nextControl reads frames off rc until the first wire.TControl, skipping the
// snapshot/live-output frames a (pre-fix) attach pump may interleave, and returns
// the decoded Control. The attach reply is OpLease on the vulnerable remote tier
// and OpError once the fail-closed gate lands, so callers must not assume either.
func nextControl(t *testing.T, rc *rawConn) Control {
	t.Helper()
	for i := 0; i < 32; i++ {
		typ, payload, err := rc.readFrame()
		if err != nil {
			t.Fatalf("read control frame: %v", err)
		}
		if typ != wire.TControl {
			continue // snapshot/live output emitted by an attach pump
		}
		c, err := DecodeControl(payload)
		if err != nil {
			t.Fatalf("DecodeControl: %v", err)
		}
		return c
	}
	t.Fatalf("no control frame within frame budget")
	return Control{}
}

// syncControlOp reads frames off rc until a wire.TControl whose Op == want,
// skipping interleaved snapshot/live frames. Because a single connection is served
// by one in-order loop, receiving the reply to a trailing op (e.g. OpList) proves
// every frame written before it — the resize / input under test — was already fully
// handled, so absence assertions are deterministic without sleeping.
func syncControlOp(t *testing.T, rc *rawConn, want string) Control {
	t.Helper()
	for i := 0; i < 32; i++ {
		typ, payload, err := rc.readFrame()
		if err != nil {
			t.Fatalf("read control frame waiting for op %q: %v", want, err)
		}
		if typ != wire.TControl {
			continue
		}
		c, err := DecodeControl(payload)
		if err != nil {
			t.Fatalf("DecodeControl: %v", err)
		}
		if c.Op == want {
			return c
		}
	}
	t.Fatalf("did not observe control op %q within frame budget", want)
	return Control{}
}

// TestProtocol_RemoteAttachRefusedNoLeaseEstablished: OpAttach on a remote-tier
// Server is refused not_authorized and opens NO upstream stream (no lease). Today
// the remote tier attaches like the owner tier, so this fails: the reply is a lease
// grant and DaemonAPI.Attach was called.
func TestProtocol_RemoteAttachRefusedNoLeaseEstablished(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpAttach, EndpointID: rep.EndpointID, SessionID: sid})
	got := nextControl(t, rc)
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("remote attach = op %q code %q; want error/not_authorized (fail-closed, no take_control gate)", got.Op, got.ErrorCode)
	}
	if n := stub.streamCount(); n != 0 {
		t.Fatalf("remote attach opened %d upstream streams; want 0 (no lease may be established on the remote tier)", n)
	}
}

// TestProtocol_RemoteResizeNotForwardedToSession: the interactive-control sequence a
// compromised gateway would use (attach to obtain a lease, then resize) must not
// reach the session's shim on the remote tier. Asserts the daemon side effect:
// forwardResize never fired. Today attach succeeds and the resize is forwarded, so
// the injected dimensions appear on the stub stream and this fails.
func TestProtocol_RemoteResizeNotForwardedToSession(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpAttach, EndpointID: rep.EndpointID, SessionID: sid})
	reply := nextControl(t, rc) // OpLease (vulnerable) carries the lease generation; OpError (fixed) carries 0

	rc.writeControl(Control{Op: OpResize, EndpointID: rep.EndpointID, SessionID: sid, Generation: reply.Generation, Cols: 123, Rows: 45})
	// Ordered sync: once the OpList reply arrives, the resize before it was handled.
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	if st := stub.lastStream(); st != nil {
		for _, rz := range st.resizesCopy() {
			if rz == [2]int{123, 45} {
				t.Fatalf("remote resize %v reached the session shim; want it dropped (HIGH-2: unsigned remote interactive control)", rz)
			}
		}
	}
}

// TestProtocol_RemoteInputFrameNotForwardedNoKeystrokeInjection: a raw wire.TDataIn
// frame on the remote tier must NOT reach the session's shim — this is the
// keystroke-injection vector. Asserts the daemon side effect: forwardInput never
// fired. Today attach binds the connection as controller and the input is forwarded,
// so the injected bytes appear on the stub stream and this fails.
func TestProtocol_RemoteInputFrameNotForwardedNoKeystrokeInjection(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpAttach, EndpointID: rep.EndpointID, SessionID: sid})
	_ = nextControl(t, rc) // drain the attach reply (lease grant when vulnerable, error when fixed)

	inject := []byte("rm -rf ~\r")
	rc.writeFrame(wire.TDataIn, inject)
	// Ordered sync: once the OpList reply arrives, the input frame before it was handled.
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	if st := stub.lastStream(); st != nil && bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("remote raw input reached the session shim (keystroke injection, HIGH-2): %q", st.inputBytes())
	}
}

// TestProtocol_RemoteAttachGate_OwnerTierStillForwardsInputAndResize is the
// regression guard proving the fail-closed gate is TIER-SCOPED (R-POL.1): the SAME
// attach -> input -> resize sequence on an OWNER-tier Server still works — attach is
// granted a lease, and both the input and resize reach the session's shim. This must
// stay green before and after the fix; if it breaks, the fix leaked into local
// interactive control.
func TestProtocol_RemoteAttachGate_OwnerTierStillForwardsInputAndResize(t *testing.T) {
	stub := newStubDaemon()
	sock := serveStub(t, stub) // owner (main) tier
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapAttach})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpAttach, EndpointID: rep.EndpointID, SessionID: sid})
	lease := nextControl(t, rc)
	if lease.Op != OpLease {
		t.Fatalf("owner-tier attach = op %q code %q; want a lease grant (local interactive control must work)", lease.Op, lease.ErrorCode)
	}

	inject := []byte("echo hi\r")
	rc.writeFrame(wire.TDataIn, inject)
	rc.writeControl(Control{Op: OpResize, EndpointID: rep.EndpointID, SessionID: sid, Generation: lease.Generation, Cols: 90, Rows: 24})
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)

	st := stub.lastStream()
	if st == nil {
		t.Fatalf("owner-tier attach opened no upstream stream; want one (attach must establish a lease)")
	}
	if !bytes.Contains(st.inputBytes(), inject) {
		t.Fatalf("owner-tier input not forwarded to the shim: got %q, want it to contain %q", st.inputBytes(), inject)
	}
	found := false
	for _, rz := range st.resizesCopy() {
		if rz == [2]int{90, 24} {
			found = true
		}
	}
	if !found {
		t.Fatalf("owner-tier resize not forwarded to the shim: got %v, want [90 24]", st.resizesCopy())
	}
}
