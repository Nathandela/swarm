package protocol

// FAILING-FIRST protocol tests for ADR-010 Amendment 1 A3 / Phase 3 PIECE 2: owner-tier
// peek.
//
// A3 is explicit that this is NOT a port. The merged terminal_subscribe handler already
// renders a session server-side and streams sanitized terminal_snapshot frames (A7 slice
// F2, server.go:1929); the ONLY change is AUTHORIZATION. An owner-tier connection on the
// main socket may ask for a snapshot without the remote preconditions — no device auth, no
// remote-control kill switch, no negotiated remote-gateway capability — because ADR-004's
// v1 trust model already grants any same-user process on that socket full daemon power.
// The REMOTE path keeps every check it has, and both directions are pinned here so a later
// edit cannot quietly widen the remote gate while "relaxing the local one".
//
// The backend seam needs nothing new: the owner-tier Server and the remote-tier Server are
// built on the SAME coreAPI (skeleton/serve.go:251 and :276), and coreAPI already
// implements TerminalTapper by subscribing read-only to the shared per-session tap
// (skeleton/api.go:794) — the tap is a daemon-side multiplexer, not a remote-gateway
// component, so it is reachable with no gateway running. What the handler must stop doing
// on the owner tier is REQUIRING the remote preconditions; the TerminalTapper backend
// itself stays required (a backend that cannot tap must refuse, not hang).
//
// FROZEN API (the implementer wires it):
//
//	// client.go, the house style of Launch (:128) / Kill (:144): request the peek and
//	// return the FIRST server-rendered snapshot. The render loop pushes the session's
//	// current grid before any new output (daemon.RenderTerminal -> renderInitial), so a
//	// one-shot peek of an idle session returns at once instead of waiting for output.
//	func (c *Client) TerminalSnapshot(id string) (*TerminalSnapshot, error)
//
// RED today: Client.TerminalSnapshot does not exist (compile-fail red), and the
// authorization tests fail on the still-remote-gated handler once it does.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// peekTextSnapshot builds a real emulator snapshot carrying the given lines, so a tap
// serves a grid the render loop can decode and push IMMEDIATELY (peekGridSnapshot's shape,
// with text instead of a fill character).
func peekTextSnapshot(t *testing.T, cols, rows int, lines ...string) []byte {
	t.Helper()
	emu := vt.NewEmulator(cols, rows)
	defer func() { _ = emu.Close() }()
	var b []byte
	for i, line := range lines {
		b = append(b, []byte("\x1b["+itoa(i+1)+";1H")...) // CUP row i+1, col 1 (no scroll)
		b = append(b, []byte(line)...)
	}
	emu.Feed(b)
	snap, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("build peek snapshot: %v", err)
	}
	return snap
}

// joined flattens a snapshot's lines for a substring assertion.
func joined(lines []string) string { return strings.Join(lines, "\n") }

// TestOwnerPeek_NeedsNoRemotePreconditions: on the MAIN socket, a connection that
// negotiated NO capabilities gets its snapshot even with the remote-control kill switch
// OFF. The switch is the remote tier's master override (`swarm remote off`); it must never
// blank the owner's own view of the owner's own machine.
func TestOwnerPeek_NeedsNoRemotePreconditions(t *testing.T) {
	stub := newTerminalTapStub()
	stub.ks.Store(false) // remote control DISABLED
	stub.nextTapSnap = peekTextSnapshot(t, 40, 4, "OWNER PEEK", "second line")
	sock := serveOwnerAPI(t, stub)

	rc := rawDial(t, sock)
	rep := rc.hello(Version, nil) // NO capabilities offered, so none negotiated
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})
	if ack := nextControl(t, rc); ack.Op != OpOK {
		t.Fatalf("owner-tier terminal_subscribe = op %q code %q error %q; want OpOK — an owner-tier "+
			"connection needs neither the remote-gateway capability nor the kill switch (ADR-010 A3)",
			ack.Op, ack.ErrorCode, ack.Error)
	}
	if n := stub.tapCount(); n != 1 {
		t.Fatalf("owner-tier peek opened %d taps; want exactly 1", n)
	}

	snap := readTerminalSnapshot(t, rc)
	if snap.Terminal == nil {
		t.Fatalf("terminal_snapshot carried no Terminal payload")
	}
	if snap.Terminal.Session != "sess1" {
		t.Errorf("Terminal.Session = %q; want the LOCAL id %q", snap.Terminal.Session, "sess1")
	}
	if !strings.Contains(joined(snap.Terminal.Lines), "OWNER PEEK") {
		t.Errorf("rendered lines %q do not contain the session's screen text", snap.Terminal.Lines)
	}
	assertNoControlBytes(t, snap.Terminal.Lines)
}

