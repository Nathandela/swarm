package shim

import (
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
	"github.com/creack/pty"
)

const (
	// drainReadSize bounds one PTY master read; the drain loop copies each read
	// into its own slice before publishing.
	drainReadSize = 32 * 1024
	// subQueueCap bounds a subscriber's outbound queue in frames. A wedged
	// consumer causes drops here rather than unbounded buffering (S9).
	subQueueCap = 256
	// replyQueueCap bounds the emulator-reply queue in frames. An agent that
	// floods terminal queries while not reading stdin fills this; further
	// replies are dropped rather than blocking the drain (S9). Query replies
	// are non-essential and self-limited to the session.
	replyQueueCap = 256
	// resizeMin/resizeMax bound resize dimensions from the untrusted socket. A
	// resize outside the range is ignored, so no negative or absurd size ever
	// reaches the emulator (panic/OOM guard).
	resizeMin = 1
	resizeMax = 1000
)

// server owns the socket, the emulator/transcript pipeline, and the PTY master
// for one session. Connections are served CONCURRENTLY (one goroutine each), so a
// controller's held attach never blocks a fresh signal/hello connection (R1.3.2);
// the hub still couples the pipeline to at most one live subscriber (S10), so a
// later attach supersedes an earlier one.
type server struct {
	hub          *hub
	ptmx         *os.File
	ptyIn        *ptyWriter // serialized writer to the PTY master (TDataIn + emulator replies)
	pgid         int        // agent process-group id (== agent pid; it leads its own group)
	graceTimeout time.Duration

	socketPath string
	listener   net.Listener

	// escalation tracks the single TERM->KILL worker so finalization can cancel
	// and JOIN it — rather than leave an armed timer that could fire a stray
	// group KILL after Run returns (at a possibly-reused pgid) — and then issue
	// exactly one final synchronous group KILL for containment.
	escMu      sync.Mutex
	escStarted bool
	escStopped bool
	escStop    chan struct{}
	escDone    chan struct{}

	// mu guards the connection set + closing flag; handlers tracks every in-flight
	// serveConn so shutdown can close each connection AND join its handler (no leak,
	// R1.3.2c). Once closing is set (by shutdown) acceptLoop refuses to serve any
	// newly-accepted connection, so no handler is ever added after the join begins.
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	closing  bool
	handlers sync.WaitGroup
}

func newServer(l net.Listener, socketPath string, emu *vt.Emulator, tr *transcript.Writer, ptmx *os.File, pgid int, grace time.Duration, m *Metrics) *server {
	return &server{
		hub:          &hub{emu: emu, tr: tr, metrics: m},
		ptmx:         ptmx,
		ptyIn:        &ptyWriter{f: ptmx},
		pgid:         pgid,
		graceTimeout: grace,
		socketPath:   socketPath,
		listener:     l,
		escStop:      make(chan struct{}),
		escDone:      make(chan struct{}),
		conns:        make(map[net.Conn]struct{}),
	}
}

// listen unlinks any stale socket, binds the UDS with the socket created
// private (0600) from the start, and re-tightens its mode as a fallback. A
// tight umask around the bind closes the TOCTOU window in which a chmod-after-
// bind would leave the socket briefly group/other-accessible.
func listen(path string) (net.Listener, error) {
	_ = os.Remove(path) // clear a stale socket from a prior crash
	// syscall.Umask is process-global; this brackets it tightly around the bind
	// and assumes one-shim-per-process (the production model — a shim process
	// owns exactly one session), so no concurrent file creation races the window.
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// drain is the PTY master read loop: every chunk is fed to the emulator +
// transcript and published to the connected client, then returns at EOF (the
// agent and all slave holders have exited). It never blocks on a slow consumer.
func (s *server) drain() {
	buf := make([]byte, drainReadSize)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			s.hub.feed(data)
		}
		if err != nil {
			return
		}
	}
}

// acceptLoop serves connections concurrently until the listener is closed: each
// accepted connection gets its own serveConn goroutine, tracked so shutdown can
// close it and join its handler. A connection accepted after shutdown began
// (closing set) is closed immediately and never served (its handler is not
// tracked), so the shutdown join can never race a late Add.
func (s *server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.handlers.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.handlers.Done()
			s.serveConn(conn)
		}()
	}
}

