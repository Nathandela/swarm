package protocol

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

// OptionWorktree is the reserved launch-option key through which the worktree
// toggle (LaunchReq.Worktree) reaches the daemon's PreLaunch/PreDelete hooks. The
// daemon LaunchSpec carries no dedicated field, so the boolean travels in Options
// (value "true"); the assembly (skeleton) reads it to gate worktree isolation.
const OptionWorktree = "worktree"

// OptionResumeFrom is the reserved launch-option key through which a resume-as-new-
// session request (Epic 11 / R-2) carries the SOURCE session's namespaced id. Like
// OptionWorktree it travels in Options rather than a dedicated wire field, so the
// frozen protocol schema is unchanged; the assembly (skeleton) resolves it to the
// source's local id, links the new session's meta.ResumedFrom, and composes the
// adapter's resume argv from the source conversation id.
const OptionResumeFrom = "resume_from"

// daemonLaunchSpec builds the DaemonAPI launch spec from a validated request,
// applying the server-side env allowlist (S-6). Argv composition is the adapter's
// job (Epic 9), so it is left empty here.
func daemonLaunchSpec(req *LaunchReq) daemon.LaunchSpec {
	return daemon.LaunchSpec{
		AgentType:     req.Agent,
		Cwd:           req.Cwd,
		ClientEnv:     persist.FilterEnv(req.Env),
		Cols:          req.Cols,
		Rows:          req.Rows,
		Options:       launchOptions(req),
		InitialPrompt: req.InitialPrompt, // carry through to the Epic 9 adapter (F8)
	}
}

// launchOptions returns the launch options, adding the reserved worktree flag when
// the request opted in. It copies the client map so the injected key never mutates
// the caller's request.
func launchOptions(req *LaunchReq) map[string]string {
	if !req.Worktree {
		return req.Options
	}
	out := make(map[string]string, len(req.Options)+1)
	for k, v := range req.Options {
		out[k] = v
	}
	out[OptionWorktree] = "true"
	return out
}

// Server-side validation caps (E6.6/P-6). Every client-supplied field is
// re-validated against these before it reaches the DaemonAPI, regardless of any
// client check.
const (
	maxDim         = 1000    // cols/rows upper bound (matches the shim's resizeMax)
	maxAgentLen    = 256     // agent-name length cap
	maxOptionValue = 4 << 10 // per-option value cap (a few KiB; well under the wire cap)
	// eventQueueCap bounds the per-subscriber event queue (S9). It is generous
	// enough to absorb a legitimate status-change burst (e.g. many sessions
	// transitioning at once on a daemon reconnect) without evicting a healthy
	// subscriber, while still far below any flood a genuinely wedged subscriber
	// produces — so a wedged subscriber is still disconnected within a bound.
	eventQueueCap = 256

	// snapshotChunkSize is the largest snapshot slice carried in one TSnapshot
	// frame. A grid snapshot can exceed wire.MaxFrame (maxDim=1000 → far over
	// 1 MiB), so the Server chunks it across frames the client reassembles (F2).
	snapshotChunkSize = wire.MaxFrame - 1
)

// pumpWriteTimeout bounds every controller-facing write (lease, snapshot chunk,
// live frame). A wedged controller's write fails at the deadline, so the pump is
// evicted and supersede/detach always proceed within a bound (F3/S9). It is an
// atomic (nanoseconds) so a test can shorten it without racing the many pump
// goroutines that read it; the zero value means the 5 s default.
var pumpWriteTimeoutNS atomic.Int64

func pumpWriteTimeout() time.Duration {
	if ns := pumpWriteTimeoutNS.Load(); ns > 0 {
		return time.Duration(ns)
	}
	return 5 * time.Second
}

// serverCaps is the capability set the daemon supports; the handshake returns the
// intersection with the client's offer.
var serverCaps = []string{"attach", "subscribe"}

// Server is the client-facing protocol endpoint: it accepts client connections on
// a UNIX socket, wraps a DaemonAPI, holds the per-session controller lease (S2),
// and fans daemon status events out to subscribers (S9).
type Server struct {
	d  DaemonAPI
	ln net.Listener

	// endpointID, when non-empty, is the STABLE endpoint id every connection to
	// this server is assigned — the assembled daemon is a single federation
	// endpoint (NewServer sets it to the daemon's persistent identity), so a
	// session's namespaced id is the same for every client and across daemon
	// restarts. Empty (the Serve default) falls back to a per-connection counter.
	endpointID string
	epSeq      atomic.Uint64 // per-connection endpoint-id source (when endpointID == "")

	mu     sync.Mutex
	conns  map[*clientConn]struct{}
	leases map[string]*sessionLease // keyed by local session id
	closed bool

	subMu sync.Mutex
	subs  map[*clientConn]struct{}

	stop chan struct{}
	wg   sync.WaitGroup
}

