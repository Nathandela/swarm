package protocol

// ADVERSARIAL confirmation test for slice A5-d, attack 10 (a second device rides
// another's control session). This is NOT a new gate: it pins the per-connection
// fail-closed default (controlGateOpen clause 2) already landed by A5-b, closing out
// the take_control adversarial acceptance bar. It must PASS against current code.
//
// The control lease lives on the *clientConn* that ran take_control (cc.control /
// cc.attSession are per-connection), so a SEPARATE connection that never took control
// of its own has cc.control == nil AND cc.attSession == "". A raw wire.TDataIn frame
// it sends is therefore dropped twice over: at controlGateOpen (clause 2, fail-closed)
// and again at the no-attach guard in handleDataIn. One device cannot inject keystrokes
// into a session merely because ANOTHER device holds a lease on it.
//
// The security property under test is the daemon-side SIDE EFFECT, not error prose:
// the second connection's bytes must NEVER reach the controller's shim stream, while
// the legitimate controller's own bytes DO (proving the stream is live and the drop is
// specific to the rider, not a dead pipe).
//
// Harness reuse (sibling _test.go files, package protocol): serveRemote (remote-tier
// Server), newStubDaemon (accepting authz by default), takeControl (drives a signed,
// authorized take_control and returns the OpLease grant), syncControlOp (ordered sync
// via a trailing OpList reply), and stubStream.inputBytes (the shim side effect).

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/wire"
)

// TestProtocol_SecondDeviceRidesSessionDropped: connection A runs an authorized
// take_control over sess1 (the one lease/stream). A SEPARATE connection B — same remote
// server, no take_control of its own — sends a raw input frame. B's bytes are dropped
// (fail-closed default: B's cc.control is nil), while A's own keystroke reaches the
// shim. A lease is bound to the connection that took control; a second device cannot
// ride it.
func TestProtocol_SecondDeviceRidesSessionDropped(t *testing.T) {
	stub := newStubDaemon() // authzFn nil => A's signed take_control is accepted
	sock := serveRemote(t, stub)

	// Connection A establishes the control session (the one and only attach/stream).
	rcA := rawDial(t, sock)
	repA := rcA.hello(Version, []string{CapRemoteGateway})
	sid := repA.EndpointID + "/sess1"
	takeControl(t, rcA, repA.EndpointID, sid, 3600)

	// A's own keystroke reaches the shim: proves the stream is live and A is controller.
	aInput := []byte("echo A\r")
	rcA.writeFrame(wire.TDataIn, aInput)
	rcA.writeControl(Control{Op: OpList, EndpointID: repA.EndpointID})
	syncControlOp(t, rcA, OpList)

	// Connection B: a distinct device/connection that never ran its own take_control.
	rcB := rawDial(t, sock)
	repB := rcB.hello(Version, []string{CapRemoteGateway})

	// B rides A's session: a raw input frame with NO control session of its own.
	bInput := []byte("rm -rf ~\r")
	rcB.writeFrame(wire.TDataIn, bInput)
	rcB.writeControl(Control{Op: OpList, EndpointID: repB.EndpointID})
	syncControlOp(t, rcB, OpList)

	st := stub.lastStream()
	if st == nil {
		t.Fatalf("A's take_control opened no upstream stream; want the control lease's stream")
	}
	if !bytes.Contains(st.inputBytes(), aInput) {
		t.Fatalf("controller A's own input never reached the shim: got %q, want it to contain %q", st.inputBytes(), aInput)
	}
	if bytes.Contains(st.inputBytes(), bInput) {
		t.Fatalf("second device B rode A's control session: its input reached the shim "+
			"(per-connection fail-closed default violated): %q", st.inputBytes())
	}
}
