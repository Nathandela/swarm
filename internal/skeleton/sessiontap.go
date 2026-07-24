package skeleton

// sessiontap.go — A7 Slice F1: the shared per-session output tap.
//
// A session's shim is STRICTLY single-consumer (internal/shim acceptLoop serves one
// connection at a time). Yet two Servers built on ONE coreAPI backend must both
// observe a session: the owner-tier controller (d.srv) and the future remote peek
// (d.remoteSrv). Both reach output only through coreAPI.Attach -> DialSession ->
// newShimStream. So the only way both can coexist is a SINGLE shared upstream teed
// to N subscribers. tapManager is that tee.
//
// Design (transparent to the battle-tested supersede/lease/survival semantics):
//   - The FIRST subscriber to a session opens the one upstream (the injected dial,
//     which in production is DialSession + newShimStream) and seeds a MIRROR vt
//     emulator from the shim's atomic snapshot. Its own Snapshot() is the raw
//     upstream snapshot, BYTE-IDENTICAL to today's sole-consumer behavior.
//   - A single pump goroutine reads the upstream's Frames() and, under the tap lock,
//     feeds the mirror AND fans each frame out to every subscriber's own bounded
//     channel. Feeding the mirror before the fan-out keeps a late joiner's seed and
//     the live stream in lock step (no gap, no dup).
//   - A LATER subscriber is seeded from mirror.Snapshot() under the tap lock — the
//     only way to hand a late joiner the CURRENT grid without re-dialing the
//     single-consumer shim — then receives live frames.
//   - Refcount: the upstream is opened on the first subscribe and closed on the LAST
//     subscriber's Close (1 -> 0). A fresh subscribe after that re-dials. The
//     upstream's Frames() closing (session end) closes every subscriber channel and
//     removes the tap.
//   - Backpressure (S9): each subscriber has its OWN cap-tapSubQueueCap channel; on
//     overflow that ONE subscriber is evicted (its channel closes) — the pump never
//     blocks on it and the other subscribers are untouched.
//   - mode: readWrite forwards Input/Resize to the upstream; readOnly makes them
//     no-ops (the future remote peek observes without driving).
//
// Locking: m.mu guards only the taps map; each tap's t.mu guards its subscriber set,
// mirror, and closed flag. The two locks are NEVER held simultaneously (subscribe
// releases m.mu before taking t.mu; teardown releases t.mu before calling removeTap),
// so no lock-ordering deadlock is possible. Different sessions never contend beyond
// the brief m.mu, preserving the no-head-of-line-blocking property (L1).

import (
	"sync"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/vt"
)

// tapSubQueueCap bounds a single subscriber's outbound frame queue, matching the
// shim's per-subscriber cap (internal/shim subQueueCap) so the eviction-on-overflow
// backpressure (S9) is identical end to end: a wedged consumer is dropped, never
// blocks the shared upstream or the other consumers.
const tapSubQueueCap = 256

// tapMode selects whether a subscriber may drive the session.
type tapMode int

const (
	// readWrite forwards Input/Resize to the shared upstream (the owner controller).
	readWrite tapMode = iota
	// readOnly makes Input/Resize no-ops (the future remote peek: observe, not drive).
	readOnly
)

// tapManager multiplexes one upstream SessionStream per session id to N subscribers.
type tapManager struct {
	dial func(id string) (protocol.SessionStream, error)

	mu   sync.Mutex
	taps map[string]*tap
}

func newTapManager(dial func(id string) (protocol.SessionStream, error)) *tapManager {
	return &tapManager{dial: dial, taps: map[string]*tap{}}
}