// sessionLease is the per-session controller lease. genCounter is monotonic for
// the Server's lifetime and never resets; stream is the single upstream pipe held
// while a controller is attached (ADR-002/L3).
type sessionLease struct {
	genCounter uint64
	stream     SessionStream
	controller *clientConn
	pumpStop   chan struct{}
	pumpDone   chan struct{}

	// inMu serializes the generation-check-and-send in forwardInput/forwardResize
	// with the supersede that invalidates the lease, so a keystroke validated at
	// generation N can never reach the shim once a supersede to N+1 has begun
	// (F5/S2). It is a per-lease lock, so no shim I/O is ever done under s.mu.
	inMu sync.Mutex

	// attachMu serializes the whole attach operation per session (claim -> tear
	// down prior -> close old shim conn -> open fresh conn -> start pump). It closes
	// the publication race (pump channels are published only with a started pump)
	// and enforces close-old-before-open-new for the one-connection shim.
	attachMu sync.Mutex
}

// Serve binds the client socket and starts accepting connections and fanning out
// daemon events. The caller closes it with Close.
func Serve(d DaemonAPI, socketPath string) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	s := newServer(d)
	s.ln = ln
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// NewServer builds a Server that does NOT own a listener: the caller accepts
// connections elsewhere and feeds it the CLIENT ones via ServeConn. This is the
// Epic 8 assembly seam — the daemon owns the singleton socket and demuxes hook
// posts from client connections, so only the client connections reach the Server.
// It still fans daemon events out to subscribers. Close it with Close.
//
// endpointID is the stable id every connection is assigned (the assembled daemon
// is one federation endpoint), so a session's namespaced id is identical for every
// client and stable across restarts. An empty endpointID falls back to the
// per-connection counter (the Serve default).
func NewServer(d DaemonAPI, endpointID string) *Server {
	s := newServer(d)
	s.endpointID = endpointID
	return s
}

// newServer allocates a Server and starts its event fan-out. The listener (Serve)
// or the per-connection feed (NewServer/ServeConn) is layered on by the caller.
func newServer(d DaemonAPI) *Server {
	s := &Server{
		d:      d,
		conns:  make(map[*clientConn]struct{}),
		leases: make(map[string]*sessionLease),
		subs:   make(map[*clientConn]struct{}),
		stop:   make(chan struct{}),
	}
	s.wg.Add(1)
	go s.fanoutLoop()
	return s
}

// ServeConn serves one already-accepted client connection to completion. It is
// the per-connection half of the accept loop, exposed for a caller that owns the
// socket (the daemon) and hands the Server only the client connections it demuxed.
// It blocks until the connection ends, so the caller runs it in its own goroutine.
func (s *Server) ServeConn(conn net.Conn) {
	cc := s.registerConn(conn)
	if cc == nil {
		return
	}
	cc.serve() // blocks; its defer calls wg.Done
}

// Close stops serving, disconnects every client, and releases the socket.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	conns := make([]*clientConn, 0, len(s.conns))
	for cc := range s.conns {
		conns = append(conns, cc)
	}
	s.mu.Unlock()

	if s.ln != nil {
		s.ln.Close() // NewServer has no listener; the daemon owns the socket
	}
	for _, cc := range conns {
		cc.close()
	}
	s.wg.Wait()
	// If the DaemonAPI runs a background event source (FromDaemon's roster
	// poller), stop it so no goroutine is left behind.
	if es, ok := s.d.(interface{ stopEvents() }); ok {
		es.stopEvents()
	}
	return nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		cc := s.registerConn(conn)
		if cc == nil {
			return // server closed while accepting
		}
		go cc.serve()
	}
}

// registerConn tracks an accepted connection and reserves its wg slot, returning
// its clientConn (or nil, having closed the connection, if the server is closing).
// The caller runs cc.serve(), which calls wg.Done when the connection ends.
func (s *Server) registerConn(conn net.Conn) *clientConn {
	cc := &clientConn{srv: s, conn: conn, done: make(chan struct{})}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		conn.Close()
		return nil
	}
	s.conns[cc] = struct{}{}
	s.mu.Unlock()
	s.wg.Add(1)
	return cc
}

