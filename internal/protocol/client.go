package protocol

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/version"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
)

// clientTimeout bounds a client dial/handshake and any single request/reply, so a
// misbehaving server can never hang a caller.
const clientTimeout = 10 * time.Second

// Client is a connected, handshaked client of the daemon protocol. It multiplexes
// synchronous request/reply ops, a subscribe event stream, and one attach data
// stream over a single connection.
type Client struct {
	conn         net.Conn
	endpointID   string
	caps         []string
	buildVersion string

	writeMu sync.Mutex

	reqMu  sync.Mutex   // one outstanding request/reply at a time
	respCh chan Control // read loop delivers responses here

	mu       sync.Mutex
	eventsCh chan Event
	att      *Attachment

	pairMu  sync.Mutex      // one pairing in flight per client (mirrors the daemon host)
	pairing *PairingSession // the in-flight pairing, routing pair_pending/pair_result pushes

	closeOnce sync.Once
	done      chan struct{}
}

// Dial connects to the daemon socket and completes the hello handshake at the
// current protocol Version, offering caps. A version mismatch returns an error
// satisfying errors.Is(err, ErrIncompatibleVersion) whose message names `swarm
// daemon restart` and states the restart is safe (D-8).
func Dial(socketPath string, caps []string) (*Client, error) {
	conn, err := net.DialTimeout("unix", socketPath, clientTimeout)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:   conn,
		respCh: make(chan Control, 1),
		done:   make(chan struct{}),
	}

	if err := c.writeControl(Control{Op: OpHello, ProtocolVersion: Version, BuildVersion: version.Version, Capabilities: caps}); err != nil {
		conn.Close()
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(clientTimeout))
	typ, payload, err := wire.ReadFrame(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Time{})
	if typ != wire.TControl {
		conn.Close()
		return nil, errors.New("protocol: handshake reply was not a control frame")
	}
	reply, err := DecodeControl(payload)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if reply.Op == OpError {
		conn.Close()
		// The daemon rejected the handshake. Synthesize the D-8 guidance
		// client-side rather than surfacing arbitrary daemon prose verbatim (F10).
		return nil, fmt.Errorf("%w: %s", ErrIncompatibleVersion, d8ClientMessage())
	}
	if reply.Op != OpHello || reply.ProtocolVersion != Version {
		conn.Close()
		return nil, fmt.Errorf("%w: %s", ErrIncompatibleVersion, d8Message(reply.ProtocolVersion, Version))
	}
	c.endpointID = reply.EndpointID
	c.caps = reply.Capabilities
	c.buildVersion = reply.BuildVersion
	go c.readLoop()
	return c, nil
}

// EndpointID returns the id the daemon assigned this connection.
func (c *Client) EndpointID() string { return c.endpointID }

// Capabilities returns the negotiated capability intersection.
func (c *Client) Capabilities() []string { return c.caps }

// BuildVersion returns the daemon's internal/version.Version, as reported on
// the hello reply (E13.2). Unlike a ProtocolVersion mismatch, a BuildVersion
// difference from this client's own version.Version is not fatal — it is the
// signal a caller uses to notice a different-build daemon and nudge `swarm
// daemon restart`.
func (c *Client) BuildVersion() string { return c.buildVersion }

// List returns the daemon's sessions, each stamped for this endpoint with a
// server-computed status Group.
func (c *Client) List() ([]SessionView, error) {
	resp, err := c.request(Control{Op: OpList, EndpointID: c.endpointID})
	if err != nil {
		return nil, err
	}
	if resp.Op == OpError {
		return nil, errors.New(resp.Error)
	}
	return resp.Sessions, nil
}

// Launch requests a new session and returns its namespaced id.
func (c *Client) Launch(req LaunchReq) (string, error) {
	r := req
	resp, err := c.request(Control{Op: OpLaunch, EndpointID: c.endpointID, Launch: &r})
	if err != nil {
		return "", err
	}
	if resp.Op == OpError {
		return "", errors.New(resp.Error)
	}
	if resp.Session == nil {
		return "", errors.New("protocol: launch reply carried no session")
	}
	return resp.Session.ID, nil
}

// Kill terminates a session.
func (c *Client) Kill(id string) error { return c.simpleOp(OpKill, id) }

// Delete removes a session.
func (c *Client) Delete(id string) error { return c.simpleOp(OpDelete, id) }

