package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// Item is one stored mailbox entry as the relay serves it: the relay's own
// monotonic storage cursor (untrusted ordering, DISTINCT from the authenticated
// per-epoch seq inside the envelope) and the opaque ciphertext envelope.
type Item struct {
	Cursor   uint64 `json:"cursor"`
	Envelope []byte `json:"envelope"`
}

// PresenceState is a party's coarse reachability as the relay sees it.
type PresenceState string

const (
	// PresenceUnknown means the relay has no live record (e.g. after restart —
	// presence is never persisted).
	PresenceUnknown PresenceState = "unknown"
	// PresenceOffline means the gateway dropped and the silent-push bound elapsed.
	PresenceOffline PresenceState = "offline"
	// PresenceOnline means a live authenticated connection is bound.
	PresenceOnline PresenceState = "online"
)

// PresenceInfo is the presence answer for a routing id.
type PresenceInfo struct {
	State PresenceState `json:"state"`
}

// ClientAuth carries the only key a party ever discloses to the untrusted relay:
// its Ed25519 relay-auth public key, plus a signer over the relay's challenge.
// The signer is a closure so a hardware-gated key never leaves its boundary.
//
// Sign can FAIL (ADR-007 B18(a)): the only production phone-side implementation is
// crypto.KeyStore.SignRelayAuth, which a hardware-gated custody refuses. Swallowing
// that refusal here — or signing nil and letting the relay reject opaquely — would
// re-create one layer up exactly the errorless interface B14 removed.
type ClientAuth struct {
	RelayAuthPub ed25519.PublicKey
	Sign         func(challenge []byte) ([]byte, error)
}

// Conn is a raw, unauthenticated framed connection to the relay over a single
// websocket. Pairing rendezvous rides it (pairing peers are not yet relay-
// registered); authenticated clients wrap it (see Dial).
type Conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex // serialises one request/response exchange
	wmu    sync.Mutex // serialises socket writes: a parked wait writes OUTSIDE mu

	// frames is non-nil on a PUMPED connection: a background reader owns every
	// socket read, so the connection's death is observed while the caller is
	// idle (see Done). A raw Conn reads inline, which is what the adversarial
	// framing paths want.
	frames chan pumpedFrame
	// pending counts requests written whose reply has not been consumed. It is
	// non-zero only after a request abandoned its reply (timeout/cancel): the
	// next request discards that many replies first, so an abandoned exchange
	// can never hand its answer to a later, unrelated one.
	pending int

	// The bounded server-side wait's client half (ADR-007 B7). A wait is the one
	// exchange that does NOT hold mu across write-then-read: it registers a
	// correlated waiter, writes its request, and parks on waitCh while ordinary
	// requests keep flowing through roundtrip. pump routes MsgWaitReply frames
	// straight here, which is the demux that stops a parked wait from
	// head-of-line-blocking the keystrokes it exists to accelerate. §6.0 caps
	// pending waits at one per client, so a single slot is the whole structure.
	waitMu  sync.Mutex
	waitSeq uint64
	waitID  uint64
	waitCh  chan waitReplyBody

	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

// pumpedFrame is one decoded frame, or the decode failure that replaced it.
type pumpedFrame struct {
	tag     MsgType
	payload []byte
	err     error
}

// errConnClosed reports a connection that died underneath a caller. The
// underlying network error is not propagated: every caller's response is the
// same (the connection is gone), and a resilient one reconnects.
var errConnClosed = errors.New("relay: connection closed")

// dialConn opens one websocket. hc is the dial client a security policy built
// (nil for the policy-free paths, which take the websocket package's default).
func dialConn(ctx context.Context, url string, hc *http.Client, pumped bool) (*Conn, error) {
	var opts *websocket.DialOptions
	if hc != nil {
		opts = &websocket.DialOptions{HTTPClient: hc}
	}
	ws, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxFrame + 64)
	cctx, cancel := context.WithCancel(context.Background())
	c := &Conn{ws: ws, ctx: cctx, cancel: cancel, done: make(chan struct{})}
	if pumped {
		c.frames = make(chan pumpedFrame, 1)
		go c.pump()
	}
	return c, nil
}

