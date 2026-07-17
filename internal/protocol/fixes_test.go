package protocol

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

// Epic 6 review-fix round (audit-006): white-box tests for the protocol Server /
// Client fixes F1-F13. They use a purpose-built DaemonAPI (fixDaemon) with one
// running session and a single controllable stream, so each fix is exercised
// deterministically without the frozen stub harness.

// ---------------------------------------------------------------------------
// fixDaemon / fixStream — a controllable DaemonAPI for the fix-round tests.
// ---------------------------------------------------------------------------

// fixDaemon is a controllable DaemonAPI whose Attach opens a FRESH stream on every
// call (matching the re-attach supersede model): each attach/supersede yields a new
// upstream stream, and the i-th attach's snapshot is snaps[i] (the last repeats).
// custom[i], if set, overrides the i-th stream (e.g. a blocking one).
type fixDaemon struct {
	mu     sync.Mutex
	snaps  [][]byte
	custom []SessionStream
	opened []SessionStream
}

func newFixDaemon(snaps ...[]byte) *fixDaemon { return &fixDaemon{snaps: snaps} }

func (d *fixDaemon) List() []persist.Meta {
	return []persist.Meta{{
		ID:        "sess1",
		AgentType: "claude",
		Cwd:       "/tmp",
		Status:    status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone},
	}}
}
func (d *fixDaemon) Launch(daemon.LaunchSpec) (persist.Meta, error) {
	return persist.Meta{}, errors.New("fixDaemon: launch unused")
}
func (d *fixDaemon) Kill(string) error   { return nil }
func (d *fixDaemon) Delete(string) error { return nil }
func (d *fixDaemon) Attach(id string) (SessionStream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := len(d.opened)
	var st SessionStream
	if i < len(d.custom) && d.custom[i] != nil {
		st = d.custom[i]
	} else {
		var snap []byte
		if i < len(d.snaps) {
			snap = d.snaps[i]
		} else if len(d.snaps) > 0 {
			snap = d.snaps[len(d.snaps)-1]
		}
		st = newFixStream(snap)
	}
	d.opened = append(d.opened, st)
	return st, nil
}
func (d *fixDaemon) Events() <-chan persist.Meta { return make(chan persist.Meta) }
func (d *fixDaemon) attachCount() int            { d.mu.Lock(); defer d.mu.Unlock(); return len(d.opened) }
func (d *fixDaemon) openedFixAt(i int) *fixStream {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i < 0 || i >= len(d.opened) {
		return nil
	}
	fs, _ := d.opened[i].(*fixStream)
	return fs
}
func (d *fixDaemon) lastOpenedFix() *fixStream {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.opened) == 0 {
		return nil
	}
	fs, _ := d.opened[len(d.opened)-1].(*fixStream)
	return fs
}

// fixStream is a controllable SessionStream that records its close.
type fixStream struct {
	snap   []byte
	frames chan []byte

	closeOnce sync.Once
	closed    chan struct{}
}

func newFixStream(snap []byte) *fixStream {
	return &fixStream{snap: snap, frames: make(chan []byte, 16), closed: make(chan struct{})}
}

