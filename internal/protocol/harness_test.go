// Package protocol is the Epic 6 client<->daemon control message set: THE
// low-reversibility wire surface (ADR-002) that Epic 7 (TUI) and Epic 8 (attach)
// consume. It layers a versioned, capability-negotiated RPC (JSON control ops in
// wire.TControl frames) plus a data plane (wire.TDataIn/TDataOut/TSnapshot binary
// frames) over the shared G1 frame envelope (internal/wire), and wraps a
// daemon.Daemon (via the DaemonAPI subset) into the full client-facing surface:
// hello, list, launch, kill, delete, attach/detach, resize, subscribe.
//
// These are FAILING-FIRST white-box tests (package protocol). They exercise the
// frozen API a separate implementer will build. The RED state is "undefined-only":
// `go test ./internal/protocol/` fails to compile because these production symbols
// do not yet exist — no assertion logic runs until the implementer defines them.
//
// FROZEN API (orchestrator decisions; every refinement/pin is in the final
// test-designer report):
//
//	const Version = 1                                   // client<->daemon protocol version
//
//	// Control-plane op vocabulary (JSON, snake_case), carried in wire.TControl frames.
//	const ( OpHello, OpList, OpLaunch, OpKill, OpDelete, OpAttach, OpDetach,
//	        OpResize, OpSubscribe, OpEvent, OpLease, OpOK, OpError string )
//
//	// Control is the single JSON envelope for every control message. F-1: every
//	// message carries endpoint_id; a session-scoped op carries a namespaced
//	// session_id (<endpoint_id>/<local>). Which other fields matter depends on Op.
//	type Control struct {
//	    Op              string        `json:"op"`
//	    EndpointID      string        `json:"endpoint_id"`
//	    SessionID       string        `json:"session_id,omitempty"`
//	    ProtocolVersion int           `json:"protocol_version,omitempty"`
//	    Capabilities    []string      `json:"capabilities,omitempty"`
//	    Generation      uint64        `json:"generation,omitempty"`
//	    Cols            int           `json:"cols,omitempty"`
//	    Rows            int           `json:"rows,omitempty"`
//	    Launch          *LaunchReq    `json:"launch,omitempty"`
//	    Sessions        []SessionView `json:"sessions,omitempty"`
//	    Session         *SessionView  `json:"session,omitempty"`
//	    Error           string        `json:"error,omitempty"`
//	}
//	func EncodeControl(c Control) ([]byte, error)   // JSON body for a wire.TControl frame
//	func DecodeControl(b []byte) (Control, error)   // tolerant of unknown fields, not of bad JSON
//
//	// SessionView is one general-view row (V-4), stamped for the receiving client:
//	// namespaced id + endpoint id + the DAEMON-COMPUTED status Group (E6.9 — clients
//	// never call status.Derive).
//	type SessionView struct {
//	    EndpointID   string        `json:"endpoint_id"`
//	    ID           string        `json:"id"`            // namespaced: <endpoint_id>/<local>
//	    Agent        string        `json:"agent"`
//	    Cwd          string        `json:"cwd"`
//	    Status       status.Status `json:"status"`        // the three raw dims
//	    Group        status.Group  `json:"group"`         // precomputed server-side (E6.9)
//	    LastActivity time.Time     `json:"last_activity"`
//	    CreatedAt    time.Time     `json:"created_at"`
//	    Summary      string        `json:"summary"`       // V-4 one-line last-output summary
//	}
//	type LaunchReq struct {
//	    Agent, Cwd     string             `json:"agent"/"cwd"`
//	    Options        map[string]string  `json:"options"`
//	    Env            []string           `json:"env"`
//	    Cols, Rows     int                `json:"cols"/"rows"`
//	    InitialPrompt  string             `json:"initial_prompt"`
//	}
//	type Event struct { Session SessionView } // client-facing subscribe payload
//
//	var ErrIncompatibleVersion error // Dial returns this on a version mismatch; the
//	                                 // wrapped message names `swarm daemon restart`
//	                                 // AND states it is safe / loses no live sessions (D-8)
//
//	// CLIENT
//	type Client struct{ ... }
//	func Dial(socketPath string, caps []string) (*Client, error) // does the hello handshake
//	func (c *Client) EndpointID() string
//	func (c *Client) Capabilities() []string                     // negotiated intersection
//	func (c *Client) List() ([]SessionView, error)
//	func (c *Client) Launch(req LaunchReq) (sessionID string, err error)
//	func (c *Client) Kill(id string) error
//	func (c *Client) Delete(id string) error
//	func (c *Client) Attach(id string) (*Attachment, error)
//	func (c *Client) Subscribe() (<-chan Event, error)
//	func (c *Client) Close() error
//
//	type Attachment struct{ ... }
//	func (a *Attachment) Snapshot() []byte          // the one snapshot (S10)
//	func (a *Attachment) Frames() <-chan []byte      // live TDataOut frames after the snapshot
//	func (a *Attachment) Input(p []byte) error       // TDataIn; honored only under the current generation
//	func (a *Attachment) Resize(cols, rows int) error
//	func (a *Attachment) Detach() error
//	func (a *Attachment) Generation() uint64
//
//	// SERVER — wraps a daemon via the DaemonAPI subset (interface, so tests stub it).
//	type SessionStream interface { // the daemon's single pipe to one session's shim
//	    Snapshot() []byte
//	    Frames() <-chan []byte
//	    Input(p []byte) error
//	    Resize(cols, rows int) error
//	    Close() error
//	}
//	type DaemonAPI interface {
//	    List() []persist.Meta
//	    Launch(daemon.LaunchSpec) (persist.Meta, error)
//	    Kill(id string) error
//	    Delete(id string) error
//	    Attach(id string) (SessionStream, error)  // opened once per lease; see PIN below
//	    Events() <-chan persist.Meta               // single status-change source; Server fans out
//	}
//	type Server struct{ ... }
//	func Serve(d DaemonAPI, socketPath string) (*Server, error)
//	func (s *Server) Close() error
//	func FromDaemon(d *daemon.Daemon) DaemonAPI     // integration adapter (real-daemon attach path)
//
//	// Namespacing helpers (E6.8/F-1).
//	func NamespacedID(endpointID, localID string) string
//	func ParseID(namespaced string) (endpointID, localID string, ok bool)
//
// PINS (see final report for the rationale of each):
//   - LEASE: SUPERSEDE, not refuse. A second concurrent attach wins a NEW, higher
//     generation; the prior controller's Input/Resize are dropped server-side
//     (applied count == 0). S2.
//   - GENERATION: monotonic per session for the Server's lifetime (starts at 1,
//     +1 on every attach/supersede); never reused. A superseded controller's
//     generation is therefore strictly less than the current one.
//   - STREAM LIFECYCLE: the Server opens exactly ONE upstream SessionStream per
//     session while a lease is held (DaemonAPI.Attach on the 0->1 transition);
//     a supersede REUSES it; the 1->0 transition (Detach or client EOF) Closes it.
//     A later attach opens a fresh stream. L3.
//   - INPUT GENERATION: bound to the attaching connection (the binary TDataIn frame
//     stays clean); Resize (JSON) also carries `generation` explicitly. Both are
//     enforced server-side; stale is dropped, never forwarded to the SessionStream.
//   - GROUP: SessionView carries a precomputed status.Group filled by the Server via
//     status.Derive; the client just displays it (E6.9 mechanism = precomputed field).
//   - DRIFT CHECK (E6.2/GG-7): protocolmd_test reflects the json tags of the pinned
//     wire types {Control, SessionView, LaunchReq} and asserts docs/specifications/
//     protocol.md documents every one. protocol.md is the implementer's deliverable
//     (deliberately NOT stubbed here), so the drift test stays RED until it exists.
//
// Every test carries a deadline; nothing may hang. UNIX socket paths are capped
// near ~104 bytes, so sockets live under /tmp.
package protocol

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

