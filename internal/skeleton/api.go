package skeleton

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

const (
	// eventPoll is how often the roster is sampled for status changes (well within
	// the L1 <=1 s bound). It mirrors protocol.FromDaemon's cadence.
	eventPoll = 200 * time.Millisecond
	// shimAttachTimeout bounds waiting for a shim's snapshot on attach.
	shimAttachTimeout = 10 * time.Second
	// eventsBuffer sizes the roster event channel the Server fans out from.
	eventsBuffer = 64
)

// coreAPI adapts the core *daemon.Daemon to the protocol.DaemonAPI the Server
// wraps. It is a leak-free, self-contained equivalent of protocol.FromDaemon: the
// same list/kill/delete/attach forwarding and roster-poll event source, plus the
// walking-skeleton's reserved-agent "fake" argv resolution on Launch. It is owned
// here (not FromDaemon) so its poller is stopped deterministically on Close — the
// daemon owns the socket, so the Server never runs FromDaemon's own stop path.
type coreAPI struct {
	core         *daemon.Daemon
	fakeAgentBin string

	events   chan persist.Meta
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func newCoreAPI(core *daemon.Daemon, fakeAgentBin string) *coreAPI {
	a := &coreAPI{
		core:         core,
		fakeAgentBin: fakeAgentBin,
		events:       make(chan persist.Meta, eventsBuffer),
		stop:         make(chan struct{}),
	}
	a.wg.Add(1)
	go a.watch()
	return a
}

func (a *coreAPI) List() []persist.Meta        { return a.core.List() }
func (a *coreAPI) Kill(id string) error        { return a.core.Kill(id) }
func (a *coreAPI) Delete(id string) error      { return a.core.Delete(id) }
func (a *coreAPI) Events() <-chan persist.Meta { return a.events }

// Launch forwards to the core, resolving the reserved walking-skeleton agent
// "fake" to the swarm-fake-agent binary + its script option (the argv the Epic 9
// adapter will otherwise compose). Every other agent's argv is the adapter's job;
// the core rejects an unresolved (empty-argv) launch, so a real agent needs Epic 9.
//
// Resume-as-new-session (Epic 11 / R-2): a launch carrying the reserved
// OptionResumeFrom option resumes a prior session. The option value is the SOURCE
// session's namespaced id; its local part becomes the new session's ResumedFrom
// link, which the daemon stamps into meta.ResumedFrom. Composing the adapter's
// resume argv from the source conversation id is the adapter's job (the fake agent
// has no resume, so it relaunches fresh while still carrying the link).
func (a *coreAPI) Launch(spec daemon.LaunchSpec) (persist.Meta, error) {
	if src := spec.Options[protocol.OptionResumeFrom]; src != "" {
		if _, local, ok := protocol.ParseID(src); ok {
			spec.ResumedFrom = local
		}
	}
	if spec.AgentType == "fake" && a.fakeAgentBin != "" && len(spec.Argv) == 0 {
		spec.Argv = []string{a.fakeAgentBin, spec.Options["script"]}
	}
	return a.core.Launch(spec)
}

// Attach opens a real SessionStream over the daemon->shim connection.
func (a *coreAPI) Attach(id string) (protocol.SessionStream, error) {
	conn, err := a.core.DialSession(id)
	if err != nil {
		return nil, err
	}
	return newShimStream(conn)
}

// emitStatus routes an engine-derived status change through both halves of Epic
// 10's status wiring (the Epic 11 carry-forward, now wired):
//
//   - PERSIST (G6): SetStatus writes the change back through the daemon's sole meta
//     writer, so it is durable and a reconnecting client's List reflects it.
//   - FAN OUT (Epic 6): the updated meta is pushed to the roster event channel the
//     protocol Server fans out, so Subscribe delivers it immediately (L1) rather
//     than waiting for the next roster poll.
//
// SetStatus is the choke point that also guards the process dimension (the daemon
// stays its sole authority), so an unknown/ended session persists nothing and is
// dropped here.
func (a *coreAPI) emitStatus(id string, s status.Status) {
	if err := a.core.SetStatus(id, s); err != nil {
		return // unknown/ended session: nothing to persist or fan out
	}
	m, ok := a.core.Get(id)
	if !ok {
		return
	}
	select {
	case a.events <- m:
	case <-a.stop:
	}
}

// close stops the roster poller and waits for it to exit, so the assembly leaves
// no goroutine behind.
func (a *coreAPI) close() {
	a.stopOnce.Do(func() { close(a.stop) })
	a.wg.Wait()
}

// watch samples the roster and emits a meta whenever a session's status changes
// (the core exposes no push source, so changes are observed by polling). It
// mirrors protocol.FromDaemon's watcher: dedup by status, retry a momentarily-full
// queue on the next poll (never drop a change), and prune vanished sessions so the
// seen map stays bounded.
func (a *coreAPI) watch() {
	defer a.wg.Done()
	seen := map[string]status.Status{}
	t := time.NewTicker(eventPoll)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			present := map[string]struct{}{}
			for _, m := range a.core.List() {
				present[m.ID] = struct{}{}
				if prev, ok := seen[m.ID]; ok && prev == m.Status {
					continue
				}
				select {
				case a.events <- m:
					seen[m.ID] = m.Status // mark seen ONLY once the change is queued
				case <-a.stop:
					return
				default:
					// Queue momentarily full: leave seen unadvanced so this change is
					// retried on the next poll rather than lost.
				}
			}
			for id := range seen {
				if _, ok := present[id]; !ok {
					delete(seen, id)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// shimStream — a protocol.SessionStream backed by a live daemon->shim connection.
// It mirrors protocol.FromDaemon's shim stream (that type is unexported), so the
// assembly can serve attach without depending on FromDaemon's bundled poller.
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
		if typ == wire.TDataOut {
			return nil, errors.New("skeleton: shim sent a live frame before the snapshot")
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
