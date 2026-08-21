package protocol

// WAVE R8 / ROUND 2 -- THE CONTROL HALF, DRIVEN OVER THE REAL ASSEMBLED REMOTE-TIER SERVER.
//
// WHY THIS FILE EXISTS AT ALL, stated as the process defect it repairs. Round 1 shipped
// `terminal_control_begin`, `terminal_control_end`, `terminal_input` and
// `terminal_control_keepalive` LIVE ON THE WIRE -- `server.go` dispatches all four and
// `remotegw/command_loop.go` forwards them from the relay, so they are reachable end to end
// from a paired device -- while every test over them was a source grep, a JSON-shape
// assertion or a constant comparison. Not one drove the assembled server. Standing
// constraint 3 ("tests must drive the REAL assembled path at least once per seam") was met
// for the read half and not for this one, and three security defects lived in the gap:
//
//  1. THE KEEPALIVE EXTENDED THE SIGNED HORIZON, so a phone held raw-input authority over a
//     live terminal indefinitely by simply not releasing it -- measured at t+4h40m through
//     a fifteen-minute wall. ADR-017 T7: "There is no silent renewal, and no keepalive
//     extends the signed horizon."
//  2. THE KILL SWITCH ONLY PAUSED A GENERATION. `swarm remote off` then `on` resumed the
//     identical generation typing onto the PTY with no fresh signed begin -- verbatim the
//     defect `SeverAllRemoteControl`'s own comment says it exists to prevent for the lease.
//  3. DEVICE REVOCATION ONLY PAUSED IT TOO, for the same root cause: `cc.termGen` was
//     referenced nowhere outside `remote_terminal.go`.
//
// Each is asserted below by DRIVING IT, and each names the byte count at the sink, because
// "refused" and "refused after writing" are the same reply and different outcomes.

import (
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// ---------------------------------------------------------------------------
// The backend: a capability lookup, a device registry, a kill switch and a PTY sink.
// ---------------------------------------------------------------------------

// controlBackendStub is the assembled backend the control plane needs, and NOTHING more: a
// per-session capability record granting terminal control, a toggleable device registry, a
// toggleable kill switch, and a TerminalInputSink that COUNTS BYTES. The byte count is the
// point of every assertion in this file -- a refusal that wrote first is not a refusal.
type controlBackendStub struct {
	*stubDaemon

	mu       sync.Mutex
	records  map[string]SessionCapabilities
	devices  map[string]bool
	killOpen bool
	written  [][]byte
}

func newControlBackendStub() *controlBackendStub {
	return &controlBackendStub{
		stubDaemon: newStubDaemon(),
		records:    map[string]SessionCapabilities{},
		devices:    map[string]bool{"devA": true},
		killOpen:   true,
	}
}

func (c *controlBackendStub) SessionCapabilities(local string) (SessionCapabilities, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.records[local]
	return rec, ok
}

func (c *controlBackendStub) setRecord(local string, rec SessionCapabilities) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records[local] = rec
}

func (c *controlBackendStub) DeviceRegistered(deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.devices[deviceID]
}

func (c *controlBackendStub) setDevice(deviceID string, paired bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.devices[deviceID] = paired
}

func (c *controlBackendStub) RemoteControlEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.killOpen
}

func (c *controlBackendStub) setKillSwitch(open bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.killOpen = open
}

// TerminalInput is the PTY. Every byte that reaches it is a byte a real terminal received.
func (c *controlBackendStub) TerminalInput(local string, p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written = append(c.written, append([]byte(nil), p...))
	return nil
}

func (c *controlBackendStub) writes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.written)
}

// ---------------------------------------------------------------------------
// The rig.
// ---------------------------------------------------------------------------

// r8ControlRig is one connected, authorised phone holding one live control generation over
// one terminal_fallback session. It freezes the server clock so every horizon in this file
// is driven rather than slept through.
type r8ControlRig struct {
	t    *testing.T
	stub *controlBackendStub
	srv  *Server
	rc   *rawConn
	// sock is the server's socket path, so a test can open the SECOND connection the
	// gateway's per-command dial actually uses (round-3 blocker 1).
	sock       string
	endpoint   string
	session    string
	generation string
	base       time.Time
}