const (
	// netTimeout bounds a single dial/handshake in the tests.
	netTimeout = 5 * time.Second
	// recvTimeout bounds waiting for one channel/frame delivery.
	recvTimeout = 2 * time.Second
	// oneSecond is the L1 fan-out latency bound asserted in fanout_test.
	oneSecond = 1 * time.Second
	// launchTimeout bounds a real-daemon launch + confirm in the integration test.
	launchTimeout = 20 * time.Second
)

// tmpSock returns a short-pathed UNIX socket path under /tmp (paths are capped
// near ~104 bytes; the long macOS $TMPDIR overflows).
func tmpSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swp")
	if err != nil {
		t.Fatalf("mkdir sock dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

// serveStub stands up a Server over stub on a fresh socket, with cleanup, and
// returns the socket path.
func serveStub(t *testing.T, stub *stubDaemon) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(stub, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// dialClient dials a Client (hello handshake), with cleanup. Fatal on error.
func dialClient(t *testing.T, sock string, caps []string) *Client {
	t.Helper()
	c, err := Dial(sock, caps)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// ---------------------------------------------------------------------------
// stub DaemonAPI (in-memory) — the fast, deterministic backend for MOST tests.
// ---------------------------------------------------------------------------

// stubDaemon is an in-memory DaemonAPI. Every call is recorded so a test can
// assert exactly what the Server forwarded (and, for revalidation, what it
// refused BEFORE forwarding).
type stubDaemon struct {
	mu sync.Mutex

	metas  []persist.Meta // returned by List
	nextID int            // local id source for Launch
	events chan persist.Meta

	// recorded calls
	launched []daemon.LaunchSpec
	killed   []string
	deleted  []string
	attached []string      // local ids passed to Attach, in order
	streams  []*stubStream // one per Attach call (newest last)

	// error injections
	launchErr error
	killErr   error
	deleteErr error
	attachErr error

	// R-POL.9 device authorization. authzFn decides each AuthorizeCommand (nil =>
	// accept); authzCalls records every tuple the Server presented so a test can
	// assert what was authorized. stubDaemon implements DeviceAuthenticator, so a
	// remote-tier Server built on it is NOT fail-closed-absent (use daemonOnly for
	// that case).
	authzFn    func(DeviceCommandAuth) error
	authzCalls []DeviceCommandAuth
}

// AuthorizeCommand makes stubDaemon a protocol.DeviceAuthenticator (R-POL.9). It
// records the presented tuple and defers the accept/reject decision to authzFn.
func (s *stubDaemon) AuthorizeCommand(a DeviceCommandAuth) error {
	s.mu.Lock()
	s.authzCalls = append(s.authzCalls, a)
	fn := s.authzFn
	s.mu.Unlock()
	if fn != nil {
		return fn(a)
	}
	return nil
}

// authorizedTuples returns a copy of every DeviceCommandAuth the Server presented.
func (s *stubDaemon) authorizedTuples() []DeviceCommandAuth {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeviceCommandAuth, len(s.authzCalls))
	copy(out, s.authzCalls)
	return out
}

// daemonOnly wraps a DaemonAPI so ONLY DaemonAPI's methods are exposed: it embeds the
// interface, so the concrete backend's DeviceAuthenticator does NOT promote through.
// A remote-tier Server built on it must fail closed (R-POL.9).
type daemonOnly struct{ DaemonAPI }

func newStubDaemon() *stubDaemon {
	return &stubDaemon{events: make(chan persist.Meta, 64)}
}

func (s *stubDaemon) List() []persist.Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]persist.Meta, len(s.metas))
	copy(out, s.metas)
	return out
}

