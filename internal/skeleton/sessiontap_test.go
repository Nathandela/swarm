package skeleton

// A7 Slice F1 — shared per-session output tap. These failing-first tests pin the
// four load-bearing properties of the tap that lets TWO consumers (the local owner
// controller and the future remote peek) observe ONE single-consumer shim session:
//
//   1. a second subscriber SHARES the one upstream (exactly one dial), and neither
//      supersedes the other — both see the same live frames;
//   2. a LATE joiner is seeded from the mirror emulator, so its initial Snapshot()
//      reflects the CURRENT grid, while the first subscriber's Snapshot() stays
//      byte-identical to the upstream's;
//   3. the shared upstream is closed exactly when the LAST subscriber closes
//      (refcount -> 0), and a fresh subscribe re-dials;
//   4. a stalled subscriber whose bounded queue overflows is evicted (its channel
//      closes) ALONE, while the other subscriber keeps receiving in order and the
//      upstream is never blocked (S9).
//
// The tap is driven through its injectable dial seam with a fake upstream, so the
// dial count and frame flow are observable without a live shim.

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/vt"
)

// fakeUpstream is a protocol.SessionStream standing in for a live shim connection:
// a fixed attach-time snapshot, a frame channel the test drives, and a Close that
// (like the real shimStream) closes the frame channel so the tap's pump unwinds.
type fakeUpstream struct {
	snap     []byte
	frames   chan []byte
	closedCh chan struct{}
	once     sync.Once

	mu     sync.Mutex
	inputs [][]byte
}

func newFakeUpstream(snap []byte) *fakeUpstream {
	return &fakeUpstream{snap: snap, frames: make(chan []byte, 16), closedCh: make(chan struct{})}
}

func (f *fakeUpstream) Snapshot() []byte      { return f.snap }
func (f *fakeUpstream) Frames() <-chan []byte { return f.frames }

func (f *fakeUpstream) Input(p []byte) error {
	f.mu.Lock()
	f.inputs = append(f.inputs, append([]byte(nil), p...))
	f.mu.Unlock()
	return nil
}

func (f *fakeUpstream) Resize(int, int) error { return nil }

func (f *fakeUpstream) Close() error {
	f.once.Do(func() {
		close(f.closedCh)
		close(f.frames)
	})
	return nil
}

func (f *fakeUpstream) isClosed() bool {
	select {
	case <-f.closedCh:
		return true
	default:
		return false
	}
}

func (f *fakeUpstream) inputCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

// mustSnap builds a real, decodable vt snapshot whose grid contains text, so the
// tap's mirror can seed from it exactly as it will from a live shim snapshot.
func mustSnap(t *testing.T, text string) []byte {
	t.Helper()
	e := vt.NewEmulator(80, 24)
	e.Feed([]byte(text))
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	e.Close()
	return b
}

// snapContains reports whether a decoded snapshot's flattened grid text contains sub.
func snapContains(t *testing.T, snap []byte, sub string) bool {
	t.Helper()
	s, err := vt.DecodeSnapshot(snap)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return strings.Contains(strings.Join(vt.SnapText(s), "\n"), sub)
}

// recvFrame reads one frame from a subscriber within a bound, failing on stall/close.
func recvFrame(t *testing.T, sub protocol.SessionStream) []byte {
	t.Helper()
	select {
	case f, ok := <-sub.Frames():
		if !ok {
			t.Fatal("subscriber frames channel closed while a frame was expected")
		}
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a frame")
		return nil
	}
}

// assertFrame reads one frame and asserts it equals want.
func assertFrame(t *testing.T, sub protocol.SessionStream, want string) {
	t.Helper()
	if got := recvFrame(t, sub); !bytes.Equal(got, []byte(want)) {
		t.Fatalf("frame = %q; want %q", got, want)
	}
}

func TestSessionTap_SecondSubscriberSharesOneUpstream(t *testing.T) {
	var dials int32
	up := newFakeUpstream(mustSnap(t, "hello"))
	mgr := newTapManager(func(string) (protocol.SessionStream, error) {
		atomic.AddInt32(&dials, 1)
		return up, nil
	})

	a, err := mgr.subscribe("s1", readWrite)
	if err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	b, err := mgr.subscribe("s1", readWrite)
	if err != nil {
		t.Fatalf("second subscribe: %v", err)
	}

	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("upstream dialed %d times; two subscribers to one session must share exactly ONE upstream", got)
	}

	// Both subscribers see the SAME live frames — neither supersedes the other.
	up.frames <- []byte("XYZ")
	assertFrame(t, a, "XYZ")
	assertFrame(t, b, "XYZ")
	up.frames <- []byte("123")
	assertFrame(t, a, "123")
	assertFrame(t, b, "123")

	_ = a.Close()
	_ = b.Close()
}

