package shim

import (
	"errors"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/submitframe"
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
	// snapshotChunkMax is the largest snapshot slice carried in one TSnapshot frame
	// on the chunked shim->daemon hop (mirrors the daemon->client snapshotChunkSize):
	// a payload of MaxFrame-1 is the biggest wire.WriteFrame accepts.
	snapshotChunkMax = wire.MaxFrame - 1
	// vtParserLogInterval rate-limits diagnostics for a persistently malformed
	// terminal stream. Every contained fault is still counted in Metrics.
	vtParserLogInterval = 30 * time.Second
)

// testHookAfterPTYResize, when non-nil, runs after the PTY winsize change and
// before the emulator resize. It is a test-only seam for proving that hub.feed
// cannot interleave in that ordering window.
var testHookAfterPTYResize func()

// server owns the socket, the emulator/transcript pipeline, and the PTY master
// for one session. Connections are served CONCURRENTLY (one goroutine each), so a
// controller's held attach never blocks a fresh signal/hello connection (R1.3.2);
// the hub still couples the pipeline to at most one live subscriber (S10), so a
// later attach supersedes an earlier one.
type server struct {
	hub          *hub
	ptmx         *os.File
	ptyIn        *ptyWriter // serialized writer to the PTY master (TDataIn + emulator replies)
	graceTimeout time.Duration

	// pgidMu guards the two process groups this shim contains. The AGENT's group is known
	// at construction on the ordinary path and only after the go-ahead on a backend
	// session, so it is read under a lock rather than treated as immutable -- and a zero is
	// never signalled, because kill(-0, ...) signals the CALLER's own group.
	pgidMu sync.Mutex
	pgid   int // agent process-group id (== agent pid; it leads its own group)
	// backendPgid is the session backend's OWN group (Wave R7, ADR-013 §R7.2a). It is a
	// SIBLING of the agent, not a member of its group, so it is signalled beside it on the
	// same TERM->grace->KILL; there is no second timer and no second grace window.
	backendPgid int
	// pendingSig is the strongest termination signal observed so far (0: none). A kill
	// that lands during BACKEND STARTUP -- the window where BOTH pgids are still zero --
	// signals nothing (a zero pgid is rightly never signalled, below), and with the
	// production grace far shorter than the startup stages, the escalation worker's KILL
	// fires into the same empty pgids: without a memory, the backend and then the agent
	// would SPAWN AFTER their termination was requested and survive it. So every kill is
	// remembered here under pgidMu, and setAgentPgid/setBackendPgid REPLAY it the moment a
	// group is created. SIGKILL is sticky over SIGTERM: a group born after the one-shot
	// escalation worker already fired must get the escalated KILL, not the stale TERM.
	pendingSig syscall.Signal

	// goAhead carries the daemon's backend_attach (ADR-013 §R7.2e). Buffered so a daemon
	// that sends it before the shim blocks is never lost, and one-shot so a second one is
	// ignored rather than racing the spawn.
	goAhead     chan shimwire.Control
	goAheadOnce sync.Once

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
		goAhead:      make(chan shimwire.Control, 1),
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
		_ = l.Close()
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
			_ = conn.Close()
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
		_ = conn.Close()
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
				// Record whether the daemon advertised snapshot chunking (per-connection),
				// and advertise the shim's own support in the reply. Both are OPTIONAL hello
				// fields; a peer that sets neither degrades to the single-frame path (G-D).
				// cw.chunkSnapshot is written and read only in this read-loop goroutine
				// (hub.attach reads it), so it never races the attach writer goroutine.
				cw.chunkSnapshot = ctrl.SnapshotChunking
				cw.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, SnapshotChunking: true, SnapshotOnly: true, SubmitTransaction: true, ControlInput: true})
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
			case shimwire.TypeSnapshotReq:
				s.hub.snapshotOnly(cw)
			case shimwire.TypeResize:
				s.resize(ctrl.Cols, ctrl.Rows)
			case shimwire.TypeSignal:
				s.onSignal(ctrl.Sig)
			case shimwire.TypeSubmit:
				// One message, atomically, or nothing. The answer rides the same
				// connection so the caller can hold exactly one in flight.
				res := shimwire.Control{Type: shimwire.TypeSubmitResult}
				switch err := s.ptyIn.submitMessage([]byte(ctrl.Text), submitframe.Gap); {
				case err == nil:
				case errors.Is(err, errInputBusy):
					// A STABLE TOKEN, not prose: the daemon turns this into the
					// wire's own refusal code, and a sentence would make that a
					// string comparison against a message somebody may reword.
					res.Refused = shimwire.RefusedInputBusy
				default:
					res.Refused = err.Error()
				}
				cw.writeControl(res)
			case shimwire.TypeControlInput:
				// Daemon-authored keys (an interrupt, a dialog answer): the provenance
				// write. The bytes reach the PTY verbatim but do not mutate the owner-input
				// tracker; the frame they arrived on is the whole of that judgement.
				_, _ = s.ptyIn.Write([]byte(ctrl.Keys))
			case shimwire.TypeBackendAttach:
				// The daemon's GO-AHEAD (ADR-013 §R7.2e): it is a connected client of the
				// backend, and the agent may now be spawned with AgentArgs appended.
				s.noteGoAhead(ctrl)
			}
		case wire.TDataIn:
			if !helloed {
				continue // ignore input until the client has said hello
			}
			_, _ = s.ptyIn.WriteInput(payload)
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
	// The PTY resize delivers SIGWINCH and may immediately produce output. Hold
	// the hub serialization point until the emulator has the same dimensions so
	// drain cannot parse that output against the previous grid.
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	_ = setWinsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if testHookAfterPTYResize != nil {
		testHookAfterPTYResize()
	}
	s.hub.emu.Resize(cols, rows)
}

