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
	peekCh   chan TerminalSnapshot // terminal_snapshot PUSHES, never the request respCh

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

// Launch requests a new session and returns its namespaced id AND the daemon's
// CANONICAL name for it (the server-sanitized/truncated label from the reply's
// SessionView). Callers that only need the id can discard the name; the auto-attach
// chrome uses the canonical name so the label matches what the daemon persisted (an
// older daemon whose reply predates naming returns an empty name).
func (c *Client) Launch(req LaunchReq) (id, name string, err error) {
	r := req
	resp, err := c.request(Control{Op: OpLaunch, EndpointID: c.endpointID, Launch: &r})
	if err != nil {
		return "", "", err
	}
	if resp.Op == OpError {
		return "", "", errors.New(resp.Error)
	}
	if resp.Session == nil {
		return "", "", errors.New("protocol: launch reply carried no session")
	}
	return resp.Session.ID, resp.Session.Name, nil
}

// SendInput writes ONE steering message into a session (ADR-010 A2): either req.Text
// (submitted with a trailing CR when req.Submit) or the single named key req.Key, never
// both. The daemon applies the r3p submit-boundary framing and its gap; the caller just
// names the message. It takes no lease and never supersedes an attached controller. An
// older daemon that predates the op, a remote-tier socket, a malformed request or a
// session that cannot receive input all come back as a Go error rather than a silent
// success.
func (c *Client) SendInput(id string, req SendInputReq) error {
	r := req
	resp, err := c.request(Control{Op: OpSendInput, EndpointID: c.endpointID, SessionID: id, SendInput: &r})
	if err != nil {
		return err
	}
	if resp.Op == OpError {
		return errors.New(resp.Error)
	}
	return nil
}

// TerminalSnapshot returns the session's CURRENT screen, server-rendered and sanitized
// (ADR-010 A3 — the owner-tier peek). The daemon's render loop pushes the session's grid
// before any new output, so a peek of an idle session returns at once instead of waiting
// for the session to print something.
//
// The peek is a server PUSH stream (like event), not a request response: the daemon keeps
// rendering after this call returns. The channel is registered BEFORE terminal_subscribe is
// sent and every snapshot is routed to it by dispatchControl, so a later push can never be
// answered as some other request's reply.
//
// A PREVIOUS peek on this client is the hazard, and TWO things answer it. The earlier peek
// keeps rendering until the daemon cancels it, so (1) this call waits on a FRESH channel —
// a reused one can already hold the previous session's screen, which would be returned here
// as if it were this session's, silently and with no error — and (2) a snapshot is accepted
// only when it is FOR the session that was asked for, so a frame still in flight from the
// previous peek is discarded rather than answered with. The daemon stamps the LOCAL session
// id on the render (server.go's renderer emits r.Session; the gateway namespaces at egress).
func (c *Client) TerminalSnapshot(id string) (*TerminalSnapshot, error) {
	ch := make(chan TerminalSnapshot, 1)
	c.mu.Lock()
	c.peekCh = ch
	c.mu.Unlock()

	// The daemon refuses a non-namespaced id outright, so the fallback below never governs a
	// successful peek; it just keeps the match total.
	want := id
	if _, local, ok := ParseID(id); ok {
		want = local
	}

	resp, err := c.request(Control{Op: OpTerminalSubscribe, EndpointID: c.endpointID, SessionID: id})
	if err != nil {
		return nil, err
	}
	if resp.Op == OpError {
		return nil, errors.New(resp.Error)
	}
	deadline := time.After(clientTimeout)
	for {
		select {
		case snap := <-ch:
			if snap.Session != want {
				continue // a leftover render from the peek this one superseded
			}
			return &snap, nil
		case <-c.done:
			return nil, errors.New("protocol: connection closed during peek")
		case <-deadline:
			return nil, errors.New("protocol: no terminal snapshot from the daemon")
		}
	}
}

// Kill terminates a session.
func (c *Client) Kill(id string) error { return c.simpleOp(OpKill, id) }

// Delete removes a session.
func (c *Client) Delete(id string) error { return c.simpleOp(OpDelete, id) }

// Rename changes a session's user-provided display label (v0.5). The new name is
// re-validated and sanitized server-side; the daemon updates the session meta,
// persists it, and broadcasts a roster event so every client converges. An OLDER
// daemon that predates the op replies with an error (unknown op), returned here so
// the caller can surface it (skew-safe: banner the refusal, never crash).
func (c *Client) Rename(id, name string) error {
	resp, err := c.request(Control{Op: OpRename, EndpointID: c.endpointID, SessionID: id, Name: name})
	if err != nil {
		return err
	}
	if resp.Op == OpError {
		return errors.New(resp.Error)
	}
	return nil
}

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

