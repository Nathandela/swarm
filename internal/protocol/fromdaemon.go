package protocol

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

const (
	// eventPoll is how often FromDaemon samples the daemon roster for status
	// changes (well within the L1 <=1 s bound).
	eventPoll = 200 * time.Millisecond
)

// shimAttachTimeout is the TOTAL deadline for reading the shim's snapshot on attach
// — one bound covering the preamble AND every chunk of a chunked transfer, so a
// stalled or short chunk stream cannot hang the attach (R1.2.4). It is a var only so
// tests can shorten it; production never reassigns it.
var shimAttachTimeout = 10 * time.Second

// FromDaemon adapts a real *daemon.Daemon to the DaemonAPI the Server wraps:
// list/launch/kill/delete forward directly; Attach opens a real SessionStream
// over the daemon->shim connection; Events polls the roster and emits a meta
// whenever a session's status changes (the daemon exposes no push source to an
// already-open instance, so changes are observed by polling).
func FromDaemon(d *daemon.Daemon) DaemonAPI {
	a := &daemonAdapter{
		d:      d,
		events: make(chan persist.Meta, 64),
		stop:   make(chan struct{}),
	}
	go a.watch()
	return a
}

type daemonAdapter struct {
	d      *daemon.Daemon
	events chan persist.Meta
	stop   chan struct{}
	stopMu sync.Once
}

func (a *daemonAdapter) List() []persist.Meta                                { return a.d.List() }
func (a *daemonAdapter) Launch(spec daemon.LaunchSpec) (persist.Meta, error) { return a.d.Launch(spec) }
func (a *daemonAdapter) Kill(id string) error                                { return a.d.Kill(id) }
func (a *daemonAdapter) Delete(id string) error                              { return a.d.Delete(id) }
func (a *daemonAdapter) Events() <-chan persist.Meta                         { return a.events }

func (a *daemonAdapter) Attach(id string) (SessionStream, error) {
	conn, caps, err := a.d.DialSession(id)
	if err != nil {
		return nil, err
	}
	return newShimStream(conn, caps)
}

// stopEvents halts the roster poller. The Server calls it (via an optional
// interface) on Close so FromDaemon leaves no goroutine behind.
func (a *daemonAdapter) stopEvents() {
	a.stopMu.Do(func() { close(a.stop) })
}

func (a *daemonAdapter) watch() {
	seen := map[string]status.Status{}
	t := time.NewTicker(eventPoll)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			present := map[string]struct{}{}
			for _, m := range a.d.List() {
				present[m.ID] = struct{}{}
				if prev, ok := seen[m.ID]; ok && prev == m.Status {
					continue
				}
				select {
				case a.events <- m:
					seen[m.ID] = m.Status // mark seen ONLY once the change is queued (F4)
				case <-a.stop:
					return
				default:
					// The fan-out queue is momentarily full: leave `seen` unadvanced so
					// this status change is retried on the next poll (<= eventPoll)
					// rather than silently lost (F4/L1).
				}
			}
			// Prune vanished (deleted) sessions so `seen` stays bounded over the
			// daemon's lifetime (F13).
			for id := range seen {
				if _, ok := present[id]; !ok {
					delete(seen, id)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// shimStream — a SessionStream backed by a live daemon->shim connection. This
// is the SOLE implementation of the shim wire protocol on the daemon side:
// FromDaemon's Attach uses it directly (same package); NewShimStream exports
// it for the skeleton assembly's coreAPI.Attach, so exactly one copy talks the
// shim wire.
// ---------------------------------------------------------------------------

// NewShimStream opens a SessionStream over an already-helloed shim connection:
// it sends the attach request, reads the shim's snapshot (S10), then streams
// live frames. caps is the capability set the shim advertised in its hello
// reply (from daemon.DialSession); the reader ENFORCES it — a chunked-snapshot
// preamble from a shim that did not advertise SnapshotChunking is a protocol
// error (R1.2.2), not an accepted stream.
func NewShimStream(conn net.Conn, caps shimwire.Caps) (SessionStream, error) {
	return newShimStream(conn, caps)
}

// SnapshotOnly requests a one-shot grid snapshot over an already-helloed shim
// connection WITHOUT attaching: it sends a snapshot_req and reads the snapshot
// using the same reader (and the same negotiated-chunking enforcement) as an
// attach. It never installs a subscriber shim-side, so it cannot supersede an
// attached controller — the C3 tap-steal fix. The caller owns conn and must
// close it; callers use a DEDICATED connection. Only valid against a shim whose
// hello reply advertised SnapshotOnly (caps).
func SnapshotOnly(conn net.Conn, caps shimwire.Caps) ([]byte, error) {
	body, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSnapshotReq})
	if err != nil {
		return nil, err
	}
	if err := wire.WriteFrame(conn, wire.TControl, body); err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(shimAttachTimeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	return readSnapshot(conn, caps.SnapshotChunking)
}

type shimStream struct {
	conn   net.Conn
	snap   []byte
	frames chan []byte

	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
}

// newShimStream sends the attach request over an already-helloed shim connection
// and reads the one snapshot frame the shim emits first (S10), then starts
// streaming live output frames.
func newShimStream(conn net.Conn, caps shimwire.Caps) (*shimStream, error) {
	body, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeAttach})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := wire.WriteFrame(conn, wire.TControl, body); err != nil {
		conn.Close()
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(shimAttachTimeout))
	snap, err := readSnapshot(conn, caps.SnapshotChunking)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Time{})

	st := &shimStream{
		conn:   conn,
		snap:   snap,
		frames: make(chan []byte, 256),
		done:   make(chan struct{}),
	}
	go st.readLoop()
	return st, nil
}