// pump owns every read on a pumped connection and exits (closing Done) as soon
// as the socket dies, which is what makes an idle drop observable.
func (c *Conn) pump() {
	defer c.markDone()
	for {
		mt, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		var f pumpedFrame
		if mt != websocket.MessageBinary {
			f.err = fmt.Errorf("relay: unexpected websocket message type %v", mt)
		} else {
			f.tag, f.payload, f.err = ReadFrame(bytes.NewReader(data))
		}
		// A wait reply is CORRELATED by its request id and handed straight to the
		// parked waiter, so it neither queues behind nor jumps ahead of the
		// serialised request/reply exchanges (ADR-007 B7). It also never blocks the
		// pump, which is what keeps an outstanding wait from stalling the socket.
		if f.err == nil && f.tag == MsgWaitReply {
			c.deliverWait(f.payload)
			continue
		}
		select {
		case c.frames <- f:
		case <-c.ctx.Done():
			return
		}
		if f.err != nil {
			return
		}
	}
}

func (c *Conn) markDone() { c.doneOnce.Do(func() { close(c.done) }) }

// Done is closed when the connection is no longer usable. On a pumped
// connection (Dial, DialSecure) that happens as soon as the peer or the network
// drops it; on a raw one it happens at Close.
func (c *Conn) Done() <-chan struct{} { return c.done }

// DialRaw opens an unauthenticated framed connection (rendezvous + adversarial
// framing use it).
//
// It applies NO transport-security policy: the URL is dialed as given. Production
// callers use DialRawSecure -- see the note on Dial.
func DialRaw(ctx context.Context, url string) (*Conn, error) {
	return dialConn(ctx, url, nil, false)
}

// DialRawSecure is DialRaw under a transport-security policy (PB-NET-2). It is the
// pairing rendezvous's entry point: no relay-auth key is disclosed there, but it is the
// first packet either end sends to a URL a scanned QR chose, so the same refusal applies
// and it is decided before a socket is opened.
func DialRawSecure(ctx context.Context, url string, sec Security) (*Conn, error) {
	cfg, err := sec.resolve(url)
	if err != nil {
		return nil, err
	}
	return dialConn(ctx, url, sec.httpClient(cfg), false)
}

func (c *Conn) writeFrame(ctx context.Context, tag MsgType, payload []byte) error {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, tag, payload); err != nil {
		return err
	}
	// wmu, not mu: a parked wait writes its request and its cancellation without
	// holding the request/reply lock, so two writers can reach the socket at once.
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.Write(ctx, websocket.MessageBinary, buf.Bytes())
}

func (c *Conn) readFrame(ctx context.Context) (MsgType, []byte, error) {
	if c.frames == nil {
		mt, data, err := c.ws.Read(ctx)
		if err != nil {
			return 0, nil, err
		}
		if mt != websocket.MessageBinary {
			return 0, nil, fmt.Errorf("relay: unexpected websocket message type %v", mt)
		}
		return ReadFrame(bytes.NewReader(data))
	}
	// A frame already delivered by the pump wins over a concurrently-observed
	// death, so the last reply before a drop is not discarded.
	select {
	case f := <-c.frames:
		return f.tag, f.payload, f.err
	default:
	}
	select {
	case f := <-c.frames:
		return f.tag, f.payload, f.err
	case <-c.done:
		return 0, nil, errConnClosed
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// WriteMsg sends one raw framed message using the connection's own context.
func (c *Conn) WriteMsg(tag MsgType, payload []byte) error {
	return c.writeFrame(c.ctx, tag, payload)
}

// ReadMsg receives one raw framed message using the connection's own context.
func (c *Conn) ReadMsg() (MsgType, []byte, error) { return c.readFrame(c.ctx) }

// CloseNow severs the connection WITHOUT the websocket close handshake, for a caller
// that is ABANDONING the exchange rather than finishing it.
//
// Close below cancels the connection's own context and then performs the polite close,
// which waits up to five seconds for the peer's close frame -- and cannot observe one,
// because cancelling the context has already stopped the reader. So an aborted
// connection pays that timeout in full, every time. That was invisible while nothing
// waited on the teardown; it is five seconds on the caller's shutdown path as soon as
// something does.
//
// It shares Close's once, so whichever teardown runs first decides and the other is a
// no-op: a graceful Close already in flight is not turned into an abort by a late
// CloseNow, or the reverse.
func (c *Conn) CloseNow() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.closeErr = c.ws.CloseNow()
		c.markDone()
	})
	return c.closeErr
}

// Close severs the connection. It is idempotent.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.closeErr = c.ws.Close(websocket.StatusNormalClosure, "")
		c.markDone()
	})
	return c.closeErr
}