// serveConn drives one client connection to completion: it reads frames and
// dispatches them, tearing down any active subscription when the connection ends.
// All writes to the connection — this loop's hello replies and the attach writer
// goroutine's snapshot/frames — go through one connWriter, so concurrent writers
// on the same connection can never interleave a frame (R1.3.2b/e).
func (s *server) serveConn(conn net.Conn) {
	cw := &connWriter{conn: conn}
	var sub *subscriber
	var helloed bool // gate: no op is honored until a hello frame arrives
	defer func() {
		if sub != nil {
			s.hub.detach(sub)
			<-sub.done
		}
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	for {
		typ, payload, err := wire.ReadFrame(conn)
		if err != nil {
			return
		}
		switch typ {
		case wire.TControl:
			ctrl, derr := shimwire.Decode(payload)
			if derr != nil {
				continue // tolerate malformed control payloads (shimwire contract)
			}
			if ctrl.Type == shimwire.TypeHello {
				helloed = true
				cw.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
				if ctrl.WireVersion != shimwire.Version {
					return // close only this connection on version skew
				}
				continue
			}
			if !helloed {
				continue // ignore attach/resize/signal until the client has said hello
			}
			switch ctrl.Type {
			case shimwire.TypeAttach:
				if sub != nil {
					s.hub.detach(sub)
					<-sub.done
				}
				sub = s.hub.attach(cw)
			case shimwire.TypeResize:
				s.resize(ctrl.Cols, ctrl.Rows)
			case shimwire.TypeSignal:
				s.onSignal(ctrl.Sig)
			}
		case wire.TDataIn:
			if !helloed {
				continue // ignore input until the client has said hello
			}
			_, _ = s.ptyIn.Write(payload)
		}
	}
}

// resize propagates a new size to both the PTY kernel winsize (delivers SIGWINCH
// to the agent) and the emulator grid. An out-of-range size from the untrusted
// socket is ignored rather than passed to the emulator.
func (s *server) resize(cols, rows int) {
	if cols < resizeMin || cols > resizeMax || rows < resizeMin || rows > resizeMax {
		return
	}
	_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	s.hub.emu.Resize(cols, rows)
}

// onSignal terminates the session process group. kill is immediate; term sends
// SIGTERM and arms the escalation worker, which SIGKILLs the group at the grace
// deadline UNLESS finalization cancels it first. The grace KILL is what reaps a
// TERM-ignoring leader so cmd.Wait can return; finalization then joins the
// worker and issues one final synchronous KILL (see finishEscalation), so no
// armed timer ever outlives Run.
func (s *server) onSignal(sig string) {
	switch sig {
	case shimwire.SigKill:
		_ = syscall.Kill(-s.pgid, syscall.SIGKILL)
	case shimwire.SigTerm:
		_ = syscall.Kill(-s.pgid, syscall.SIGTERM)
		s.escMu.Lock()
		if !s.escStarted && !s.escStopped {
			s.escStarted = true
			go s.escalationWorker()
		}
		s.escMu.Unlock()
	}
}

// escalationWorker SIGKILLs the group once the grace window elapses, unless
// finalization cancels it via escStop first. Started at most once per session.
func (s *server) escalationWorker() {
	defer close(s.escDone)
	select {
	case <-time.After(s.graceTimeout):
		_ = syscall.Kill(-s.pgid, syscall.SIGKILL)
	case <-s.escStop:
	}
}

// finishEscalation cancels and JOINS the escalation worker (if TERM ever armed
// it), then issues exactly one final synchronous SIGKILL to the group. Called
// once during finalization, before Run returns: after it, no goroutine remains
// that could later signal the pgid, and the group is guaranteed contained (a
// descendant that ignored TERM without holding the PTY is reaped here, not left
// to a timer). Killing an already-empty group is a harmless ESRCH no-op.
func (s *server) finishEscalation() {
	s.escMu.Lock()
	started := s.escStarted
	if !s.escStopped {
		s.escStopped = true
		close(s.escStop)
	}
	s.escMu.Unlock()
	if started {
		<-s.escDone
	}
	_ = syscall.Kill(-s.pgid, syscall.SIGKILL)
}

// shutdown flushes buffered DataOut to the attached client, emits the exit_report
// control after it, then tears down the socket and EVERY connection. It is called
// once, after the agent has exited and the side-files are written. It closes every
// tracked connection and joins every serveConn handler, so no connection or
// goroutine is left behind (R1.3.2c).
func (s *server) shutdown(rep shimwire.Control) {
	// 1. Publish the exit_report to the attached subscriber (if any) and let its
	//    writer drain + emit it before we tear the connections down.
	s.hub.mu.Lock()
	s.hub.shutdown = true
	s.hub.exitReport = rep
	sub := s.hub.sub
	s.hub.sub = nil
	s.hub.mu.Unlock()

	if sub != nil {
		sub.closeQueue() // writer drains, then emits exit_report
		select {
		case <-sub.done:
		case <-time.After(2 * time.Second): // never hang on a wedged client
		}
	}

	// 2. Stop accepting, refuse any newly-accepted connection, and snapshot the set
	//    of live connections to close.
	s.mu.Lock()
	s.closing = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	s.listener.Close()
	// net.UnixListener unlinks the socket on Close; remove it explicitly too so
	// the session dir is left clean even if that ever changes (idempotent).
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}

	// 3. Close every connection to unblock its parked reader, then join every
	//    handler so Run never returns with a serveConn still running.
	for _, c := range conns {
		c.Close()
	}
	s.handlers.Wait()
}

