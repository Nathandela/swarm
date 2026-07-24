package protocol

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/version"
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

// remoteForbiddenOptions is the hard-coded, value-aware launch-option denylist for the
// remote tier (R-POL.4): each guarded option key maps to its single forbidden value, so
// the safe default of the same key ("dangerously-skip-permissions"=="false",
// "sandbox"=="workspace-write") is still allowed. Config-free by design (slice 1b adds
// config).
var remoteForbiddenOptions = map[string]string{
	"dangerously-skip-permissions": "true",               // claude adapter full-access (claude.go:193)
	"sandbox":                      "danger-full-access", // codex adapter full-access (codex.go:89)
}

// daemonLaunchSpec builds the DaemonAPI launch spec from a validated request,
// applying the server-side env allowlist (S-6). Argv composition is the adapter's
// job (Epic 9), so it is left empty here. On the remote tier the client env is DROPPED
// entirely (R-POL.5): it is an unauthenticated channel (LaunchContentHash excludes Env),
// so filtering is not enough — it must not survive at all.
func daemonLaunchSpec(req *LaunchReq, remote bool) daemon.LaunchSpec {
	clientEnv := persist.FilterEnv(req.Env)
	if remote {
		clientEnv = nil // R-POL.5: remote launch carries no phone-supplied env
	}
	return daemon.LaunchSpec{
		AgentType:     req.Agent,
		Cwd:           req.Cwd,
		ClientEnv:     clientEnv,
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

	// journalSndBuf bounds the kernel send buffer (SO_SNDBUF) of a journal-subscribe
	// connection. Journal events are small, so with a default (large, OS-autotuned)
	// send buffer a subscriber that stops reading can have hundreds of KB of events
	// buffered in the kernel before its writer ever blocks — pinning kernel memory and
	// deferring eviction. A small bound caps per-subscriber kernel memory and makes a
	// wedged subscriber's writer block after a bounded volume, so its queue overflows
	// and the fan-out evicts it (S9/P-3). See handleJournalSubscribe.
	journalSndBuf = 4 << 10

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

// serverNowNS is the server-clock seam (mirroring pumpWriteTimeoutNS): when nonzero
// it fixes s.now() to that wall-clock instant (unix nanoseconds), so a test can
// freeze/advance the clock to drive control-session lazy expiry deterministically.
// Zero (the default) means the real time.Now().
var serverNowNS atomic.Int64

func (s *Server) now() time.Time {
	if ns := serverNowNS.Load(); ns > 0 {
		return time.Unix(0, ns)
	}
	return time.Now()
}

// maxControlSessionTTL is the server cap on a control-session lifetime (slice A5-b / A7 R7):
// the lifetime is the EARLIEST of the device-signed ExpiresAt, now+maxControlSessionTTL, and
// — when the caller sets one — now+TTLSeconds. The R5 lower-clamp keeps an overflowing
// TTLSeconds from wrapping to a past (immediately-expired) instant.
const maxControlSessionTTL = 30 * time.Minute

// serverCaps is the capability set the daemon supports; the handshake returns the
// intersection with the client's offer. The remote-tier caps are advertised
// unconditionally; a journal op still requires both the negotiated `journal` cap
// and a JournalBackend, and a remote mutating op is gated by the remote tier.
var serverCaps = []string{
	CapAttach, CapSubscribe,
	CapRemoteGateway, CapJournal, CapActivity, CapPolicy, CapPairing,
}

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

	// remoteTier marks a Server bound on the dedicated remote socket (ServeRemote):
	// every connection is unconditionally remote-origin, so every remote mutating op
	// must carry an operation_id (amendment D.0-A1/A4).
	remoteTier bool

	mu     sync.Mutex
	conns  map[*clientConn]struct{}
	leases map[string]*sessionLease // keyed by local session id
	closed bool

	subMu sync.Mutex
	subs  map[*clientConn]struct{}

	jsubMu sync.Mutex
	jsubs  map[*clientConn]struct{} // journal subscribers (fanned out separately)

	journalCancel func() // stops the JournalBackend subscription on Close (if any)

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
		jsubs:  make(map[*clientConn]struct{}),
		stop:   make(chan struct{}),
	}
	s.wg.Add(1)
	go s.fanoutLoop()
	// When the backend exposes a journal, drain its single source and fan journal
	// events out to journal subscribers (reusing the bounded-queue evict discipline).
	if jb, ok := d.(JournalBackend); ok {
		source, cancel := jb.JournalSubscribe()
		s.journalCancel = cancel
		s.wg.Add(1)
		go s.journalFanoutLoop(source)
	}
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
	if s.journalCancel != nil {
		s.journalCancel() // release the JournalBackend subscription
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

// journalFanoutLoop drains the single JournalBackend source and distributes each
// record to every journal subscriber via its bounded queue; a wedged subscriber is
// evicted, never allowed to block the loop (S9, mirrors fanoutLoop).
func (s *Server) journalFanoutLoop(source <-chan JournalRecord) {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case rec, ok := <-source:
			if !ok {
				return
			}
			s.distributeJournal(rec)
		}
	}
}