// ListDevices returns the daemon's paired-device roster (requires the negotiated
// `pairing` capability).
func (c *Client) ListDevices() ([]DeviceView, error) {
	resp, err := c.request(Control{Op: OpDeviceList, EndpointID: c.endpointID})
	if err != nil {
		return nil, err
	}
	if resp.Op == OpError {
		return nil, errors.New(resp.Error)
	}
	return resp.Devices, nil
}

// RevokeDevice removes targetID from the daemon's device registry.
func (c *Client) RevokeDevice(targetID string) error {
	resp, err := c.request(Control{Op: OpDeviceRevoke, EndpointID: c.endpointID, TargetDeviceID: targetID})
	if err != nil {
		return err
	}
	if resp.Op == OpError {
		return errors.New(resp.Error)
	}
	return nil
}

// SetRemoteControl durably flips the remote-control master override (A4, `swarm remote
// off`/`on`): enabled=false disables remote control regardless of paired devices,
// enabled=true returns to the device-derived value. Owner-tier only — the daemon refuses
// it on the remote tier (requires the negotiated `pairing` capability).
func (c *Client) SetRemoteControl(enabled bool) error {
	resp, err := c.request(Control{Op: OpRemoteSetControl, EndpointID: c.endpointID, RemoteControl: &enabled})
	if err != nil {
		return err
	}
	if resp.Op == OpError {
		return errors.New(resp.Error)
	}
	return nil
}

func (c *Client) simpleOp(op, id string) error {
	resp, err := c.request(Control{Op: op, EndpointID: c.endpointID, SessionID: id})
	if err != nil {
		return err
	}
	if resp.Op == OpError {
		return errors.New(resp.Error)
	}
	return nil
}

// Subscribe returns a channel of status-change events for this endpoint.
func (c *Client) Subscribe() (<-chan Event, error) {
	c.mu.Lock()
	if c.eventsCh == nil {
		c.eventsCh = make(chan Event, eventQueueCap)
	}
	ch := c.eventsCh
	c.mu.Unlock()

	resp, err := c.request(Control{Op: OpSubscribe, EndpointID: c.endpointID})
	if err != nil {
		return nil, err
	}
	if resp.Op == OpError {
		return nil, errors.New(resp.Error)
	}
	return ch, nil
}

// Attach takes the controller lease on a session and returns its Attachment: the
// one snapshot followed by the live output stream.
func (c *Client) Attach(id string) (*Attachment, error) {
	// A second attach on this client auto-detaches the first cleanly, before the
	// new lease is installed, so a detach meant for the first never cross-closes
	// the second (F7).
	c.mu.Lock()
	prev := c.att
	c.mu.Unlock()
	if prev != nil {
		_ = prev.Detach()
	}

	att := newAttachment(c, id)
	c.mu.Lock()
	c.att = att
	c.mu.Unlock()

	resp, err := c.request(Control{Op: OpAttach, EndpointID: c.endpointID, SessionID: id})
	if err != nil {
		c.clearAttachment(att)
		return nil, err
	}
	if resp.Op == OpError {
		c.clearAttachment(att)
		return nil, errors.New(resp.Error)
	}
	if resp.Op != OpLease {
		c.clearAttachment(att)
		return nil, fmt.Errorf("protocol: attach expected a lease, got %q", resp.Op)
	}
	att.gen = resp.Generation

	select {
	case <-att.snapReady:
		if att.snapFailed {
			c.clearAttachment(att)
			return nil, errors.New("protocol: invalid snapshot framing from daemon")
		}
	case <-c.done:
		return nil, errors.New("protocol: connection closed during attach")
	case <-time.After(clientTimeout):
		return nil, errors.New("protocol: no snapshot after lease grant")
	}
	return att, nil
}

// Close disconnects the client; the server observes the EOF and releases any lease
// this client held (P-4/L3).
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.conn.Close()
	})
	return nil
}

// ---------------------------------------------------------------------------
// Pairing — the async owner-tier pairing session (slice A4). The daemon HOSTS the
// handshake (ADR-007 "Pairing host: Option A"): pair_start replies synchronously with
// the rendezvous view, then the daemon PUSHES pair_pending (the SAS gate) and, after
// the human decides, pair_result (the terminal outcome). Client.request is a strict
// 1-req/1-resp round-trip, so those pushes get their own session-scoped channels
// (mirrors the Subscribe eventsCh lifecycle + the dispatchControl routing).
// ---------------------------------------------------------------------------

// PairingPending is one SAS-gate prompt pushed by the daemon (pair_pending): the
// short-authentication-string words the human compares and the requesting device's
// name. The caller displays these and answers with PairingSession.Confirm.
type PairingPending struct {
	SAS        []string
	DeviceName string
}