// hub couples the emulator/transcript pipeline to at most one live subscriber.
// Its mutex is the single serialization point: the drain loop feeds + publishes
// under it, and attach snapshots + subscribes under it, so the snapshot/stream
// boundary is gapless and overlap-free (S10).
type hub struct {
	emu     *vt.Emulator
	tr      *transcript.Writer
	metrics *Metrics

	mu         sync.Mutex
	sub        *subscriber
	shutdown   bool
	exitReport shimwire.Control
}

// feed advances the grid + transcript by one PTY chunk and publishes it to the
// subscriber, dropping (and counting) the chunk if the bounded queue is full.
// Feeding under mu keeps the grid state and the published byte stream in lock
// step with attach's snapshot point.
func (h *hub) feed(data []byte) {
	h.mu.Lock()
	h.emu.Feed(data)
	_, _ = h.tr.Write(data)
	if h.sub != nil {
		select {
		case h.sub.queue <- data:
		default:
			h.metrics.FramesDropped.Add(1)
		}
	}
	h.mu.Unlock()
}

// attach atomically snapshots the grid and installs a fresh subscriber, then
// spawns the connection's writer goroutine: it sends exactly that snapshot first,
// then streams queued live frames, and emits the exit_report on a shutdown-
// triggered close.
//
// It tears down any EXISTING subscriber under h.mu before installing the new one:
// the superseded subscriber's queue is closed so its writer exits (rather than
// blocking forever on a never-closed queue) and the superseded client stops
// receiving frames (R1.3.3). feed publishes under the same h.mu, so it never sends
// to the closed queue (no send-on-closed race). If the hub is already shutting
// down, the new subscriber is NOT installed as h.sub: its writer sends the
// snapshot then the exit_report and exits, so a late attach still sees a final
// screen without being left waiting on the drain.
func (h *hub) attach(cw *connWriter) *subscriber {
	h.mu.Lock()
	if h.sub != nil {
		h.sub.closeQueue() // supersede: terminate the prior writer, free h.sub
		h.sub = nil
	}
	snap, _ := h.emu.Snapshot()
	shuttingDown := h.shutdown
	rep := h.exitReport
	sub := &subscriber{queue: make(chan []byte, subQueueCap), done: make(chan struct{})}
	if !shuttingDown {
		h.sub = sub
	}
	h.mu.Unlock()

	go func() {
		defer close(sub.done)
		if err := cw.writeFrame(wire.TSnapshot, snap); err != nil {
			h.drainQueue(sub)
			return
		}
		if shuttingDown {
			cw.writeControl(rep) // agent already gone: snapshot then exit_report
			h.drainQueue(sub)
			return
		}
		for data := range sub.queue {
			if err := cw.writeFrame(wire.TDataOut, data); err != nil {
				h.drainQueue(sub)
				break
			}
		}
		h.mu.Lock()
		shuttingDownNow := h.shutdown
		repNow := h.exitReport
		h.mu.Unlock()
		if shuttingDownNow {
			cw.writeControl(repNow)
		}
	}()
	return sub
}