func (s *fixStream) Snapshot() []byte      { return s.snap }
func (s *fixStream) Frames() <-chan []byte { return s.frames }
func (s *fixStream) Input([]byte) error    { return nil }
func (s *fixStream) Resize(int, int) error { return nil }
func (s *fixStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// serveFix stands up a Server over a DaemonAPI and returns the socket + handle.
func serveFix(t *testing.T, d DaemonAPI) (string, *Server) {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(d, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock, srv
}

// ---------------------------------------------------------------------------
// F1 — supersede must re-snapshot (fresh grid, not the stale cached one).
// ---------------------------------------------------------------------------

func TestFix_SupersedeSendsFreshSnapshot(t *testing.T) {
	// snaps[0] is the first attach's grid; snaps[1] the current grid at supersede.
	d := newFixDaemon([]byte("STALE-GRID"), []byte("FRESH-GRID-AFTER-OUTPUT"))
	sock, _ := serveFix(t, d)

	ca := dialClient(t, sock, []string{"attach"})
	a, err := ca.Attach(onlyViewID(t, ca))
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if !bytes.Equal(a.Snapshot(), []byte("STALE-GRID")) {
		t.Fatalf("first attach snapshot = %q, want the first stream's snapshot", a.Snapshot())
	}

	cb := dialClient(t, sock, []string{"attach"})
	b, err := cb.Attach(onlyViewID(t, cb))
	if err != nil {
		t.Fatalf("supersede Attach: %v", err)
	}
	if !bytes.Equal(b.Snapshot(), []byte("FRESH-GRID-AFTER-OUTPUT")) {
		t.Fatalf("supersede snapshot = %q, want the fresh re-attach current grid (F1)", b.Snapshot())
	}
	// Supersede RE-ATTACHES: a fresh stream is opened and the old one closed.
	if d.attachCount() != 2 {
		t.Fatalf("DaemonAPI.Attach called %d times; supersede must open a FRESH stream (re-attach)", d.attachCount())
	}
	if st0 := d.openedFixAt(0); st0 == nil || !waitClosedFix(st0, recvTimeout) {
		t.Fatalf("supersede did not close the old upstream stream (F1 re-attach)")
	}
}

// ---------------------------------------------------------------------------
// F2 — a snapshot larger than wire.MaxFrame chunks + reassembles intact.
// ---------------------------------------------------------------------------

func TestFix_LargeSnapshotChunkedRoundTrips(t *testing.T) {
	big := make([]byte, 3*wire.MaxFrame+12345)
	for i := range big {
		big[i] = byte('A' + i%26)
	}
	d := newFixDaemon(big)
	sock, _ := serveFix(t, d)

	c := dialClient(t, sock, []string{"attach"})
	a, err := c.Attach(onlyViewID(t, c))
	if err != nil {
		t.Fatalf("Attach with a >MaxFrame snapshot: %v", err)
	}
	if !bytes.Equal(a.Snapshot(), big) {
		t.Fatalf("chunked snapshot mismatch: got %d bytes, want %d", len(a.Snapshot()), len(big))
	}
}

// ---------------------------------------------------------------------------
// F2 — a write failure mid-snapshot must not wedge a goroutine; Close returns.
// ---------------------------------------------------------------------------

func TestFix_SnapshotWriteFailureNoDeadlock(t *testing.T) {
	big := bytes.Repeat([]byte("Z"), 2*wire.MaxFrame)
	d := newFixDaemon(big)
	sock, srv := serveFix(t, d)

	// Attach at the wire level, then drop the connection so the server's snapshot
	// write fails partway through.
	r := rawDial(t, sock)
	hello := r.hello(Version, []string{"attach"})
	r.writeControl(Control{Op: OpList, EndpointID: hello.EndpointID})
	list := r.readControl()
	if len(list.Sessions) != 1 {
		t.Fatalf("list = %d sessions, want 1", len(list.Sessions))
	}
	r.writeControl(Control{Op: OpAttach, EndpointID: hello.EndpointID, SessionID: list.Sessions[0].ID})
	_ = r.conn.Close() // break the pipe mid-snapshot

	done := make(chan struct{})
	go func() { _ = srv.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Server.Close hung after a mid-snapshot write failure (wedged pump goroutine, F2)")
	}
}

// ---------------------------------------------------------------------------
// F3 — a wedged controller is superseded/detached within a bound.
// ---------------------------------------------------------------------------

func TestFix_WedgedControllerSupersededWithinBound(t *testing.T) {
	old := pumpWriteTimeoutNS.Load()
	pumpWriteTimeoutNS.Store(int64(200 * time.Millisecond))
	defer pumpWriteTimeoutNS.Store(old)

	d := newFixDaemon([]byte("SNAP"))
	sock, _ := serveFix(t, d)

	// Wedged controller A: attach raw, then never read again, while frames flood its
	// upstream so its pump has to write to a socket it never drains.
	a := rawDial(t, sock)
	ah := a.hello(Version, []string{"attach"})
	a.writeControl(Control{Op: OpList, EndpointID: ah.EndpointID})
	sid := a.readControl().Sessions[0].ID
	a.writeControl(Control{Op: OpAttach, EndpointID: ah.EndpointID, SessionID: sid})
	var astream *fixStream
	for i := 0; i < 200 && astream == nil; i++ {
		astream = d.openedFixAt(0)
		if astream == nil {
			sleepMS(5)
		}
	}
	if astream == nil {
		t.Fatalf("A's upstream stream never opened")
	}
	stop := make(chan struct{})
	go func() {
		blob := bytes.Repeat([]byte("x"), 32<<10)
		for {
			select {
			case <-stop:
				return
			case astream.frames <- blob:
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	defer close(stop)

	// B supersedes the wedged A: the supersede must complete within a bound (never
	// blocked on A's wedged pump), and B's fresh lease must work — proving A was
	// evicted from the lease and the daemon is unaffected (F3/S9 liveness).
	cb := dialClient(t, sock, []string{"attach"})
	bid := onlyViewID(t, cb)
	type attachRes struct {
		b   *Attachment
		err error
	}
	attached := make(chan attachRes, 1)
	go func() {
		b, err := cb.Attach(bid)
		attached <- attachRes{b, err}
	}()
	var b *Attachment
	select {
	case res := <-attached:
		if res.err != nil {
			t.Fatalf("superseding attach failed: %v", res.err)
		}
		b = res.b
	case <-time.After(5 * time.Second):
		t.Fatalf("supersede blocked on a wedged controller (F3/S9 liveness)")
	}
	bStream := d.lastOpenedFix()
	bStream.frames <- []byte("b-live")
	if got, ok := recvFrame(t, b.Frames(), recvTimeout); !ok || !bytes.Equal(got, []byte("b-live")) {
		t.Fatalf("superseding controller's lease not working after evicting the wedged one (got %q ok=%v)", got, ok)
	}
}

// ---------------------------------------------------------------------------
// F7 — a second attach on one client connection detaches the first.
// ---------------------------------------------------------------------------

func TestFix_SecondAttachSameConnDetachesFirst(t *testing.T) {
	st1 := newFixStream([]byte("SNAP1"))
	// Two sessions: the same fixDaemon returns a fresh stream per Attach id here.
	d := &multiFixDaemon{streams: map[string]*fixStream{
		"s1": st1,
		"s2": newFixStream([]byte("SNAP2")),
	}}
	sock, _ := serveFix(t, d)

	c := dialClient(t, sock, nil)
	ep := c.EndpointID()

	a1, err := c.Attach(NamespacedID(ep, "s1"))
	if err != nil {
		t.Fatalf("Attach s1: %v", err)
	}
	a2, err := c.Attach(NamespacedID(ep, "s2"))
	if err != nil {
		t.Fatalf("Attach s2 (should auto-detach s1): %v", err)
	}
	_ = a2

	// The first attachment's Frames() must close (its lease was released).
	select {
	case _, ok := <-a1.Frames():
		if ok {
			// a stray queued frame is tolerable; drain until closed
			select {
			case <-a1.Frames():
			case <-time.After(recvTimeout):
				t.Fatalf("first attachment Frames() did not close after a second attach (F7)")
			}
		}
	case <-time.After(recvTimeout):
		t.Fatalf("first attachment Frames() neither closed nor delivered after a second attach (F7)")
	}

	// s1's upstream stream was released (closed) by the auto-detach.
	if !waitClosedFix(st1, recvTimeout) {
		t.Fatalf("s1 upstream stream not closed after the first lease was auto-detached (F7)")
	}
}

// ---------------------------------------------------------------------------
// F11 — a delayed old-generation detach must not release the current lease.
// ---------------------------------------------------------------------------

func TestFix_StaleGenerationDetachIgnored(t *testing.T) {
	d := newFixDaemon([]byte("SNAP"), []byte("SNAP2"))
	sock, _ := serveFix(t, d)

	ca := dialClient(t, sock, []string{"attach"})
	a, err := ca.Attach(onlyViewID(t, ca))
	if err != nil {
		t.Fatalf("Attach A: %v", err)
	}
	cb := dialClient(t, sock, []string{"attach"})
	b, err := cb.Attach(onlyViewID(t, cb))
	if err != nil {
		t.Fatalf("Attach B (supersede): %v", err)
	}

	// A (superseded, gen < B) detaches. Its old-gen detach must NOT release B's lease.
	if err := a.Detach(); err != nil {
		t.Fatalf("A.Detach: %v", err)
	}

	// B still holds the lease: its live frames still flow, and its (fresh) stream
	// stays open.
	bStream := d.lastOpenedFix()
	if bStream == nil {
		t.Fatalf("B's upstream stream not found")
	}
	bStream.frames <- []byte("after-stale-detach")
	got, ok := recvFrame(t, b.Frames(), recvTimeout)
	if !ok || !bytes.Equal(got, []byte("after-stale-detach")) {
		t.Fatalf("current controller lost its lease to a stale-generation detach (F11): got %q ok=%v", got, ok)
	}
	select {
	case <-bStream.closed:
		t.Fatalf("current upstream stream closed by a stale-generation detach (F11)")
	default:
	}
}

// ---------------------------------------------------------------------------
// F5 — a validated in-flight input serializes the supersede, so a keystroke
// validated at generation N cannot be reordered across a supersede to N+1.
// ---------------------------------------------------------------------------

// blockingStream blocks inside Input until released, so a test can pin an input
// as "validated and in-flight" and observe whether a supersede can slip past it.
type blockingStream struct {
	*fixStream
	enterOnce sync.Once
	enter     chan struct{}
	release   chan struct{}
}

func (b *blockingStream) Input(p []byte) error {
	b.enterOnce.Do(func() { close(b.enter) })
	<-b.release
	return nil
}

func TestFix_InputInFlightSerializesSupersede(t *testing.T) {
	bs := &blockingStream{
		fixStream: newFixStream([]byte("SNAP")),
		enter:     make(chan struct{}),
		release:   make(chan struct{}),
	}
	// The first attach uses the blocking stream; the supersede's fresh re-attach
	// uses a normal stream.
	d := newFixDaemon([]byte("SNAP2"))
	d.custom = []SessionStream{bs}
	sock, _ := serveFix(t, d)

	ca := dialClient(t, sock, []string{"attach"})
	a, err := ca.Attach(onlyViewID(t, ca))
	if err != nil {
		t.Fatalf("Attach A: %v", err)
	}

	// A's input is validated at gen A and blocks in the shim write (in-flight).
	go func() { _ = a.Input([]byte("A-INFLIGHT")) }()
	select {
	case <-bs.enter:
	case <-time.After(recvTimeout):
		t.Fatalf("A's input never reached the stream")
	}

	// B supersedes. The generation bump must WAIT for A's in-flight, validated
	// input to finish, so the keystroke cannot be applied AFTER the supersede (F5).
	cb := dialClient(t, sock, []string{"attach"})
	superseded := make(chan error, 1)
	go func() { _, e := cb.Attach(onlyViewID(t, cb)); superseded <- e }()

	select {
	case <-superseded:
		t.Fatalf("supersede completed while a validated input was in-flight — F5 TOCTOU window open")
	case <-time.After(300 * time.Millisecond):
		// good: the supersede is serialized behind the in-flight input
	}

	close(bs.release) // let A's validated input finish
	select {
	case e := <-superseded:
		if e != nil {
			t.Fatalf("supersede after release: %v", e)
		}
	case <-time.After(recvTimeout):
		t.Fatalf("supersede did not complete after the in-flight input finished")
	}
}

// ---------------------------------------------------------------------------
// F10 — the client synthesizes the D-8 message, never returning daemon prose.
// ---------------------------------------------------------------------------

func TestFix_ClientSynthesizesD8OnServerError(t *testing.T) {
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
		if _, _, err := wire.ReadFrame(conn); err != nil {
			return
		}
		// Reply with an error carrying ARBITRARY prose, not the D-8 text.
		body, _ := EncodeControl(Control{Op: OpError, EndpointID: "srv", Error: "arbitrary-daemon-prose-xyzzy"})
		_ = wire.WriteFrame(conn, wire.TControl, body)
	}()

	_, err = Dial(sock, nil)
	if err == nil {
		t.Fatalf("Dial against an error-replying server: err = nil")
	}
	if !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("Dial error = %v, want ErrIncompatibleVersion", err)
	}
	assertD8Message(t, err.Error())
	if strings.Contains(err.Error(), "arbitrary-daemon-prose-xyzzy") {
		t.Fatalf("client returned daemon prose verbatim (F10): %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// F8 — initial_prompt is carried through to daemon.LaunchSpec.
// ---------------------------------------------------------------------------

func TestFix_InitialPromptReachesDaemonSpec(t *testing.T) {
	stub := newStubDaemon()
	sock := serveStub(t, stub)
	c := dialClient(t, sock, nil)

	dir := t.TempDir()
	if _, err := c.Launch(LaunchReq{Agent: "claude", Cwd: dir, Cols: 80, Rows: 24, InitialPrompt: "do-the-thing"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	specs := stub.launchSpecs()
	if len(specs) != 1 {
		t.Fatalf("recorded launch specs = %d, want 1", len(specs))
	}
	if specs[0].InitialPrompt != "do-the-thing" {
		t.Fatalf("daemon.LaunchSpec.InitialPrompt = %q, want the carried prompt (F8)", specs[0].InitialPrompt)
	}
}

// ---------------------------------------------------------------------------
// F13 — delete drops the session's lease (bounded map growth) and detaches its
// controller.
// ---------------------------------------------------------------------------

func TestFix_DeleteDropsLeaseAndDetachesController(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)
	c := dialClient(t, sock, []string{"attach"})
	id := onlyViewID(t, c)

	a, err := c.Attach(id)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := c.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// The controller's Frames() closes (its lease was dropped)...
	select {
	case _, ok := <-a.Frames():
		if ok {
			select {
			case <-a.Frames():
			case <-time.After(recvTimeout):
				t.Fatalf("controller Frames() did not close after delete (F13)")
			}
		}
	case <-time.After(recvTimeout):
		t.Fatalf("controller Frames() neither closed nor delivered after delete (F13)")
	}
	// ...and the upstream stream is closed.
	if st := stub.streamAt(0); st == nil || !st.waitClosed(recvTimeout) {
		t.Fatalf("upstream stream not closed after delete (F13)")
	}
}

// ---------------------------------------------------------------------------
// NEW HIGH — untrusted snapshot_len: reject negative/huge/overshoot, never OOM.
// ---------------------------------------------------------------------------

// rawLeaseServer stands up a minimal server that completes the hello, then on the
// first attach replies with a lease carrying snapshotLen and the given raw
// TSnapshot chunk frames. Used to feed the client malformed snapshot framing.
func rawLeaseServer(t *testing.T, snapshotLen int, chunks [][]byte) string {
	t.Helper()
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
		typ, payload, err := wire.ReadFrame(conn) // client hello
		if err != nil || typ != wire.TControl {
			return
		}
		hello, _ := DecodeControl(payload)
		ep := "ep-1"
		hb, _ := EncodeControl(Control{Op: OpHello, EndpointID: ep, ProtocolVersion: Version, Capabilities: hello.Capabilities})
		if err := wire.WriteFrame(conn, wire.TControl, hb); err != nil {
			return
		}
		for {
			typ, payload, err := wire.ReadFrame(conn)
			if err != nil {
				return
			}
			if typ != wire.TControl {
				continue
			}
			c, _ := DecodeControl(payload)
			if c.Op != OpAttach {
				continue
			}
			lb, _ := EncodeControl(Control{Op: OpLease, EndpointID: ep, SessionID: c.SessionID, Generation: 1, SnapshotLen: snapshotLen})
			_ = wire.WriteFrame(conn, wire.TControl, lb)
			for _, ch := range chunks {
				_ = wire.WriteFrame(conn, wire.TSnapshot, ch)
			}
			return
		}
	}()
	return sock
}

func TestFix_InvalidSnapshotLenRejected(t *testing.T) {
	cases := []struct {
		name    string
		snapLen int
		chunks  [][]byte
	}{
		{"negative", -1, nil},
		{"huge", (16 << 20) + 1, nil},
		{"overshoot", 4, [][]byte{bytes.Repeat([]byte("x"), 9)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sock := rawLeaseServer(t, tc.snapLen, tc.chunks)
			c, err := Dial(sock, []string{"attach"})
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if _, err := c.Attach(NamespacedID(c.EndpointID(), "sess1")); err == nil {
				t.Fatalf("Attach accepted an invalid snapshot_len (%d) — want an error, not a panic/OOM", tc.snapLen)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F11 — detach generation 0 is invalid; a same-connection stale-generation detach
// does not release the current lease.
// ---------------------------------------------------------------------------

func TestFix_DetachGenerationZeroRejected(t *testing.T) {
	d := newFixDaemon([]byte("SNAP"))
	sock, _ := serveFix(t, d)

	r := rawDial(t, sock)
	h := r.hello(Version, []string{"attach"})
	r.writeControl(Control{Op: OpList, EndpointID: h.EndpointID})
	sid := r.readControl().Sessions[0].ID
	r.writeControl(Control{Op: OpAttach, EndpointID: h.EndpointID, SessionID: sid})
	readLease(t, r) // lease + snapshot

	// A detach with generation 0 is rejected as invalid (not a wildcard).
	r.writeControl(Control{Op: OpDetach, EndpointID: h.EndpointID, SessionID: sid, Generation: 0})
	if got := r.readControl(); got.Op != OpError {
		t.Fatalf("detach gen 0 reply op = %q, want %q (F11)", got.Op, OpError)
	}
	// The lease survives: a live frame still reaches this controller.
	d.lastOpenedFix().frames <- []byte("still-attached")
	if !rawReceivesDataOut(t, r, []byte("still-attached")) {
		t.Fatalf("gen-0 detach released the current lease (F11)")
	}
}

func TestFix_SameConnStaleGenerationDetachIgnored(t *testing.T) {
	d := newFixDaemon([]byte("SNAP1"), []byte("SNAP2"))
	sock, _ := serveFix(t, d)

	r := rawDial(t, sock)
	h := r.hello(Version, []string{"attach"})
	r.writeControl(Control{Op: OpList, EndpointID: h.EndpointID})
	sid := r.readControl().Sessions[0].ID

	// Attach (gen1), then re-attach on the SAME connection (self-supersede -> gen2).
	r.writeControl(Control{Op: OpAttach, EndpointID: h.EndpointID, SessionID: sid})
	gen1 := readLease(t, r)
	r.writeControl(Control{Op: OpAttach, EndpointID: h.EndpointID, SessionID: sid})
	gen2 := readLease(t, r)
	if gen2 <= gen1 {
		t.Fatalf("self-supersede generation: gen2=%d not > gen1=%d", gen2, gen1)
	}
	current := d.lastOpenedFix() // the gen2 upstream stream

	// A delayed detach carrying the STALE gen1 must NOT release the gen2 lease. Follow
	// it with a list request as a barrier: the server's per-connection loop processes
	// the detach BEFORE the list reply, so once we read the list reply the detach has
	// definitely been handled and we can check the lease state without a race.
	r.writeControl(Control{Op: OpDetach, EndpointID: h.EndpointID, SessionID: sid, Generation: gen1})
	r.writeControl(Control{Op: OpList, EndpointID: h.EndpointID})
	readUntilOp(t, r, OpList)

	select {
	case <-current.closed:
		t.Fatalf("same-connection stale-generation detach released the current lease (F11)")
	default:
	}
}

// ---------------------------------------------------------------------------
// F3 / publication race — a supersede during a slow attach's snapshot send is not
// blocked.
// ---------------------------------------------------------------------------

func TestFix_SupersedeDuringSlowSnapshotNotBlocked(t *testing.T) {
	old := pumpWriteTimeoutNS.Load()
	pumpWriteTimeoutNS.Store(int64(300 * time.Millisecond))
	defer pumpWriteTimeoutNS.Store(old)

	// Large snapshot to a non-reading controller: its pump wedges mid snapshot send.
	d := newFixDaemon(bytes.Repeat([]byte("Z"), 4*wire.MaxFrame))
	sock, _ := serveFix(t, d)

	a := rawDial(t, sock)
	ah := a.hello(Version, []string{"attach"})
	a.writeControl(Control{Op: OpList, EndpointID: ah.EndpointID})
	sid := a.readControl().Sessions[0].ID
	a.writeControl(Control{Op: OpAttach, EndpointID: ah.EndpointID, SessionID: sid})
	// Do NOT read A's snapshot, so A's pump wedges while sending the chunks.

	cb := dialClient(t, sock, []string{"attach"})
	bid := onlyViewID(t, cb)
	attached := make(chan error, 1)
	go func() { _, e := cb.Attach(bid); attached <- e }()
	select {
	case err := <-attached:
		if err != nil {
			t.Fatalf("superseding attach failed: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("supersede blocked on a controller wedged mid snapshot send (F3/publication race)")
	}
}

// readLease reads control/snapshot frames until the OpLease grant, consuming its
// snapshot chunks, and returns the lease generation.
func readLease(t *testing.T, r *rawConn) uint64 {
	t.Helper()
	var gen uint64
	var haveLease bool
	var need, got int
	for i := 0; i < 64; i++ {
		typ, payload, err := r.readFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		switch typ {
		case wire.TControl:
			c, derr := DecodeControl(payload)
			if derr == nil && c.Op == OpLease {
				gen, need, haveLease = c.Generation, c.SnapshotLen, true
				if need == 0 {
					return gen
				}
			}
		case wire.TSnapshot:
			if haveLease {
				got += len(payload)
				if got >= need {
					return gen
				}
			}
		}
	}
	t.Fatalf("no lease grant within the frame budget")
	return 0
}

// readUntilOp reads frames until a TControl carrying op arrives (skipping data /
// snapshot frames), failing if it does not appear within the frame budget.
func readUntilOp(t *testing.T, r *rawConn, op string) Control {
	t.Helper()
	for i := 0; i < 64; i++ {
		typ, payload, err := r.readFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if typ != wire.TControl {
			continue
		}
		if c, derr := DecodeControl(payload); derr == nil && c.Op == op {
			return c
		}
	}
	t.Fatalf("op %q not seen within the frame budget", op)
	return Control{}
}

// rawReceivesDataOut reports whether want arrives as a TDataOut frame within a
// bound (skipping intervening control frames).
func rawReceivesDataOut(t *testing.T, r *rawConn, want []byte) bool {
	t.Helper()
	for i := 0; i < 16; i++ {
		typ, payload, err := r.readFrame()
		if err != nil {
			return false
		}
		if typ == wire.TDataOut && bytes.Equal(payload, want) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// multiFixDaemon returns a distinct stream per session id, opening a fresh one
// each Attach (used by the F7 auto-detach test).
type multiFixDaemon struct {
	mu      sync.Mutex
	streams map[string]*fixStream
}

func (d *multiFixDaemon) List() []persist.Meta {
	return []persist.Meta{
		{ID: "s1", AgentType: "claude", Cwd: "/tmp", Status: runningStatus()},
		{ID: "s2", AgentType: "claude", Cwd: "/tmp", Status: runningStatus()},
	}
}
func (d *multiFixDaemon) Launch(daemon.LaunchSpec) (persist.Meta, error) {
	return persist.Meta{}, errors.New("unused")
}
func (d *multiFixDaemon) Kill(string) error   { return nil }
func (d *multiFixDaemon) Delete(string) error { return nil }
func (d *multiFixDaemon) Attach(id string) (SessionStream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.streams[id]
	if st == nil {
		return nil, errors.New("no such stream")
	}
	return st, nil
}
func (d *multiFixDaemon) Events() <-chan persist.Meta { return make(chan persist.Meta) }

func runningStatus() status.Status {
	return status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone}
}

func waitClosedFix(s *fixStream, d time.Duration) bool {
	select {
	case <-s.closed:
		return true
	case <-time.After(d):
		return false
	}
}