// TestOwnerPeek_RemoteTierKeepsItsFullGate is the tier-scoping regression guard (the
// remote_input_refused_test.go precedent): relaxing the OWNER tier must leave the remote
// tier's preconditions exactly as they were. Neither refusal may open a tap.
func TestOwnerPeek_RemoteTierKeepsItsFullGate(t *testing.T) {
	t.Run("kill switch off", func(t *testing.T) {
		stub := newTerminalTapStub()
		stub.ks.Store(false)
		rc := rawDial(t, serveRemoteAPI(t, stub))
		rep := rc.hello(Version, []string{CapRemoteGateway})

		rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1"})
		got := nextControl(t, rc)
		if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
			t.Fatalf("remote peek with the switch OFF = op %q code %q; want error/kill_switch (fail-closed)", got.Op, got.ErrorCode)
		}
		if n := stub.tapCount(); n != 0 {
			t.Fatalf("a refused remote peek opened %d taps; want 0", n)
		}
	})

	t.Run("remote-gateway capability not negotiated", func(t *testing.T) {
		stub := newTerminalTapStub() // switch ON
		rc := rawDial(t, serveRemoteAPI(t, stub))
		rep := rc.hello(Version, nil)

		rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1"})
		if got := nextControl(t, rc); got.Op != OpError {
			t.Fatalf("remote peek without the negotiated capability = op %q; want an error refusal", got.Op)
		}
		if n := stub.tapCount(); n != 0 {
			t.Fatalf("a refused remote peek opened %d taps; want 0", n)
		}
	})
}

// TestOwnerPeek_BackendWithoutTapperRefused: the relaxation is authorization only. A
// backend that implements no TerminalTapper still cannot serve a peek, and must say so
// rather than leave the caller waiting for a frame that never comes.
func TestOwnerPeek_BackendWithoutTapperRefused(t *testing.T) {
	stub := newStubDaemon() // DaemonAPI, but no TerminalTapper
	rc := rawDial(t, serveOwnerAPI(t, stub))
	rep := rc.hello(Version, nil)

	rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1"})
	if got := nextControl(t, rc); got.Op != OpError {
		t.Fatalf("peek against a non-tapping backend = op %q; want an error refusal", got.Op)
	}
}