// fanoutLoop drains the single daemon event source and distributes each status
// change to every subscriber via its bounded queue; a wedged subscriber is
// disconnected, never allowed to block the loop (S9, L1).
func (s *Server) fanoutLoop() {
	defer s.wg.Done()
	events := s.d.Events()
	for {
		select {
		case <-s.stop:
			return
		case m, ok := <-events:
			if !ok {
				return
			}
			s.distribute(m)
		}
	}
}

func (s *Server) distribute(m persist.Meta) {
	group := status.Derive(m.Status)
	s.subMu.Lock()
	var dead []*clientConn
	for sc := range s.subs {
		ev := Control{Op: OpEvent, EndpointID: sc.endpointID, Session: sc.stampView(m, group)}
		select {
		case sc.eventQ <- ev:
		default:
			dead = append(dead, sc)
		}
	}
	for _, sc := range dead {
		delete(s.subs, sc)
	}
	s.subMu.Unlock()
	for _, sc := range dead {
		sc.close() // wedged subscriber: disconnect within a bound (S9/P-3)
	}
}

func (s *Server) removeConn(cc *clientConn) {
	s.mu.Lock()
	delete(s.conns, cc)
	s.mu.Unlock()
	s.subMu.Lock()
	delete(s.subs, cc)
	s.subMu.Unlock()
}

// attach installs cc as the controller of local at a new, higher generation (S2),
// superseding any prior controller. Rather than splice a fresh snapshot into a
// reused stream, a supersede RE-ATTACHES: it tears down the prior controller,
// CLOSES the old upstream shim connection, and opens a FRESH one. The shim serves
// one connection at a time and delivers snapshot-then-frames atomically (Epic 4,
// S10) — so the new controller always sees the CURRENT grid with no daemon-side
// splice (F1). A fresh-attach failure is a HARD error: the supersede fails cleanly
// and never shows a stale screen.
//
// The whole attach is serialized per session by attachMu: this closes the
// publication race (the controller + pump channels are published ONLY once a real
// pump is started, so a concurrent supersede/detach never waits on a not-yet-
// started pump) and guarantees the close-old-before-open-new ordering the shim
// needs. A supersede's own blocking is bounded by the shim dial; it never waits on
// a wedged pump (evicted here within the pump's write bound).
func (s *Server) attach(cc *clientConn, local string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("protocol: server closed")
	}
	ls := s.leases[local]
	if ls == nil {
		ls = &sessionLease{}
		s.leases[local] = ls
	}
	s.mu.Unlock()

	ls.attachMu.Lock()
	defer ls.attachMu.Unlock()

	// Phase 1 — claim the lease at a NEW generation and capture the prior state.
	// inMu serializes the generation bump with in-flight input (F5); lock order is
	// always attachMu -> inMu -> s.mu. The controller is published with a nil
	// stream/pump until Phase 4 installs a real pump, so stale/own input is dropped
	// (stream == nil) meanwhile.
	ls.inMu.Lock()
	s.mu.Lock()
	if s.closed || s.leases[local] != ls {
		s.mu.Unlock()
		ls.inMu.Unlock()
		return fmt.Errorf("protocol: session %q no longer attachable", local)
	}
	prev := ls.controller
	prevStop, prevDone := ls.pumpStop, ls.pumpDone
	oldStream := ls.stream
	ls.genCounter++
	myGen := ls.genCounter
	ls.controller = cc
	ls.stream = nil
	ls.pumpStop = nil
	ls.pumpDone = nil
	s.mu.Unlock()
	ls.inMu.Unlock()

	cc.setAttach(local, myGen)

	// Phase 2 — tear down the prior controller and FREE its shim connection so the
	// fresh attach below can be served (the shim handles one connection at a time).
	if prev != nil {
		if prevStop != nil {
			close(prevStop)
			<-prevDone
		}
		if prev != cc { // self re-attach must not clear its own new claim
			prev.sendDetach(local)
			prev.clearAttach(local)
		}
	}
	if oldStream != nil {
		_ = oldStream.Close()
	}

	// Phase 3 — open a FRESH upstream stream: its snapshot is the shim's CURRENT
	// grid, atomic with its first live frame (S10). A failure is a HARD error (F1).
	newStream, aerr := s.d.Attach(local)
	if aerr != nil {
		s.abandonClaim(cc, local, myGen)
		return aerr
	}
	snap := newStream.Snapshot()

	// Phase 4 — install the fresh stream + pump IF still the current controller.
	// Publishing the pump channels together with the started pump keeps done always
	// closable, so no supersede/detach ever waits on a dangling pumpDone (F2).
	ls.inMu.Lock()
	s.mu.Lock()
	if s.closed || s.leases[local] != ls || ls.controller != cc || ls.genCounter != myGen {
		s.mu.Unlock()
		ls.inMu.Unlock()
		_ = newStream.Close() // superseded/released while opening: a newer attach owns it
		return nil
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	ls.stream = newStream
	ls.pumpStop = stop
	ls.pumpDone = done
	s.wg.Add(1)
	go s.pump(cc, local, newStream, myGen, snap, stop, done)
	s.mu.Unlock()
	ls.inMu.Unlock()
	return nil
}