// PairingResult is the terminal outcome of a pairing pushed by the daemon
// (pair_result). Paired is the sole success signal; the identity fields are set only
// when Paired. A declined SAS gate, a TTL/rendezvous failure, or a dropped connection
// all yield Paired=false — fail closed, nothing enrolled.
type PairingResult struct {
	Paired     bool
	DeviceID   string
	Name       string
	Capability string
}

// pairingResultFromControl maps a pushed pair_result payload to a PairingResult. The
// daemon sends a nil Pairing on failure and a populated one (with DeviceID) on success.
func pairingResultFromControl(p *PairingControl) PairingResult {
	if p == nil || p.DeviceID == "" {
		return PairingResult{Paired: false}
	}
	return PairingResult{Paired: true, DeviceID: p.DeviceID, Name: p.Name, Capability: p.Capability}
}

// PairingSession is a client's handle to one in-flight owner-tier pairing. The
// rendezvous view (QR/RendezvousID/ExpiresAt), from the synchronous pair_start reply,
// is displayed to bootstrap the phone; Pending() delivers the SAS gate; Confirm answers
// it; Result() delivers the single terminal outcome. Close (or a dropped connection)
// ends the session fail-closed.
type PairingSession struct {
	c *Client

	// The synchronous rendezvous view from the pair_start reply.
	QR           string
	RendezvousID string
	ExpiresAt    *time.Time

	pending chan PairingPending // SAS-gate pushes (pair_pending), buffered
	result  chan PairingResult  // the single terminal outcome (pair_result / fail-closed)

	resultOnce sync.Once
}

// StartPairing opens an owner-tier pairing session: it sends pair_start and returns a
// PairingSession carrying the synchronous rendezvous view (QR + rendezvous id +
// expiry). The daemon then HOSTS the handshake, PUSHING pair_pending (the SAS gate, on
// Pending()) and, after Confirm, pair_result (the terminal outcome, on Result()). Only
// ONE pairing may be in flight per client (mirrors the daemon's one-per-connection
// host). The session ends fail-closed on Close or a dropped connection — a session that
// never reaches a paired Result() enrolls nothing.
func (c *Client) StartPairing(req PairStartReq) (*PairingSession, error) {
	sess := &PairingSession{
		c:       c,
		pending: make(chan PairingPending, 1),
		result:  make(chan PairingResult, 1),
	}

	// Register the session BEFORE writing pair_start so the daemon's pair_pending /
	// pair_result PUSHES route to the session channels (dispatchControl) and never the
	// request respCh — even if a push races ahead of the pair_start reply on the wire.
	c.pairMu.Lock()
	if c.pairing != nil {
		c.pairMu.Unlock()
		return nil, errors.New("protocol: a pairing is already in progress")
	}
	c.pairing = sess
	c.pairMu.Unlock()

	resp, err := c.request(Control{Op: OpPairStart, EndpointID: c.endpointID,
		Pairing: &PairingControl{Capability: req.Capability, TTLSeconds: req.TTLSeconds}})
	if err != nil {
		c.clearPairing(sess)
		return nil, err
	}
	if resp.Op == OpError {
		c.clearPairing(sess)
		return nil, errors.New(resp.Error)
	}
	if resp.Op != OpPairStart || resp.Pairing == nil {
		c.clearPairing(sess)
		return nil, fmt.Errorf("protocol: pair_start expected a rendezvous view, got %q", resp.Op)
	}
	sess.QR = resp.Pairing.QR
	sess.RendezvousID = resp.Pairing.RendezvousID
	sess.ExpiresAt = resp.Pairing.ExpiresAt
	return sess, nil
}

// Pending returns the SAS-gate stream: each pair_pending push the daemon sends while a
// pairing is in flight.
func (s *PairingSession) Pending() <-chan PairingPending { return s.pending }

// Result returns the terminal-outcome channel; it delivers exactly one PairingResult
// (the pair_result push, or a fail-closed non-paired result on disconnect/Close).
func (s *PairingSession) Result() <-chan PairingResult { return s.result }

// Confirm answers the SAS gate: it sends pair_confirm(Allow=allow). The daemon routes
// the decision to its blocked confirm closure; there is no reply, so this is a
// fire-and-forget write (an error means the connection is gone — fail closed).
func (s *PairingSession) Confirm(allow bool) error {
	return s.c.writeControl(Control{Op: OpPairConfirm, EndpointID: s.c.endpointID, Pairing: &PairingControl{Allow: allow}})
}

