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
		Options:       req.Options,
		InitialPrompt: req.InitialPrompt, // carry through to the Epic 9 adapter (F8)
	}
}

// Server-side validation caps (E6.6/P-6). Every client-supplied field is
// re-validated against these before it reaches the DaemonAPI, regardless of any
// client check.
const (
	maxDim         = 1000    // cols/rows upper bound (matches the shim's resizeMax)
	maxAgentLen    = 256     // agent-name length cap
	maxOptionValue = 4 << 10 // per-option value cap (a few KiB; well under the wire cap)
	eventQueueCap  = 64      // bounded per-subscriber event queue (S9)

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

	epSeq atomic.Uint64 // endpoint-id source

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
}

// Serve binds the client socket and starts accepting connections and fanning out
// daemon events. The caller closes it with Close.
func Serve(d DaemonAPI, socketPath string) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	s := &Server{
		d:      d,
		ln:     ln,
		conns:  make(map[*clientConn]struct{}),
		leases: make(map[string]*sessionLease),
		subs:   make(map[*clientConn]struct{}),
		stop:   make(chan struct{}),
	}
	s.wg.Add(2)
	go s.acceptLoop()
	go s.fanoutLoop()
	return s, nil
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

	s.ln.Close()
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
		cc := &clientConn{srv: s, conn: conn, done: make(chan struct{})}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[cc] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go cc.serve()
	}
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

// attach installs cc as the controller of local, superseding any prior controller
// (S2). It opens the upstream stream on the 0->1 transition, reuses it on
// supersede, and starts the pump that writes the lease grant + snapshot (S10) and
// then streams live frames. On supersede the snapshot is RE-fetched so the new
// controller sees the current grid, not the stale one captured at stream open (F1).
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

	// inMu serializes the generation bump with in-flight input (F5). Lock order is
	// always inMu -> s.mu; forwardInput/forwardResize use the same order.
	ls.inMu.Lock()
	s.mu.Lock()
	if s.closed || s.leases[local] != ls {
		s.mu.Unlock()
		ls.inMu.Unlock()
		return fmt.Errorf("protocol: session %q no longer attachable", local)
	}
	freshStream := false
	if ls.stream == nil {
		st, aerr := s.d.Attach(local)
		if aerr != nil {
			s.mu.Unlock()
			ls.inMu.Unlock()
			return aerr
		}
		ls.stream = st
		freshStream = true
	}
	stream := ls.stream
	prev := ls.controller
	prevStop, prevDone := ls.pumpStop, ls.pumpDone
	ls.genCounter++
	gen := ls.genCounter
	ls.controller = cc
	ls.pumpStop = make(chan struct{})
	ls.pumpDone = make(chan struct{})
	newStop, newDone := ls.pumpStop, ls.pumpDone
	s.mu.Unlock()
	ls.inMu.Unlock()

	if prev != nil {
		close(prevStop)
		<-prevDone
		prev.sendDetach(local) // superseded controller's Frames() closes (client side)
		prev.clearAttach(local)
	}

	cc.setAttach(local, gen)

	// A freshly opened stream's Snapshot() is current; a reused (supersede) one is
	// stale, so re-snapshot the CURRENT grid when the stream supports it (F1).
	snap := stream.Snapshot()
	if !freshStream {
		if rs, ok := stream.(reSnapshotter); ok {
			if fresh, rerr := rs.ReSnapshot(); rerr == nil {
				snap = fresh
			}
		}
	}

	// The pump owns the lease grant + snapshot writes AND the live stream, so its
	// done channel is ALWAYS closed on exit: a write failure can never strand a
	// dangling pumpDone that Close/releaseLease/supersede waits on forever (F2).
	s.wg.Add(1)
	go s.pump(cc, local, stream, gen, snap, newStop, newDone)
	return nil
}

// pump writes one controller's lease grant + snapshot (S10), then streams its
// live output frames until superseded/detached (stop) or the upstream ends. Every
// controller-facing write is deadline-bounded, so a wedged controller is evicted
// within a bound and never blocks supersede/detach (F3).
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
	if werr := cc.writeFrameDeadline(wire.TControl, body); werr != nil {
		s.evictPump(cc, local)
		return
	}
	if werr := cc.writeSnapshot(snap); werr != nil {
		s.evictPump(cc, local)
		return
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
// the pump and closes the single upstream stream (1->0, L3). A non-zero gen is
// validated against the current lease generation, so a delayed old-generation
// detach cannot release the current lease (F11). notify sends the client an
// OpDetach so its Frames() closes on an orderly detach.
func (s *Server) releaseLease(cc *clientConn, local string, gen uint64, notify bool) {
	s.mu.Lock()
	ls := s.leases[local]
	if ls == nil || ls.controller != cc || (gen != 0 && ls.genCounter != gen) {
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
	cc.endpointID = "ep-" + strconv.FormatUint(cc.srv.epSeq.Add(1), 10)
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
		cc.srv.releaseLease(cc, prev, prevGen, true)
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
	// Validate the detach's generation so a delayed old-generation detach cannot
	// release the current lease (F11).
	cc.srv.releaseLease(cc, local, c.Generation, true)
}

func (cc *clientConn) handleResize(c Control) {
	ep, local, ok := ParseID(c.SessionID)
	if !ok || ep != cc.endpointID || !validLocalID(local) {
		return // resize is fire-and-forget; a bad id is simply dropped
	}
	cc.srv.forwardResize(cc, local, c.Generation, c.Cols, c.Rows)
}

func (cc *clientConn) handleSubscribe() {
	first := false
	cc.subOnce.Do(func() {
		cc.eventQ = make(chan Control, eventQueueCap)
		// Register BEFORE the ack so no status change between the ack and
		// registration is lost; events buffer in eventQ until the writer starts
		// below (F4/L1).
		cc.srv.subMu.Lock()
		cc.srv.subs[cc] = struct{}{}
		cc.srv.subMu.Unlock()
		first = true
	})
	cc.replyOK("") // ack; the writer starts after, so the ack still precedes any event on the wire
	if first {
		cc.srv.wg.Add(1)
		go cc.eventWriter()
	}
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
		cc.srv.releaseLease(cc, local, 0, false) // client EOF releases the lease regardless of gen (P-4/L3)
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

// writeFrameDeadline writes one frame under pumpWriteTimeout, then clears the
// deadline so other writers on the connection are unaffected. A wedged controller
// fails here at the deadline, so the pump/supersede/detach never block (F3).
func (cc *clientConn) writeFrameDeadline(typ wire.Type, payload []byte) error {
	cc.writeMu.Lock()
	defer cc.writeMu.Unlock()
	_ = cc.conn.SetWriteDeadline(time.Now().Add(pumpWriteTimeout()))
	err := wire.WriteFrame(cc.conn, typ, payload)
	_ = cc.conn.SetWriteDeadline(time.Time{})
	return err
}

// writeSnapshot writes the snapshot as one or more TSnapshot chunk frames (a full
// grid snapshot can exceed wire.MaxFrame). The client reassembles the chunks up to
// the lease's SnapshotLen before painting (F2). A snapshot that fits in one frame
// is sent as a single raw TSnapshot frame (the common case).
func (cc *clientConn) writeSnapshot(snap []byte) error {
	for off := 0; off < len(snap); off += snapshotChunkSize {
		end := off + snapshotChunkSize
		if end > len(snap) {
			end = len(snap)
		}
		if err := cc.writeFrameDeadline(wire.TSnapshot, snap[off:end]); err != nil {
			return err
		}
	}
	return nil
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