// abandonClaim clears a lease claim whose fresh-stream open failed, if this
// connection still holds it (no stream/pump were installed for it).
func (s *Server) abandonClaim(cc *clientConn, local string, gen uint64) {
	s.mu.Lock()
	if ls := s.leases[local]; ls != nil && ls.controller == cc && ls.genCounter == gen {
		ls.controller = nil
	}
	s.mu.Unlock()
	cc.clearAttach(local)
}

// pump writes one controller's lease grant + snapshot (S10), then streams its live
// output frames until superseded/detached (stop) or the upstream ends. The lease +
// snapshot chunks share a single TOTAL write deadline and the loop checks stop
// BETWEEN chunks, so a supersede/detach during a slow snapshot send is never
// blocked; each live frame gets its own write deadline. A wedged controller is
// evicted within a bound and never blocks supersede/detach (F3).
func (s *Server) pump(cc *clientConn, local string, stream SessionStream, gen uint64, snap []byte, stop, done chan struct{}) {
	defer s.wg.Done()
	defer close(done)

	// Lease grant carrying the snapshot's total length (for chunk reassembly),
	// then the snapshot chunk frames, BEFORE any live frame (S10/F2).
	body, err := EncodeControl(Control{
		Op:          OpLease,
		EndpointID:  cc.endpointID,
		SessionID:   NamespacedID(cc.endpointID, local),
		Generation:  gen,
		SnapshotLen: len(snap),
	})
	if err != nil {
		s.evictPump(cc, local)
		return
	}
	deadline := time.Now().Add(pumpWriteTimeout()) // TOTAL bound for lease + all chunks
	select {
	case <-stop:
		return
	default:
	}
	if werr := cc.writeFrameBy(wire.TControl, body, deadline); werr != nil {
		s.evictPump(cc, local)
		return
	}
	for off := 0; off < len(snap); off += snapshotChunkSize {
		select {
		case <-stop:
			return // supersede/detach during the snapshot send: stop promptly, don't evict
		default:
		}
		end := off + snapshotChunkSize
		if end > len(snap) {
			end = len(snap)
		}
		if werr := cc.writeFrameBy(wire.TSnapshot, snap[off:end], deadline); werr != nil {
			s.evictPump(cc, local)
			return
		}
	}

	frames := stream.Frames()
	for {
		select {
		case <-stop:
			return
		case data, ok := <-frames:
			if !ok {
				s.releaseFromPump(cc, local, true)
				return
			}
			if werr := cc.writeFrameDeadline(wire.TDataOut, data); werr != nil {
				s.evictPump(cc, local) // wedged/gone controller: evict within a bound (F3)
				return
			}
		}
	}
}

// releaseFromPump releases cc's lease from within its exiting pump: it clears the
// controller and closes the single upstream stream (1->0), optionally telling the
// client its Frames() is closing. It never touches the pump lifecycle channels
// (the pump is already exiting).
func (s *Server) releaseFromPump(cc *clientConn, local string, notify bool) {
	s.mu.Lock()
	ls := s.leases[local]
	if ls == nil || ls.controller != cc {
		s.mu.Unlock()
		return
	}
	stream := ls.stream
	ls.controller = nil
	ls.stream = nil
	ls.pumpStop = nil
	ls.pumpDone = nil
	s.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	if notify {
		cc.sendDetach(local)
	}
	cc.clearAttach(local)
}

