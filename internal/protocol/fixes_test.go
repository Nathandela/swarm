package protocol

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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

type fixDaemon struct {
	mu       sync.Mutex
	stream   SessionStream
	attaches int
}

func newFixDaemon(stream SessionStream) *fixDaemon { return &fixDaemon{stream: stream} }

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
	d.attaches++
	d.mu.Unlock()
	return d.stream, nil
}
func (d *fixDaemon) Events() <-chan persist.Meta { return make(chan persist.Meta) }
func (d *fixDaemon) attachCount() int            { d.mu.Lock(); defer d.mu.Unlock(); return d.attaches }

// fixStream is a controllable SessionStream. When fresh != nil it also implements
// the re-snapshot capability, returning fresh on supersede (F1).
type fixStream struct {
	snap   []byte
	fresh  []byte
	frames chan []byte

	closeOnce sync.Once
	closed    chan struct{}
	resnaps   int32
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

// ReSnapshot makes fixStream a reSnapshotter when fresh is set; the Server calls
// it on supersede to fetch the CURRENT grid (F1).
func (s *fixStream) ReSnapshot() ([]byte, error) {
	atomic.AddInt32(&s.resnaps, 1)
	return s.fresh, nil
}

// serveFix stands up a Server over a fixDaemon and returns the socket + handle.
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
	st := newFixStream([]byte("STALE-GRID"))
	st.fresh = []byte("FRESH-GRID-AFTER-OUTPUT")
	d := newFixDaemon(st)
	sock, _ := serveFix(t, d)

	ca := dialClient(t, sock, []string{"attach"})
	a, err := ca.Attach(onlyViewID(t, ca))
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if !bytes.Equal(a.Snapshot(), []byte("STALE-GRID")) {
		t.Fatalf("first attach snapshot = %q, want the fresh-stream snapshot", a.Snapshot())
	}

	cb := dialClient(t, sock, []string{"attach"})
	b, err := cb.Attach(onlyViewID(t, cb))
	if err != nil {
		t.Fatalf("supersede Attach: %v", err)
	}
	if !bytes.Equal(b.Snapshot(), []byte("FRESH-GRID-AFTER-OUTPUT")) {
		t.Fatalf("supersede snapshot = %q, want the RE-snapshotted current grid (F1)", b.Snapshot())
	}
	if d.attachCount() != 1 {
		t.Fatalf("DaemonAPI.Attach called %d times; supersede must reuse the one stream", d.attachCount())
	}
	if atomic.LoadInt32(&st.resnaps) != 1 {
		t.Fatalf("ReSnapshot called %d times; supersede must re-snapshot exactly once", st.resnaps)
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
	d := newFixDaemon(newFixStream(big))
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
	d := newFixDaemon(newFixStream(big))
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
	// Short pump write timeout so a wedged controller is evicted fast.
	old := pumpWriteTimeoutNS.Load()
	pumpWriteTimeoutNS.Store(int64(300 * time.Millisecond))
	defer pumpWriteTimeoutNS.Store(old)

	st := newFixStream([]byte("SNAP"))
	d := newFixDaemon(st)
	sock, _ := serveFix(t, d)

	// Wedged controller A: attach raw, then never read again.
	a := rawDial(t, sock)
	ah := a.hello(Version, []string{"attach"})
	a.writeControl(Control{Op: OpList, EndpointID: ah.EndpointID})
	alist := a.readControl()
	sid := alist.Sessions[0].ID
	a.writeControl(Control{Op: OpAttach, EndpointID: ah.EndpointID, SessionID: sid})
	// Flood live frames so A's socket buffer fills and its pump wedges on write.
	stop := make(chan struct{})
	go func() {
		blob := bytes.Repeat([]byte("x"), 32<<10)
		for {
			select {
			case <-stop:
				return
			case st.frames <- blob:
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	defer close(stop)

	// B supersedes; its attach must complete within a bound despite A being wedged.
	cb := dialClient(t, sock, []string{"attach"})
	bid := onlyViewID(t, cb)
	attached := make(chan error, 1)
	go func() {
		_, err := cb.Attach(bid)
		attached <- err
	}()
	select {
	case err := <-attached:
		if err != nil {
			t.Fatalf("superseding attach failed: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("supersede blocked on a wedged controller (F3/S9 liveness)")
	}

	// A is evicted: its socket eventually reports closed.
	if !a.eventuallyClosed(3 * time.Second) {
		t.Fatalf("wedged controller not evicted within bound (F3)")
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
	st := newFixStream([]byte("SNAP"))
	st.fresh = []byte("SNAP2")
	d := newFixDaemon(st)
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

	// B still holds the lease: its live frames still flow, and its stream is open.
	st.frames <- []byte("after-stale-detach")
	got, ok := recvFrame(t, b.Frames(), recvTimeout)
	if !ok || !bytes.Equal(got, []byte("after-stale-detach")) {
		t.Fatalf("current controller lost its lease to a stale-generation detach (F11): got %q ok=%v", got, ok)
	}
	select {
	case <-st.closed:
		t.Fatalf("upstream stream closed by a stale-generation detach (F11)")
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
	d := newFixDaemon(bs)
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