// Close ends the session: it stops routing further pushes and delivers a fail-closed
// (non-paired) terminal result if none has arrived, so a caller blocked on Result()
// unblocks. Safe to call more than once and after a disconnect.
func (s *PairingSession) Close() {
	s.c.clearPairing(s)
	s.deliverResult(PairingResult{Paired: false})
}

// deliverResult delivers the one terminal outcome and ends the session. It is
// idempotent: a real pair_result, a Close, and a fail-closed disconnect can all fire,
// but resultOnce lets exactly one reach the (buffered, cap-1) channel without blocking.
func (s *PairingSession) deliverResult(r PairingResult) {
	s.resultOnce.Do(func() { s.result <- r })
}

// clearPairing releases this client's in-flight pairing slot if sess still holds it, so
// no further pushes route to it.
func (c *Client) clearPairing(sess *PairingSession) {
	c.pairMu.Lock()
	if c.pairing == sess {
		c.pairing = nil
	}
	c.pairMu.Unlock()
}

// request performs one synchronous control round-trip.
func (c *Client) request(req Control) (Control, error) {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()
	select { // drop any stale response
	case <-c.respCh:
	default:
	}
	if err := c.writeControl(req); err != nil {
		return Control{}, err
	}
	select {
	case resp := <-c.respCh:
		return resp, nil
	case <-c.done:
		return Control{}, errors.New("protocol: connection closed")
	case <-time.After(clientTimeout):
		return Control{}, errors.New("protocol: request timed out")
	}
}

func (c *Client) readLoop() {
	defer c.closeReadLoop()
	for {
		typ, payload, err := wire.ReadFrame(c.conn)
		if err != nil {
			return
		}
		switch typ {
		case wire.TControl:
			ctrl, derr := DecodeControl(payload)
			if derr != nil {
				continue
			}
			c.dispatchControl(ctrl)
		case wire.TSnapshot:
			c.mu.Lock()
			att := c.att
			c.mu.Unlock()
			if att != nil {
				att.deliverSnapshotChunk(payload)
			}
		case wire.TDataOut:
			c.mu.Lock()
			att := c.att
			c.mu.Unlock()
			if att != nil {
				att.deliverFrame(c.done, payload)
			}
		}
	}
}

func (c *Client) dispatchControl(ctrl Control) {
	switch ctrl.Op {
	case OpEvent:
		c.mu.Lock()
		ch := c.eventsCh
		c.mu.Unlock()
		if ch != nil && ctrl.Session != nil {
			select {
			case ch <- Event{Session: *ctrl.Session}:
			case <-c.done:
			}
		}
	case OpLease:
		// The lease grant carries the snapshot's total length. Begin reassembly in
		// the read-loop goroutine BEFORE the following TSnapshot chunk frames are
		// read, then forward the lease to the pending Attach as its response (F2).
		c.mu.Lock()
		att := c.att
		c.mu.Unlock()
		if att != nil {
			att.beginSnapshot(ctrl.SnapshotLen)
		}
		select {
		case c.respCh <- ctrl:
		default:
		}
	case OpDetach:
		// Server revoked our lease (supersede or orderly detach): close the
		// attachment's Frames() channel. This runs in the read-loop goroutine, the
		// sole sender to that channel, so the close never races a send.
		c.mu.Lock()
		att := c.att
		c.att = nil
		c.mu.Unlock()
		if att != nil {
			att.closeFrames()
		}
	case OpPairPending:
		// The daemon-hosted pairing PUSHES the SAS gate. Route it to the in-flight
		// pairing session's channel, NEVER the request respCh (the pair_start reply,
		// OpPairStart, is the only pairing frame that is a request response and it falls
		// through to default below). Registered before pair_start is sent, so a push that
		// races ahead of the reply still lands here.
		c.pairMu.Lock()
		ps := c.pairing
		c.pairMu.Unlock()
		if ps != nil && ctrl.Pairing != nil {
			select {
			case ps.pending <- PairingPending{SAS: ctrl.Pairing.SAS, DeviceName: ctrl.Pairing.DeviceName}:
			case <-c.done:
			}
		}
	case OpPairResult:
		// The daemon PUSHES the terminal outcome. Route it to the session (nil Pairing =>
		// a failed pairing), ending the session.
		c.pairMu.Lock()
		ps := c.pairing
		if c.pairing == ps {
			c.pairing = nil
		}
		c.pairMu.Unlock()
		if ps != nil {
			ps.deliverResult(pairingResultFromControl(ctrl.Pairing))
		}
	default:
		// A response to a pending request (OpOK/OpError/OpList/OpLaunch/OpLease/OpPairStart).
		select {
		case c.respCh <- ctrl:
		default:
		}
	}
}

