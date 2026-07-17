package protocol

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

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
		AgentType: req.Agent,
		Cwd:       req.Cwd,
		ClientEnv: persist.FilterEnv(req.Env),
		Cols:      req.Cols,
		Rows:      req.Rows,
		Options:   req.Options,
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
)

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
// supersede, and returns the new generation plus the snapshot. Ordering (S10) is
// the caller's: it must write the lease grant and the single snapshot before the
// pump streams live frames.
func (s *Server) attach(cc *clientConn, local string) (gen uint64, snap []byte, err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, nil, fmt.Errorf("protocol: server closed")
	}
	ls := s.leases[local]
	if ls == nil {
		ls = &sessionLease{}
		s.leases[local] = ls
	}
	if ls.stream == nil {
		st, aerr := s.d.Attach(local)
		if aerr != nil {
			s.mu.Unlock()
			return 0, nil, aerr
		}
		ls.stream = st
	}
	stream := ls.stream
	prev := ls.controller
	prevStop, prevDone := ls.pumpStop, ls.pumpDone
	ls.genCounter++
	gen = ls.genCounter
	ls.controller = cc
	ls.pumpStop = make(chan struct{})
	ls.pumpDone = make(chan struct{})
	newStop, newDone := ls.pumpStop, ls.pumpDone
	s.mu.Unlock()

	if prev != nil {
		close(prevStop)
		<-prevDone
		prev.sendDetach(local) // superseded controller's Frames() closes (client side)
		prev.clearAttach(local)
	}

	cc.setAttach(local, gen)
	snap = stream.Snapshot()

	// Lease grant + the one snapshot, in order, BEFORE any live frame (S10).
	if werr := cc.writeControl(Control{Op: OpLease, EndpointID: cc.endpointID, SessionID: NamespacedID(cc.endpointID, local), Generation: gen}); werr != nil {
		return 0, nil, werr
	}
	if werr := cc.writeFrame(wire.TSnapshot, snap); werr != nil {
		return 0, nil, werr
	}
	s.wg.Add(1)
	go s.pump(cc, local, stream, newStop, newDone)
	return gen, snap, nil
}

// pump streams one controller's live output frames until superseded/detached
// (stop) or the upstream ends.
func (s *Server) pump(cc *clientConn, local string, stream SessionStream, stop, done chan struct{}) {
	defer s.wg.Done()
	defer close(done)
	frames := stream.Frames()
	for {
		select {
		case <-stop:
			return
		case data, ok := <-frames:
			if !ok {
				s.releaseFromPump(cc, local)
				return
			}
			if err := cc.writeFrame(wire.TDataOut, data); err != nil {
				return
			}
		}
	}
}

// releaseFromPump releases the lease when the upstream stream ends on its own,
// notifying the controller (its Frames() closes). It never touches the pump
// lifecycle channels (the pump is already exiting).
func (s *Server) releaseFromPump(cc *clientConn, local string) {
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
	cc.sendDetach(local)
	cc.clearAttach(local)
}

// releaseLease releases cc's lease on local (self-detach or client EOF): it stops
// the pump and closes the single upstream stream (1->0, L3). notify sends the
// client an OpDetach so its Frames() closes on an orderly detach.
func (s *Server) releaseLease(cc *clientConn, local string, notify bool) {
	s.mu.Lock()
	ls := s.leases[local]
	if ls == nil || ls.controller != cc {
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

// forwardInput forwards a controller's input only under the current lease
// generation; a stale (superseded) connection's input is dropped server-side (S2).
func (s *Server) forwardInput(cc *clientConn, local string, gen uint64, p []byte) {
	s.mu.Lock()
	ls := s.leases[local]
	if ls == nil || ls.controller != cc || ls.genCounter != gen {
		s.mu.Unlock()
		return
	}
	stream := ls.stream
	s.mu.Unlock()
	if stream != nil {
		_ = stream.Input(p)
	}
}

// forwardResize forwards a resize only under the current lease generation and
// within the accepted dimension range; stale or out-of-range resizes are dropped
// (S2/P-5/P-6).
func (s *Server) forwardResize(cc *clientConn, local string, gen uint64, cols, rows int) {
	if cols < 1 || cols > maxDim || rows < 1 || rows > maxDim {
		return
	}
	s.mu.Lock()
	ls := s.leases[local]
	if ls == nil || ls.controller != cc || ls.genCounter != gen {
		s.mu.Unlock()
		return
	}
	stream := ls.stream
	s.mu.Unlock()
	if stream != nil {
		_ = stream.Resize(cols, rows)
	}
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
		cc.replyError(d8Message(c.ProtocolVersion, Version))
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
	cc.replyOK(c.SessionID)
}

func (cc *clientConn) handleAttach(c Control) {
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	if _, _, err := cc.srv.attach(cc, local); err != nil {
		cc.replyError("attach: " + err.Error())
	}
}

func (cc *clientConn) handleDetach(c Control) {
	ep, local, ok := ParseID(c.SessionID)
	if !ok || ep != cc.endpointID || !validLocalID(local) {
		cc.replyError("invalid session id")
		return
	}
	cc.srv.releaseLease(cc, local, true)
}

func (cc *clientConn) handleResize(c Control) {
	ep, local, ok := ParseID(c.SessionID)
	if !ok || ep != cc.endpointID || !validLocalID(local) {
		return // resize is fire-and-forget; a bad id is simply dropped
	}
	cc.srv.forwardResize(cc, local, c.Generation, c.Cols, c.Rows)
}

func (cc *clientConn) handleSubscribe() {
	cc.replyOK("") // ack before any event so the client sees the ack first
	cc.subOnce.Do(func() {
		cc.eventQ = make(chan Control, eventQueueCap)
		cc.srv.wg.Add(1)
		go cc.eventWriter()
	})
	cc.srv.subMu.Lock()
	cc.srv.subs[cc] = struct{}{}
	cc.srv.subMu.Unlock()
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
		cc.srv.releaseLease(cc, local, false) // client EOF releases the lease (P-4/L3)
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
// closes (the "you lost the lease" signal). Best-effort.
func (cc *clientConn) sendDetach(local string) {
	_ = cc.writeControl(Control{Op: OpDetach, EndpointID: cc.endpointID, SessionID: NamespacedID(cc.endpointID, local)})
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