// newControlRig assembles the server, opens the connection and mints the generation. It
// restores the global server clock in t.Cleanup, and the Server is closed by
// serveRemoteAPISrv's own cleanup, so nothing this rig starts outlives the test.
func newControlRig(t *testing.T) *r8ControlRig {
	t.Helper()
	stub := newControlBackendStub()
	stub.setRecord("sess1", SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true, TerminalControl: true,
	})
	// A STABLE endpoint id, because the assembled daemon serves its remote socket with one
	// (ServeRemoteWithID) and resolveSession requires every session id to be namespaced with
	// the CONNECTION's endpoint. A per-connection id would make a second connection unable to
	// name the first connection's session at all -- which is a second way a single-connection
	// test differs from the product, where ForwardCommand dials per command (round-3 blocker 1).
	sock, srv := serveRemoteAPIStableSrv(t, stub, "mach1")

	base := time.Now()
	old := serverNowNS.Load()
	serverNowNS.Store(base.UnixNano())
	t.Cleanup(func() { serverNowNS.Store(old) })

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	rig := &r8ControlRig{
		t: t, stub: stub, srv: srv, rc: rc, sock: sock,
		endpoint: rep.EndpointID, session: rep.EndpointID + "/sess1", base: base,
	}
	rig.generation = rig.begin()
	return rig
}

// at advances the frozen server clock to base+d.
func (r *r8ControlRig) at(d time.Duration) {
	serverNowNS.Store(r.base.Add(d).UnixNano())
}

// begin drives one SIGNED terminal_control_begin and returns the minted generation.
func (r *r8ControlRig) begin() string {
	r.t.Helper()
	exp := r.base.Add(time.Hour)
	r.rc.writeControl(Control{
		Op: OpTerminalControlBegin, EndpointID: r.endpoint, SessionID: r.session,
		OperationID: "devA:01JBEGIN0000000000000001", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion: schema.CurrentProfileVersion,
		TerminalControlBegin: &TerminalControlBeginReq{
			Session: r.session, SessionInstance: "inst-1", Profile: schema.CurrentProfileVersion,
		},
	})
	got := r.rc.readControl()
	if got.Op != OpOK || got.ControlGeneration == "" {
		r.t.Fatalf("terminal_control_begin = op %q code %q error %q; want ok with a minted generation",
			got.Op, got.ErrorCode, got.Error)
	}
	return got.ControlGeneration
}

// typeByte drives one terminal_input frame and returns the reply.
func (r *r8ControlRig) typeByte(b string) Control {
	r.t.Helper()
	r.rc.writeControl(Control{
		Op: OpTerminalInput, EndpointID: r.endpoint, SessionID: r.session,
		TerminalInput: &TerminalInputReq{
			Session: r.session, SessionInstance: "inst-1",
			ControlGeneration: r.generation, Bytes: []byte(b),
		},
	})
	return r.rc.readControl()
}

// keepalive drives one terminal_control_keepalive frame and returns the reply.
func (r *r8ControlRig) keepalive() Control {
	r.t.Helper()
	r.rc.writeControl(Control{
		Op: OpTerminalControlKeepalive, EndpointID: r.endpoint, SessionID: r.session,
		ControlGeneration: r.generation,
	})
	return r.rc.readControl()
}

// ---------------------------------------------------------------------------
// The positive control, first: none of the refusals below are worth anything if
// input never worked in the first place.
// ---------------------------------------------------------------------------

// TestR8Control_ALiveGenerationTypesOntoThePTY is the vacuity guard for this whole file.
func TestR8Control_ALiveGenerationTypesOntoThePTY(t *testing.T) {
	rig := newControlRig(t)
	if got := rig.typeByte("x"); got.Op != OpOK {
		t.Fatalf("terminal_input under a live generation = op %q code %q error %q; want ok",
			got.Op, got.ErrorCode, got.Error)
	}
	if n := rig.stub.writes(); n != 1 {
		t.Fatalf("PTY received %d writes under a live generation; want 1. Every refusal in this "+
			"file is measured against this number, so a zero here would make all of them vacuous", n)
	}
}

