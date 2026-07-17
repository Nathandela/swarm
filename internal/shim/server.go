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
)

// server owns the socket, the emulator/transcript pipeline, and the PTY master
// for one session. Exactly one client connection is served at a time (v1 shim
// pin).
type server struct {
	hub          *hub
	ptmx         *os.File
	ptyIn        *ptyWriter // serialized writer to the PTY master (TDataIn + emulator replies)
	pgid         int        // agent process-group id (== agent pid; it leads its own group)
	graceTimeout time.Duration

	listener net.Listener
	exited   chan struct{} // closed when the agent process has been reaped

	mu      sync.Mutex
	curConn net.Conn // the connection currently being served, or nil
}

func newServer(l net.Listener, emu *vt.Emulator, tr *transcript.Writer, ptmx *os.File, pgid int, grace time.Duration, m *Metrics) *server {
	return &server{
		hub:          &hub{emu: emu, tr: tr, metrics: m},
		ptmx:         ptmx,
		ptyIn:        &ptyWriter{f: ptmx},
		pgid:         pgid,
		graceTimeout: grace,
		listener:     l,
		exited:       make(chan struct{}),
	}
}

// listen unlinks any stale socket, binds the UDS, and tightens its mode to 0600.
func listen(path string) (net.Listener, error) {
	_ = os.Remove(path) // clear a stale socket from a prior crash
	l, err := net.Listen("unix", path)
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

// acceptLoop serves connections one at a time until the listener is closed.
func (s *server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.serveConn(conn)
	}
}

// serveConn drives one client connection to completion: it reads frames and
// dispatches them, tearing down any active subscription when the connection
// ends.
func (s *server) serveConn(conn net.Conn) {
	s.mu.Lock()
	s.curConn = conn
	s.mu.Unlock()

	var sub *subscriber
	defer func() {
		if sub != nil {
			s.hub.detach(sub)
			<-sub.done
		}
		s.mu.Lock()
		s.curConn = nil
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
			switch ctrl.Type {
			case shimwire.TypeHello:
				writeControl(conn, shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
				if ctrl.WireVersion != shimwire.Version {
					return // close only this connection on version skew
				}
			case shimwire.TypeAttach:
				if sub != nil {
					s.hub.detach(sub)
					<-sub.done
				}
				sub = s.hub.attach(conn)
			case shimwire.TypeResize:
				s.resize(ctrl.Cols, ctrl.Rows)
			case shimwire.TypeSignal:
				s.onSignal(ctrl.Sig)
			}
		case wire.TDataIn:
			_, _ = s.ptyIn.Write(payload)
		}
	}
}

// resize propagates a new size to both the PTY kernel winsize (delivers SIGWINCH
// to the agent) and the emulator grid.
func (s *server) resize(cols, rows int) {
	_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	s.hub.emu.Resize(cols, rows)
}

// onSignal terminates the session process group. kill is immediate; term sends
// SIGTERM, then SIGKILL after the grace window unless the agent has already
// exited.
func (s *server) onSignal(sig string) {
	switch sig {
	case shimwire.SigKill:
		_ = syscall.Kill(-s.pgid, syscall.SIGKILL)
	case shimwire.SigTerm:
		go func() {
			_ = syscall.Kill(-s.pgid, syscall.SIGTERM)
			select {
			case <-s.exited:
			case <-time.After(s.graceTimeout):
				_ = syscall.Kill(-s.pgid, syscall.SIGKILL)
			}
		}()
	}
}

// shutdown flushes buffered DataOut to a connected client, emits the exit_report
// control after it, then tears down the socket. It is called once, after the
// agent has exited and the side-files are written.
func (s *server) shutdown(rep shimwire.Control) {
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

	s.listener.Close()
	s.mu.Lock()
	conn := s.curConn
	s.mu.Unlock()
	if conn != nil {
		conn.Close() // unblock a reader still parked on this connection
	}
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
// spawns the connection's writer goroutine: it sends exactly that snapshot
// first, then streams queued live frames, and emits the exit_report on a
// shutdown-triggered close.
func (h *hub) attach(conn net.Conn) *subscriber {
	h.mu.Lock()
	snap, _ := h.emu.Snapshot()
	sub := &subscriber{queue: make(chan []byte, subQueueCap), done: make(chan struct{})}
	h.sub = sub
	h.mu.Unlock()

	go func() {
		defer close(sub.done)
		if err := wire.WriteFrame(conn, wire.TSnapshot, snap); err != nil {
			h.drainQueue(sub)
			return
		}
		for data := range sub.queue {
			if err := wire.WriteFrame(conn, wire.TDataOut, data); err != nil {
				h.drainQueue(sub)
				break
			}
		}
		h.mu.Lock()
		shuttingDown := h.shutdown
		rep := h.exitReport
		h.mu.Unlock()
		if shuttingDown {
			writeControl(conn, rep)
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

// writeControl encodes and sends a shimwire.Control as a TControl frame,
// best-effort (a broken connection is handled by the reader path).
func writeControl(conn net.Conn, ctrl shimwire.Control) {
	b, err := shimwire.Encode(ctrl)
	if err != nil {
		return
	}
	_ = wire.WriteFrame(conn, wire.TControl, b)
}

// exitReport builds the exit_report control from the agent's exit outcome.
func exitReport(code int, signal string) shimwire.Control {
	c := code
	return shimwire.Control{Type: shimwire.TypeExitReport, ExitCode: &c, ExitSignal: signal}
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