var (
	errDataBeforeSnapshot   = errors.New("protocol: shim sent a live frame before the snapshot completed")
	errSnapshotLen          = errors.New("protocol: shim declared an invalid snapshot length")
	errSnapshotOvershoot    = errors.New("protocol: shim sent more snapshot bytes than it declared")
	errDuplicatePreamble    = errors.New("protocol: shim sent a duplicate snapshot preamble")
	errUnnegotiatedPreamble = errors.New("protocol: shim sent a chunked-snapshot preamble without advertising snapshot_chunking")
)

// readSnapshot reads the shim's snapshot before the live stream. It accepts two
// on-wire encodings and completes the moment the snapshot is whole — without waiting
// on a following frame, so an idle session never hangs (R1.2.2):
//
//   - CHUNKED (snapshot chunking negotiated at hello): a shimwire snapshot_info
//     control preamble declares the total length up front, then that many bytes arrive
//     across one or more TSnapshot chunk frames. chunkingNegotiated carries the
//     shim's advertised capability from its hello reply; a preamble arriving when
//     it is false is a protocol error — the daemon reassembles ONLY when the
//     shim's reply advertised support (R1.2.2), enforced here rather than assumed.
//   - SINGLE-FRAME (old shim, or chunking not negotiated): one bare TSnapshot frame,
//     exactly today's behavior (R1.1.4 carried forward).
//
// A live TDataOut before the snapshot completes violates S10 and is an error. The
// caller sets a TOTAL read deadline (shimAttachTimeout) covering the preamble and
// every chunk, so a short or stalled stream fails within a bound (R1.2.4).
func readSnapshot(conn net.Conn, chunkingNegotiated bool) ([]byte, error) {
	for {
		typ, payload, err := wire.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		switch typ {
		case wire.TSnapshot:
			return payload, nil
		case wire.TDataOut:
			return nil, errDataBeforeSnapshot
		case wire.TControl:
			c, derr := shimwire.Decode(payload)
			if derr != nil {
				continue // tolerate a malformed control frame (shimwire contract)
			}
			if c.Type == shimwire.TypeSnapshotInfo {
				if !chunkingNegotiated {
					return nil, errUnnegotiatedPreamble
				}
				return readChunkedSnapshot(conn, c.SnapshotLen)
			}
			// ignore any other pre-snapshot control frame
		}
	}
}

// readChunkedSnapshot reassembles a chunked snapshot of EXACTLY n bytes, arriving as
// TSnapshot chunk frames after the snapshot_info preamble. It bounds n by the same cap
// the client<->daemon hop uses (maxSnapshotBytes), so a bogus/huge declared length is
// rejected before any allocation — a hostile shim cannot OOM the daemon (R1.2.1).
// Overshoot, a live frame before completion, and a duplicate preamble are protocol
// errors; a short/stalled stream fails via the caller's total read deadline. n==0
// completes immediately (an empty snapshot never waits for a chunk frame).
func readChunkedSnapshot(conn net.Conn, n int) ([]byte, error) {
	if n < 0 || n > maxSnapshotBytes {
		return nil, errSnapshotLen
	}
	buf := make([]byte, 0, n)
	for len(buf) < n {
		typ, payload, err := wire.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		switch typ {
		case wire.TSnapshot:
			if len(buf)+len(payload) > n {
				return nil, errSnapshotOvershoot
			}
			buf = append(buf, payload...)
		case wire.TDataOut:
			return nil, errDataBeforeSnapshot
		case wire.TControl:
			c, derr := shimwire.Decode(payload)
			if derr == nil && c.Type == shimwire.TypeSnapshotInfo {
				return nil, errDuplicatePreamble
			}
			// ignore any other control frame interleaved in the chunk stream
		}
	}
	return buf, nil
}

func (st *shimStream) readLoop() {
	defer close(st.frames)
	for {
		typ, payload, err := wire.ReadFrame(st.conn)
		if err != nil {
			return
		}
		switch typ {
		case wire.TDataOut:
			select {
			case st.frames <- payload:
			case <-st.done:
				return
			}
		case wire.TControl:
			c, derr := shimwire.Decode(payload)
			if derr == nil && c.Type == shimwire.TypeExitReport {
				return // session ended
			}
		}
	}
}

func (st *shimStream) Snapshot() []byte      { return st.snap }
func (st *shimStream) Frames() <-chan []byte { return st.frames }

func (st *shimStream) Input(p []byte) error {
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	return wire.WriteFrame(st.conn, wire.TDataIn, p)
}

func (st *shimStream) Resize(cols, rows int) error {
	body, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeResize, Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	return wire.WriteFrame(st.conn, wire.TControl, body)
}

func (st *shimStream) Close() error {
	st.closeOnce.Do(func() {
		close(st.done)
		st.conn.Close()
	})
	return nil
}
