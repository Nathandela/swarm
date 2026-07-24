package protocol

// FAILING-FIRST protocol tests for A7 renderer slice F2 (terminal_subscribe peek handler).
// A remote peek is a READ-ONLY window onto a session: the daemon renders the session's VT
// grid server-side (slice E) and streams sanitized OpTerminalSnapshot frames to the phone.
// It is SECURITY-CRITICAL. The contract pinned here:
//
//   - a terminal_subscribe on a session with NO local controller still streams the rendered,
//     sanitized grid (the peek is independent of the interactive lease);
//   - a concurrent peek NEVER supersedes the local controller: the owner keeps its lease
//     generation and keeps receiving raw output (the peek uses a separate read-only tap, not
//     the lease pump);
//   - the peek path NEVER injects input — no keystroke or resize sent over the peek
//     connection ever reaches the session (handler never forwards input; the tap is read-only);
//   - with the kill switch OFF the peek is REFUSED fail-closed (CodeKillSwitch) before any
//     tap is opened or a single frame streamed — `swarm remote off` blanks the phone.
//
// FROZEN API these tests expect (the implementer wires it):
//
//	// The Server serves terminal_subscribe when its DaemonAPI ALSO implements TerminalTapper
//	// (optional-interface type assertion, the JournalBackend seam) AND the remote-gateway
//	// capability was negotiated. The tap is read-only.
//	type TerminalTapper interface { TerminalTap(local string) (SessionStream, error) }
//	// Dispatch: case OpTerminalSubscribe -> handleTerminalSubscribe(c), which replies OpOK
//	// then streams Control{Op: OpTerminalSnapshot, Terminal: &TerminalSnapshot{...}} frames.

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
)

// terminalTapStub is a remote backend that is a full DaemonAPI + DeviceAuthenticator +
// OperationClaimer (via the embedded *stubDaemon) AND a TerminalTapper serving a read-only
// per-session tap, AND a toggleable KillSwitch. terminal_subscribe (A7 F2) renders one of
// these taps server-side; the kill switch blanks it fail-closed. Because it satisfies every
// optional interface off ONE backend, a remote-tier Server type-asserts them exactly as the
// production coreAPI does. TerminalTap streams are recorded SEPARATELY from Attach streams
// (stubDaemon.streams), so a test can prove the peek path and the lease pump never cross.
type terminalTapStub struct {
	*stubDaemon
	ks          atomic.Bool // kill switch (toggleable under -race): ON when true, OFF (remote disabled) when false
	tapErr      error       // injected TerminalTap failure
	nextTapSnap []byte      // when set, the initial snapshot the NEXT tap serves (else the default)

	mu   sync.Mutex
	taps []*stubStream // one per TerminalTap call (newest last)
}

func newTerminalTapStub() *terminalTapStub {
	s := &terminalTapStub{stubDaemon: newStubDaemon()}
	s.ks.Store(true) // kill switch ON by default
	return s
}

// RemoteControlEnabled makes terminalTapStub the pinned KillSwitch (overriding the embedded
// stub's always-on): the switch is ON when ks is true, OFF (remote control disabled) otherwise.
// It is an atomic so a test can flip it mid-peek from another goroutine under -race.
func (t *terminalTapStub) RemoteControlEnabled() bool { return t.ks.Load() }

// TerminalTap opens a read-only tap stream for local and records it, so a test can feed
// output frames and assert ZERO input was ever forwarded to it (the peek is read-only). When
// nextTapSnap is set (armed BEFORE the server starts, so the write is visible to the tap),
// the tap serves it as the initial grid — letting a test render an arbitrarily large screen.
func (t *terminalTapStub) TerminalTap(local string) (SessionStream, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tapErr != nil {
		return nil, t.tapErr
	}
	st := newStubStream()
	if t.nextTapSnap != nil {
		st.snap = t.nextTapSnap
	}
	t.taps = append(t.taps, st)
	return st, nil
}

func (t *terminalTapStub) tapCount() int { t.mu.Lock(); defer t.mu.Unlock(); return len(t.taps) }