// roundtrip writes one request frame and reads exactly one reply, mapping an
// r_error reply to its sentinel error.
//
// A caller that abandons its reply (context deadline or cancellation) leaves the
// exchange outstanding rather than tearing the connection down; the next caller
// discards the replies owed to those abandoned requests before claiming its own,
// so a slow answer is never mistaken for the answer to a later question.
func (c *Conn) roundtrip(ctx context.Context, tag MsgType, req any) (json.RawMessage, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writeFrame(ctx, tag, body); err != nil {
		return nil, err
	}
	c.pending++
	for {
		rtag, payload, err := c.readFrame(ctx)
		if err != nil {
			return nil, err
		}
		c.pending--
		if c.pending > 0 {
			continue // owed to an earlier request that gave up on its reply
		}
		if rtag == MsgError {
			return nil, decodeError(payload)
		}
		return json.RawMessage(payload), nil
	}
}

// control issues a generic MsgRelay control op with a JSON body.
func (c *Conn) control(ctx context.Context, op string, req map[string]any) (json.RawMessage, error) {
	if req == nil {
		req = map[string]any{}
	}
	req["op"] = op
	return c.roundtrip(ctx, MsgRelay, req)
}

func decodeError(payload []byte) error {
	var eb errorBody
	_ = json.Unmarshal(payload, &eb)
	if e, ok := codeToErr[eb.Code]; ok {
		return e
	}
	if eb.Message != "" {
		return fmt.Errorf("relay: %s", eb.Message)
	}
	if eb.Code != "" {
		return fmt.Errorf("relay: %s", eb.Code)
	}
	return errors.New("relay: server error")
}

// Hello negotiates the protocol version and the intersected capability set. An
// unsupported version is refused (returns a non-nil error), not downgraded.
func (c *Conn) Hello(ctx context.Context, version int, caps []string) (int, []string, error) {
	resp, err := c.control(ctx, "hello", map[string]any{"version": version, "caps": caps})
	if err != nil {
		return 0, nil, err
	}
	var r struct {
		Version int      `json:"version"`
		Caps    []string `json:"caps"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return 0, nil, err
	}
	return r.Version, r.Caps, nil
}

// RendezvousCreate opens a two-party pairing rendezvous keyed by id.
func (c *Conn) RendezvousCreate(ctx context.Context, id string) error {
	_, err := c.control(ctx, "rendezvous_create", map[string]any{"id": id})
	return err
}

// RendezvousClaim joins an existing rendezvous as its single second participant.
func (c *Conn) RendezvousClaim(ctx context.Context, id string) error {
	_, err := c.control(ctx, "rendezvous_claim", map[string]any{"id": id})
	return err
}

// RendezvousSend forwards opaque bytes to the other participant.
func (c *Conn) RendezvousSend(ctx context.Context, id string, msg []byte) error {
	_, err := c.control(ctx, "rendezvous_send", map[string]any{"id": id, "data": msg})
	return err
}

// RendezvousRecv blocks for the next opaque message from the other participant.
func (c *Conn) RendezvousRecv(ctx context.Context) ([]byte, error) {
	resp, err := c.control(ctx, "rendezvous_recv", nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}

// RendezvousComplete burns the rendezvous id (single use).
func (c *Conn) RendezvousComplete(ctx context.Context, id string) error {
	_, err := c.control(ctx, "rendezvous_complete", map[string]any{"id": id})
	return err
}

// Client is an authenticated relay connection bound to RoutingID(relay-auth pub).
type Client struct {
	conn *Conn
	rid  string
}

// Dial opens a connection and completes the Ed25519 signed-challenge handshake,
// binding the connection to RoutingID(auth.RelayAuthPub). A revoked key, a rate
// refusal, or a bad signature returns a non-nil error and no Client.
//
// It applies no transport-security policy: the URL is dialed as given, so the
// auth_init frame -- which carries auth.RelayAuthPub in full -- is readable by any
// observer of a ws:// hop (ADR-007 B37). It remains exported for the test rigs that
// stand up an in-process relay and dial it deliberately; NO production caller may
// reach it, which internal/remote/transport's productiondial_test.go enforces at the
// call site. Production dials go through DialSecure, machine-side ones under
// MachineSecurity.
func Dial(ctx context.Context, url string, auth ClientAuth) (*Client, error) {
	conn, err := dialConn(ctx, url, nil, true)
	if err != nil {
		return nil, err
	}
	return authenticate(ctx, conn, auth)
}

// DialSecure is Dial under an explicit transport-security policy (PB-NET-2):
// TLS verified against the platform's stated trust roots by default, a pinned
// certificate as a per-connection opt-in for a self-hosted relay, and cleartext
// refused. The policy is decided before any packet is sent, so a refusal returns
// ErrCleartextRefused/ErrPinMismatch without a connection attempt and never
// yields a Client. It is re-decided on every redirect hop, so a relay cannot
// answer the upgrade with a 302 into cleartext.
func DialSecure(ctx context.Context, url string, auth ClientAuth, sec Security) (*Client, error) {
	cfg, err := sec.resolve(url)
	if err != nil {
		return nil, err
	}
	conn, err := dialConn(ctx, url, sec.httpClient(cfg), true)
	if err != nil {
		return nil, err
	}
	return authenticate(ctx, conn, auth)
}

// authenticate runs the signed-challenge handshake over an open connection.
func authenticate(ctx context.Context, conn *Conn, auth ClientAuth) (*Client, error) {
	rid := RoutingID(auth.RelayAuthPub)

	resp, err := conn.control(ctx, "auth_init", map[string]any{"relay_auth_pub": []byte(auth.RelayAuthPub)})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var chal struct {
		Nonce []byte `json:"nonce"`
	}
	if err := json.Unmarshal(resp, &chal); err != nil {
		_ = conn.Close()
		return nil, err
	}

	sig, err := auth.Sign(AuthChallengeMessage(chal.Nonce, rid))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp2, err := conn.control(ctx, "auth_resp", map[string]any{"signature": sig})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var ok struct {
		RoutingID string `json:"routing_id"`
	}
	if err := json.Unmarshal(resp2, &ok); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Client{conn: conn, rid: ok.RoutingID}, nil
}

// RoutingID returns the connection's bound routing id.
func (c *Client) RoutingID() string { return c.rid }

// Done is closed when the underlying connection dies, so a caller can notice a
// drop without issuing a request.
func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

// Close severs the connection. It is idempotent.
func (c *Client) Close() error { return c.conn.Close() }

// AuthorizeDevice pairs this machine with a device's relay-auth key, authorizing
// mailbox/push routing between the two.
func (c *Client) AuthorizeDevice(ctx context.Context, devicePub ed25519.PublicKey) error {
	_, err := c.conn.control(ctx, "authorize_device", map[string]any{"device_pub": []byte(devicePub)})
	return err
}

// MailboxAppend stores an opaque envelope in target's mailbox and returns the
// relay's assigned storage cursor.
func (c *Client) MailboxAppend(ctx context.Context, target string, env []byte) (uint64, error) {
	resp, err := c.conn.roundtrip(ctx, MsgMailboxAppend, map[string]any{"target": target, "envelope": env})
	if err != nil {
		return 0, err
	}
	var r struct {
		Cursor uint64 `json:"cursor"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return 0, err
	}
	return r.Cursor, nil
}