// setWinsize issues the TIOCSWINSZ ioctl that pty.Setsize would, but through
// f.SyscallConn().Control instead of pty.Setsize's own f.Fd(): Control routes
// through internal/poll's ref-counted fdMutex (the same guard that makes
// Read/Write safe against a concurrent Close), so this can never race a
// concurrent ptmx.Close() the way Fd() does, and once f is closed Control
// fails outright instead of ioctl'ing a possibly fd-reused descriptor. See
// close_resize_race_test.go.
func setWinsize(f *os.File, ws *pty.Winsize) error {
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr error
	if err := sc.Control(func(fd uintptr) {
		//nolint:gosec // unsafe pointer required for the ioctl syscall, mirrors pty.Setsize
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(ws)))
		if errno != 0 {
			ioctlErr = errno
		}
	}); err != nil {
		return err
	}
	return ioctlErr
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
		s.killGroups(syscall.SIGKILL)
	case shimwire.SigTerm:
		s.killGroups(syscall.SIGTERM)
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
		s.killGroups(syscall.SIGKILL)
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
	s.killGroups(syscall.SIGKILL)
}

// killGroups signals BOTH contained process groups: the agent's and, when this session has
// one, the backend's.
//
// THE BACKEND IS NOT IN THE AGENT'S GROUP and never was. It is a sibling exec.Cmd, so every
// kill(-agentPgid) misses it -- which is why the first R7 draft's claim that the backend
// "joins the agent's existing containment" had to be retracted. Signalling both here is what
// makes the claim true, on ONE grace window rather than two: the escalation worker and
// finishEscalation call this same function, so the backend cannot outlive the agent's KILL.
//
// A ZERO pgid is never signalled: kill(-0, sig) signals the caller's OWN process group, which
// on a backend session (where the agent's pgid is unknown until the go-ahead) would be the
// shim signalling itself.
func (s *server) killGroups(sig syscall.Signal) {
	s.pgidMu.Lock()
	agent, backend := s.pgid, s.backendPgid
	if s.pendingSig == 0 || sig == syscall.SIGKILL {
		s.pendingSig = sig // remembered for replay on a group created later; KILL is sticky
	}
	s.pgidMu.Unlock()
	if agent > 0 {
		_ = syscall.Kill(-agent, sig)
	}
	if backend > 0 && backend != agent {
		_ = syscall.Kill(-backend, sig)
	}
}

