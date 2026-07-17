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
	// shimAttachTimeout bounds waiting for the shim's snapshot on attach.
	shimAttachTimeout = 10 * time.Second
)

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
	conn, err := a.d.DialSession(id)
	if err != nil {
		return nil, err
	}
	return newShimStream(conn)
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
// shimStream — a SessionStream backed by a live daemon->shim connection.
// ---------------------------------------------------------------------------

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
func newShimStream(conn net.Conn) (*shimStream, error) {
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
	snap, err := readSnapshot(conn)
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

// readSnapshot reads frames until the shim's single TSnapshot arrives.
func readSnapshot(conn net.Conn) ([]byte, error) {
	for {
		typ, payload, err := wire.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		if typ == wire.TSnapshot {
			return payload, nil
		}
		// Ignore any pre-snapshot control frame; a data frame before the snapshot
		// would violate the shim's S10 guarantee, so treat it as an error.
		if typ == wire.TDataOut {
			return nil, errors.New("protocol: shim sent a live frame before the snapshot")
		}
	}
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
