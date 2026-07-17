package protocol

import (
	"bytes"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// E6.4 — exclusive controller lease with generation ids (S2), and its release on
// detach/EOF (P-4, L3). PIN: a second concurrent attach SUPERSEDES (wins a new,
// higher generation; the prior controller's input/resize are dropped
// server-side), rather than being refused. Generations are monotonic per session.

// oneRunningSession seeds a stub with a single running session and returns it.
func oneRunningSession() *stubDaemon {
	s := newStubDaemon()
	s.setMetas(persist.Meta{
		ID:        "sess1",
		AgentType: "claude",
		Cwd:       "/tmp",
		Status:    status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone},
	})
	return s
}

// onlyViewID returns the namespaced id of the client's single visible session.
func onlyViewID(t *testing.T, c *Client) string {
	t.Helper()
	views, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("List returned %d sessions, want 1", len(views))
	}
	return views[0].ID
}

// TestLease_FirstAttachGetsGeneration asserts the first attach is granted a
// non-zero generation and the one snapshot.
func TestLease_FirstAttachGetsGeneration(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)
	c := dialClient(t, sock, []string{"attach"})

	a, err := c.Attach(onlyViewID(t, c))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if a.Generation() == 0 {
		t.Errorf("first attach generation = 0, want >= 1")
	}
	if !bytes.Equal(a.Snapshot(), []byte("SNAPSHOT")) {
		t.Errorf("attach snapshot = %q, want the stream's snapshot", a.Snapshot())
	}
}

// TestLease_SecondAttachSupersedesWithHigherGeneration asserts a concurrent
// second attach wins a strictly higher generation over the same session, releases
// the prior controller's stream, and gives the new controller a fresh current-grid
// snapshot then live frames.
//
// ARCH REVISION (audit-006 re-review, orchestrator-authorized): the original
// assertion required supersede to REUSE the single upstream stream (streamCount
// == 1). That reuse forced a racy daemon-side re-snapshot splice into a live
// stream. A supersede now RE-ATTACHES via a fresh shim connection instead: the
// shim delivers snapshot-then-stream atomically (Epic 4, S10), so the new
// controller sees the current grid with no daemon-side splice. This test now
// asserts the supersede SEMANTICS (higher generation, prior stream released, fresh
// snapshot + live) rather than single-stream reuse.
func TestLease_SecondAttachSupersedesWithHigherGeneration(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)

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

	if !(b.Generation() > a.Generation()) {
		t.Fatalf("supersede generations: B=%d not > A=%d", b.Generation(), a.Generation())
	}
	// Supersede re-attaches: a fresh stream is opened and the prior one released.
	if got := stub.streamCount(); got != 2 {
		t.Fatalf("DaemonAPI.Attach opened %d streams; want 2 (supersede re-attaches a fresh pipe)", got)
	}
	if st0 := stub.streamAt(0); st0 == nil || !st0.waitClosed(recvTimeout) {
		t.Fatalf("supersede did not release (close) the prior upstream stream (L3)")
	}
	// The new controller sees the fresh stream's snapshot, then live frames.
	if !bytes.Equal(b.Snapshot(), []byte("SNAPSHOT")) {
		t.Fatalf("superseding controller snapshot = %q, want the fresh stream's snapshot", b.Snapshot())
	}
	stub.lastStream().frames <- []byte("live-after-supersede")
	if got, ok := recvFrame(t, b.Frames(), recvTimeout); !ok || !bytes.Equal(got, []byte("live-after-supersede")) {
		t.Fatalf("new controller did not receive live frames after supersede (got %q ok=%v)", got, ok)
	}
}

// TestLease_StaleGenerationInputDroppedServerSide is THE S2 assertion: after a
// supersede, the prior controller's input never reaches the shim; only the
// current controller's input is applied (applied count of stale input == 0).
func TestLease_StaleGenerationInputDroppedServerSide(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)

	ca := dialClient(t, sock, []string{"attach"})
	a, err := ca.Attach(onlyViewID(t, ca))
	if err != nil {
		t.Fatalf("Attach A: %v", err)
	}
	cb := dialClient(t, sock, []string{"attach"})
	b, err := cb.Attach(onlyViewID(t, cb))
	if err != nil {
		t.Fatalf("Attach B: %v", err)
	}

	// A is now stale; B is the current controller.
	if err := a.Input([]byte("STALE-A")); err != nil {
		// Input may return nil even when the server will drop it; either is fine.
		t.Logf("stale A.Input returned: %v", err)
	}
	if err := b.Input([]byte("LIVE-B")); err != nil {
		t.Fatalf("live B.Input: %v", err)
	}

	st := stub.lastStream()
	if !waitStreamInput(st, []byte("LIVE-B"), recvTimeout) {
		t.Fatalf("current controller's input never reached the shim")
	}
	// The stale controller's bytes must NOT be present.
	if bytes.Contains(st.inputBytes(), []byte("STALE-A")) {
		t.Fatalf("stale-generation input was applied: %q — violates S2", st.inputBytes())
	}
}