// TestR8Control_TheKeepaliveDoesNotExtendTheSignedHorizon is round-2 BLOCKER 1.
//
// ADR-017 T7 is explicit: "There is no silent renewal, and no keepalive extends the signed
// horizon." T7's stated job is to bound "authority which cannot be revoked in real time"
// for "a phone that is off, out of coverage or in an attacker's hands after the transport
// dropped" -- and a keepalive that moves the wall erases exactly that bound, without limit,
// because the phone need only keep asking.
func TestR8Control_TheKeepaliveDoesNotExtendTheSignedHorizon(t *testing.T) {
	rig := newControlRig(t)

	// A DILIGENT PHONE: it renews well inside the missing-keepalive deadline, right up to the
	// signed wall. This is the attacker's best play and it is also exactly what an honest
	// foreground screen does, which is why the horizon and the keepalive deadline must be
	// different clocks -- and why this loop runs to the wall rather than stopping short. A
	// probe taken while the keepalive deadline has ALSO lapsed proves nothing about the
	// horizon: either clause would refuse it, and the weaker one would mask the stronger.
	step := TerminalKeepaliveTTL / 2
	var last time.Duration
	for at := step; at < TerminalControlTTL; at += step {
		rig.at(at)
		if got := rig.keepalive(); got.Op != OpOK {
			t.Fatalf("keepalive at t+%s = op %q code %q; want ok (it is inside the signed horizon "+
				"and inside the missing-keepalive deadline)", at, got.Op, got.ErrorCode)
		}
		last = at
	}
	if got := rig.typeByte("a"); got.Op != OpOK {
		t.Fatalf("input at t+%s under continuous keepalives = op %q code %q; want ok", last, got.Op, got.ErrorCode)
	}
	before := rig.stub.writes()

	// PAST THE SIGNED WALL, AND WITH THE KEEPALIVE DEADLINE STILL FRESH. The last renewal's
	// deadline has not passed at the probe instant -- the guard below refuses to run otherwise
	// -- so the ONLY clause that can refuse what follows is the signed horizon.
	probe := TerminalControlTTL + time.Second
	if last+TerminalKeepaliveTTL <= probe {
		t.Fatalf("the fixture is mis-tuned: the keepalive deadline (%s) lapses before the probe at "+
			"%s, so this test would pass on the missing-keepalive clause and measure nothing about "+
			"the horizon", last+TerminalKeepaliveTTL, probe)
	}
	rig.at(probe)
	if ka := rig.keepalive(); ka.Op != OpError || ka.ErrorCode != CodeStaleGeneration {
		t.Fatalf("keepalive past the signed horizon = op %q code %q; want error/%s -- a keepalive "+
			"the server accepts past the wall is a wall the phone can walk through, and this is "+
			"exactly how a phone held raw-input authority for 4h40m through a 15m horizon",
			ka.Op, ka.ErrorCode, CodeStaleGeneration)
	}
	got := rig.typeByte("b")
	if got.Op != OpError || got.ErrorCode != CodeStaleGeneration {
		t.Fatalf("input past the signed horizon, under continuous keepalives = op %q code %q; want error/%s. "+
			"ADR-017 T7: no keepalive extends the signed horizon -- otherwise a phone holds "+
			"raw-input authority over a live terminal for as long as it keeps asking, which is "+
			"the unrevocable authority the horizon exists to bound.",
			got.Op, got.ErrorCode, CodeStaleGeneration)
	}
	if n := rig.stub.writes(); n != before {
		t.Fatalf("PTY received %d writes past the signed horizon (was %d); want none. A refusal "+
			"that wrote first is not a refusal", n-before, before)
	}

}

// TestR8Control_AnUnrenewedGenerationDiesOnTheKeepaliveDeadline is the other half of the
// same split: the missing-keepalive clock (T8) is REAL and much shorter than the horizon,
// so a phone that goes quiet loses control in seconds rather than in a quarter of an hour.
func TestR8Control_AnUnrenewedGenerationDiesOnTheKeepaliveDeadline(t *testing.T) {
	rig := newControlRig(t)
	rig.at(TerminalKeepaliveTTL + time.Second)
	got := rig.typeByte("a")
	if got.Op != OpError || got.ErrorCode != CodeStaleGeneration {
		t.Fatalf("input after %s of silence = op %q code %q; want error/%s",
			TerminalKeepaliveTTL, got.Op, got.ErrorCode, CodeStaleGeneration)
	}
	if n := rig.stub.writes(); n != 0 {
		t.Fatalf("PTY received %d writes past the keepalive deadline; want 0", n)
	}
}