func (t *terminalTapStub) lastTap() *stubStream {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.taps) == 0 {
		return nil
	}
	return t.taps[len(t.taps)-1]
}

// Compile-time proof the stub satisfies every surface a remote-tier peek server asserts.
var (
	_ DaemonAPI      = (*terminalTapStub)(nil)
	_ TerminalTapper = (*terminalTapStub)(nil)
	_ KillSwitch     = (*terminalTapStub)(nil)
)

// readTerminalSnapshot reads frames off rc until a terminal_snapshot control frame arrives,
// skipping any interleaved data/snapshot frames, and returns it.
func readTerminalSnapshot(t *testing.T, rc *rawConn) Control {
	t.Helper()
	for i := 0; i < 64; i++ {
		typ, payload, err := rc.readFrame()
		if err != nil {
			t.Fatalf("read terminal_snapshot frame: %v", err)
		}
		if typ != wire.TControl {
			continue
		}
		c, err := DecodeControl(payload)
		if err != nil {
			t.Fatalf("DecodeControl: %v", err)
		}
		if c.Op == OpTerminalSnapshot {
			return c
		}
	}
	t.Fatalf("no terminal_snapshot within frame budget")
	return Control{}
}

// assertNoControlBytes is the peek-side sanitization guard: no rendered line may carry a
// terminal control character (checked at the rune level, so multi-byte UTF-8 is not a false
// positive). It proves the daemon render pipeline (emulator + SnapText) ran end to end
// through the handler, never leaking a raw escape to the phone.
func assertNoControlBytes(t *testing.T, lines []string) {
	t.Helper()
	for i, line := range lines {
		for _, ru := range line {
			if ru < 0x20 || ru == 0x7f || (ru >= 0x80 && ru <= 0x9f) {
				t.Errorf("line %d: control char %#x leaked into a rendered peek line", i, ru)
			}
		}
	}
}

// TestRemotePeek_WorksWithNoLocalController: a remote-tier terminal_subscribe for a session
// with NO controller replies OpOK then streams OpTerminalSnapshot frames whose Terminal.Lines
// are the rendered, sanitized grid. A colored, escape-laden output frame is fed; the visible
// text survives and every control byte is stripped.
func TestRemotePeek_WorksWithNoLocalController(t *testing.T) {
	stub := newTerminalTapStub() // kill switch ON
	sock := serveRemoteAPI(t, stub)

	peek := rawDial(t, sock)
	rep := peek.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	peek.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})
	if ack := nextControl(t, peek); ack.Op != OpOK {
		t.Fatalf("terminal_subscribe reply = op %q code %q; want OpOK", ack.Op, ack.ErrorCode)
	}

	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("terminal_subscribe opened no tap; want one read-only tap for the peeked session")
	}
	// Drive a colored, escape-laden output frame; the daemon renders it server-side and
	// streams a sanitized snapshot whose visible text survives and whose controls are gone.
	tap.frames <- []byte("\x1b[31mPEEK\x1b[0m")

	snap := readTerminalSnapshot(t, peek)
	if snap.Terminal == nil {
		t.Fatalf("terminal_snapshot carried no Terminal payload")
	}
	if snap.Terminal.Session != "sess1" {
		t.Errorf("Terminal.Session = %q; want the LOCAL id %q (the gateway namespaces at egress)", snap.Terminal.Session, "sess1")
	}
	joined := strings.Join(snap.Terminal.Lines, "")
	if !strings.Contains(joined, "PEEK") {
		t.Fatalf("rendered grid missing visible text %q; grid=%q", "PEEK", joined)
	}
	assertNoControlBytes(t, snap.Terminal.Lines)
}