// closeReadLoop runs when the read loop exits: it closes any live attachment's
// Frames() channel (from the read-loop goroutine, so no send races the close) and
// closes the client.
func (c *Client) closeReadLoop() {
	c.mu.Lock()
	att := c.att
	c.att = nil
	c.mu.Unlock()
	if att != nil {
		att.closeFrames()
	}
	// Fail-closed pairing teardown: a dropped connection ENDS an in-flight pairing with
	// a non-paired result, so a caller blocked on Result() unblocks and nothing enrolls
	// (the daemon's connection-derived ctx cancels its confirm in parallel).
	c.pairMu.Lock()
	ps := c.pairing
	c.pairing = nil
	c.pairMu.Unlock()
	if ps != nil {
		ps.deliverResult(PairingResult{Paired: false})
	}
	c.Close()
}

func (c *Client) clearAttachment(att *Attachment) {
	c.mu.Lock()
	if c.att == att {
		c.att = nil
	}
	c.mu.Unlock()
}

func (c *Client) writeControl(ctrl Control) error {
	body, err := EncodeControl(ctrl)
	if err != nil {
		return err
	}
	return c.writeFrame(wire.TControl, body)
}

func (c *Client) writeFrame(typ wire.Type, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wire.WriteFrame(c.conn, typ, payload)
}

// ---------------------------------------------------------------------------
// Attachment — one controller attach's client-side handle.
// ---------------------------------------------------------------------------