// RegrantDevice mints a fresh sealed epoch grant for targetID and converges its registry
// record onto the current machine epoch (PB-KEY-3 / PB-KEY-4). Owner-tier only.
func (c *Client) RegrantDevice(targetID string) error {
	resp, err := c.request(Control{Op: OpDeviceRegrant, EndpointID: c.endpointID, TargetDeviceID: targetID})
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
// all yield Paired=false — fail closed, nothing enrolled — and Failure says WHICH
// (ADR-007 B71(1)): without it the owner cannot tell a gate they closed themselves from
// a window that expired from a daemon that went away.
type PairingResult struct {
	Paired     bool
	DeviceID   string
	Name       string
	Capability string
	Failure    PairFailure // why nothing was enrolled; PairFailNone iff Paired
}

// pairingResultFromControl maps a pushed pair_result payload to a PairingResult. The
// daemon sends a populated Pairing (with DeviceID) on success and one carrying only the
// failure code otherwise.
//
// It NORMALISES the code: anything outside PairFailure's vocabulary — including a payload
// from a daemon that learned a cause this client has not — becomes PairFailInternal. That
// is what makes the closed set a guarantee rather than a convention, and it is why
// cmd/swarm can print from a fixed table without ever rendering bytes off the wire.
func pairingResultFromControl(p *PairingControl) PairingResult {
	if p == nil || p.DeviceID == "" {
		cause := PairFailInternal
		if p != nil {
			if f := PairFailure(p.Failure); f != PairFailNone {
				if _, ok := pairFailures[f]; ok {
					cause = f
				}
			}
		}
		return PairingResult{Paired: false, Failure: cause}
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
	ShortCode    string

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
	sess.ShortCode = resp.Pairing.ShortCode
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
	s.deliverResult(PairingResult{Paired: false, Failure: PairFailSessionClosed})
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
	case OpTerminalSnapshot:
		// A peek is a server PUSH stream: the daemon renders on its own schedule and keeps
		// pushing after the one-shot TerminalSnapshot returned. Route it to the peek channel
		// and NEVER the request respCh, or a later request would be answered with a stale
		// screen. The send is non-blocking: a one-shot caller reads one snapshot, so the
		// pushes behind it are dropped rather than backing the read loop up.
		c.mu.Lock()
		ch := c.peekCh
		c.mu.Unlock()
		if ch != nil && ctrl.Terminal != nil {
			select {
			case ch <- *ctrl.Terminal:
			default:
			}
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
// Frames() channel and eventsCh (from the read-loop goroutine, so no send races
// the close), then closes the client.
//
// This is the single convergence point for BOTH teardown paths (agents-tracker-
// 1uq): an explicit Close() closes c.conn, which makes the read loop's
// wire.ReadFrame error out and return, running this deferred call; a dead peer
// (pump eviction, daemon crash/restart) makes wire.ReadFrame fail the same way.
// Either way, closeReadLoop only runs once — it is the sole defer of the sole
// invocation of readLoop (started once, in Dial) — and only after readLoop's for
// loop has permanently exited. dispatchControl's OpEvent case, the only other
// sender to eventsCh, runs synchronously inside that same for loop, so by the
// time this method closes eventsCh no send can still be in flight: closer and
// sender are the same goroutine, strictly sequenced by the loop's exit. No
// sync.Once is needed beyond that ordering.
func (c *Client) closeReadLoop() {
	c.mu.Lock()
	att := c.att
	c.att = nil
	ch := c.eventsCh
	c.eventsCh = nil
	c.mu.Unlock()
	if att != nil {
		att.closeFrames()
	}
	if ch != nil {
		close(ch)
	}
	// Fail-closed pairing teardown: a dropped connection ENDS an in-flight pairing with
	// a non-paired result, so a caller blocked on Result() unblocks and nothing enrolls
	// (the daemon's connection-derived ctx cancels its confirm in parallel).
	c.pairMu.Lock()
	ps := c.pairing
	c.pairing = nil
	c.pairMu.Unlock()
	if ps != nil {
		ps.deliverResult(PairingResult{Paired: false, Failure: PairFailConnectionLost})
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
// the shim can LEGALLY produce. The vt emulator serializes at most ONE JSON run per
// grid cell — the WORST case, reached only when no two adjacent cells share a style.
// Since item 4.3 (agents-tracker-ut0) it MERGES adjacent same-style cells into one
// run, which can only SHRINK a snapshot: a merged run replaces N per-cell runs'
// fixed JSON with a single copy and carries those cells' still-per-cell-clamped
// texts concatenated, never more bytes than the N runs it replaces. The emulator
// also CLAMPS the two free-form fields producer-side (vt.SnapshotTextMax per cell,
// vt.SnapshotTitleMax for the title), so the largest legal snapshot at maxDim x maxDim
// stays bounded by the one-run-per-cell worst case below. The cap is finite (a
// garbage/huge/negative length is rejected without allocation) yet large enough that
// no legal snapshot is rejected. It DEPENDS on the vt producer-side limits it
// references — if those or maxDim change, the cap tracks them.
//
// Derivation (one-run-per-cell worst case; merging always comes in under it):
//   - per cell: a fully-styled Run's fixed JSON fields (~124 B) + the clamped cell
//     text. A cell's Text is ONE grapheme (vt), so at most its base rune is
//     JSON-escapable (< > & -> \uXXXX, 6 B); combining marks are emitted verbatim.
//     So escaped text <= vt.SnapshotTextMax + a small escape slack. A merged run
//     carries the SAME summed cell text under a SINGLE fixed-field overhead, so it
//     never exceeds this per-cell sum.
//   - per line: the {"runs":[ ... ]} array framing.
//   - once: the title (free-form, so every byte may escape to \uXXXX) + the Snap
//     wrapper (version/cols/rows/cursor/keys).
//
// Epic 8 note (N-1 first-paint budget): the one-run-per-cell WORST case is still
// large (~190 MiB at maxDim=1000, every adjacent cell differing in style); item 4.3
// run-merging shrinks the TYPICAL styled snapshot by the run-length factor (~68x on
// a uniform-per-row 200x50 grid), which is where the real first-paint win lands. The
// cap is unchanged: it must still admit the worst case.
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
// p is not copied: wire.ReadFrame allocates a fresh body slice per frame
// (wire.go) and the read loop never touches p again after this call returns
// (checked against the TSnapshot/chunking path too), so ownership transfers
// onto the channel without a redundant copy (R3.3.2).
func (a *Attachment) deliverFrame(done <-chan struct{}, p []byte) {
	select {
	case a.frames <- p:
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