func (s *Server) distributeJournal(rec JournalRecord) {
	s.jsubMu.Lock()
	var dead []*clientConn
	for sc := range s.jsubs {
		// Encode HERE (in the fan-out), not in the writer, so one encoding is shared
		// across subscribers and the fan-out never blocks on a slow writer.
		body, err := EncodeControl(Control{Op: OpJournalEvent, EndpointID: sc.endpointID, Cursor: rec.Cursor, Journal: []JournalRecord{rec}})
		if err != nil {
			continue
		}
		select {
		case sc.jEventQ <- body:
		default:
			// Full queue: the subscriber is not draining its socket (its writer is
			// blocked on a full kernel buffer while eventQueueCap events backed up).
			// Evict it here, within the bound (S9/P-3), so a wedged subscriber never
			// grows the queue unboundedly nor blocks the fan-out. A draining subscriber
			// keeps its queue below the cap and is never evicted (mirrors distribute).
			dead = append(dead, sc)
		}
	}
	for _, sc := range dead {
		delete(s.jsubs, sc)
	}
	s.jsubMu.Unlock()
	for _, sc := range dead {
		sc.close()
	}
}

func (s *Server) removeConn(cc *clientConn) {
	s.mu.Lock()
	delete(s.conns, cc)
	s.mu.Unlock()
	s.subMu.Lock()
	delete(s.subs, cc)
	s.subMu.Unlock()
	s.jsubMu.Lock()
	delete(s.jsubs, cc)
	s.jsubMu.Unlock()
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

	// A REMOTE-tier controller (the phone, via take_control) gets OpLease + input acks
	// ONLY: its terminal view is the sealed daemon-rendered snapshot stream (Slices
	// C/D/E/F2), so raw output is suppressed — SnapshotLen 0, no TSnapshot chunks, no
	// live TDataOut. Frames are STILL drained below so end-of-session and lease
	// lifecycle are unchanged. The LOCAL (owner) tier path is byte-identical (A7/F3).
	suppress := s.remoteTier
	snapLen := len(snap)
	if suppress {
		snapLen = 0
	}
	// Lease grant carrying the snapshot's total length (for chunk reassembly),
	// then the snapshot chunk frames, BEFORE any live frame (S10/F2).
	body, err := EncodeControl(Control{
		Op:          OpLease,
		EndpointID:  cc.endpointID,
		SessionID:   NamespacedID(cc.endpointID, local),
		Generation:  gen,
		SnapshotLen: snapLen,
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
	if !suppress {
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
			if suppress {
				continue // remote tier: drain the frame (end detection intact) but send no raw output
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

	// journal subscription (separate bounded queue + writer, same evict discipline).
	// The queue carries PRE-ENCODED frame bodies: encoding happens in the fan-out
	// goroutine, not the writer, so the fan-out and writer run at balanced speeds and
	// a draining subscriber's writer keeps its queue below the cap (only a wedged one
	// overflows).
	jSubOnce sync.Once
	jEventQ  chan []byte

	// controller state (this conn as the controller of attSession)
	attMu      sync.Mutex
	attSession string
	attGen     uint64

	// pairing state: at most one owner-tier pairing in flight per connection
	// (handlePairStart). pair is guarded by pairMu; handlePairConfirm routes the
	// SAS-gate decision through it.
	pairMu sync.Mutex
	pair   *pairSession

	// remote-control lease state (slice A5-a): the session this connection took
	// control of via take_control and the lease generation attach assigned it.
	// Guarded by ctlMu, mirroring pairMu/pair. A5-b adds input forwarding under this
	// lease and A5-c binds a single-use gate token; both extend controlSession then.
	ctlMu   sync.Mutex
	control *controlSession

	closeOnce sync.Once
	done      chan struct{}
}

// controlSession records an established take_control lease: the target local session
// id, the lease generation s.attach assigned (published via setAttach), and the
// server-clock instant at which the session lazily expires (slice A5-b). Its fields are
// set once at establishment and never mutated, so the input gate can capture the struct
// under ctlMu and read them after releasing the lock. Slice A5-c adds the gate token.
type controlSession struct {
	target   string
	leaseGen uint64
	expiry   time.Time
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
	case OpJournalRead:
		cc.handleJournalRead(c)
	case OpJournalSubscribe:
		cc.handleJournalSubscribe()
	case OpDeviceList:
		cc.handleDeviceList()
	case OpPolicyQuery:
		cc.handlePolicyQuery()
	case OpDeviceRevoke:
		cc.handleDeviceRevoke(c)
	case OpTakeControl:
		cc.handleTakeControl(c)
	case OpTakeControlEnd:
		cc.handleTakeControlEnd(c)
	case OpPairStart:
		cc.handlePairStart(c)
	case OpPairConfirm:
		cc.handlePairConfirm(c)
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
		BuildVersion:    version.Version,
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
	// R-POL.9: launch has no pre-existing session, so it is signed over the reserved
	// LaunchSessionSentinel, and its spec is bound via LaunchContentHash so a gateway
	// cannot alter the agent/cwd/options/prompt of a validly-signed launch.
	if !cc.requireRemoteAuthz(c, ActionLaunch, LaunchSessionSentinel, LaunchContentHash(req)) {
		return
	}
	// R-POL.4/.2: on the remote tier refuse a dangerous option (value-aware, hard-coded)
	// AFTER authz but BEFORE argv/cwd validation, so the policy refusal precedes the cwd
	// stat and produces no daemon side effect.
	if cc.srv.remoteTier {
		for k, v := range req.Options {
			if forbidden, ok := remoteForbiddenOptions[k]; ok && forbidden == v {
				cc.replyErrorCode("launch: option "+strconv.Quote(k)+"="+strconv.Quote(v)+" not permitted on the remote tier", CodePolicy)
				return
			}
		}
	}
	// R-POL.3/.2: on the remote tier, when the backend exposes a LaunchPolicy, confine the
	// launch to its machine-configured cwd roots. Resolve the cwd (symlink-hardened) HERE so
	// the RESOLVED real path is what the policy checks — a symlink textually under a root but
	// resolving outside it is refused — and do it AFTER authz/denylist but BEFORE the cwd
	// stat / any side effect. An unresolvable cwd (e.g. nonexistent) is refused CodePolicy.
	if cc.srv.remoteTier {
		if lp, ok := cc.launchPolicy(); ok {
			resolved, err := filepath.EvalSymlinks(req.Cwd)
			if err != nil {
				cc.replyErrorCode("launch: cwd is not a resolvable directory on the remote tier", CodePolicy)
				return
			}
			if err := lp.RemoteLaunchAllowed(resolved); err != nil {
				cc.replyErrorCode("launch: "+err.Error(), CodePolicy)
				return
			}
		}
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
	spec := daemonLaunchSpec(req, cc.srv.remoteTier)
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
	if !cc.requireRemoteAuthz(c, ActionKill, c.SessionID, nil) {
		return
	}
	// Replay-safe when the backend is an IdempotentExecutor (DHI-3): claim the
	// operation_id AFTER authz. A replay (existed) replies the CACHED outcome WITHOUT
	// re-executing Kill, so a captured remote kill cannot double-fire the side effect.
	// REMOTE-tier ONLY: owner-tier local calls carry no operation_id and must bypass the
	// claim (which rejects an empty operation_id); requireOperationID has already ensured
	// a non-empty operation_id on the remote tier.
	if exec, ok := cc.srv.d.(IdempotentExecutor); ok && cc.srv.remoteTier {
		existed, priorOK, err := exec.ClaimIdempotentOp(c.OperationID, ActionKill, local)
		if err != nil {
			cc.replyError("kill: " + err.Error())
			return
		}
		if existed {
			if priorOK {
				cc.replyOK(c.SessionID)
			} else {
				cc.replyError("kill: prior attempt failed")
			}
			return
		}
		kerr := cc.srv.d.Kill(local)
		_ = exec.CommitIdempotentOp(c.OperationID, kerr == nil)
		if kerr != nil {
			cc.replyError("kill: " + kerr.Error())
			return
		}
		cc.replyOK(c.SessionID)
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
	if !cc.requireRemoteAuthz(c, ActionDelete, c.SessionID, nil) {
		return
	}
	// Replay-safe when the backend is an IdempotentExecutor (DHI-3): claim the
	// operation_id AFTER authz. A replay (existed) replies the CACHED outcome WITHOUT
	// re-executing Delete, so a captured remote delete cannot append a duplicate
	// tombstone or re-fire OnSessionEnd.
	// REMOTE-tier ONLY: owner-tier local calls carry no operation_id and must bypass the
	// claim (which rejects an empty operation_id); requireOperationID has already ensured
	// a non-empty operation_id on the remote tier.
	if exec, ok := cc.srv.d.(IdempotentExecutor); ok && cc.srv.remoteTier {
		existed, priorOK, err := exec.ClaimIdempotentOp(c.OperationID, ActionDelete, local)
		if err != nil {
			cc.replyError("delete: " + err.Error())
			return
		}
		if existed {
			if priorOK {
				cc.replyOK(c.SessionID)
			} else {
				cc.replyError("delete: prior attempt failed")
			}
			return
		}
		derr := cc.srv.d.Delete(local)
		_ = exec.CommitIdempotentOp(c.OperationID, derr == nil)
		if derr != nil {
			cc.replyError("delete: " + derr.Error())
			return
		}
		cc.srv.dropLease(local) // bound s.leases growth: drop the deleted session's lease (F13)
		cc.replyOK(c.SessionID)
		return
	}
	if err := cc.srv.d.Delete(local); err != nil {
		cc.replyError("delete: " + err.Error())
		return
	}
	cc.srv.dropLease(local) // bound s.leases growth: drop the deleted session's lease (F13)
	cc.replyOK(c.SessionID)
}

// handleDeviceRevoke serves device_revoke (slice A3.2): removes a paired device from
// the daemon's device registry. The resource being acted on is the TARGET device
// (c.TargetDeviceID), NOT the caller's own authenticating device (c.DeviceID) --
// passing TargetDeviceID as requireRemoteAuthz's resource means the caller's
// signature binds the target, so a device can revoke another device, not just
// itself (see remote_devicerevoke_test.go's field-collision guard).
//
// KNOWN GAPS (out of scope for A3.2, tracked for later slices): (a) this removes
// only the daemon-side device.Registry entry -- it does NOT purge the relay-side
// registration/mailbox (atomic-revoke-closes-live-socket is A6/ME-1); (b)
// device_revoke maps to ActionControl (deviceauth.go actionClass), so any CapFull
// device can revoke any other device -- there is no separate admin tier yet.
func (cc *clientConn) handleDeviceRevoke(c Control) {
	dr, ok := cc.srv.d.(DeviceRevoker)
	if !ok {
		cc.replyError("device_revoke not supported by this daemon")
		return
	}
	if !cc.requireRemoteAuthz(c, ActionDeviceRevoke, c.TargetDeviceID, nil) {
		return
	}
	if _, err := dr.RevokeDevice(c.TargetDeviceID); err != nil {
		cc.replyError("device_revoke: " + err.Error())
		return
	}
	cc.replyOK(c.TargetDeviceID)
}

func (cc *clientConn) handleAttach(c Control) {
	// Fail closed on the remote tier: no signed take_control gate exists yet, so
	// interactive control (lease acquisition) is refused before any session is
	// resolved or lease established (HIGH-2 / A4-R).
	if cc.srv.remoteTier {
		cc.replyErrorCode("interactive control not permitted on the remote tier (take_control not yet implemented)", CodeNotAuthorized)
		return
	}
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

// handleTakeControl serves the signed take_control op (slice A5-a) — the ONLY
// remote-tier path that acquires a controller lease. It mirrors handleKill's
// authorization (the SAME requireRemoteAuthz choke point every remote mutating op
// uses: kill switch first, then operation_id + DeviceAuthenticator) and, only once
// authorized, handleAttach's lease establishment (the SAME s.attach path the owner
// tier uses). On an authenticator refusal requireRemoteAuthz has already replied, so
// we return WITHOUT attaching — no lease may open on refusal. On success the pump's
// OpLease grant is the observed reply (no extra reply here, exactly like handleAttach).
// SCOPE: establishment + authz. Input forwarding under this lease
// (OpDataIn/OpResize) is slice A5-b.
//
// Slice A5-c binds a one-shot, "biometric-attested" gate token into the device
// signature and makes the operation_id single-use. Both properties require the durable
// idempotency store, so the whole mechanism engages ONLY when the backend implements
// OperationClaimer (the production coreAPI always does); a bare stub keeps the A5-a/A5-b
// establishment path unchanged. When engaged, the order is: present-check (an absent
// token can never gate control, even though SHA256("") is a valid 32-byte hash) ->
// requireRemoteAuthz with content_hash = SHA256(GateToken) (so a relay that swaps the
// wire token breaks the signature, exactly as launch binds its spec) -> single-use claim
// AFTER authz (so an unauthenticated caller cannot flood the durable log) -> attach.
func (cc *clientConn) handleTakeControl(c Control) {
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	// The gate-token/single-use mechanism is coupled to the durable store: single-use is
	// unenforceable without it, so it engages only when the backend is an OperationClaimer.
	claimer, gated := cc.srv.d.(OperationClaimer)
	var contentHash []byte
	if gated {
		// Present-check: refuse an empty one-shot token before authz. A hash-only check
		// would wrongly accept it because SHA256("") is a valid 32-byte hash (A5-c).
		if c.GateToken == "" {
			cc.replyErrorCode("take_control requires a gate token", CodeInvalidField)
			return
		}
		// Bind the gate token into the signed tuple via content_hash. The daemon
		// recomputes SHA256(wire GateToken); a swapped token yields a different hash, so
		// the device signature (which covers it) fails to verify (anti-tamper).
		h := sha256.Sum256([]byte(c.GateToken))
		contentHash = h[:]
	}
	if !cc.requireRemoteAuthz(c, ActionTakeControl, c.SessionID, contentHash) {
		return
	}
	// Single-use: claim the operation_id AFTER authz. A duplicate (existed) is a REPLAY —
	// refuse with NO attach, so a captured take_control cannot open a second lease. Unlike
	// launch, take_control is never redriven; a consumed operation_id stays consumed.
	if gated {
		existed, err := claimer.ClaimOperation(c.OperationID, ActionTakeControl, local)
		if err != nil || existed {
			cc.replyErrorCode("take_control operation_id already used", CodeStaleApproval)
			return
		}
	}
	// A second lease on this connection auto-detaches the first (mirror handleAttach),
	// so one connection never holds two leases or cross-routes data (F7).
	cc.attMu.Lock()
	prev, prevGen := cc.attSession, cc.attGen
	cc.attMu.Unlock()
	if prev != "" && prev != local {
		cc.srv.releaseLease(cc, prev, prevGen, false, true)
	}
	if err := cc.srv.attach(cc, local); err != nil {
		cc.replyError("take_control: " + err.Error())
		return
	}
	// Record the controller session at the generation attach assigned (attach publishes
	// it via setAttach -> cc.attGen), stamping its lazy-expiry deadline from the caller's
	// requested TTL clamped to the server bounds (never immediately-expired nor unbounded).
	cc.attMu.Lock()
	gen := cc.attGen
	cc.attMu.Unlock()
	// Bind the control-session lifetime to the EARLIEST of three bounds so it can never
	// outlive what the device SIGNED nor the server cap (R7): the server maximum
	// (now+maxControlSessionTTL); the signed command ExpiresAt (always present on the remote
	// tier — requireRemoteAuthz refuses a nil expires_at and verifies *c.ExpiresAt against the
	// signature, so it is device-authenticated, not a relay-forgeable hint); and, when the
	// caller requested one, now+TTLSeconds (the A5-b hint).
	now := cc.srv.now()
	expiry := now.Add(maxControlSessionTTL)
	if c.TTLSeconds > 0 {
		ttl := time.Duration(c.TTLSeconds) * time.Second
		// ttl <= 0 catches an int64 overflow (a huge TTLSeconds wraps the ns multiply to a
		// NEGATIVE duration): an absurdly large request clamps to the server maximum, never
		// to a past expiry, so the session is never immediately-expired (R5).
		if ttl <= 0 || ttl > maxControlSessionTTL {
			ttl = maxControlSessionTTL
		}
		if t := now.Add(ttl); t.Before(expiry) {
			expiry = t
		}
	}
	if c.ExpiresAt != nil && c.ExpiresAt.Before(expiry) {
		expiry = *c.ExpiresAt
	}
	cc.ctlMu.Lock()
	cc.control = &controlSession{target: local, leaseGen: gen, expiry: expiry}
	cc.ctlMu.Unlock()
}

// handleTakeControlEnd serves take_control_end (slice A5-b): the caller-scoped teardown
// of its OWN control session. It clears cc.control (shutting the input gate — clause 2
// fail-closed once cc.control is nil) and releases the caller's lease using the session
// + generation it carries, mirroring handleDetach. No device signature is required: a
// caller can only end a session it already holds, and releaseLease's controller==cc +
// generation match is the gate (a delayed old-generation end cannot release a later
// controller's lease, F11).
func (cc *clientConn) handleTakeControlEnd(c Control) {
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	// Shut the input gate ONLY when the end identifies the CURRENT control session (same
	// target + generation). A STALE end carrying an OLD generation (reordered by the
	// untrusted relay) targets a superseded lease, so it must leave the live, newer control
	// session intact: releaseLease already refuses the release on a generation mismatch
	// (F11), and this makes the input-gate side agree so a replayed end can never shut a
	// newer session's keystrokes (R3).
	cc.ctlMu.Lock()
	if cc.control != nil && cc.control.target == local && cc.control.leaseGen == c.Generation {
		cc.control = nil
	}
	cc.ctlMu.Unlock()
	cc.srv.releaseLease(cc, local, c.Generation, true, true)
}

// controlGateOpen is the slice A5-b four-clause gate: on the remote tier a keystroke or
// resize reaches the shim ONLY inside a live, authorized control session. Every clause
// must hold: (1) the kill switch is still ON (re-checked here so a mid-session `off`
// halts input), (2) a control session exists (fail-closed default), (3) it has not
// lazily expired on the server clock, and (4) it still targets this connection's current
// lease (session + generation). Any clause false => drop. It captures the control-session
// fields under ctlMu and the lease identity under attMu, releasing each lock before the
// caller forwards, so ctlMu is never held across the lease locks forwardInput takes.
func (cc *clientConn) controlGateOpen() bool {
	// clause 1 — re-check the kill switch on every keystroke.
	if ks, ok := cc.killSwitch(); ok && !ks.RemoteControlEnabled() {
		return false
	}
	// clause 2 — fail-closed default: capture the (immutable) control session, release ctlMu.
	cc.ctlMu.Lock()
	ctl := cc.control
	cc.ctlMu.Unlock()
	if ctl == nil {
		return false
	}
	// clause 3 — lazy expiry on the server clock.
	if !cc.srv.now().Before(ctl.expiry) {
		return false
	}
	// clause 4 — still bound to this connection's current lease (session + generation).
	cc.attMu.Lock()
	sess, gen := cc.attSession, cc.attGen
	cc.attMu.Unlock()
	return ctl.target == sess && ctl.leaseGen == gen
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
	// Remote tier (slice A5-b): a resize reaches the shim ONLY inside a live, authorized
	// control session (the four-clause gate); any out-of-session resize is dropped. On the
	// remote tier the resize is forwarded on the SAME server-tracked lease identity the gate
	// validated (cc.attSession/cc.attGen), mirroring handleDataIn, so the gated identity and
	// the forwarded identity are identical rather than the (potentially divergent) wire
	// session/generation (R4). The owner tier keeps full interactive trust and its original
	// wire-addressed behavior — the gate applies only on the remote tier. Resize is
	// fire-and-forget, so no reply either way.
	if cc.srv.remoteTier {
		if !cc.controlGateOpen() {
			return
		}
		cc.attMu.Lock()
		local, gen := cc.attSession, cc.attGen
		cc.attMu.Unlock()
		if local == "" {
			return
		}
		cc.srv.forwardResize(cc, local, gen, c.Cols, c.Rows)
		return
	}
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

// handleJournalRead serves journal_read(from_cursor): it requires the `journal`
// capability negotiated and a JournalBackend, then returns the snapshot+range from
// the cursor (atomic per R-JRN.4) with the boundary cursor and full-resync flag.
func (cc *clientConn) handleJournalRead(c Control) {
	jb, ok := cc.journalBackend()
	if !ok {
		return
	}
	res, err := jb.JournalReadFrom(c.Cursor)
	if err != nil {
		cc.replyError("journal_read: " + err.Error())
		return
	}
	_ = cc.writeControl(Control{
		Op:         OpJournalRead,
		EndpointID: cc.endpointID,
		Cursor:     res.Cursor,
		Journal:    res.Events,
		Roster:     res.Roster,
		FullResync: res.FullResync,
	})
}

// handleJournalSubscribe registers a journal subscriber (journal-capable backend +
// negotiated `journal` cap) and starts its bounded-queue writer, then acks. Journal
// events stream as journal_event frames via the journal fan-out.
func (cc *clientConn) handleJournalSubscribe() {
	if _, ok := cc.journalBackend(); !ok {
		return
	}
	cc.jSubOnce.Do(func() {
		// Bound this subscribe connection's kernel send buffer (see journalSndBuf):
		// caps per-subscriber kernel memory and makes a wedged subscriber block (and
		// be evicted) after a bounded volume. Best-effort — a conn without a settable
		// buffer keeps its default.
		if uc, ok := cc.conn.(interface{ SetWriteBuffer(int) error }); ok {
			_ = uc.SetWriteBuffer(journalSndBuf)
		}
		cc.jEventQ = make(chan []byte, eventQueueCap)
		cc.srv.wg.Add(1)
		go cc.journalWriter()
		cc.srv.jsubMu.Lock()
		cc.srv.jsubs[cc] = struct{}{}
		cc.srv.jsubMu.Unlock()
	})
	cc.replyOK("")
}

// journalBackend returns the backend's JournalBackend if journal ops are available
// to this connection (negotiated `journal` cap AND a journal-capable backend),
// replying with an error refusal otherwise (R-PROT.1: an unnegotiated op is refused).
func (cc *clientConn) journalBackend() (JournalBackend, bool) {
	if !cc.hasCap(CapJournal) {
		cc.replyError("journal capability not negotiated")
		return nil, false
	}
	jb, ok := cc.srv.d.(JournalBackend)
	if !ok {
		cc.replyError("journal not supported by this daemon")
		return nil, false
	}
	return jb, true
}

// handleDeviceList serves device_list (slice A3.1): the backend's full
// paired-device roster (R-DEV.1). It is a READ, so no requireRemoteAuthz gate
// applies — only the negotiated `pairing` capability plus a DeviceLister backend.
func (cc *clientConn) handleDeviceList() {
	dl, ok := cc.deviceLister()
	if !ok {
		return
	}
	_ = cc.writeControl(Control{Op: OpDeviceList, EndpointID: cc.endpointID, Devices: dl.ListDevices()})
}

// deviceLister returns the backend's DeviceLister if device_list is available to
// this connection (negotiated `pairing` cap AND a device-listing backend),
// replying with an error refusal otherwise (mirrors journalBackend()).
func (cc *clientConn) deviceLister() (DeviceLister, bool) {
	if !cc.hasCap(CapPairing) {
		cc.replyError("pairing capability not negotiated")
		return nil, false
	}
	dl, ok := cc.srv.d.(DeviceLister)
	if !ok {
		cc.replyError("device_list not supported by this daemon")
		return nil, false
	}
	return dl, true
}

// handlePairStart serves the owner-tier pair_start (slice A3.3-bc, ADR-007
// amendment "Pairing host: Option A"). Gate order is load-bearing: pairing is
// owner-tier only, so a remote-tier connection is refused not_authorized BEFORE the
// host is ever consulted (mirrors handleAttach's remote-tier refusal); then the
// negotiated `pairing` cap is required; then the backend must implement PairingHost.
//
// It drives the anti-MITM SAS gate fail-closed: the pairing ctx is derived from the
// CONNECTION lifetime, so a disconnect cancels it and the in-flight confirm returns
// a non-nil error (a decline) rather than hanging. BeginPairing returns the PairView
// synchronously (replied as pair_start) and runs the handshake in a background
// goroutine that calls confirm at the SAS gate (pushed as pair_pending, blocking for
// the matching pair_confirm or ctx cancel) and result at the terminal outcome
// (pushed as pair_result). Only ONE pairing is in flight per connection.
func (cc *clientConn) handlePairStart(c Control) {
	// Owner-tier only: fail closed on the remote tier before consulting the host
	// (mirrors handleAttach's remote-tier refusal).
	if cc.srv.remoteTier {
		cc.replyErrorCode("pairing is owner-tier only", CodeNotAuthorized)
		return
	}
	if !cc.hasCap(CapPairing) {
		cc.replyError("pairing capability not negotiated")
		return
	}
	host, ok := cc.srv.d.(PairingHost)
	if !ok {
		cc.replyError("pairing not supported by this daemon")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	ps := &pairSession{confirm: make(chan bool, 1), cancel: cancel}

	cc.pairMu.Lock()
	if cc.pair != nil { // one pairing in flight per connection
		cc.pairMu.Unlock()
		cancel()
		cc.replyError("pairing already in progress")
		return
	}
	cc.pair = ps
	cc.pairMu.Unlock()

	// Fail-closed wiring: a dropped connection closes cc.done, which cancels the
	// pairing ctx so an in-flight confirm returns ctx.Err() (a decline). The
	// goroutine also exits when the pairing ends normally (result -> cancel).
	go func() {
		select {
		case <-cc.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	var req PairStartReq
	if c.Pairing != nil {
		req = PairStartReq{Capability: c.Pairing.Capability, TTLSeconds: c.Pairing.TTLSeconds}
	}

	// confirm pushes the SAS gate (pair_pending) to THIS connection, then blocks for
	// the matching pair_confirm — or, if the connection drops, ctx cancel makes it
	// fail closed (false, non-nil err). Writes use the connection's thread-safe path.
	confirm := func(sas []string, deviceName string) (bool, error) {
		ps.mu.Lock()
		rvz := ps.rvz
		ps.mu.Unlock()
		_ = cc.writeControl(Control{Op: OpPairPending, EndpointID: cc.endpointID,
			Pairing: &PairingControl{SAS: sas, DeviceName: deviceName, RendezvousID: rvz}})
		select {
		case allow := <-ps.confirm:
			return allow, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	// result pushes the terminal outcome (pair_result) and ends the pairing: success
	// carries the device identity; failure carries no device (nil Pairing).
	result := func(r PairResult) {
		cc.clearPairing(ps)
		var p *PairingControl
		if r.Err == nil {
			p = &PairingControl{DeviceID: r.DeviceID, Name: r.Name, Capability: r.Capability}
		}
		_ = cc.writeControl(Control{Op: OpPairResult, EndpointID: cc.endpointID, Pairing: p})
	}

	view, err := host.BeginPairing(ctx, req, confirm, result)
	if err != nil {
		cc.clearPairing(ps)
		cc.replyError("pair_start: " + err.Error())
		return
	}
	ps.mu.Lock()
	ps.rvz = view.RendezvousID
	ps.mu.Unlock()
	// The pair_start reply and the pair_pending push race (this goroutine vs the
	// host's background goroutine); both go through writeMu, so they are serialized
	// and the test classifies the two frames by Op.
	_ = cc.writeControl(Control{Op: OpPairStart, EndpointID: cc.endpointID,
		Pairing: &PairingControl{QR: view.QR, RendezvousID: view.RendezvousID, ExpiresAt: view.ExpiresAt}})
}

// handlePairConfirm routes a pair_confirm's decision to this connection's in-flight
// pairing's blocked confirm closure (a cap-1 channel, non-blocking send). No pairing
// in flight -> error.
func (cc *clientConn) handlePairConfirm(c Control) {
	cc.pairMu.Lock()
	ps := cc.pair
	cc.pairMu.Unlock()
	if ps == nil {
		cc.replyError("no pairing in flight")
		return
	}
	allow := c.Pairing != nil && c.Pairing.Allow
	select {
	case ps.confirm <- allow:
	default: // already decided/cancelled: drop the duplicate
	}
}

// clearPairing releases this connection's in-flight pairing slot (if ps still holds
// it) and cancels its ctx, so the connection-lifetime canceller goroutine exits.
func (cc *clientConn) clearPairing(ps *pairSession) {
	cc.pairMu.Lock()
	if cc.pair == ps {
		cc.pair = nil
	}
	cc.pairMu.Unlock()
	ps.cancel()
}

// handlePolicyQuery serves policy_query (slice A3.1): the backend's configured
// remote launch policy (allowed cwd roots, R-POL.3). It is a READ, so no
// requireRemoteAuthz gate applies — only the negotiated `policy` capability plus a
// PolicyDescriber backend.
func (cc *clientConn) handlePolicyQuery() {
	pd, ok := cc.policyDescriber()
	if !ok {
		return
	}
	pv := pd.DescribePolicy()
	_ = cc.writeControl(Control{Op: OpPolicyQuery, EndpointID: cc.endpointID, Policy: &pv})
}

// policyDescriber returns the backend's PolicyDescriber if policy_query is
// available to this connection (negotiated `policy` cap AND a policy-describing
// backend), replying with an error refusal otherwise (mirrors journalBackend()).
func (cc *clientConn) policyDescriber() (PolicyDescriber, bool) {
	if !cc.hasCap(CapPolicy) {
		cc.replyError("policy capability not negotiated")
		return nil, false
	}
	pd, ok := cc.srv.d.(PolicyDescriber)
	if !ok {
		cc.replyError("policy_query not supported by this daemon")
		return nil, false
	}
	return pd, true
}

// journalWriter drains this subscriber's bounded journal queue (pre-encoded frame
// bodies) to the socket. A wedged subscriber (one not draining its socket) blocks
// the writer on a full kernel buffer, backs its queue up to eventQueueCap, and is
// evicted by the fan-out on the next overflow; the close then unblocks this write
// with an error and the goroutine exits.
func (cc *clientConn) journalWriter() {
	defer cc.srv.wg.Done()
	for {
		select {
		case <-cc.done:
			return
		case body := <-cc.jEventQ:
			if err := cc.writeFrame(wire.TControl, body); err != nil {
				cc.close()
				return
			}
		}
	}
}

func (cc *clientConn) handleDataIn(payload []byte) {
	// Remote tier (slice A5-b): a raw input frame reaches the shim ONLY inside a live,
	// authorized control session (the four-clause gate) — this is the keystroke-injection
	// vector, so every out-of-session frame is dropped. The owner tier keeps full
	// interactive trust — the gate applies only on the remote tier, so the path below is
	// unchanged there.
	if cc.srv.remoteTier && !cc.controlGateOpen() {
		return
	}
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

// replyErrorCode is replyError carrying a machine-readable refusal code (R-PROT.7).
func (cc *clientConn) replyErrorCode(msg string, code ErrorCode) {
	_ = cc.writeControl(Control{Op: OpError, EndpointID: cc.endpointID, Error: msg, ErrorCode: code})
}

func (cc *clientConn) replyOK(sessionID string) {
	_ = cc.writeControl(Control{Op: OpOK, EndpointID: cc.endpointID, SessionID: sessionID})
}

// hasCap reports whether cap was negotiated for this connection.
func (cc *clientConn) hasCap(cap string) bool {
	for _, c := range cc.caps {
		if c == cap {
			return true
		}
	}
	return false
}

// requireOperationID enforces the remote-tier rule that every remote mutating op
// carries an operation_id (R-IDP.1/A4): on the remote tier a missing operation_id is
// refused with invalid_field before any action. On the main (owner) tier it is a
// no-op. Returns false when the caller must stop (already replied).
func (cc *clientConn) requireOperationID(c Control) bool {
	if cc.srv.remoteTier && c.OperationID == "" {
		cc.replyErrorCode("remote mutating op requires operation_id", CodeInvalidField)
		return false
	}
	return true
}

// deviceAuthenticator returns the backend's DeviceAuthenticator if it implements one.
func (cc *clientConn) deviceAuthenticator() (DeviceAuthenticator, bool) {
	da, ok := cc.srv.d.(DeviceAuthenticator)
	return da, ok
}

// killSwitch returns the backend's KillSwitch if it implements one.
func (cc *clientConn) killSwitch() (KillSwitch, bool) {
	ks, ok := cc.srv.d.(KillSwitch)
	return ks, ok
}

// launchPolicy returns the backend's LaunchPolicy if it implements one (R-POL.3).
func (cc *clientConn) launchPolicy() (LaunchPolicy, bool) {
	lp, ok := cc.srv.d.(LaunchPolicy)
	return lp, ok
}

// requireRemoteAuthz is the single choke point for a remote mutating op (R-POL.9): it
// gates launch/kill/delete before any side effect. On the owner (main) tier it is a
// no-op — local connections keep full trust (R-POL.1). On the remote tier it enforces,
// in order: operation_id present (R-IDP.1); the backend exposes a DeviceAuthenticator
// (else FAIL CLOSED — a misassembled remote server authorizes nothing); the device
// identity fields (device_id, device_sig, expires_at) are present; and finally the
// authenticator verifies the signature over the canonical tuple AND the device's
// capability permits action. A missing structural field is invalid_field; any
// authorization failure is not_authorized. Returns false when the caller must stop
// (a refusal has already been sent). `session` is the namespaced session id, empty
// for launch (which creates a session). contentHash optionally binds op content.
func (cc *clientConn) requireRemoteAuthz(c Control, action string, session string, contentHash []byte) bool {
	if !cc.srv.remoteTier {
		return true
	}
	// Kill switch (R-KS.1): fail closed BEFORE any authz work — a valid device signature
	// must not bypass a disabled remote-control switch.
	if ks, ok := cc.killSwitch(); ok && !ks.RemoteControlEnabled() {
		cc.replyErrorCode("remote control is disabled (kill switch off)", CodeKillSwitch)
		return false
	}
	if !cc.requireOperationID(c) {
		return false
	}
	auth, ok := cc.deviceAuthenticator()
	if !ok {
		cc.replyErrorCode("remote authorization unavailable", CodeNotAuthorized)
		return false
	}
	if c.DeviceID == "" || c.DeviceSig == "" || c.ExpiresAt == nil {
		cc.replyErrorCode("remote mutating op requires device_id, device_sig, and expires_at", CodeInvalidField)
		return false
	}
	if err := auth.AuthorizeCommand(DeviceCommandAuth{
		DeviceID:    c.DeviceID,
		Action:      action,
		Machine:     cc.endpointID,
		Session:     session,
		OperationID: c.OperationID,
		ExpiresAt:   *c.ExpiresAt,
		ContentHash: contentHash,
		Sig:         c.DeviceSig,
	}); err != nil {
		cc.replyErrorCode("device command not authorized", CodeNotAuthorized)
		return false
	}
	return true
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