// subscribe returns a SessionStream sharing one upstream for id. The first caller
// opens the upstream (dial + mirror seed + pump); later callers join the running
// pump and are seeded from the mirror's current grid.
func (m *tapManager) subscribe(id string, mode tapMode) (*tapSub, error) {
	for {
		m.mu.Lock()
		t := m.taps[id]
		if t == nil {
			t = &tap{mgr: m, id: id, subs: map[*tapSub]struct{}{}}
			m.taps[id] = t
		}
		m.mu.Unlock()

		t.mu.Lock()
		if t.closed {
			// This tap was torn down between the map read and the lock; drop it and
			// retry so we find/create a live one.
			t.mu.Unlock()
			continue
		}
		if t.up == nil {
			// First subscriber: open the single upstream and seed the mirror.
			up, err := m.dial(id)
			if err != nil {
				t.closed = true
				t.mu.Unlock()
				m.removeTap(id, t)
				return nil, err
			}
			t.up = up
			seed := up.Snapshot()
			t.mirror = seedMirror(seed)
			go t.pump()
			// The first subscriber's snapshot is the raw upstream snapshot: identical
			// to the sole-consumer behavior the lease/attach path relies on.
			sub := t.newSubLocked(mode, seed)
			t.mu.Unlock()
			return sub, nil
		}
		// Later subscriber: seed atomically from the mirror's CURRENT grid so it joins
		// mid-stream without re-dialing the single-consumer shim.
		var snap []byte
		if t.mirror != nil {
			snap, _ = t.mirror.Snapshot()
		}
		sub := t.newSubLocked(mode, snap)
		t.mu.Unlock()
		return sub, nil
	}
}

// removeTap drops t from the map iff it is still the mapped tap for id (idempotent
// across the refcount-close and session-end teardown paths).
func (m *tapManager) removeTap(id string, t *tap) {
	m.mu.Lock()
	if m.taps[id] == t {
		delete(m.taps, id)
	}
	m.mu.Unlock()
}

// seedMirror builds the mirror emulator that tracks the current grid for late
// joiners: it decodes the shim's atomic snapshot and replays it as ANSI into a fresh
// emulator, so feeding subsequent live frames keeps the mirror current. An
// undecodable snapshot yields a default-sized empty mirror rather than failing the
// attach (late joiners then reflect only post-attach frames).
func seedMirror(snap []byte) *vt.Emulator {
	s, err := vt.DecodeSnapshot(snap)
	if err != nil {
		return vt.NewEmulator(80, 24)
	}
	e := vt.NewEmulator(s.Cols, s.Rows)
	e.Feed(vt.RenderSnapshot(s))
	return e
}

// tap is one session's shared upstream and its subscriber fan-out.
type tap struct {
	mgr *tapManager
	id  string

	mu     sync.Mutex
	up     protocol.SessionStream // the single shared upstream (nil until first subscribe)
	mirror *vt.Emulator           // tracks the current grid to seed late joiners
	subs   map[*tapSub]struct{}
	closed bool
}

// newSubLocked creates a subscriber, snapshots it, and registers it. Caller holds t.mu.
func (t *tap) newSubLocked(mode tapMode, snap []byte) *tapSub {
	sub := &tapSub{
		t:      t,
		mode:   mode,
		snap:   snap,
		frames: make(chan []byte, tapSubQueueCap),
	}
	t.subs[sub] = struct{}{}
	return sub
}

// pump reads the shared upstream's live frames and, under t.mu, advances the mirror
// then fans each frame out to every subscriber. When the upstream ends (session
// end), it tears the tap down: every subscriber channel closes and the tap is
// removed so a later subscribe re-dials.
func (t *tap) pump() {
	frames := t.up.Frames()
	for frame := range frames {
		t.mu.Lock()
		if t.mirror != nil {
			t.mirror.Feed(frame)
		}
		for sub := range t.subs {
			select {
			case sub.frames <- frame:
			default:
				// Overflow: evict THIS subscriber only (S9). Closing its channel drops
				// it from the fan-out; the pump never blocks and other subs are untouched.
				sub.evict()
				delete(t.subs, sub)
			}
		}
		t.mu.Unlock()
	}
	t.teardown()
}