// setAgentPgid records the agent's group once it exists (the backend path spawns the agent
// after the go-ahead, so the group is not known at construction), REPLAYING any termination
// observed while it did not: a killed shim must never leave a newly-born group running. The
// read and the write share one pgidMu section, so this can never miss a concurrent kill --
// whichever of the two runs second sees the other's effect.
func (s *server) setAgentPgid(pgid int) {
	s.pgidMu.Lock()
	s.pgid = pgid
	replay := s.pendingSig
	s.pgidMu.Unlock()
	if replay != 0 && pgid > 0 {
		_ = syscall.Kill(-pgid, replay)
	}
}

// setBackendPgid records the session backend's group, replaying any termination observed
// before it existed (see setAgentPgid; the zero-pgid guard also skips the deliberate
// setBackendPgid(0) after containBackendFailure).
func (s *server) setBackendPgid(pgid int) {
	s.pgidMu.Lock()
	s.backendPgid = pgid
	replay := s.pendingSig
	s.pgidMu.Unlock()
	if replay != 0 && pgid > 0 {
		_ = syscall.Kill(-pgid, replay)
	}
}

// noteGoAhead delivers the daemon's backend_attach exactly once.
func (s *server) noteGoAhead(ctrl shimwire.Control) {
	s.goAheadOnce.Do(func() { s.goAhead <- ctrl })
}

// waitBackendGoAhead blocks until the daemon says go ahead, or the bound elapses.
//
// THE TIMEOUT IS WHAT KEEPS THE HANDSHAKE FROM BEING A NEW WAY TO HANG. A daemon that crashed
// between spawning this shim and dialing the backend must not leave the owner with a terminal
// that never starts, so a go-ahead that never arrives SPAWNS THE AGENT ANYWAY -- degraded (no
// AgentArgs, therefore no --remote) and logged.
func (s *server) waitBackendGoAhead(d time.Duration) ([]string, bool) {
	select {
	case ctrl := <-s.goAhead:
		return ctrl.AgentArgs, true
	case <-time.After(d):
		return nil, false
	}
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

	_ = s.listener.Close()
	// net.UnixListener unlinks the socket on Close; remove it explicitly too so
	// the session dir is left clean even if that ever changes (idempotent).
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}

	// 3. Close every connection to unblock its parked reader, then join every
	//    handler so Run never returns with a serveConn still running.
	for _, c := range conns {
		_ = c.Close()
	}
	s.handlers.Wait()
}