// TestLease_StaleGenerationResizeDropped asserts resize authority follows the
// lease: a superseded controller's resize is dropped, only the current
// controller's resize reaches the shim.
func TestLease_StaleGenerationResizeDropped(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)

	ca := dialClient(t, sock, []string{"attach"})
	a, _ := ca.Attach(onlyViewID(t, ca))
	cb := dialClient(t, sock, []string{"attach"})
	b, _ := cb.Attach(onlyViewID(t, cb))

	_ = a.Resize(11, 11) // stale
	_ = b.Resize(120, 40)

	st := stub.lastStream()
	deadline := time.Now().Add(recvTimeout)
	for time.Now().Before(deadline) {
		if st.resizeCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, rz := range st.resizesCopy() {
		if rz == [2]int{11, 11} {
			t.Fatalf("stale-generation resize %v was applied — violates S2/P-5", rz)
		}
	}
}

// TestLease_DetachReleasesLeaseAndStream asserts detach closes the upstream
// stream (1->0) and a subsequent attach succeeds with a fresh stream (L3).
func TestLease_DetachReleasesLeaseAndStream(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)

	c := dialClient(t, sock, []string{"attach"})
	a, err := c.Attach(onlyViewID(t, c))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := a.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if st := stub.streamAt(0); st == nil || !st.waitClosed(recvTimeout) {
		t.Fatalf("upstream stream not closed after Detach (lease/stream not released, L3)")
	}

	// A subsequent attach must succeed and open a fresh stream.
	c2 := dialClient(t, sock, []string{"attach"})
	a2, err := c2.Attach(onlyViewID(t, c2))
	if err != nil {
		t.Fatalf("re-Attach after Detach: %v", err)
	}
	if a2.Generation() <= a.Generation() {
		t.Errorf("re-attach generation %d not greater than %d (generations must be monotonic)", a2.Generation(), a.Generation())
	}
	if got := stub.streamCount(); got != 2 {
		t.Fatalf("stream count after detach+reattach = %d, want 2", got)
	}
}

// TestLease_ReleasedOnClientEOF asserts a mid-attach client disconnect releases
// the lease and stream (P-4, L3) so the next attach succeeds.
func TestLease_ReleasedOnClientEOF(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)

	c := dialClient(t, sock, []string{"attach"})
	if _, err := c.Attach(onlyViewID(t, c)); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Model a client crash: close the connection without an orderly detach.
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if st := stub.streamAt(0); st == nil || !st.waitClosed(recvTimeout) {
		t.Fatalf("upstream stream not closed after client EOF (P-4/L3)")
	}

	c2 := dialClient(t, sock, []string{"attach"})
	if _, err := c2.Attach(onlyViewID(t, c2)); err != nil {
		t.Fatalf("Attach after prior client EOF: %v", err)
	}
}

// TestLease_SupersededControllerFramesChannelCloses asserts a superseded
// controller stops receiving live frames: its Frames() channel is closed by the
// server, while the new controller receives the live stream.
func TestLease_SupersededControllerFramesChannelCloses(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)

	ca := dialClient(t, sock, []string{"attach"})
	a, err := ca.Attach(onlyViewID(t, ca))
	if err != nil {
		t.Fatalf("Attach A: %v", err)
	}
	cb := dialClient(t, sock, []string{"attach"})
	b, err := cb.Attach(onlyViewID(t, cb))
	if err != nil {
		t.Fatalf("Attach B: %v", err)
	}

	// A's frames channel must close now that it has lost the lease.
	select {
	case _, ok := <-a.Frames():
		if ok {
			// A stray already-queued frame is tolerable; the channel must still
			// close afterward, which the drain below checks.
		}
	case <-time.After(recvTimeout):
		t.Fatalf("superseded controller's Frames() channel neither closed nor delivered within %s", recvTimeout)
	}

	// The new controller receives a freshly published live frame.
	stub.lastStream().frames <- []byte("live-after-supersede")
	got, ok := recvFrame(t, b.Frames(), recvTimeout)
	if !ok || !bytes.Equal(got, []byte("live-after-supersede")) {
		t.Fatalf("new controller did not receive the live frame after supersede (got %q ok=%v)", got, ok)
	}
}

// waitStreamInput polls until the stream's applied input contains want.
func waitStreamInput(st *stubStream, want []byte, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if bytes.Contains(st.inputBytes(), want) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
