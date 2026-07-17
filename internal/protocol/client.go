package protocol

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

// clientTimeout bounds a client dial/handshake and any single request/reply, so a
// misbehaving server can never hang a caller.
const clientTimeout = 10 * time.Second

// Client is a connected, handshaked client of the daemon protocol. It multiplexes
// synchronous request/reply ops, a subscribe event stream, and one attach data
// stream over a single connection.
type Client struct {
	conn       net.Conn
	endpointID string
	caps       []string

	writeMu sync.Mutex

	reqMu  sync.Mutex   // one outstanding request/reply at a time
	respCh chan Control // read loop delivers responses here

	mu       sync.Mutex
	eventsCh chan Event
	att      *Attachment

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

	if err := c.writeControl(Control{Op: OpHello, ProtocolVersion: Version, Capabilities: caps}); err != nil {
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
	go c.readLoop()
	return c, nil
}

// EndpointID returns the id the daemon assigned this connection.
func (c *Client) EndpointID() string { return c.endpointID }

// Capabilities returns the negotiated capability intersection.
func (c *Client) Capabilities() []string { return c.caps }

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
	default:
		// A response to a pending request (OpOK/OpError/OpList/OpLaunch/OpLease).
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
// the shim can legally produce. The vt emulator serializes ONE JSON run per grid
// cell (Epic 2's accepted no-run-merging decision): a maxDim x maxDim grid is up to
// maxDim*maxDim runs, and a fully-styled run (text + width + fg/bg hex + the five
// attribute flags) serializes to ~124 bytes. snapshotBytesPerCell rounds that up to
// leave margin for multi-codepoint graphemes and the per-line array framing, so the
// cap = maxDim*maxDim*snapshotBytesPerCell is finite (a garbage/huge length is
// rejected, no OOM) yet large enough that no legal max-grid snapshot is rejected.
// If maxDim (the resize clamp) changes, this cap tracks it automatically.
//
// Epic 8 note (N-1 first-paint budget): per-cell run serialization makes a
// worst-case snapshot large (~150 MiB at maxDim=1000). If first-paint latency
// suffers, run-merging in the snapshot format is the eventual optimization.
const (
	snapshotBytesPerCell = 160
	maxSnapshotBytes     = maxDim * maxDim * snapshotBytesPerCell
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