// hub couples the emulator/transcript pipeline to at most one live subscriber.
// Its mutex is the single serialization point: the drain loop publishes + feeds
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
// The subscriber publish and transcript hand-off happen BEFORE the emulator
// parse (R2.2.1/2.2): an attached client sees each raw chunk as soon as it
// arrives rather than waiting behind emu.Feed's parse cost, which the S9
// non-blocking drop-on-full send never depended on in the first place. This
// does not change per-chunk throughput — the drain loop still holds h.mu for
// the full duration of emu.Feed, so the ~3.7MB/s parse cap (R1.4.1(a))
// remains the pipeline's aggregate bottleneck (R2.2.5 explicitly does not
// decouple emu.Feed to its own goroutine); the reorder only removes parse
// latency from what publish waits on. Feeding under mu keeps the grid state
// and the published byte stream in lock step with attach's snapshot point:
// attach's snapshot+install and feed's publish+parse each run under one
// single h.mu hold, so per-chunk snapshot-inclusion XOR frame-delivery is
// preserved regardless of which half of feed the boundary falls in (S10).
// defer h.mu.Unlock() keeps every exit path from leaking the hub mutex;
// FeedChecked additionally contains upstream parser panics at the VT boundary.
func (h *hub) feed(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sub != nil {
		select {
		case h.sub.queue <- data:
		default:
			h.metrics.FramesDropped.Add(1)
		}
	}
	// data is a fresh per-read allocation (drain(), server.go) that no one
	// mutates: the subscriber path only ever reads it (wire.WriteFrame copies
	// into its own frame buffer before writing) and emu.Feed's parser also only
	// reads. WriteOwned's no-copy handoff is therefore safe here (R3.3.3).
	_, _ = h.tr.WriteOwned(data)
	if err := h.emu.FeedChecked(data); err != nil {
		n := h.metrics.VTParserFaults.Add(1)
		now := time.Now().UnixNano()
		last := h.metrics.vtLastLog.Load()
		if now-last >= int64(vtParserLogInterval) && h.metrics.vtLastLog.CompareAndSwap(last, now) {
			// S9: logging must never stall the PTY drain. The rate limiter bounds
			// this to one best-effort goroutine per interval.
			go log.Printf("shim: terminal parser fault contained (%d total): %v", n, err)
		}
	}
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
	// Read the per-connection chunking flag in the caller's (serveConn read-loop)
	// goroutine and hand a copy to the writer goroutine, so the writer never races a
	// later hello that might rewrite cw.chunkSnapshot.
	chunk := cw.chunkSnapshot
	h.mu.Lock()
	old := h.sub
	if old != nil {
		old.closeQueue() // supersede: terminate the prior writer, free h.sub
		h.sub = nil
	}
	snap, _ := h.emu.Snapshot()
	shuttingDown := h.shutdown
	rep := h.exitReport
	sub := &subscriber{queue: make(chan []byte, subQueueCap), done: make(chan struct{}), conn: cw.conn}
	if !shuttingDown {
		h.sub = sub
	}
	h.mu.Unlock()
	// An UNCOORDINATED supersede (a second connection attaching while the prior
	// subscriber's connection is still open) also closes the superseded CONNECTION,
	// outside h.mu: its peer sees prompt EOF instead of a silently-frozen stream,
	// and a writer wedged mid-Write on that socket is unblocked (R1.3.3 hardening,
	// C3 committee). The daemon-coordinated supersede already closed the old
	// connection before attaching anew, making this a no-op there; serveConn's own
	// deferred Close makes the double-close harmless.
	if old != nil && old.conn != nil {
		_ = old.conn.Close()
	}

	go func() {
		defer close(sub.done)
		if err := cw.sendSnapshot(snap, chunk); err != nil {
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

// snapshotOnly answers a TypeSnapshotReq: it snapshots the grid under h.mu and
// writes it to the requesting connection with the SAME encoding an attach uses
// (sendSnapshot: chunked iff this connection negotiated chunking) — and never
// touches h.sub, so it cannot supersede an attached controller no matter how it
// races an attach (the C3 tap-steal fix). Runs synchronously in the caller's
// serveConn read loop; the per-connection connWriter serializes its frames
// against any writer on the same connection.
func (h *hub) snapshotOnly(cw *connWriter) {
	chunk := cw.chunkSnapshot
	h.mu.Lock()
	snap, _ := h.emu.Snapshot()
	h.mu.Unlock()
	_ = cw.sendSnapshot(snap, chunk)
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

// subscriber is one attached client's outbound side: a bounded live-frame queue,
// a done signal for its writer goroutine, and the connection it writes to (so an
// uncoordinated supersede can close the superseded peer's connection — see
// hub.attach).
type subscriber struct {
	queue     chan []byte
	done      chan struct{}
	conn      net.Conn
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

// sendSnapshot writes the attach snapshot to the connection, first (S10 — before any
// live TDataOut the caller streams afterward). When the peer negotiated chunking it
// emits a snapshot_info preamble declaring the total length up front, then the
// snapshot as <= snapshotChunkMax TSnapshot chunk frames (an empty snapshot is the
// preamble alone, so the reader completes without waiting for a chunk). Otherwise it
// emits today's single TSnapshot frame — which wire.WriteFrame rejects past MaxFrame-1,
// so an oversized grid still fails for a non-chunking (old-daemon) peer, no worse than
// today (G-D). It returns the first write error so the caller can drain the queue.
func (w *connWriter) sendSnapshot(snap []byte, chunk bool) error {
	if !chunk {
		return w.writeFrame(wire.TSnapshot, snap)
	}
	body, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSnapshotInfo, SnapshotLen: len(snap)})
	if err != nil {
		return err
	}
	if err := w.writeFrame(wire.TControl, body); err != nil {
		return err
	}
	for off := 0; off < len(snap); off += snapshotChunkMax {
		end := off + snapshotChunkMax
		if end > len(snap) {
			end = len(snap)
		}
		if err := w.writeFrame(wire.TSnapshot, snap[off:end]); err != nil {
			return err
		}
	}
	return nil
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
//
// IT ALSO TRACKS THE LOGICAL INPUT LINE (Slice 0, agents-tracker-bzfe). The original byte
// count was safe for a draft that remained, but falsely latched after ordinary editing:
// Backspace, Delete and complete cursor navigation added bytes instead of removing/moving
// logical input. inputLineTracker models only editor operations whose effect is characterized
// from the input stream and marks provider-dependent word/Meta operations, history/completion,
// and lone/incomplete escape sequences unknown (busy).
// That lets submitMessage distinguish a deleted draft from a real remaining one without
// reading or guessing from the rendered grid. Emulator replies do not enter this path: they
// are the shim answering the agent's own queries, not somebody typing.
type ptyWriter struct {
	mu     sync.Mutex
	f      *os.File
	closed bool

	inputLine inputLineTracker
}

func (p *ptyWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writeLocked(b)
}

// WriteInput is Write for bytes somebody TYPED (TDataIn), which are the only bytes
// that dirty the input line. Daemon-authored control keys (shimwire.TypeControlInput)
// go through Write for the same reason emulator replies do: they are not somebody typing.
func (p *ptyWriter) WriteInput(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, err := p.writeLocked(b)
	p.inputLine.apply(b[:n])
	return n, err
}

func (p *ptyWriter) writeLocked(b []byte) (int, error) {
	if p.closed {
		return len(b), nil
	}
	return p.f.Write(b)
}

// errInputBusy is the shim's refusal: the input line is not clean, so this message
// cannot be delivered as a message.
var errInputBusy = errors.New("the session's input line was not empty")

// submitMessage writes one whole message -- text, the frame gap, then the carriage
// return -- under ONE hold of the PTY writer's lock, or writes nothing at all.
//
// THE HOLD ACROSS THE GAP IS THE POINT, not an oversight. submitframe.Gap exists so no
// downstream batching recompresses the text and its return into one PTY read tick; while
// it elapses, nothing else may reach this PTY, which is exactly what stops a second send
// (or the owner's own keystroke) landing between them. internal/skeleton/supervision.go
// records the same shape for the passive supervisor: typing a notification "holds the
// source's input serialization for at least submitframe.Gap". It is bounded at one gap,
// because a message is two frames and never more.
func (p *ptyWriter) submitMessage(text []byte, gap time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inputLine.dirty() {
		return errInputBusy
	}
	if _, err := p.writeLocked(text); err != nil {
		return err
	}
	time.Sleep(gap)
	if _, err := p.writeLocked([]byte{'\r'}); err != nil {
		// The text is in and the return is not. Count it as dirty, honestly: the line
		// now holds words nobody submitted, and the next submit must refuse rather
		// than run them together with its own.
		p.inputLine.apply(text)
		return err
	}
	p.inputLine.reset()
	return nil
}

func (p *ptyWriter) close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}