// TestRemotePeek_DoesNotSupersedeLocalController: a LOCAL controller holds the session at
// generation G (owner-tier attach, open pump, receiving TDataOut). A concurrent REMOTE peek
// receives rendered snapshots AND the local controller KEEPS its lease — same generation, no
// OpDetach, still receiving raw output. The peek uses a separate read-only tap, never the
// lease pump, so it can never supersede the owner.
func TestRemotePeek_DoesNotSupersedeLocalController(t *testing.T) {
	stub := newTerminalTapStub() // kill switch ON
	ownerSock := serveOwner(t, stub)
	remoteSock := serveRemoteAPI(t, stub)

	// LOCAL controller: owner-tier attach establishes a lease + pump (attach stream index 0).
	local := rawDial(t, ownerSock)
	lrep := local.hello(Version, []string{CapAttach})
	lsid := lrep.EndpointID + "/sess1"
	local.writeControl(Control{Op: OpAttach, EndpointID: lrep.EndpointID, SessionID: lsid})
	lLease := nextControl(t, local)
	if lLease.Op != OpLease || lLease.Generation == 0 {
		t.Fatalf("owner-tier attach = op %q gen %d; want OpLease with a nonzero generation", lLease.Op, lLease.Generation)
	}
	genG := lLease.Generation

	// REMOTE peek: terminal_subscribe on the remote tier renders a read-only tap.
	peek := rawDial(t, remoteSock)
	prep := peek.hello(Version, []string{CapRemoteGateway})
	psid := prep.EndpointID + "/sess1"
	peek.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: prep.EndpointID, SessionID: psid})
	if ack := nextControl(t, peek); ack.Op != OpOK {
		t.Fatalf("terminal_subscribe reply = op %q code %q; want OpOK", ack.Op, ack.ErrorCode)
	}
	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("peek opened no tap")
	}
	tap.frames <- []byte("PEEK")
	snap := readTerminalSnapshot(t, peek)
	if snap.Terminal == nil || !strings.Contains(strings.Join(snap.Terminal.Lines, ""), "PEEK") {
		t.Fatalf("remote peek did not receive the rendered snapshot")
	}

	// The peek did NOT supersede the local controller: its lease stays at generation G and it
	// keeps receiving raw output. The attach stream is index 0 (the peek uses a SEPARATE tap
	// stream, recorded in stub.taps, never stub.streams), so feeding it delivers TDataOut to
	// the owner. A supersede would instead close the owner's Frames() / send OpDetach.
	attachStream := stub.streamAt(0)
	if attachStream == nil {
		t.Fatalf("owner attach opened no upstream stream")
	}
	attachStream.frames <- []byte("LOCAL-OUTPUT")

	sawOut := false
	for i := 0; i < 16 && !sawOut; i++ {
		typ, payload, err := local.readFrame()
		if err != nil {
			t.Fatalf("local: read frame: %v", err)
		}
		switch typ {
		case wire.TDataOut:
			if bytes.Equal(payload, []byte("LOCAL-OUTPUT")) {
				sawOut = true
			}
		case wire.TControl:
			c, derr := DecodeControl(payload)
			if derr == nil && c.Op == OpDetach {
				t.Fatalf("local controller was superseded (OpDetach) by a READ-ONLY remote peek; the peek must never touch the lease (gen %d)", genG)
			}
		}
	}
	if !sawOut {
		t.Fatalf("local controller stopped receiving output after a remote peek; the peek must not disturb the lease at generation %d", genG)
	}
}

// TestRemotePeek_ReadOnly_NoInputInjection: drive the peek path, then attempt to inject a
// keystroke AND a resize over the peek connection. A read-only peek must NEVER forward input
// to the session — the handler never calls Input/Resize on the tap, so the tap stream records
// zero of each.
func TestRemotePeek_ReadOnly_NoInputInjection(t *testing.T) {
	stub := newTerminalTapStub()
	sock := serveRemoteAPI(t, stub)

	peek := rawDial(t, sock)
	rep := peek.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"
	peek.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})
	if ack := nextControl(t, peek); ack.Op != OpOK {
		t.Fatalf("terminal_subscribe reply = op %q; want OpOK", ack.Op)
	}
	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("peek opened no tap")
	}

	// Confirm the peek is live (a render round-trips) ...
	tap.frames <- []byte("PEEK")
	_ = readTerminalSnapshot(t, peek)

	// ... then attempt to inject keystrokes and a resize over the peek connection.
	peek.writeFrame(wire.TDataIn, []byte("rm -rf /\n"))
	peek.writeControl(Control{Op: OpResize, EndpointID: rep.EndpointID, SessionID: sid, Cols: 200, Rows: 50, Generation: 1})

	// Synchronize: a trailing op whose reply we observe proves the injection frames were
	// already fully processed by the in-order per-connection loop (no sleep, deterministic).
	peek.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	_ = syncControlOp(t, peek, OpList)

	if in := tap.inputBytes(); len(in) != 0 {
		t.Fatalf("read-only peek forwarded %q to the session; a terminal peek must NEVER inject input (A7/F2)", in)
	}
	if n := tap.resizeCount(); n != 0 {
		t.Fatalf("read-only peek forwarded %d resizes; a terminal peek must never drive the session", n)
	}
}

