package protocol

// WAVE R8 / ROUND 3 -- THE CONTROL GENERATION MUST OUTLIVE THE CONNECTION THAT MINTED IT.
//
// THE DEFECT, stated as the product path rather than as a code smell. remotegw's
// Gateway.ForwardCommand DIALS A FRESH DAEMON CONNECTION PER COMMAND and closes it on the
// reply ("A fresh connection is used per command", gateway.go). Round 2 stored the control
// generation on `clientConn.termGen` -- PER CONNECTION -- with no server-wide registry. So
// over the real composition:
//
//	terminal_control_begin      -> conn A, mints gen G, conn A closes
//	terminal_input(G)           -> conn B, cc.termGen == nil -> stale_generation
//
// Measured on the assembled remote-tier server before this file's fix: `op="error"
// code="stale_generation"`, BYTES REACHING THE PTY = 0. Every round-2 control test held ONE
// rawConn for the whole test, so the seam the product actually uses was never driven -- the
// fourth wave in a row to lose a round to a defect only the real composition reveals.
//
// It failed CLOSED, so it was never an exploit. It made the wave's exit -- "OpenCode and AGY
// can be launched and safely monitored AND CONTROLLED from the fallback" -- unreachable by
// anything speaking the protocol through the relay and the gateway, which is the product.
//
// WHAT BINDS A GENERATION NOW THAT THE CONNECTION DOES NOT. Exactly what ADR-017 T6 always
// said bound it, and nothing the connection was carrying by accident: the E2EE seal's own
// authenticated sender, the unguessable 128-bit generation the server minted and returned
// only to the authenticated `terminal_control_begin`, and the per-frame re-evaluation of the
// kill switch, the SIGNING DEVICE's continued registration, the session's capability record,
// the session INSTANCE and both walls (T6-e). The connection identity was never one of them;
// it was an implementation accident that read as a binding.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// secondConn opens a SECOND authorised connection to the same server, which is what
// ForwardCommand does on every single command.
func (r *r8ControlRig) secondConn(t *testing.T) *rawConn {
	t.Helper()
	rc := rawDial(t, r.sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	if rep.EndpointID != r.endpoint {
		t.Fatalf("second connection endpoint = %q, want the server's own %q", rep.EndpointID, r.endpoint)
	}
	return rc
}

// closeMintingConn closes the connection the generation was minted on, which is what
// ForwardCommand does the instant the begin's reply lands.
func (r *r8ControlRig) closeMintingConn() {
	r.t.Helper()
	_ = r.rc.conn.Close()
}

// typeByteOn drives one terminal_input frame down an arbitrary connection.
func (r *r8ControlRig) typeByteOn(rc *rawConn, b string) Control {
	r.t.Helper()
	rc.writeControl(Control{
		Op: OpTerminalInput, EndpointID: r.endpoint, SessionID: r.session,
		TerminalInput: &TerminalInputReq{
			Session: r.session, SessionInstance: "inst-1",
			ControlGeneration: r.generation, Bytes: []byte(b),
		},
	})
	return rc.readControl()
}

// TestR8R3_AGenerationOutlivesTheConnectionThatMintedIt is the whole of blocker 1, driven at
// the frame level: begin on one connection, type on ANOTHER, exactly as the gateway does.
func TestR8R3_AGenerationOutlivesTheConnectionThatMintedIt(t *testing.T) {
	rig := newControlRig(t)
	before := rig.stub.writes()

	other := rig.secondConn(t)
	if got := rig.typeByteOn(other, "x"); got.Op != OpOK {
		t.Fatalf("terminal_input on a SECOND connection = op %q code %q error %q; want ok.\n"+
			"The gateway dials a fresh daemon connection per command, so this is the only shape "+
			"the product ever uses. A generation bound to the minting connection is a generation "+
			"no phone can ever use.", got.Op, got.ErrorCode, got.Error)
	}
	if got := rig.stub.writes(); got != before+1 {
		t.Fatalf("bytes reaching the PTY = %d write(s), want %d", got, before+1)
	}
}

// TestR8R3_AGenerationSurvivesTheMintingConnectionClosing is the same fact one step further:
// ForwardCommand does not merely use another connection, it CLOSES the first one.
func TestR8R3_AGenerationSurvivesTheMintingConnectionClosing(t *testing.T) {
	rig := newControlRig(t)
	rig.closeMintingConn()

	other := rig.secondConn(t)
	if got := rig.typeByteOn(other, "y"); got.Op != OpOK {
		t.Fatalf("terminal_input after the minting connection closed = op %q code %q; want ok",
			got.Op, got.ErrorCode)
	}
	if rig.stub.writes() == 0 {
		t.Fatalf("no byte reached the PTY after the minting connection closed")
	}
}

// TestR8R3_TheKeepaliveAlsoCrossesConnections: the keepalive is the OTHER unsigned frame, and
// it arrives on its own fresh connection too. A keepalive that could not find the generation
// would sever a live screen every 30 seconds.
func TestR8R3_TheKeepaliveAlsoCrossesConnections(t *testing.T) {
	rig := newControlRig(t)
	other := rig.secondConn(t)
	other.writeControl(Control{
		Op: OpTerminalControlKeepalive, EndpointID: rig.endpoint, SessionID: rig.session,
		ControlGeneration: rig.generation,
	})
	if got := other.readControl(); got.Op != OpOK {
		t.Fatalf("terminal_control_keepalive on a second connection = op %q code %q; want ok",
			got.Op, got.ErrorCode)
	}
	// And it renewed the deadline it is supposed to renew: at t+20s the frame is still live.
	rig.at(20 * time.Second)
	if got := rig.typeByteOn(other, "z"); got.Op != OpOK {
		t.Fatalf("terminal_input 20s after a cross-connection keepalive = op %q code %q; want ok",
			got.Op, got.ErrorCode)
	}
}

// TestR8R3_ReleasingOnAnotherConnectionStillReleases: `terminal_control_end` is a SIGNED op,
// and it too arrives on a fresh connection. A release that could only be honoured on the
// minting connection would leave the banner's disappearance a lie -- the phone would believe
// it gave the keyboard back while the generation typed on.
func TestR8R3_ReleasingOnAnotherConnectionStillReleases(t *testing.T) {
	rig := newControlRig(t)
	other := rig.secondConn(t)

	exp := rig.base.Add(time.Hour)
	other.writeControl(Control{
		Op: OpTerminalControlEnd, EndpointID: rig.endpoint, SessionID: rig.session,
		OperationID: "devA:01JBEND00000000000000001", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion: schema.CurrentProfileVersion,
	})
	if got := other.readControl(); got.Op != OpOK {
		t.Fatalf("terminal_control_end on a second connection = op %q code %q error %q; want ok",
			got.Op, got.ErrorCode, got.Error)
	}
	third := rig.secondConn(t)
	before := rig.stub.writes()
	got := rig.typeByteOn(third, "q")
	if got.ErrorCode != CodeStaleGeneration {
		t.Fatalf("terminal_input after a cross-connection release = op %q code %q; want %q",
			got.Op, got.ErrorCode, CodeStaleGeneration)
	}
	if after := rig.stub.writes(); after != before {
		t.Fatalf("a released generation still typed: %d write(s) became %d", before, after)
	}
}

// TestR8R3_AnUnknownGenerationIsRefusedOnAnyConnection is the vacuity guard for every test
// above: "any connection may type" must not have become "any frame may type". The generation
// is a 128-bit unguessable secret the server minted and returned to ONE authenticated begin,
// and possession of it is the authority the unsigned frame carries.
func TestR8R3_AnUnknownGenerationIsRefusedOnAnyConnection(t *testing.T) {
	rig := newControlRig(t)
	other := rig.secondConn(t)
	before := rig.stub.writes()
	other.writeControl(Control{
		Op: OpTerminalInput, EndpointID: rig.endpoint, SessionID: rig.session,
		TerminalInput: &TerminalInputReq{
			Session: rig.session, SessionInstance: "inst-1",
			ControlGeneration: "00000000000000000000000000000000", Bytes: []byte("!"),
		},
	})
	got := other.readControl()
	if got.ErrorCode != CodeStaleGeneration {
		t.Fatalf("terminal_input under a forged generation = op %q code %q; want %q",
			got.Op, got.ErrorCode, CodeStaleGeneration)
	}
	if after := rig.stub.writes(); after != before {
		t.Fatalf("a forged generation typed onto the PTY: %d write(s) became %d", before, after)
	}
}

// TestR8R3_TheKillSwitchStillSeversAcrossConnections re-drives round-2's blocker 2 against the
// registry: the sever must reach a generation whose minting connection is long gone, which is
// every generation the product ever has.
func TestR8R3_TheKillSwitchStillSeversAcrossConnections(t *testing.T) {
	rig := newControlRig(t)
	rig.closeMintingConn()
	rig.srv.SeverAllRemoteControl()

	other := rig.secondConn(t)
	before := rig.stub.writes()
	got := rig.typeByteOn(other, "x")
	if got.Op != OpError {
		t.Fatalf("terminal_input after SeverAllRemoteControl = op %q; want an error", got.Op)
	}
	if after := rig.stub.writes(); after != before {
		t.Fatalf("a severed generation typed onto the PTY: %d write(s) became %d", before, after)
	}
	if rig.srv.anyLiveTerminalGeneration() {
		t.Fatalf("the generation survived SeverAllRemoteControl in the server's own state")
	}
}