func TestSessionTap_LateJoinerSeededFromMirror(t *testing.T) {
	seed := mustSnap(t, "FIRST")
	up := newFakeUpstream(seed)
	mgr := newTapManager(func(string) (protocol.SessionStream, error) { return up, nil })

	a, err := mgr.subscribe("s1", readWrite)
	if err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	// The first subscriber's snapshot is BYTE-IDENTICAL to the upstream's (today's
	// behavior is preserved exactly for the sole-consumer case).
	if !bytes.Equal(a.Snapshot(), seed) {
		t.Fatalf("first subscriber snapshot is not byte-identical to the upstream snapshot")
	}

	// A live frame advances the grid; draining it on A guarantees the pump has fed
	// the mirror before the late joiner subscribes (same critical section).
	up.frames <- []byte("\r\nSECOND")
	assertFrame(t, a, "\r\nSECOND")

	b, err := mgr.subscribe("s1", readOnly)
	if err != nil {
		t.Fatalf("late subscribe: %v", err)
	}
	if !snapContains(t, b.Snapshot(), "SECOND") {
		t.Fatalf("late joiner snapshot did not reflect the CURRENT grid (missing the frame fed after attach)")
	}
	if snapContains(t, a.Snapshot(), "SECOND") {
		t.Fatalf("first subscriber snapshot must remain the original seed, not the current grid")
	}

	_ = a.Close()
	_ = b.Close()
}

func TestSessionTap_LastCloseClosesUpstream(t *testing.T) {
	var mu sync.Mutex
	var ups []*fakeUpstream
	mgr := newTapManager(func(string) (protocol.SessionStream, error) {
		mu.Lock()
		defer mu.Unlock()
		u := newFakeUpstream(mustSnap(t, "x"))
		ups = append(ups, u)
		return u, nil
	})

	a, err := mgr.subscribe("s1", readWrite)
	if err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	b, err := mgr.subscribe("s1", readWrite)
	if err != nil {
		t.Fatalf("second subscribe: %v", err)
	}

	mu.Lock()
	if len(ups) != 1 {
		mu.Unlock()
		t.Fatalf("dialed %d upstreams; two subscribers must share exactly one", len(ups))
	}
	up1 := ups[0]
	mu.Unlock()

	// First close: upstream stays open — the second subscriber still holds a ref.
	_ = a.Close()
	if up1.isClosed() {
		t.Fatal("upstream closed after the FIRST subscriber left; it must stay open while another holds a ref")
	}

	// Last close: refcount -> 0 closes the single shared upstream.
	_ = b.Close()
	if !waitClosed(up1, time.Second) {
		t.Fatal("upstream not closed after the LAST subscriber left (refcount -> 0)")
	}

	// A fresh subscribe after teardown RE-DIALS a new upstream.
	c, err := mgr.subscribe("s1", readWrite)
	if err != nil {
		t.Fatalf("re-subscribe: %v", err)
	}
	mu.Lock()
	if len(ups) != 2 {
		mu.Unlock()
		t.Fatalf("re-subscribe dialed %d total upstreams; want a fresh dial (2)", len(ups))
	}
	mu.Unlock()
	_ = c.Close()
}

func TestSessionTap_OverflowEvictsThatSubOnly(t *testing.T) {
	up := newFakeUpstream(mustSnap(t, "x"))
	mgr := newTapManager(func(string) (protocol.SessionStream, error) { return up, nil })

	// A is the stalled subscriber (never read); B is drained in lock-step.
	a, err := mgr.subscribe("s1", readWrite)
	if err != nil {
		t.Fatalf("subscribe a: %v", err)
	}
	b, err := mgr.subscribe("s1", readWrite)
	if err != nil {
		t.Fatalf("subscribe b: %v", err)
	}

	// Push past A's bounded queue; B is read each iteration so it never overflows.
	// The upstream must never block (the test's sends would otherwise stall), which
	// proves the pump neither blocks on A nor on the other subscriber.
	n := tapSubQueueCap + 8
	for i := 0; i < n; i++ {
		up.frames <- []byte(fmt.Sprintf("f%d;", i))
		if got := recvFrame(t, b); !bytes.Equal(got, []byte(fmt.Sprintf("f%d;", i))) {
			t.Fatalf("subscriber B frame %d = %q; want in-order f%d;", i, got, i)
		}
	}

	// A overflowed and was evicted: its channel drains its buffered prefix, then closes.
	count, closed := drainUntilClosed(t, a, 2*time.Second)
	if !closed {
		t.Fatalf("stalled subscriber A was not evicted (its frames channel never closed); drained %d", count)
	}
	if count > tapSubQueueCap {
		t.Fatalf("evicted subscriber A drained %d frames; a bounded queue caps at %d", count, tapSubQueueCap)
	}

	_ = a.Close()
	_ = b.Close()
}

// waitClosed polls an upstream until it reports closed or the deadline passes.
func waitClosed(up *fakeUpstream, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if up.isClosed() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return up.isClosed()
}

// drainUntilClosed reads frames from a subscriber until its channel closes or the
// deadline passes, returning how many it drained and whether the channel closed.
func drainUntilClosed(t *testing.T, sub protocol.SessionStream, d time.Duration) (int, bool) {
	t.Helper()
	count := 0
	timeout := time.After(d)
	for {
		select {
		case _, ok := <-sub.Frames():
			if !ok {
				return count, true
			}
			count++
		case <-timeout:
			return count, false
		}
	}
}