// TestRemotePeek_RefusedWhenKillSwitchOff: with RemoteControlEnabled()==false, terminal_subscribe
// is REFUSED (error/CodeKillSwitch) as the FIRST gate — before any tap is opened or a single
// frame streamed. Terminal content is more sensitive than journal metadata, so `swarm remote off`
// must blank the phone: fail-closed. The stub IS a TerminalTapper, so a tap WOULD open if the
// gate ran after the backend check — proving the kill switch precedes it.
func TestRemotePeek_RefusedWhenKillSwitchOff(t *testing.T) {
	stub := newTerminalTapStub()
	stub.ks.Store(false) // remote control disabled
	sock := serveRemoteAPI(t, stub)

	peek := rawDial(t, sock)
	rep := peek.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"
	peek.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})

	got := nextControl(t, peek)
	if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Fatalf("kill switch OFF: terminal_subscribe = op %q code %q; want error/kill_switch (fail-closed)", got.Op, got.ErrorCode)
	}
	if n := stub.tapCount(); n != 0 {
		t.Fatalf("kill switch OFF opened %d taps; want 0 (no tap, nothing streamed)", n)
	}
}

// TestRemotePeek_TerminatesOnKillSwitchFlip pins defect C: the kill switch is checked only
// at subscribe (the first gate), so an ESTABLISHED peek survives a mid-stream `swarm remote
// off` (or revoking the last device). The push closure must RE-CHECK RemoteControlEnabled()
// before each emission — a disabled switch must blank the phone: stop emitting, terminate the
// render loop, and release the read-only tap. Contrast controlGateOpen clause 1, which
// re-checks the switch on every keystroke.
func TestRemotePeek_TerminatesOnKillSwitchFlip(t *testing.T) {
	stub := newTerminalTapStub() // kill switch ON
	sock := serveRemoteAPI(t, stub)

	peek := rawDial(t, sock)
	rep := peek.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"
	peek.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})
	if ack := nextControl(t, peek); ack.Op != OpOK {
		t.Fatalf("terminal_subscribe reply = op %q code %q; want OpOK", ack.Op, ack.ErrorCode)
	}
	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("peek opened no tap")
	}

	// Establish the peek: one frame renders and a snapshot is received.
	tap.frames <- []byte("PEEK")
	_ = readTerminalSnapshot(t, peek)

	// Flip the kill switch OFF mid-peek (`swarm remote off`), THEN drive one more render.
	// The store happens-before the send, so the render goroutine observes the OFF state.
	stub.ks.Store(false)
	tap.frames <- []byte("MORE")

	// No snapshot leaks after the flip: the re-check must blank MORE before it is written.
	_ = peek.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if typ, payload, err := wire.ReadFrame(peek.conn); err == nil && typ == wire.TControl {
		if c, derr := DecodeControl(payload); derr == nil && c.Op == OpTerminalSnapshot {
			t.Fatalf("terminal_snapshot emitted AFTER the kill switch was flipped OFF; an established peek must blank the phone (C)")
		}
	}

	// The peek terminates: its read-only tap is released (the loop returned).
	if !tap.waitClosed(recvTimeout) {
		t.Fatalf("kill switch flipped OFF mid-peek but the tap was never released; the peek must terminate (C)")
	}
}

