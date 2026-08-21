package protocol

// FINAL AUDIT COMMITTEE, FENCE FOR OPUS H2 -- THE PARKED-CONTROL ARM, DRIVEN.
//
// r8r4_parkedcontrol_test.go (internal/verify) pins that TerminalInputSink has no
// production IMPLEMENTOR. That is an interface-level fact; the actual gate is the
// CodeNotImplemented arm in handleTerminalInput (remote_terminal.go): a backend that is
// not a TerminalInputSink refuses every input frame WITHOUT delivering the bytes any
// other way. Nothing pinned that behaviour, so the arm could be rewritten to route the
// bytes around the interface -- Attach + SessionStream.Input, for instance -- while the
// interface scan stayed green. This file closes that evasion by DRIVING the real
// assembled server over a sink-less backend and counting every write path the backend
// exposes.

import (
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// noSinkBackendStub is controlBackendStub minus the sink: capability record, device
// registry and kill switch so an input frame passes every gate BEFORE the sink check,
// and NO TerminalInput method. The embedded stubDaemon records every DaemonAPI call, so
// "nothing was delivered" is measured, not assumed.
type noSinkBackendStub struct {
	*stubDaemon

	mu      sync.Mutex
	records map[string]SessionCapabilities
	devices map[string]bool
}

func newNoSinkBackendStub() *noSinkBackendStub {
	return &noSinkBackendStub{
		stubDaemon: newStubDaemon(),
		records:    map[string]SessionCapabilities{},
		devices:    map[string]bool{"devA": true},
	}
}

func (c *noSinkBackendStub) SessionCapabilities(local string) (SessionCapabilities, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.records[local]
	return rec, ok
}

func (c *noSinkBackendStub) DeviceRegistered(deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.devices[deviceID]
}

// TestR8R4Parked_NoSinkInputRefusedWithoutAnyDelivery is the behavioural half of the
// parked-control ledger row: a live, fully authorised generation over a backend with no
// TerminalInputSink gets op_not_implemented, and NOT ONE byte reaches ANY write path the
// backend exposes -- no Attach (so no SessionStream.Input), no Launch, no Kill, nothing.
//
// The code being CodeNotImplemented is itself the proof the frame passed the capability,
// device and generation gates and reached the sink arm; any earlier refusal answers a
// different code and would fail the assertion.
func TestR8R4Parked_NoSinkInputRefusedWithoutAnyDelivery(t *testing.T) {
	stub := newNoSinkBackendStub()
	// ANTI-VACUITY: the whole point is a backend that is NOT a TerminalInputSink. If this
	// stub ever grows a TerminalInput method, the test measures nothing.
	if _, ok := interface{}(stub).(TerminalInputSink); ok {
		t.Fatalf("noSinkBackendStub implements TerminalInputSink; this fence requires a sink-less backend")
	}
	stub.records["sess1"] = SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true, TerminalControl: true,
	}
	sock, _ := serveRemoteAPIStableSrv(t, stub, "mach1")

	base := time.Now()
	old := serverNowNS.Load()
	serverNowNS.Store(base.UnixNano())
	t.Cleanup(func() { serverNowNS.Store(old) })

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	session := rep.EndpointID + "/sess1"

	// Mint a LIVE generation: begin does not need the sink, and a refused begin would make
	// the input refusal below prove nothing about the sink arm.
	exp := base.Add(time.Hour)
	rc.writeControl(Control{
		Op: OpTerminalControlBegin, EndpointID: rep.EndpointID, SessionID: session,
		OperationID: "devA:01JBEGIN0000000000000001", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion: schema.CurrentProfileVersion,
		TerminalControlBegin: &TerminalControlBeginReq{
			Session: session, SessionInstance: "inst-1", Profile: schema.CurrentProfileVersion,
		},
	})
	begun := rc.readControl()
	if begun.Op != OpOK || begun.ControlGeneration == "" {
		t.Fatalf("terminal_control_begin = op %q code %q error %q; want ok with a minted generation",
			begun.Op, begun.ErrorCode, begun.Error)
	}

	rc.writeControl(Control{
		Op: OpTerminalInput, EndpointID: rep.EndpointID, SessionID: session,
		TerminalInput: &TerminalInputReq{
			Session: session, SessionInstance: "inst-1",
			ControlGeneration: begun.ControlGeneration, Bytes: []byte("rm -rf /\n"),
		},
	})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeNotImplemented {
		t.Fatalf("terminal_input on a sink-less backend = op %q code %q error %q; want op %q code %q.\n"+
			"The CodeNotImplemented arm in handleTerminalInput is the parked-control gate (ADR-017 "+
			"amendment C0): a backend that does not implement TerminalInputSink must refuse, never "+
			"deliver the bytes by another route.", got.Op, got.ErrorCode, got.Error, OpError, CodeNotImplemented)
	}

	// NOTHING reached any write path. The stub records every forwarded DaemonAPI call;
	// Attach is the only road to a SessionStream (and its Input), so zero attaches means
	// zero PTY-bound bytes by construction, and the rest close the remaining side doors.
	d := stub.stubDaemon
	d.mu.Lock()
	attached, streams := len(d.attached), len(d.streams)
	launched, killed, deleted := len(d.launched), len(d.killed), len(d.deleted)
	d.mu.Unlock()
	if attached != 0 || streams != 0 {
		t.Fatalf("refused terminal_input still opened %d Attach stream(s); the bytes had a road to a "+
			"PTY. The CodeNotImplemented arm must not deliver input through any seam other than "+
			"TerminalInputSink", attached)
	}
	if launched != 0 || killed != 0 || deleted != 0 {
		t.Fatalf("refused terminal_input forwarded daemon calls (launched=%d killed=%d deleted=%d); "+
			"want none", launched, killed, deleted)
	}
}