// drainQueue empties a subscriber's queue after its writer has stopped writing,
// so the drain loop's non-blocking sends always have a reader and never wedge on
// a full channel.
func (h *hub) drainQueue(sub *subscriber) {
	for range sub.queue {
	}
}

// detach removes sub if it is the current subscriber and closes its queue,
// letting the writer goroutine finish. Idempotent across the reader-teardown and
// shutdown paths.
func (h *hub) detach(sub *subscriber) {
	h.mu.Lock()
	if h.sub == sub {
		h.sub = nil
	}
	h.mu.Unlock()
	sub.closeQueue()
}

// subscriber is one attached client's outbound side: a bounded live-frame queue
// and a done signal for its writer goroutine.
type subscriber struct {
	queue     chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (s *subscriber) closeQueue() {
	s.closeOnce.Do(func() { close(s.queue) })
}

// connWriter serializes every frame write to one client connection under a mutex,
// so the reader loop's hello replies and the attach writer goroutine's snapshot/
// frames can run concurrently without ever interleaving a frame on the wire
// (R1.3.2b/e).
//
// chunkSnapshot records whether THIS connection's peer (the daemon) advertised
// snapshot chunking in its hello: it is set once by serveConn from the hello frame
// and read once by hub.attach (both in the connection's read-loop goroutine, so it
// never races the attach writer goroutine, which uses a captured copy). It defaults
// false, so a connection whose peer did not advertise chunking — an old daemon, or
// any direct connWriter{} construction — uses today's single-frame snapshot path.
type connWriter struct {
	mu            sync.Mutex
	conn          net.Conn
	chunkSnapshot bool
}

func (w *connWriter) writeFrame(typ wire.Type, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return wire.WriteFrame(w.conn, typ, payload)
}

// writeControl encodes and sends a shimwire.Control as a TControl frame,
// best-effort (a broken connection is handled by the reader path).
func (w *connWriter) writeControl(ctrl shimwire.Control) {
	b, err := shimwire.Encode(ctrl)
	if err != nil {
		return
	}
	_ = w.writeFrame(wire.TControl, b)
}

// exitReport builds the exit_report control from the agent's exit outcome.
func exitReport(code int, signal string) shimwire.Control {
	c := code
	return shimwire.Control{Type: shimwire.TypeExitReport, ExitCode: &c, ExitSignal: signal}
}

// replyPump is the non-blocking bridge from the emulator's query-reply drain to
// the PTY master. The emulator's drain writes replies through Write, which only
// ever enqueues into a bounded channel (dropping when full); a dedicated writer
// goroutine is the sole caller that may block on the PTY master. This keeps a
// query-flooding agent that never reads its stdin from wedging the vt drain —
// and therefore the whole PTY drain — behind a full PTY input buffer (S9).
type replyPump struct {
	out       *ptyWriter
	queue     chan []byte
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newReplyPump(out *ptyWriter) *replyPump {
	p := &replyPump{
		out:   out,
		queue: make(chan []byte, replyQueueCap),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go p.run()
	return p
}

// Write never blocks: it enqueues a copy of b, or drops it when the queue is
// full or the pump is closing. It always reports the full length written so the
// emulator's reply drain treats every reply as consumed.
func (p *replyPump) Write(b []byte) (int, error) {
	cp := append([]byte(nil), b...)
	select {
	case p.queue <- cp:
	case <-p.stop:
	default:
	}
	return len(b), nil
}

func (p *replyPump) run() {
	defer close(p.done)
	for {
		select {
		case b := <-p.queue:
			_, _ = p.out.Write(b) // may block on a full PTY; only this goroutine does
		case <-p.stop:
			return
		}
	}
}

// close stops the writer goroutine and waits for it to exit. It never closes
// the queue channel, so a late reply provoked during emulator teardown can be
// enqueued or dropped by Write without a send-on-closed-channel panic.
func (p *replyPump) close() {
	p.closeOnce.Do(func() { close(p.stop) })
	<-p.done
}

// ptyWriter serializes writes to the PTY master and becomes a silent no-op once
// the master is closed, so late emulator replies never touch a closed fd.
type ptyWriter struct {
	mu     sync.Mutex
	f      *os.File
	closed bool
}

func (p *ptyWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return len(b), nil
	}
	return p.f.Write(b)
}

func (p *ptyWriter) close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}