// TestClient_TerminalSnapshot pins the one-shot client method: it returns the session's
// CURRENT screen without waiting for new output, and the snapshot frames the daemon keeps
// pushing afterwards must not be mistaken for the reply to a LATER request on the same
// connection (the peek is a server PUSH stream, like OpEvent — not a request response).
func TestClient_TerminalSnapshot(t *testing.T) {
	stub := newTerminalTapStub()
	stub.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	stub.nextTapSnap = peekTextSnapshot(t, 40, 4, "READY >")
	sock := serveOwnerAPI(t, stub)

	c := dialClient(t, sock, nil)
	sid := NamespacedID(c.EndpointID(), "sess1")

	snap, err := c.TerminalSnapshot(sid)
	if err != nil {
		t.Fatalf("Client.TerminalSnapshot: %v", err)
	}
	if snap == nil {
		t.Fatalf("Client.TerminalSnapshot returned no snapshot and no error")
	}
	if !strings.Contains(joined(snap.Lines), "READY >") {
		t.Errorf("snapshot lines %q do not contain the session's screen text", snap.Lines)
	}
	assertNoControlBytes(t, snap.Lines)

	// The peek keeps rendering. A later request on the same client must still get ITS
	// reply, not a stray snapshot frame.
	if tap := stub.lastTap(); tap != nil {
		for i := 0; i < 3; i++ {
			tap.frames <- []byte("more output\r\n")
		}
	}
	sleepMS(100) // let the pushes land ahead of the request below

	sessions, err := c.List()
	if err != nil {
		t.Fatalf("List after a peek: %v (peek pushes must not be routed to the request channel)", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("List after a peek returned %d sessions, want 1 — a snapshot push was answered as the reply", len(sessions))
	}
}

// perSessionTapStub serves a DIFFERENT screen per session, so a snapshot can be traced back
// to the session it was rendered for. It embeds terminalTapStub for everything else (the
// DaemonAPI surface, the kill switch, the tap recording) and only decides which grid a new
// tap starts from; snaps is written once at construction and read-only afterwards.
type perSessionTapStub struct {
	*terminalTapStub
	snaps map[string][]byte // local session id -> the screen its tap serves
}

func (s *perSessionTapStub) TerminalTap(local string) (SessionStream, error) {
	st := newStubStream()
	if snap, ok := s.snaps[local]; ok {
		st.snap = snap
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.taps = append(s.taps, st)
	return st, nil
}

var _ TerminalTapper = (*perSessionTapStub)(nil)

// TestClient_TerminalSnapshot_SecondPeekIsNotTheFirstScreen is the stale-snapshot
// regression guard. A peek is a server PUSH stream that keeps rendering until the daemon
// cancels it, so by the time a SECOND peek is issued on the same client a snapshot of the
// FIRST session can already be sitting in the client's peek plumbing — and a peek that
// returns whatever it finds there answers "what is session B doing?" with session A's
// screen, silently, with no error. That is worse than a timeout: a steering agent reads the
// wrong screen and sends input based on it.
//
// Two things must hold, and the test needs both to pass: the channel a call waits on cannot
// be one a previous call could still be filling, AND a snapshot is only accepted when its
// Terminal.Session is the session that was asked for (the daemon emits the LOCAL id), so an
// in-flight frame from the previous peek is discarded rather than returned.
func TestClient_TerminalSnapshot_SecondPeekIsNotTheFirstScreen(t *testing.T) {
	stub := &perSessionTapStub{
		terminalTapStub: newTerminalTapStub(),
		snaps: map[string][]byte{
			"sessA": peekTextSnapshot(t, 40, 4, "SCREEN A"),
			"sessB": peekTextSnapshot(t, 40, 4, "SCREEN B"),
		},
	}
	stub.setMetas(
		statusMeta("sessA", status.TurnIdle, status.InteractionNone),
		statusMeta("sessB", status.TurnIdle, status.InteractionNone),
	)
	c := dialClient(t, serveOwnerAPI(t, stub), nil)

	snapA, err := c.TerminalSnapshot(NamespacedID(c.EndpointID(), "sessA"))
	if err != nil {
		t.Fatalf("peek sessA: %v", err)
	}
	if !strings.Contains(joined(snapA.Lines), "SCREEN A") {
		t.Fatalf("peek sessA returned %q, want its own screen", snapA.Lines)
	}

	// A keeps printing: its render loop pushes again, so a sessA snapshot is in flight (or
	// already buffered) when the next peek is issued.
	if tap := stub.lastTap(); tap != nil {
		tap.frames <- []byte("A KEEPS PRINTING\r\n")
	}
	sleepMS(100)

	snapB, err := c.TerminalSnapshot(NamespacedID(c.EndpointID(), "sessB"))
	if err != nil {
		t.Fatalf("peek sessB: %v", err)
	}
	if snapB.Session != "sessB" {
		t.Errorf("the second peek returned a snapshot of session %q; want sessB — a peek must never "+
			"answer with another session's screen", snapB.Session)
	}
	if !strings.Contains(joined(snapB.Lines), "SCREEN B") || strings.Contains(joined(snapB.Lines), "SCREEN A") {
		t.Errorf("the second peek returned %q; want sessB's screen, not the screen left over from the "+
			"peek of sessA", snapB.Lines)
	}
}

// TestOwnerPeek_SnapshotSanitized keeps the security property visible on the local path
// too: hostile output rendered for an owner-tier caller is still escape-free plain text.
func TestOwnerPeek_SnapshotSanitized(t *testing.T) {
	stub := newTerminalTapStub()
	sock := serveOwnerAPI(t, stub)

	rc := rawDial(t, sock)
	rep := rc.hello(Version, nil)
	rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1"})
	if ack := nextControl(t, rc); ack.Op != OpOK {
		t.Fatalf("owner-tier terminal_subscribe = op %q code %q; want OpOK", ack.Op, ack.ErrorCode)
	}
	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("owner-tier peek opened no tap")
	}
	tap.frames <- []byte("\x1b[31mDANGER\x1b[0m\x07")

	snap := readTerminalSnapshot(t, rc)
	if snap.Terminal == nil {
		t.Fatalf("terminal_snapshot carried no Terminal payload")
	}
	if !strings.Contains(joined(snap.Terminal.Lines), "DANGER") {
		t.Errorf("visible text lost in the render: %q", snap.Terminal.Lines)
	}
	assertNoControlBytes(t, snap.Terminal.Lines)
	if bytes.Contains([]byte(joined(snap.Terminal.Lines)), []byte("\x1b")) {
		t.Errorf("an escape survived into an owner-tier snapshot: %q", snap.Terminal.Lines)
	}
}