func (s *stubDaemon) Launch(spec daemon.LaunchSpec) (persist.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.launched = append(s.launched, spec)
	if s.launchErr != nil {
		return persist.Meta{}, s.launchErr
	}
	s.nextID++
	m := persist.Meta{
		ID:        "sess" + itoa(s.nextID),
		AgentType: spec.AgentType,
		Cwd:       spec.Cwd,
		Env:       spec.ClientEnv,
		Status:    status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
	}
	s.metas = append(s.metas, m)
	return m, nil
}

func (s *stubDaemon) Kill(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.killed = append(s.killed, id)
	return s.killErr
}

func (s *stubDaemon) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return s.deleteErr
}

func (s *stubDaemon) Attach(id string) (SessionStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attached = append(s.attached, id)
	if s.attachErr != nil {
		return nil, s.attachErr
	}
	st := newStubStream()
	s.streams = append(s.streams, st)
	return st, nil
}

func (s *stubDaemon) Events() <-chan persist.Meta { return s.events }

// pushStatus publishes a status-change event to the fan-out source.
func (s *stubDaemon) pushStatus(m persist.Meta) { s.events <- m }

// snapshot accessors (each takes the lock).
func (s *stubDaemon) killedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.killed...)
}
func (s *stubDaemon) deletedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleted...)
}
func (s *stubDaemon) launchSpecs() []daemon.LaunchSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]daemon.LaunchSpec(nil), s.launched...)
}
func (s *stubDaemon) streamCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.streams) }
func (s *stubDaemon) lastStream() *stubStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streams) == 0 {
		return nil
	}
	return s.streams[len(s.streams)-1]
}
func (s *stubDaemon) streamAt(i int) *stubStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.streams) {
		return nil
	}
	return s.streams[i]
}

// setMetas replaces the List backing store.
func (s *stubDaemon) setMetas(ms ...persist.Meta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metas = ms
}

// ---------------------------------------------------------------------------
// stub SessionStream — the daemon's pipe to one shim, recording inputs/resizes.
// ---------------------------------------------------------------------------

type stubStream struct {
	snap   []byte
	frames chan []byte

	mu       sync.Mutex
	inputs   [][]byte
	resizes  [][2]int
	closed   bool
	closedCh chan struct{}
}