// TestRemotePeek_IdleKillSwitchFlipTerminatesAndSignals pins Blocker 1 (teardown + recovery):
// the kill-switch re-check used to live ONLY in the push closure, which runs on an EMISSION —
// so an IDLE peek (no output) never re-checked and lingered after `swarm remote off`, and the
// daemon never told the gateway the peek was over (its RunTerminal polled the silent conn
// forever, so OFF->ON never recovered). The fixed handler must, for an idle peek whose kill
// switch flips OFF: (1) terminate the render loop within the poll interval (release the tap),
// and (2) write an OpError control frame to the peek conn so Gateway.RunTerminal returns and
// reconnects. NO output is ever fed here, so only a per-tick liveness gate can drive it.
func TestRemotePeek_IdleKillSwitchFlipTerminatesAndSignals(t *testing.T) {
	stub := newTerminalTapStub() // kill switch ON
	sock := serveRemoteAPI(t, stub)

	peek := rawDial(t, sock)
	rep := peek.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"
	peek.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})
	if ack := nextControl(t, peek); ack.Op != OpOK {
		t.Fatalf("terminal_subscribe reply = op %q code %q; want OpOK", ack.Op, ack.ErrorCode)
	}
	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("peek opened no tap")
	}

	// The peek is IDLE: no frame is ever fed. Flip the kill switch OFF (`swarm remote off`).
	stub.ks.Store(false)

	// (2) The daemon signals the gateway the peek ended: the NEXT control frame is an OpError,
	// so Gateway.RunTerminal returns and reconnects instead of polling the silent conn forever.
	got := nextControl(t, peek)
	if got.Op != OpError {
		t.Fatalf("idle peek after kill switch OFF: next frame op = %q; want OpError (the gateway-stop signal). "+
			"Without it RunTerminal polls the silent conn forever and OFF->ON never recovers (Blocker 1b)", got.Op)
	}

	// (1) The render loop terminated and released the read-only tap (Blocker 1a): an idle peek
	// must not linger past `swarm remote off`.
	if !tap.waitClosed(recvTimeout) {
		t.Fatalf("idle peek after kill switch OFF: tap never released; the render loop must terminate " +
			"even with no output pending (Blocker 1a)")
	}
}