// evictPump releases the lease when a controller-facing write fails (wedged or
// gone): it releases like an upstream end but skips the best-effort detach notice
// (the socket is already broken) and disconnects the controller, so a wedged
// client can never hold the lease or block supersede/detach (F3/S9).
func (s *Server) evictPump(cc *clientConn, local string) {
	s.releaseFromPump(cc, local, false)
	cc.close()
}

// releaseLease releases cc's lease on local (self-detach or client EOF): it stops
// the pump and closes the fresh upstream stream (1->0, L3). When matchGen is set
// the detach's gen MUST equal the current lease generation, so a delayed
// old-generation detach cannot release the current lease (F11); the EOF path uses
// matchGen=false to release whatever this connection holds. notify sends the
// client an OpDetach so its Frames() closes on an orderly detach.
func (s *Server) releaseLease(cc *clientConn, local string, gen uint64, matchGen, notify bool) {
	s.mu.Lock()
	ls := s.leases[local]
	if ls == nil || ls.controller != cc || (matchGen && ls.genCounter != gen) {
		s.mu.Unlock()
		return
	}
	stop, done := ls.pumpStop, ls.pumpDone
	stream := ls.stream
	ls.controller = nil
	ls.stream = nil
	ls.pumpStop = nil
	ls.pumpDone = nil
	s.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	if stream != nil {
		_ = stream.Close()
	}
	if notify {
		cc.sendDetach(local)
	}
	cc.clearAttach(local)
}

// dropLease releases and removes a deleted session's lease so s.leases stays
// bounded over the Server's lifetime (F13). Any controller (this connection or
// another) is stopped, its stream closed, and it is notified.
func (s *Server) dropLease(local string) {
	s.mu.Lock()
	ls := s.leases[local]
	if ls == nil {
		s.mu.Unlock()
		return
	}
	delete(s.leases, local)
	controller := ls.controller
	stop, done := ls.pumpStop, ls.pumpDone
	stream := ls.stream
	ls.controller = nil
	ls.stream = nil
	ls.pumpStop = nil
	ls.pumpDone = nil
	s.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	if stream != nil {
		_ = stream.Close()
	}
	if controller != nil {
		controller.sendDetach(local)
		controller.clearAttach(local)
	}
}

// forwardInput forwards a controller's input only under the current lease
// generation; a stale (superseded) connection's input is dropped server-side (S2).
// The generation check and the shim write are serialized against supersede by the
// per-lease input lock, so a keystroke validated at generation N never reaches the
// shim after a supersede to N+1 (F5); s.mu is never held across the shim I/O.
func (s *Server) forwardInput(cc *clientConn, local string, gen uint64, p []byte) {
	s.mu.Lock()
	ls := s.leases[local]
	s.mu.Unlock()
	if ls == nil {
		return
	}
	ls.inMu.Lock()
	defer ls.inMu.Unlock()
	s.mu.Lock()
	valid := s.leases[local] == ls && ls.controller == cc && ls.genCounter == gen
	stream := ls.stream
	s.mu.Unlock()
	if !valid || stream == nil {
		return
	}
	_ = stream.Input(p)
}

// forwardResize forwards a resize only under the current lease generation and
// within the accepted dimension range; stale or out-of-range resizes are dropped
// (S2/P-5/P-6). Like forwardInput it serializes the generation check + shim write
// against supersede via the per-lease input lock (F5).
func (s *Server) forwardResize(cc *clientConn, local string, gen uint64, cols, rows int) {
	if cols < 1 || cols > maxDim || rows < 1 || rows > maxDim {
		return
	}
	s.mu.Lock()
	ls := s.leases[local]
	s.mu.Unlock()
	if ls == nil {
		return
	}
	ls.inMu.Lock()
	defer ls.inMu.Unlock()
	s.mu.Lock()
	valid := s.leases[local] == ls && ls.controller == cc && ls.genCounter == gen
	stream := ls.stream
	s.mu.Unlock()
	if !valid || stream == nil {
		return
	}
	_ = stream.Resize(cols, rows)
}

// ---------------------------------------------------------------------------
// clientConn — one client connection's server-side state.
// ---------------------------------------------------------------------------

type clientConn struct {
	srv        *clientSrv
	conn       net.Conn
	endpointID string
	caps       []string
	helloed    bool

	writeMu sync.Mutex

	// subscription
	subOnce sync.Once
	eventQ  chan Control

	// controller state (this conn as the controller of attSession)
	attMu      sync.Mutex
	attSession string
	attGen     uint64

	closeOnce sync.Once
	done      chan struct{}
}

