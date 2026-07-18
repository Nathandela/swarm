package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
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
type ClientAuth struct {
	RelayAuthPub ed25519.PublicKey
	Sign         func(challenge []byte) []byte
}

// Conn is a raw, unauthenticated framed connection to the relay over a single
// websocket. Pairing rendezvous rides it (pairing peers are not yet relay-
// registered); authenticated clients wrap it (see Dial).
type Conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex // serialises one request/response exchange
}

func dialConn(ctx context.Context, url string) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxFrame + 64)
	cctx, cancel := context.WithCancel(context.Background())
	return &Conn{ws: ws, ctx: cctx, cancel: cancel}, nil
}

// DialRaw opens an unauthenticated framed connection (rendezvous + adversarial
// framing use it).
func DialRaw(ctx context.Context, url string) (*Conn, error) { return dialConn(ctx, url) }

func (c *Conn) writeFrame(ctx context.Context, tag MsgType, payload []byte) error {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, tag, payload); err != nil {
		return err
	}
	return c.ws.Write(ctx, websocket.MessageBinary, buf.Bytes())
}

func (c *Conn) readFrame(ctx context.Context) (MsgType, []byte, error) {
	mt, data, err := c.ws.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	if mt != websocket.MessageBinary {
		return 0, nil, fmt.Errorf("relay: unexpected websocket message type %v", mt)
	}
	return ReadFrame(bytes.NewReader(data))
}

// WriteMsg sends one raw framed message using the connection's own context.
func (c *Conn) WriteMsg(tag MsgType, payload []byte) error {
	return c.writeFrame(c.ctx, tag, payload)
}

// ReadMsg receives one raw framed message using the connection's own context.
func (c *Conn) ReadMsg() (MsgType, []byte, error) { return c.readFrame(c.ctx) }

// Close severs the connection.
func (c *Conn) Close() error {
	c.cancel()
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

// roundtrip writes one request frame and reads exactly one reply, mapping an
// r_error reply to its sentinel error.
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
	rtag, payload, err := c.readFrame(ctx)
	if err != nil {
		return nil, err
	}
	if rtag == MsgError {
		return nil, decodeError(payload)
	}
	return json.RawMessage(payload), nil
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
func Dial(ctx context.Context, url string, auth ClientAuth) (*Client, error) {
	conn, err := dialConn(ctx, url)
	if err != nil {
		return nil, err
	}
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

	sig := auth.Sign(AuthChallengeMessage(chal.Nonce, rid))
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

// Close severs the connection.
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
func (c *Client) MailboxRead(ctx context.Context, cursor uint64) ([]Item, error) {
	resp, err := c.conn.control(ctx, "mailbox_read", map[string]any{"cursor": cursor})
	if err != nil {
		return nil, err
	}
	var r struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return nil, err
	}
	return r.Items, nil
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