// TestRemotePeek_TerminatesOnWriteError pins defect #7: the push closure ignores
// writeFrameDeadline errors, so a readable-but-unwritable connection stalls pumpWriteTimeout
// per frame FOREVER, retaining the renderer and its tap. On the first write error the loop
// must terminate (cancel the render ctx) and release the tap within a bound. The peek
// connection goes wedged (never reads again) while output is fed continuously, so the render
// writes pile into a socket the client never drains until a write hits the deadline and fails.
func TestRemotePeek_TerminatesOnWriteError(t *testing.T) {
	old := pumpWriteTimeoutNS.Load()
	pumpWriteTimeoutNS.Store(int64(200 * time.Millisecond))
	defer pumpWriteTimeoutNS.Store(old)

	stub := newTerminalTapStub()
	// A moderately large initial grid so each render is a sizable frame that fills the
	// wedged socket's buffers in a handful of writes (armed before the server starts).
	stub.nextTapSnap = peekGridSnapshot(t, 200, 50, "x")
	sock := serveRemoteAPI(t, stub)

	peek := rawDial(t, sock)
	rep := peek.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"
	peek.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})
	if ack := nextControl(t, peek); ack.Op != OpOK {
		t.Fatalf("terminal_subscribe reply = op %q code %q; want OpOK", ack.Op, ack.ErrorCode)
	}
	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("peek opened no tap")
	}

	// The peek connection is now WEDGED: we never read another frame. Feed distinct output
	// continuously so the render loop keeps pushing snapshots to a socket the client never
	// drains — once the buffers fill, a write hits the 200ms deadline and FAILS.
	stopFeed := make(chan struct{})
	go func() {
		i := 0
		for {
			select {
			case <-stopFeed:
				return
			case tap.frames <- []byte("line-" + itoa(i) + "\r\n"):
				i++
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	defer close(stopFeed)

	// The render loop terminates on the first write error and releases the tap within a
	// bound; the buggy loop retries every 200ms forever and never releases it.
	if !tap.waitClosed(10 * time.Second) {
		t.Fatalf("peek write failed but the render loop never terminated (tap not released); a write error must stop the renderer, not retry forever (#7)")
	}
}

// TestRemotePeek_LargeGridClippedUnderMaxFrame pins defect J: a peek snapshot is a SINGLE
// unchunked TControl frame, so a large grid (up to maxDim=1000 square, forwardResize-reachable)
// encodes past wire.MaxFrame and is SILENTLY DROPPED by WriteFrame. The render must be CLIPPED
// to the phone-viewport bound (maxPeekCols x maxPeekRows) before encoding, so the frame always
// fits and is delivered. The grid below is filled with '<' (JSON-escaped to <, 6 bytes
// each), so its unclipped snapshot encodes well past 1 MiB.
func TestRemotePeek_LargeGridClippedUnderMaxFrame(t *testing.T) {
	stub := newTerminalTapStub()
	// 600x500 of '<' -> ~300k cells * 6 JSON bytes ~= 1.8 MiB unclipped, far over wire.MaxFrame.
	stub.nextTapSnap = peekGridSnapshot(t, 600, 500, "<")
	sock := serveRemoteAPI(t, stub)

	peek := rawDial(t, sock)
	rep := peek.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"
	peek.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: sid})
	if ack := nextControl(t, peek); ack.Op != OpOK {
		t.Fatalf("terminal_subscribe reply = op %q code %q; want OpOK", ack.Op, ack.ErrorCode)
	}

	// The rendered initial snapshot must arrive at all — an unclipped >MaxFrame frame is
	// dropped by WriteFrame and never delivered, so this read would time out.
	snap := readTerminalSnapshot(t, peek)
	if snap.Terminal == nil {
		t.Fatalf("terminal_snapshot carried no Terminal payload")
	}

	// Clipped to the phone-viewport bound: bounded line count and bounded per-line width.
	if len(snap.Terminal.Lines) > maxPeekRows {
		t.Fatalf("peek snapshot has %d lines; want clipped to <= %d (maxPeekRows) so the frame fits (J)", len(snap.Terminal.Lines), maxPeekRows)
	}
	for i, line := range snap.Terminal.Lines {
		if n := len([]rune(line)); n > maxPeekCols {
			t.Fatalf("peek line %d has %d runes; want clipped to <= %d (maxPeekCols) (J)", i, n, maxPeekCols)
		}
	}

	// And the encoded control frame is safely under wire.MaxFrame (never silently dropped).
	body, err := EncodeControl(Control{Op: OpTerminalSnapshot, EndpointID: rep.EndpointID, Terminal: snap.Terminal})
	if err != nil {
		t.Fatalf("re-encode peek snapshot: %v", err)
	}
	if len(body) >= wire.MaxFrame {
		t.Fatalf("clipped peek frame is %d bytes; want < wire.MaxFrame (%d) so WriteFrame never drops it (J)", len(body), wire.MaxFrame)
	}
	assertNoControlBytes(t, snap.Terminal.Lines)
}

// peekGridSnapshot builds a valid vt snapshot of a fully-populated cols x rows grid (each row
// filled with `fill`), so a test can arm a tap with an arbitrarily large initial screen. It is
// the protocol-side analogue of the daemon test's snapBytes.
func peekGridSnapshot(t *testing.T, cols, rows int, fill string) []byte {
	t.Helper()
	emu := vt.NewEmulator(cols, rows)
	defer emu.Close()
	var b []byte
	for y := 0; y < rows; y++ {
		b = append(b, []byte("\x1b["+itoa(y+1)+";1H")...) // CUP row y+1, col 1 (no scroll)
		b = append(b, bytes.Repeat([]byte(fill), cols)...)
	}
	emu.Feed(b)
	snap, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("build peek grid snapshot: %v", err)
	}
	return snap
}