// TestR8Control_AnIdleGenerationIsSweptOnTheServersOwnClock is amendment T6-c: the daemon's
// expiry "fires on an idle generation with no inbound frames at all, never driven off frame
// arrival".
//
// The distinction is not academic. A deadline consulted only when a frame arrives is a
// VALIDITY TEST ON THE FRAME, not an expiry: the generation stays in the server's own state,
// so a kill switch flipped OFF and back ON, or a device revoked and re-paired, finds it
// there. This drives the REAL ticker -- the clock is frozen and advanced, and the test waits
// for the sweep goroutine the assembled server started, rather than calling the sweep itself.
func TestR8Control_AnIdleGenerationIsSweptOnTheServersOwnClock(t *testing.T) {
	rig := newControlRig(t)
	rig.at(TerminalControlTTL + time.Minute) // past BOTH walls, with no frame sent

	deadline := time.Now().Add(10 * time.Second)
	for rig.srv.anyLiveTerminalGeneration() {
		if time.Now().After(deadline) {
			t.Fatalf("the server still holds a terminal control generation %s after both of its "+
				"walls passed, with no inbound frame. ADR-017 T6-c: the expiry fires on an IDLE "+
				"generation and is never driven off frame arrival -- otherwise the generation "+
				"survives in server state and a kill switch or a revoke that flips back resumes it.",
				TerminalControlTTL+time.Minute)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := rig.stub.writes(); n != 0 {
		t.Fatalf("PTY received %d writes; want 0", n)
	}
}

// TestR8Control_TheKillSwitchSeversRatherThanPauses is round-2 BLOCKER 2.
//
// `SeverAllRemoteControl`'s own comment states the rule for the lease: "the per-keystroke
// controlGateOpen clause-1 drop only PAUSES a live lease, so turning the switch back ON
// before the signed expiry would silently RESUME it." The generation had exactly that
// defect, measured: OFF -> sever -> input refused (good) -> ON -> THE SAME GENERATION typed
// onto the PTY, with no new terminal_control_begin and no new device signature.
func TestR8Control_TheKillSwitchSeversRatherThanPauses(t *testing.T) {
	rig := newControlRig(t)

	rig.stub.setKillSwitch(false)
	rig.srv.SeverAllRemoteControl()

	if got := rig.typeByte("a"); got.Op != OpError {
		t.Fatalf("input with the kill switch OFF = op %q; want an error", got.Op)
	}

	// THE SWITCH COMES BACK ON. This is the whole test: what survives the round trip?
	rig.stub.setKillSwitch(true)
	got := rig.typeByte("b")
	if got.Op != OpError || got.ErrorCode != CodeStaleGeneration {
		t.Fatalf("input after the kill switch went OFF and back ON = op %q code %q; want "+
			"error/%s. ADR-017 T8 lists the kill switch as SYNCHRONOUS AT THE DAEMON: a lazy "+
			"per-frame drop merely pauses the generation, and a pause reverses. Resuming control "+
			"must cost a fresh signed terminal_control_begin.",
			got.Op, got.ErrorCode, CodeStaleGeneration)
	}
	if n := rig.stub.writes(); n != 0 {
		t.Fatalf("PTY received %d writes across the kill-switch round trip; want 0", n)
	}
}

// TestR8Control_DeviceRevocationSeversRatherThanPauses is round-2 MAJOR 8, the same root
// cause one authority over. ADR-017 T8 lists device revocation as "Synchronous at the
// daemon"; it was a lazy `DeviceRegistered` poll, and re-pairing the same device id resumed
// the same generation.
func TestR8Control_DeviceRevocationSeversRatherThanPauses(t *testing.T) {
	rig := newControlRig(t)

	rig.stub.setDevice("devA", false)
	rig.srv.severRevokedDeviceControl("devA")

	if got := rig.typeByte("a"); got.Op != OpError {
		t.Fatalf("input from a revoked device = op %q; want an error", got.Op)
	}

	// THE SAME DEVICE ID IS RE-PAIRED. A device that pairs again is a device the owner
	// approved again -- for a FRESH generation, not for the one it held before it was revoked.
	rig.stub.setDevice("devA", true)
	got := rig.typeByte("b")
	if got.Op != OpError || got.ErrorCode != CodeStaleGeneration {
		t.Fatalf("input after the device was revoked and re-paired = op %q code %q; want "+
			"error/%s. A revoke that only pauses is a revoke that reverses.",
			got.Op, got.ErrorCode, CodeStaleGeneration)
	}
	if n := rig.stub.writes(); n != 0 {
		t.Fatalf("PTY received %d writes across the revoke round trip; want 0", n)
	}
}

// TestR8Control_ReleasedControlCannotBeResumed pins the ordinary path for completeness: an
// explicit end is as final as a severance, and re-typing under the ended generation is
// refused rather than silently accepted by a connection that still remembers it.
func TestR8Control_ReleasedControlCannotBeResumed(t *testing.T) {
	rig := newControlRig(t)
	exp := rig.base.Add(time.Hour)
	rig.rc.writeControl(Control{
		Op: OpTerminalControlEnd, EndpointID: rig.endpoint, SessionID: rig.session,
		OperationID: "devA:01JEND00000000000000001", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion: schema.CurrentProfileVersion,
	})
	if got := rig.rc.readControl(); got.Op != OpOK {
		t.Fatalf("terminal_control_end = op %q code %q; want ok", got.Op, got.ErrorCode)
	}
	got := rig.typeByte("a")
	if got.Op != OpError || got.ErrorCode != CodeStaleGeneration {
		t.Fatalf("input after release = op %q code %q; want error/%s", got.Op, got.ErrorCode, CodeStaleGeneration)
	}
	if n := rig.stub.writes(); n != 0 {
		t.Fatalf("PTY received %d writes after release; want 0", n)
	}
}