// teardown closes every subscriber channel, closes the mirror, and removes the tap.
// Idempotent via t.closed; safe to call from the pump (session end) and the refcount
// path. The upstream is closed by whoever triggered teardown (the refcount path), or
// has already ended itself (the session-end path); Close is idempotent regardless.
func (t *tap) teardown() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	for sub := range t.subs {
		sub.evict()
		delete(t.subs, sub)
	}
	up, mirror := t.up, t.mirror
	t.mu.Unlock()

	t.releaseResources(up, mirror)
}

// releaseResources drops the tap from the map and closes the shared upstream and mirror.
// It runs after t.closed has been set under t.mu (by teardown or removeSub), so exactly
// one teardown path reaches it per tap; Close is idempotent on both handles regardless.
func (t *tap) releaseResources(up protocol.SessionStream, mirror *vt.Emulator) {
	t.mgr.removeTap(t.id, t)
	if up != nil {
		_ = up.Close()
	}
	if mirror != nil {
		_ = mirror.Close()
	}
}

// hookAfterLastDetach, when non-nil, fires in removeSub after the last subscriber has
// detached and t.mu has been released, before the shared upstream is torn down. It is a
// TEST SEAM only (nil in production, so production behavior is unchanged); it exists to
// widen the post-detach window and prove a concurrent subscribe is never handed a dead
// subscriber on the dying tap.
var hookAfterLastDetach func()

// removeSub drops sub on its Close. When the last subscriber leaves (refcount -> 0) it
// tears the tap down, closing the single shared upstream. The last-subscriber detection
// and the t.closed transition are ATOMIC under one t.mu hold: closed is set BEFORE the
// lock is released, so a subscribe that interleaves after this either already joined
// (then it is not last and the tap stays open) or sees t.closed and re-dials a fresh tap
// -- it can never register on this dying tap and be evicted by the teardown that follows.
func (t *tap) removeSub(sub *tapSub) {
	t.mu.Lock()
	if _, ok := t.subs[sub]; ok {
		delete(t.subs, sub)
		sub.evict() // detach closes the subscriber's frames so its consumer unwinds
	}
	if len(t.subs) != 0 || t.closed {
		t.mu.Unlock()
		return
	}
	// Last subscriber: commit the close under the lock so it is atomic with the
	// last-detection, then release the shared resources after unlocking.
	t.closed = true
	up, mirror := t.up, t.mirror
	t.mu.Unlock()
	if hookAfterLastDetach != nil {
		hookAfterLastDetach()
	}
	t.releaseResources(up, mirror)
}

// tapSub is one subscriber's view of the shared session: its own snapshot, its own
// bounded live-frame channel, and the mode gate on Input/Resize. It satisfies
// protocol.SessionStream so a Server drives it exactly like a direct shim stream.
type tapSub struct {
	t    *tap
	mode tapMode
	snap []byte

	frames    chan []byte
	evictOnce sync.Once
	closeOnce sync.Once
}

var _ protocol.SessionStream = (*tapSub)(nil)

// evict closes the subscriber's frame channel exactly once. All callers hold t.mu,
// so it is serialized with the pump's fan-out sends — no send races the close.
func (s *tapSub) evict() {
	s.evictOnce.Do(func() { close(s.frames) })
}

func (s *tapSub) Snapshot() []byte      { return s.snap }
func (s *tapSub) Frames() <-chan []byte { return s.frames }

// Input forwards to the shared upstream for a readWrite subscriber; a readOnly peek
// drops it (observe, never drive).
func (s *tapSub) Input(p []byte) error {
	if s.mode != readWrite {
		return nil
	}
	return s.t.up.Input(p)
}

// Resize forwards to the shared upstream for a readWrite subscriber; a readOnly peek
// drops it. (One shared upstream means the last readWrite resize wins, as with a
// single lease today.)
func (s *tapSub) Resize(cols, rows int) error {
	if s.mode != readWrite {
		return nil
	}
	return s.t.up.Resize(cols, rows)
}

// Close detaches this subscriber; the last detach closes the shared upstream.
func (s *tapSub) Close() error {
	s.closeOnce.Do(func() { s.t.removeSub(s) })
	return nil
}