func newStubStream() *stubStream {
	return &stubStream{
		snap:     []byte("SNAPSHOT"),
		frames:   make(chan []byte, 64),
		closedCh: make(chan struct{}),
	}
}

func (s *stubStream) Snapshot() []byte      { return s.snap }
func (s *stubStream) Frames() <-chan []byte { return s.frames }

func (s *stubStream) Input(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, append([]byte(nil), p...))
	return nil
}

func (s *stubStream) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resizes = append(s.resizes, [2]int{cols, rows})
	return nil
}

func (s *stubStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.closedCh)
	}
	return nil
}

// inputBytes returns the concatenation of every Input the Server forwarded.
func (s *stubStream) inputBytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []byte
	for _, in := range s.inputs {
		out = append(out, in...)
	}
	return out
}

func (s *stubStream) resizeCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.resizes) }

func (s *stubStream) resizesCopy() [][2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]int(nil), s.resizes...)
}

// waitClosed reports whether the stream is Closed within d.
func (s *stubStream) waitClosed(d time.Duration) bool {
	select {
	case <-s.closedCh:
		return true
	case <-time.After(d):
		return false
	}
}

// ---------------------------------------------------------------------------
// Raw wire helpers — for codec/handshake/ordering/error tests that speak the
// protocol at the frame level.
// ---------------------------------------------------------------------------

// rawConn wraps a net.Conn with control-frame read/write helpers and a per-op
// deadline so a wrong implementation cannot hang a test.
type rawConn struct {
	t    *testing.T
	conn net.Conn
}

// rawDial opens a raw connection to sock (no hello). Cleanup closes it.
func rawDial(t *testing.T, sock string) *rawConn {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, netTimeout)
	if err != nil {
		t.Fatalf("raw dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &rawConn{t: t, conn: conn}
}

func (r *rawConn) writeControl(c Control) {
	r.t.Helper()
	body, err := EncodeControl(c)
	if err != nil {
		r.t.Fatalf("EncodeControl: %v", err)
	}
	_ = r.conn.SetWriteDeadline(time.Now().Add(recvTimeout))
	if err := wire.WriteFrame(r.conn, wire.TControl, body); err != nil {
		r.t.Fatalf("write control frame: %v", err)
	}
}

// writeFrame writes a raw typed frame (used to test data-plane demux).
func (r *rawConn) writeFrame(typ wire.Type, payload []byte) {
	r.t.Helper()
	_ = r.conn.SetWriteDeadline(time.Now().Add(recvTimeout))
	if err := wire.WriteFrame(r.conn, typ, payload); err != nil {
		r.t.Fatalf("write frame: %v", err)
	}
}

// readFrame reads one frame with a deadline.
func (r *rawConn) readFrame() (wire.Type, []byte, error) {
	r.t.Helper()
	_ = r.conn.SetReadDeadline(time.Now().Add(recvTimeout))
	return wire.ReadFrame(r.conn)
}

// readControl reads one frame, requires it be a TControl, and decodes it.
func (r *rawConn) readControl() Control {
	r.t.Helper()
	typ, payload, err := r.readFrame()
	if err != nil {
		r.t.Fatalf("read control frame: %v", err)
	}
	if typ != wire.TControl {
		r.t.Fatalf("frame type = %d, want TControl", typ)
	}
	c, err := DecodeControl(payload)
	if err != nil {
		r.t.Fatalf("DecodeControl: %v", err)
	}
	return c
}

// hello performs the raw hello handshake at protocolVersion with caps, returning
// the server's hello reply (endpoint_id + negotiated capabilities).
func (r *rawConn) hello(protocolVersion int, caps []string) Control {
	r.t.Helper()
	r.writeControl(Control{Op: OpHello, ProtocolVersion: protocolVersion, Capabilities: caps})
	return r.readControl()
}

// ---------------------------------------------------------------------------
// tiny helpers
// ---------------------------------------------------------------------------

func itoa(n int) string { return strconv.Itoa(n) }

func sleepMS(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// recvView waits for one Event on ch, returning its SessionView, or fails.
func recvEvent(t *testing.T, ch <-chan Event, d time.Duration) (Event, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(d):
		return Event{}, false
	}
}

// recvFrame waits for one []byte on a frames channel.
func recvFrame(t *testing.T, ch <-chan []byte, d time.Duration) ([]byte, bool) {
	t.Helper()
	select {
	case f, ok := <-ch:
		return f, ok
	case <-time.After(d):
		return nil, false
	}
}

// jsonEqual reports whether a and b marshal to equal JSON (order-insensitive for
// maps). Used by the codec round-trip test.
func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