// clientSrv is the subset of *Server a clientConn needs; it is *Server. (Named to
// keep the field concise.)
type clientSrv = Server

func (cc *clientConn) serve() {
	defer cc.srv.wg.Done()
	defer cc.cleanup()
	for {
		typ, payload, err := wire.ReadFrame(cc.conn)
		if err != nil {
			return
		}
		switch typ {
		case wire.TControl:
			ctrl, derr := DecodeControl(payload)
			if derr != nil {
				cc.replyError("malformed control payload")
				continue
			}
			cc.handleControl(ctrl)
		case wire.TDataIn:
			cc.handleDataIn(payload)
		default:
			// TDataOut/TSnapshot are server->client only; ignore from a client.
		}
	}
}

func (cc *clientConn) handleControl(c Control) {
	if c.Op == OpHello {
		cc.handleHello(c)
		return
	}
	if !cc.helloed {
		cc.replyError("handshake required: send hello first")
		return
	}
	// F-1: every post-hello message must carry this connection's endpoint id.
	if c.EndpointID != cc.endpointID {
		cc.replyError("foreign endpoint id")
		return
	}
	switch c.Op {
	case OpList:
		cc.handleList()
	case OpLaunch:
		cc.handleLaunch(c)
	case OpKill:
		cc.handleKill(c)
	case OpDelete:
		cc.handleDelete(c)
	case OpAttach:
		cc.handleAttach(c)
	case OpDetach:
		cc.handleDetach(c)
	case OpResize:
		cc.handleResize(c)
	case OpSubscribe:
		cc.handleSubscribe()
	default:
		cc.replyError("unknown op " + strconv.Quote(c.Op))
	}
}

func (cc *clientConn) handleHello(c Control) {
	if cc.helloed {
		return
	}
	if cc.srv.endpointID != "" {
		cc.endpointID = cc.srv.endpointID // stable per-daemon id (assembly)
	} else {
		cc.endpointID = "ep-" + strconv.FormatUint(cc.srv.epSeq.Add(1), 10)
	}
	if c.ProtocolVersion != Version {
		cc.replyError(d8Message(Version, c.ProtocolVersion)) // (daemonV, clientV) — was swapped (F10)
		return
	}
	cc.caps = intersectCaps(c.Capabilities, serverCaps)
	cc.helloed = true
	_ = cc.writeControl(Control{
		Op:              OpHello,
		EndpointID:      cc.endpointID,
		ProtocolVersion: Version,
		Capabilities:    cc.caps,
	})
}

func (cc *clientConn) handleList() {
	metas := cc.srv.d.List()
	views := make([]SessionView, 0, len(metas))
	for _, m := range metas {
		views = append(views, *cc.stampView(m, status.Derive(m.Status)))
	}
	_ = cc.writeControl(Control{Op: OpList, EndpointID: cc.endpointID, Sessions: views})
}

func (cc *clientConn) handleLaunch(c Control) {
	req := c.Launch
	if req == nil {
		cc.replyError("launch: missing request")
		return
	}
	if req.Agent == "" || len(req.Agent) > maxAgentLen {
		cc.replyError("launch: invalid agent")
		return
	}
	if fi, err := os.Stat(req.Cwd); err != nil || !fi.IsDir() {
		cc.replyError("launch: cwd is not an existing directory")
		return
	}
	for k, v := range req.Options {
		if len(v) > maxOptionValue {
			cc.replyError("launch: option " + strconv.Quote(k) + " value too large")
			return
		}
	}
	if req.Cols < 1 || req.Cols > maxDim || req.Rows < 1 || req.Rows > maxDim {
		cc.replyError("launch: cols/rows out of range")
		return
	}
	spec := daemonLaunchSpec(req)
	m, err := cc.srv.d.Launch(spec)
	if err != nil {
		cc.replyError("launch: " + err.Error())
		return
	}
	_ = cc.writeControl(Control{Op: OpLaunch, EndpointID: cc.endpointID, Session: cc.stampView(m, status.Derive(m.Status))})
}

func (cc *clientConn) handleKill(c Control) {
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	if err := cc.srv.d.Kill(local); err != nil {
		cc.replyError("kill: " + err.Error())
		return
	}
	cc.replyOK(c.SessionID)
}