// MailboxRead returns items whose storage cursor is strictly greater than cursor.
// The reply is a bounded first page (CR-4): on a large backlog it returns a
// subset that fits one frame rather than tearing the connection. Callers that
// need to drain a backlog and observe whether more remains use MailboxReadPage.
func (c *Client) MailboxRead(ctx context.Context, cursor uint64) ([]Item, error) {
	items, _, err := c.MailboxReadPage(ctx, cursor, 0)
	return items, err
}

// MailboxReadPage returns at most a bounded page of items whose storage cursor is
// strictly greater than cursor, plus has_more indicating whether further items
// remain past the page (CR-4). limit caps the page's item count; limit <= 0 asks
// for the server's own default page bound. A page always fits under MaxFrame, so
// draining an arbitrarily large backlog is a loop of MailboxReadPage + MailboxAck
// that never overflows a frame.
func (c *Client) MailboxReadPage(ctx context.Context, cursor uint64, limit int) ([]Item, bool, error) {
	resp, err := c.conn.control(ctx, "mailbox_read", map[string]any{"cursor": cursor, "limit": limit})
	if err != nil {
		return nil, false, err
	}
	var r struct {
		Items   []Item `json:"items"`
		HasMore bool   `json:"has_more"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return nil, false, err
	}
	return r.Items, r.HasMore, nil
}

// MailboxWait blocks SERVER-side until at least one item past cursor exists in
// this client's own mailbox, and returns that bounded page — the same
// {items, has_more} shape as MailboxReadPage. At Config.MaxServerWait it returns
// an empty page and a nil error, so a caller's loop is a wait, not a poll.
//
// It returns the ITEMS, not a bare signal: a signal a caller then had to read
// would cost two metered ops per batch, which §6.0's inbound drain budget cannot
// absorb. It meters exactly ONCE per call however many items come back, so
// batching under load actually buys something.
//
// §6.0 caps pending waits per client at one. A second concurrent wait is refused
// with ErrWaitInProgress, never queued — a queue would make cancellation
// ambiguous and let one connection pin unbounded server-side wait state.
func (c *Client) MailboxWait(ctx context.Context, cursor uint64) ([]Item, bool, error) {
	return c.conn.mailboxWait(ctx, cursor)
}

func (c *Conn) mailboxWait(ctx context.Context, cursor uint64) ([]Item, bool, error) {
	c.waitMu.Lock()
	if c.waitCh != nil {
		c.waitMu.Unlock()
		return nil, false, ErrWaitInProgress
	}
	c.waitSeq++
	id := c.waitSeq
	ch := make(chan waitReplyBody, 1)
	c.waitID, c.waitCh = id, ch
	c.waitMu.Unlock()

	defer func() {
		c.waitMu.Lock()
		if c.waitID == id {
			c.waitCh = nil
		}
		c.waitMu.Unlock()
	}()

	body, err := json.Marshal(map[string]any{"op": "mailbox_wait", "cursor": cursor, "wait_id": id})
	if err != nil {
		return nil, false, err
	}
	if err := c.writeFrame(ctx, MsgRelay, body); err != nil {
		return nil, false, err
	}
	select {
	case r := <-ch:
		if r.Code != "" {
			return nil, false, errForCode(r.Code)
		}
		return r.Items, r.HasMore, nil
	case <-ctx.Done():
		// Release the SERVER's slot too. An orphaned wait would hold the single
		// pending-wait slot until its ceiling elapsed, so the next wait — the one a
		// reconnecting live tail parks — would be refused and typing would be dead
		// for the remainder of the ceiling.
		c.cancelWait(id)
		return nil, false, fmt.Errorf("relay: mailbox wait cancelled: %w", ctx.Err())
	case <-c.done:
		return nil, false, errConnClosed
	}
}

// cancelWait withdraws a parked wait. It is fire-and-forget and carries the wait
// id: the withdrawal and any later wait travel the same stream in order, so the
// server frees the slot before it sees the replacement, and the correlation id
// makes the abandoned wait's reply discardable rather than mis-delivered.
func (c *Conn) cancelWait(id uint64) {
	body, err := json.Marshal(map[string]any{"op": "mailbox_wait_cancel", "wait_id": id})
	if err != nil {
		return
	}
	_ = c.writeFrame(c.ctx, MsgRelay, body) // the caller's ctx is already done
}

// deliverWait routes one MsgWaitReply to the parked waiter it names. A reply for
// any other id is dropped: it belongs to a wait this client already withdrew.
func (c *Conn) deliverWait(payload []byte) {
	var r waitReplyBody
	if err := json.Unmarshal(payload, &r); err != nil {
		return
	}
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	if c.waitCh == nil || c.waitID != r.WaitID {
		return
	}
	// This send cannot block, which matters because it happens on the read pump
	// under waitMu: the channel is created per wait with capacity 1, and it is
	// cleared here under the same lock, so even a relay that replied twice to one
	// wait id finds waitCh nil on the second frame and is dropped above.
	c.waitCh <- r
	c.waitCh = nil
}

// MailboxAck compacts away every item at or below cursor.
func (c *Client) MailboxAck(ctx context.Context, cursor uint64) error {
	_, err := c.conn.control(ctx, "mailbox_ack", map[string]any{"cursor": cursor})
	return err
}

// TokenRegister registers (or refreshes) this device's APNs push token.
func (c *Client) TokenRegister(ctx context.Context, token string) error {
	_, err := c.conn.control(ctx, "token_register", map[string]any{"token": token})
	return err
}

// TokenDelete stops push delivery to this device.
func (c *Client) TokenDelete(ctx context.Context) error {
	_, err := c.conn.control(ctx, "token_delete", nil)
	return err
}

// Presence returns target's coarse reachability.
func (c *Client) Presence(ctx context.Context, target string) (PresenceInfo, error) {
	resp, err := c.conn.control(ctx, "presence", map[string]any{"target": target})
	if err != nil {
		return PresenceInfo{}, err
	}
	var p PresenceInfo
	if err := json.Unmarshal(resp, &p); err != nil {
		return PresenceInfo{}, err
	}
	return p, nil
}

// PushTrigger forwards an opaque wake envelope to target's registered push token.
func (c *Client) PushTrigger(ctx context.Context, target string, env []byte) error {
	_, err := c.conn.control(ctx, "push_trigger", map[string]any{"target": target, "envelope": env})
	return err
}

// DeviceRevoke de-authorizes target's relay-auth registration and purges its
// relay-side mailbox.
func (c *Client) DeviceRevoke(ctx context.Context, target string) error {
	_, err := c.conn.control(ctx, "device_revoke", map[string]any{"target": target})
	return err
}