// Attachment is a client's controller view of one session: the one snapshot (S10)
// and the live output Frames() stream, plus input/resize under the lease
// generation. A superseded or detached attachment's Frames() channel closes.
type Attachment struct {
	c   *Client
	id  string // namespaced session id
	gen uint64

	snapshot  []byte
	snapReady chan struct{}
	snapOnce  sync.Once

	// Snapshot reassembly, driven only by the read-loop goroutine: chunk frames
	// accumulate into snapBuf until it reaches EXACTLY snapLen bytes, then the whole
	// snapshot is delivered (F2). An invalid declared length or an overshooting
	// chunk stream fails the attach (snapFailed) rather than allocating/over-reading.
	// No lock is needed — beginSnapshot, deliverSnapshotChunk and closeFrames are all
	// read-loop-serialized.
	snapLen    int
	snapBuf    []byte
	snapDone   bool
	snapFailed bool

	frames    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func newAttachment(c *Client, id string) *Attachment {
	return &Attachment{
		c:         c,
		id:        id,
		snapReady: make(chan struct{}),
		frames:    make(chan []byte, 256),
		closed:    make(chan struct{}),
	}
}

// Snapshot returns the single snapshot delivered on attach (S10).
func (a *Attachment) Snapshot() []byte { return a.snapshot }

// Frames returns the live output stream; it closes when the lease is lost.
func (a *Attachment) Frames() <-chan []byte { return a.frames }

// Generation returns this attach's lease generation.
func (a *Attachment) Generation() uint64 { return a.gen }

// Input sends terminal input; the server honors it only under the current lease
// generation (S2). The generation is bound to this connection, so the frame
// carries no generation prefix.
func (a *Attachment) Input(p []byte) error {
	return a.c.writeFrame(wire.TDataIn, p)
}

// Resize requests a terminal resize under this attach's generation; the server
// drops it if the generation is stale or the dimensions are out of range.
func (a *Attachment) Resize(cols, rows int) error {
	return a.c.writeControl(Control{
		Op:         OpResize,
		EndpointID: a.c.endpointID,
		SessionID:  a.id,
		Generation: a.gen,
		Cols:       cols,
		Rows:       rows,
	})
}

// Detach releases the lease. It returns once the server has confirmed the release
// (the Frames() channel has closed) or the connection is gone.
func (a *Attachment) Detach() error {
	if err := a.c.writeControl(Control{Op: OpDetach, EndpointID: a.c.endpointID, SessionID: a.id, Generation: a.gen}); err != nil {
		return err
	}
	select {
	case <-a.closed:
	case <-a.c.done:
	case <-time.After(clientTimeout):
	}
	return nil
}

// maxSnapshotBytes caps a reassembled snapshot so a garbage or oversized
// snapshot_len can never OOM the client, while still admitting the LARGEST snapshot
// the shim can LEGALLY produce. The vt emulator serializes ONE JSON run per grid
// cell (Epic 2's accepted no-run-merging decision) and CLAMPS the two free-form
// fields producer-side (vt.SnapshotTextMax per cell, vt.SnapshotTitleMax for the
// title), so the largest legal snapshot at maxDim x maxDim is bounded. The cap is
// finite (a garbage/huge/negative length is rejected without allocation) yet large
// enough that no legal snapshot is rejected. It DEPENDS on the vt producer-side
// limits it references — if those or maxDim change, the cap tracks them.
//
// Derivation:
//   - per cell: a fully-styled Run's fixed JSON fields (~124 B) + the clamped cell
//     text. A cell's Text is ONE grapheme (vt), so at most its base rune is
//     JSON-escapable (< > & -> \uXXXX, 6 B); combining marks are emitted verbatim.
//     So escaped text <= vt.SnapshotTextMax + a small escape slack.
//   - per line: the {"runs":[ ... ]} array framing.
//   - once: the title (free-form, so every byte may escape to \uXXXX) + the Snap
//     wrapper (version/cols/rows/cursor/keys).
//
// Epic 8 note (N-1 first-paint budget): per-cell run serialization still makes a
// worst-case snapshot large (~190 MiB at maxDim=1000); run-merging in the snapshot
// format is the eventual optimization if first-paint latency suffers.
const (
	snapshotRunFixedMax     = 128                    // fully-styled Run JSON, empty text, + separator
	snapshotCellTextMax     = vt.SnapshotTextMax + 8 // clamped one-grapheme text, escaped worst case
	snapshotBytesPerCell    = snapshotRunFixedMax + snapshotCellTextMax
	snapshotLineFraming     = 16                         // {"runs":[ ]} + separator, per line
	snapshotTitleSerialized = vt.SnapshotTitleMax*6 + 16 // clamped title, every byte escaped to \uXXXX
	snapshotWrapperMax      = 256                        // version/cols/rows/cursor + keys

	maxSnapshotBytes = maxDim*maxDim*snapshotBytesPerCell +
		maxDim*snapshotLineFraming +
		snapshotTitleSerialized +
		snapshotWrapperMax
)

// beginSnapshot starts snapshot reassembly for a lease whose snapshot is n bytes
// total. A negative or oversized length is rejected (no allocation); an empty
// snapshot is delivered immediately. Read-loop goroutine only.
func (a *Attachment) beginSnapshot(n int) {
	if n < 0 || n > maxSnapshotBytes {
		a.failSnapshot()
		return
	}
	a.snapLen = n
	a.snapBuf = make([]byte, 0, n)
	if n == 0 {
		a.finishSnapshot()
	}
}

// deliverSnapshotChunk appends one snapshot chunk, delivering the whole snapshot
// once EXACTLY snapLen bytes have arrived. A chunk stream that overshoots the
// declared length fails the attach rather than growing unbounded (F2). Read-loop
// goroutine only.
func (a *Attachment) deliverSnapshotChunk(p []byte) {
	if a.snapDone {
		return
	}
	if len(a.snapBuf)+len(p) > a.snapLen {
		a.failSnapshot()
		return
	}
	a.snapBuf = append(a.snapBuf, p...)
	if len(a.snapBuf) == a.snapLen {
		a.finishSnapshot()
	}
}

func (a *Attachment) finishSnapshot() {
	a.snapDone = true
	a.snapOnce.Do(func() {
		a.snapshot = a.snapBuf
		close(a.snapReady)
	})
}

// failSnapshot aborts the attach on invalid snapshot framing: it unblocks Attach
// (which returns an error on snapFailed) and closes the attachment. Read-loop only.
func (a *Attachment) failSnapshot() {
	a.snapDone = true
	a.snapFailed = true
	a.snapOnce.Do(func() { close(a.snapReady) })
	a.closeFrames()
}

// deliverFrame delivers one live frame. It is only ever called from the client
// read-loop goroutine, so it never races closeFrames (also read-loop-driven).
func (a *Attachment) deliverFrame(done <-chan struct{}, p []byte) {
	select {
	case a.frames <- append([]byte(nil), p...):
	case <-a.closed:
	case <-done:
	}
}

func (a *Attachment) closeFrames() {
	a.closeOnce.Do(func() {
		close(a.closed)
		close(a.frames)
	})
}