func (cc *clientConn) handleDelete(c Control) {
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	if err := cc.srv.d.Delete(local); err != nil {
		cc.replyError("delete: " + err.Error())
		return
	}
	cc.srv.dropLease(local) // bound s.leases growth: drop the deleted session's lease (F13)
	cc.replyOK(c.SessionID)
}

func (cc *clientConn) handleAttach(c Control) {
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	// A second attach on this connection auto-detaches the first, so one
	// connection never holds two leases or cross-routes data (F7).
	cc.attMu.Lock()
	prev, prevGen := cc.attSession, cc.attGen
	cc.attMu.Unlock()
	if prev != "" && prev != local {
		cc.srv.releaseLease(cc, prev, prevGen, false, true) // release this conn's first lease
	}
	if err := cc.srv.attach(cc, local); err != nil {
		cc.replyError("attach: " + err.Error())
	}
}

func (cc *clientConn) handleDetach(c Control) {
	ep, local, ok := ParseID(c.SessionID)
	if !ok || ep != cc.endpointID || !validLocalID(local) {
		cc.replyError("invalid session id")
		return
	}
	// The detach MUST carry the current lease generation: generation 0 is not a
	// wildcard, and a delayed old-generation detach cannot release the current
	// lease (F11).
	if c.Generation == 0 {
		cc.replyError("detach: invalid generation")
		return
	}
	cc.srv.releaseLease(cc, local, c.Generation, true, true)
}

func (cc *clientConn) handleResize(c Control) {
	ep, local, ok := ParseID(c.SessionID)
	if !ok || ep != cc.endpointID || !validLocalID(local) {
		return // resize is fire-and-forget; a bad id is simply dropped
	}
	cc.srv.forwardResize(cc, local, c.Generation, c.Cols, c.Rows)
}

func (cc *clientConn) handleSubscribe() {
	cc.subOnce.Do(func() {
		cc.eventQ = make(chan Control, eventQueueCap)
		// Start the writer BEFORE registering, so the bounded queue is always being
		// drained the moment distribute can see this subscriber — no fill window that
		// could wrongly evict a healthy subscriber. Register BEFORE the ack, so a
		// status change right after subscribe is never lost (F4/L1/S9).
		cc.srv.wg.Add(1)
		go cc.eventWriter()
		cc.srv.subMu.Lock()
		cc.srv.subs[cc] = struct{}{}
		cc.srv.subMu.Unlock()
	})
	cc.replyOK("")
}

func (cc *clientConn) handleDataIn(payload []byte) {
	cc.attMu.Lock()
	local, gen := cc.attSession, cc.attGen
	cc.attMu.Unlock()
	if local == "" {
		return // no attach: stray data frame, demuxed and dropped (server survives)
	}
	cc.srv.forwardInput(cc, local, gen, payload)
}

// eventWriter drains this subscriber's bounded queue to the socket; a write error
// (including one provoked by a disconnect) tears the subscriber down.
func (cc *clientConn) eventWriter() {
	defer cc.srv.wg.Done()
	for {
		select {
		case <-cc.done:
			return
		case ev := <-cc.eventQ:
			if err := cc.writeControl(ev); err != nil {
				cc.close()
				return
			}
		}
	}
}

func (cc *clientConn) cleanup() {
	cc.attMu.Lock()
	local := cc.attSession
	cc.attMu.Unlock()
	if local != "" {
		cc.srv.releaseLease(cc, local, 0, false, false) // client EOF releases the lease regardless of gen (P-4/L3)
	}
	cc.srv.removeConn(cc)
	cc.close()
}

func (cc *clientConn) close() {
	cc.closeOnce.Do(func() {
		close(cc.done)
		cc.conn.Close()
	})
}

// resolveSession validates a session-scoped op's namespaced id: it must parse,
// belong to this endpoint, and carry a path-safe local id — else the op is
// refused before any DaemonAPI call (E6.6/E6.8).
func (cc *clientConn) resolveSession(c Control) (string, bool) {
	ep, local, ok := ParseID(c.SessionID)
	if !ok || ep != cc.endpointID || !validLocalID(local) {
		cc.replyError("invalid session id")
		return "", false
	}
	return local, true
}

func (cc *clientConn) stampView(m persist.Meta, group status.Group) *SessionView {
	return &SessionView{
		EndpointID:   cc.endpointID,
		ID:           NamespacedID(cc.endpointID, m.ID),
		Agent:        m.AgentType,
		Cwd:          m.Cwd,
		Status:       m.Status,
		Group:        group,
		LastActivity: m.LastActivity,
		CreatedAt:    m.CreatedAt,
		// Summary (V-4 one-line last-output) is derived from the session's grid by
		// the status engine (Epic 7/10), which persist.Meta does not yet carry. Left
		// "" until then rather than presented as a stale/empty live summary (F12).
		Summary: "",
	}
}

func (cc *clientConn) setAttach(local string, gen uint64) {
	cc.attMu.Lock()
	cc.attSession = local
	cc.attGen = gen
	cc.attMu.Unlock()
}

func (cc *clientConn) clearAttach(local string) {
	cc.attMu.Lock()
	if cc.attSession == local {
		cc.attSession = ""
		cc.attGen = 0
	}
	cc.attMu.Unlock()
}

// sendDetach tells the client its lease on local ended, so its Frames() channel
// closes (the "you lost the lease" signal). Best-effort and deadline-bounded, so
// a wedged superseded controller can never block the supersede path (F3).
func (cc *clientConn) sendDetach(local string) {
	body, err := EncodeControl(Control{Op: OpDetach, EndpointID: cc.endpointID, SessionID: NamespacedID(cc.endpointID, local)})
	if err != nil {
		return
	}
	_ = cc.writeFrameDeadline(wire.TControl, body)
}

func (cc *clientConn) writeControl(c Control) error {
	body, err := EncodeControl(c)
	if err != nil {
		return err
	}
	return cc.writeFrame(wire.TControl, body)
}

func (cc *clientConn) writeFrame(typ wire.Type, payload []byte) error {
	cc.writeMu.Lock()
	defer cc.writeMu.Unlock()
	return wire.WriteFrame(cc.conn, typ, payload)
}

// writeFrameBy writes one frame under an absolute deadline, then clears the
// deadline so other writers on the connection are unaffected. A shared deadline
// across a chunked snapshot bounds the whole send (not per-chunk); a wedged
// controller fails here at the deadline, so the pump/supersede/detach never block
// (F3).
func (cc *clientConn) writeFrameBy(typ wire.Type, payload []byte, deadline time.Time) error {
	cc.writeMu.Lock()
	defer cc.writeMu.Unlock()
	_ = cc.conn.SetWriteDeadline(deadline)
	err := wire.WriteFrame(cc.conn, typ, payload)
	_ = cc.conn.SetWriteDeadline(time.Time{})
	return err
}

// writeFrameDeadline writes one frame under a fresh pumpWriteTimeout (used for
// each independent live frame).
func (cc *clientConn) writeFrameDeadline(typ wire.Type, payload []byte) error {
	return cc.writeFrameBy(typ, payload, time.Now().Add(pumpWriteTimeout()))
}

func (cc *clientConn) replyError(msg string) {
	_ = cc.writeControl(Control{Op: OpError, EndpointID: cc.endpointID, Error: msg})
}

func (cc *clientConn) replyOK(sessionID string) {
	_ = cc.writeControl(Control{Op: OpOK, EndpointID: cc.endpointID, SessionID: sessionID})
}

// intersectCaps returns the capabilities present in both offered and supported,
// in the server's order.
func intersectCaps(offered, supported []string) []string {
	want := make(map[string]bool, len(offered))
	for _, c := range offered {
		want[c] = true
	}
	var out []string
	for _, c := range supported {
		if want[c] {
			out = append(out, c)
		}
	}
	return out
}

// d8Message is the D-8 version-skew message: it names the fix command and states
// the restart is safe (loses no live sessions).
func d8Message(daemonV, clientV int) string {
	return fmt.Sprintf("daemon speaks protocol v%d, client v%d; run `swarm daemon restart` "+
		"(safe: your running sessions keep running and are reconnected — no live sessions are lost)",
		daemonV, clientV)
}

// d8ClientMessage is the client-synthesized D-8 message for a handshake the daemon
// rejected with an error op (the daemon's version is unknown and its prose is not
// trusted). It still names the fix command and states the restart is safe (F10).
func d8ClientMessage() string {
	return fmt.Sprintf("daemon rejected the protocol handshake (client speaks v%d); run `swarm daemon restart` "+
		"(safe: your running sessions keep running and are reconnected — no live sessions are lost)",
		Version)
}
