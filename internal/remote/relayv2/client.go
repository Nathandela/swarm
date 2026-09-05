// Package relayv2 is the native websocket client for the bounded relay-v2
// Worker. It carries opaque end-to-end ciphertext and pairing Noise frames; it
// does not implement or adapt the retired relay-v1 codec.
package relayv2

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/coder/websocket"
	"golang.org/x/crypto/hkdf"
)

const (
	maxMessage          = 1 << 20
	maxQueuedEventBytes = 1 << 20
	maxCiphertextBytes  = (maxMessage - 1024) * 3 / 4
	maxPendingRequests  = 64
	defaultCallTimeout  = 10 * time.Second
	defaultDialTimeout  = 10 * time.Second
)

type Role string
type Purpose string

const (
	RoleMachine Role = "machine"
	RolePhone   Role = "phone"

	PurposeControl Purpose = "control"
	PurposeStream  Purpose = "stream"
)

type Profile struct {
	RelayURL          string
	MachineRID        string
	OperatorNamespace string
	Security          relay.Security
}

type Auth struct {
	PublicKey ed25519.PublicKey
	Sign      func([]byte) ([]byte, error)
	Role      Role
	Purpose   Purpose
}

type Binding struct {
	MachineRID string
	PeerRID    string
	Generation uint64
}

type Checkpoint struct {
	Incarnation string
	Cursor      uint64
}

type AppendResult struct {
	Cursor  uint64
	Deduped bool
}

type Delivery struct {
	Cursor     uint64
	MessageID  string
	Ciphertext []byte
}

type ProtocolError struct{ Code string }

func (e *ProtocolError) Error() string { return "relay v2: " + e.Code }

type wireFrame struct {
	V           int    `json:"v"`
	Type        string `json:"type"`
	RequestID   string `json:"request_id"`
	Code        string `json:"code"`
	Nonce       string `json:"nonce"`
	Home        string `json:"home"`
	ExpiresAt   string `json:"expires_at"`
	RID         string `json:"rid"`
	Role        string `json:"role"`
	Purpose     string `json:"purpose"`
	Generation  string `json:"generation"`
	PhoneRID    string `json:"phone_rid"`
	PeerRID     string `json:"peer_rid"`
	Cursor      string `json:"cursor"`
	After       string `json:"after"`
	Incarnation string `json:"incarnation"`
	MessageID   string `json:"msg_id"`
	Ciphertext  string `json:"ciphertext"`
	Deduped     bool   `json:"deduped"`
	Ceremony    string `json:"ceremony"`
}

type Conn struct {
	ws         *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	role       Role
	purpose    Purpose
	machineRID string
	rid        string

	writeGate chan struct{}
	mu        sync.Mutex
	pending   map[string]chan wireFrame
	err       error
	done      chan struct{}
	nextID    atomic.Uint64

	queueMu       sync.Mutex
	deliveryBytes int
	pairBytes     int
	deliveries    chan queuedFrame
	pairFrames    chan queuedFrame
	peerSPKI      []byte
}

type queuedFrame struct {
	frame wireFrame
	size  int
}

var token = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var errorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func Dial(ctx context.Context, profile Profile, auth Auth) (*Conn, error) {
	if profile.OperatorNamespace == "" {
		return nil, errors.New("relay v2: operator namespace is required")
	}
	if !validRID(profile.MachineRID) || len(auth.PublicKey) != ed25519.PublicKeySize || auth.Sign == nil {
		return nil, errors.New("relay v2: invalid profile or auth")
	}
	if auth.Role != RoleMachine && auth.Role != RolePhone {
		return nil, errors.New("relay v2: invalid role")
	}
	if auth.Purpose != PurposeStream && (auth.Role != RoleMachine || auth.Purpose != PurposeControl) {
		return nil, errors.New("relay v2: invalid purpose")
	}
	endpoint, err := relayEndpoint(profile.RelayURL, "/v2/ws", url.Values{"machine_rid": {profile.MachineRID}})
	if err != nil {
		return nil, err
	}
	hc, observer, err := secureHTTPClient(profile.Security, endpoint)
	if err != nil {
		return nil, err
	}
	c, err := dialRaw(ctx, endpoint, hc)
	if err != nil {
		return nil, err
	}
	c.peerSPKI = observer.get()
	c.role, c.purpose, c.machineRID = auth.Role, auth.Purpose, profile.MachineRID
	c.rid = RoutingID(auth.PublicKey)
	challenge, err := c.call(ctx, "CHALLENGE", map[string]any{
		"v": 2, "type": "AUTH_INIT", "role": auth.Role, "purpose": auth.Purpose, "pub": encode64(auth.PublicKey),
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	wantHome := HomeID(profile.OperatorNamespace, profile.MachineRID)
	if challenge.Home != wantHome {
		c.Close()
		return nil, errors.New("relay v2: challenge named a non-canonical home")
	}
	nonce, err := decode64(challenge.Nonce, 32)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("relay v2: challenge nonce: %w", err)
	}
	expiresAt, err := parseUint64(challenge.ExpiresAt)
	if err != nil || expiresAt <= uint64(time.Now().UnixMilli()) || expiresAt > uint64(time.Now().Add(time.Minute).UnixMilli()) {
		c.Close()
		return nil, errors.New("relay v2: invalid challenge expiry")
	}
	signature, err := auth.Sign(authMessage(nonce, c.rid, wantHome, auth.Role, auth.Purpose))
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("relay v2: sign auth challenge: %w", err)
	}
	if len(signature) != ed25519.SignatureSize {
		c.Close()
		return nil, errors.New("relay v2: signer returned invalid signature length")
	}
	authed, err := c.call(ctx, "AUTHENTICATED", map[string]any{
		"v": 2, "type": "AUTH_PROVE", "signature": encode64(signature),
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	if authed.RID != c.rid || authed.Role != string(auth.Role) || authed.Purpose != string(auth.Purpose) || authed.Home != wantHome {
		c.Close()
		return nil, errors.New("relay v2: authenticated response binding mismatch")
	}
	if auth.Role == RolePhone {
		generation, err := parseUint64(authed.Generation)
		if err != nil || generation == 0 {
			c.Close()
			return nil, errors.New("relay v2: invalid authenticated generation")
		}
	} else if authed.Generation != "" {
		c.Close()
		return nil, errors.New("relay v2: machine auth response carried a phone generation")
	}
	return c, nil
}

func dialRaw(ctx context.Context, endpoint string, hc *http.Client) (*Conn, error) {
	dialCtx, cancelDial := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancelDial()
	var options *websocket.DialOptions
	if hc != nil {
		options = &websocket.DialOptions{HTTPClient: hc}
	}
	ws, _, err := websocket.Dial(dialCtx, endpoint, options)
	if err != nil {
		return nil, fmt.Errorf("relay v2: dial: %w", err)
	}
	ws.SetReadLimit(maxMessage)
	readCtx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		ws: ws, ctx: readCtx, cancel: cancel, pending: make(map[string]chan wireFrame), done: make(chan struct{}),
		writeGate: make(chan struct{}, 1), deliveries: make(chan queuedFrame, 64), pairFrames: make(chan queuedFrame, 8),
	}
	c.writeGate <- struct{}{}
	go c.readLoop()
	return c, nil
}

func (c *Conn) Close() {
	c.fail(errors.New("relay v2: closed"))
}

func (c *Conn) Done() <-chan struct{} { return c.done }

// PeerSPKI is the SHA-256 digest of the peer's SubjectPublicKeyInfo on a TLS
// dial. Pairing compares it with the pin delivered inside the Noise transcript.
func (c *Conn) PeerSPKI() []byte { return append([]byte(nil), c.peerSPKI...) }

func (c *Conn) readLoop() {
	for {
		kind, data, err := c.ws.Read(c.ctx)
		if err != nil {
			c.fail(err)
			return
		}
		if kind != websocket.MessageText {
			c.fail(errors.New("relay v2: non-text websocket message"))
			return
		}
		frame, err := decodeFrame(data)
		if err != nil {
			c.fail(err)
			return
		}
		switch frame.Type {
		case "DELIVER":
			if !c.enqueueDelivery(queuedFrame{frame: frame, size: len(data)}) {
				c.fail(errors.New("relay v2: delivery queue overflow"))
				return
			}
			continue
		case "PAIR_FRAME":
			if !c.enqueuePair(queuedFrame{frame: frame, size: len(data)}) {
				c.fail(errors.New("relay v2: pairing queue overflow"))
				return
			}
			continue
		}
		c.mu.Lock()
		response := c.pending[frame.RequestID]
		if response != nil {
			delete(c.pending, frame.RequestID)
		}
		c.mu.Unlock()
		if response == nil {
			c.fail(errors.New("relay v2: unsolicited response"))
			return
		}
		response <- frame
	}
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	first := false
	if c.err == nil {
		c.err = err
		first = true
	}
	c.mu.Unlock()
	if first {
		c.cancel()
		_ = c.ws.CloseNow()
		close(c.done)
	}
}

func (c *Conn) enqueueDelivery(item queuedFrame) bool {
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	if c.deliveryBytes+item.size > maxQueuedEventBytes {
		return false
	}
	select {
	case c.deliveries <- item:
		c.deliveryBytes += item.size
		return true
	default:
		return false
	}
}

func (c *Conn) enqueuePair(item queuedFrame) bool {
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	if c.pairBytes+item.size > maxQueuedEventBytes {
		return false
	}
	select {
	case c.pairFrames <- item:
		c.pairBytes += item.size
		return true
	default:
		return false
	}
}

func (c *Conn) releaseDelivery(size int) {
	c.queueMu.Lock()
	c.deliveryBytes -= size
	c.queueMu.Unlock()
}

func (c *Conn) releasePair(size int) {
	c.queueMu.Lock()
	c.pairBytes -= size
	c.queueMu.Unlock()
}

func (c *Conn) connectionError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return errors.New("relay v2: connection closed")
}

func (c *Conn) call(ctx context.Context, want string, request map[string]any) (wireFrame, error) {
	callCtx, cancelCall := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancelCall()
	id := "c" + strconv.FormatUint(c.nextID.Add(1), 10)
	request["request_id"] = id
	body, err := json.Marshal(request)
	if err != nil {
		return wireFrame{}, err
	}
	response := make(chan wireFrame, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return wireFrame{}, err
	}
	if len(c.pending) >= maxPendingRequests {
		c.mu.Unlock()
		return wireFrame{}, errors.New("relay v2: too many pending requests")
	}
	c.pending[id] = response
	c.mu.Unlock()
	select {
	case <-c.writeGate:
	case <-callCtx.Done():
		c.fail(callCtx.Err())
		return wireFrame{}, callCtx.Err()
	case <-c.done:
		return wireFrame{}, c.connectionError()
	}
	err = c.ws.Write(callCtx, websocket.MessageText, body)
	c.writeGate <- struct{}{}
	if err != nil {
		c.fail(err)
		return wireFrame{}, err
	}
	select {
	case frame := <-response:
		if frame.Type == "ERROR" {
			return wireFrame{}, &ProtocolError{Code: frame.Code}
		}
		if frame.Type != want {
			c.fail(fmt.Errorf("relay v2: got %s, want %s", frame.Type, want))
			return wireFrame{}, c.connectionError()
		}
		return frame, nil
	case <-callCtx.Done():
		c.fail(callCtx.Err())
		return wireFrame{}, callCtx.Err()
	case <-c.done:
		return wireFrame{}, c.connectionError()
	}
}

func (c *Conn) Authorize(ctx context.Context, phonePub ed25519.PublicKey, consent []byte) (Binding, error) {
	if c.role != RoleMachine || c.purpose != PurposeControl || len(phonePub) != ed25519.PublicKeySize {
		return Binding{}, errors.New("relay v2: authorize requires machine control")
	}
	frame, err := c.call(ctx, "AUTHORIZED", map[string]any{
		"v": 2, "type": "AUTHORIZE", "phone_pub": encode64(phonePub), "consent": encode64(consent),
	})
	if err != nil {
		return Binding{}, err
	}
	generation, err := parseUint64(frame.Generation)
	if err != nil || generation == 0 || !validRID(frame.PhoneRID) || frame.PhoneRID != RoutingID(phonePub) {
		return Binding{}, errors.New("relay v2: invalid authorization response")
	}
	return Binding{MachineRID: c.machineRID, PeerRID: frame.PhoneRID, Generation: generation}, nil
}

func (c *Conn) Append(ctx context.Context, binding Binding, messageID string, ciphertext []byte) (AppendResult, error) {
	if c.purpose != PurposeStream || !token.MatchString(messageID) || len(ciphertext) == 0 || len(ciphertext) > maxCiphertextBytes {
		return AppendResult{}, errors.New("relay v2: invalid append")
	}
	peer, err := c.bindingPeer(binding)
	if err != nil {
		return AppendResult{}, err
	}
	frame, err := c.call(ctx, "APPENDED", map[string]any{
		"v": 2, "type": "APPEND", "peer_rid": peer, "generation": formatUint64(binding.Generation), "msg_id": messageID, "ciphertext": encode64(ciphertext),
	})
	if err != nil {
		return AppendResult{}, err
	}
	cursor, err := parseUint64(frame.Cursor)
	if err != nil || cursor == 0 || frame.PeerRID != peer || frame.Generation != formatUint64(binding.Generation) {
		return AppendResult{}, errors.New("relay v2: invalid append response")
	}
	return AppendResult{Cursor: cursor, Deduped: frame.Deduped}, nil
}

func (c *Conn) Subscribe(ctx context.Context, binding Binding, checkpoint Checkpoint) (*Subscription, error) {
	if c.purpose != PurposeStream || (checkpoint.Incarnation != "" && !validIncarnation(checkpoint.Incarnation)) {
		return nil, errors.New("relay v2: invalid subscription")
	}
	peer, err := c.bindingPeer(binding)
	if err != nil {
		return nil, err
	}
	frame, err := c.call(ctx, "SUBSCRIBED", map[string]any{
		"v": 2, "type": "SUBSCRIBE", "peer_rid": peer, "generation": formatUint64(binding.Generation), "incarnation": checkpoint.Incarnation, "after": formatUint64(checkpoint.Cursor),
	})
	if err != nil {
		return nil, err
	}
	after, err := parseUint64(frame.After)
	if err != nil || after != checkpoint.Cursor || frame.PeerRID != peer || frame.Generation != formatUint64(binding.Generation) || !validIncarnation(frame.Incarnation) {
		return nil, errors.New("relay v2: invalid subscription response")
	}
	return &Subscription{conn: c, binding: binding, peer: peer, incarnation: frame.Incarnation}, nil
}

func (c *Conn) Revoke(ctx context.Context, binding Binding) error {
	peer, err := c.bindingPeer(binding)
	if err != nil {
		return err
	}
	frame, err := c.call(ctx, "REVOKED", map[string]any{
		"v": 2, "type": "REVOKE", "peer_rid": peer, "generation": formatUint64(binding.Generation),
	})
	if err != nil {
		return err
	}
	if frame.PeerRID != peer {
		return errors.New("relay v2: invalid revoke response")
	}
	return nil
}

func (c *Conn) bindingPeer(binding Binding) (string, error) {
	if binding.MachineRID != c.machineRID || !validRID(binding.PeerRID) || binding.Generation == 0 {
		return "", errors.New("relay v2: invalid binding")
	}
	if c.role == RolePhone {
		if c.rid != binding.PeerRID {
			return "", errors.New("relay v2: binding belongs to another phone")
		}
		return binding.MachineRID, nil
	}
	if c.rid != binding.MachineRID {
		return "", errors.New("relay v2: binding belongs to another machine")
	}
	return binding.PeerRID, nil
}

type Subscription struct {
	conn        *Conn
	binding     Binding
	peer        string
	incarnation string
}

func (s *Subscription) Incarnation() string { return s.incarnation }

func (s *Subscription) Recv(ctx context.Context) (Delivery, error) {
	select {
	case queued := <-s.conn.deliveries:
		s.conn.releaseDelivery(queued.size)
		frame := queued.frame
		cursor, err := parseUint64(frame.Cursor)
		if err != nil || frame.PeerRID != s.peer || frame.Generation != formatUint64(s.binding.Generation) || frame.Incarnation != s.incarnation || !validIncarnation(frame.Incarnation) || !token.MatchString(frame.MessageID) {
			return Delivery{}, errors.New("relay v2: invalid delivery")
		}
		ciphertext, err := decode64(frame.Ciphertext, -1)
		if err != nil || len(ciphertext) == 0 || len(ciphertext) > maxCiphertextBytes {
			return Delivery{}, errors.New("relay v2: invalid delivery ciphertext")
		}
		return Delivery{Cursor: cursor, MessageID: frame.MessageID, Ciphertext: ciphertext}, nil
	case <-ctx.Done():
		return Delivery{}, ctx.Err()
	case <-s.conn.done:
		return Delivery{}, s.conn.connectionError()
	}
}

func (s *Subscription) Ack(ctx context.Context, cursor uint64) error {
	frame, err := s.conn.call(ctx, "ACKED", map[string]any{
		"v": 2, "type": "ACK", "peer_rid": s.peer, "generation": formatUint64(s.binding.Generation), "incarnation": s.incarnation, "cursor": formatUint64(cursor),
	})
	if err != nil {
		return err
	}
	got, err := parseUint64(frame.Cursor)
	if err != nil || got != cursor || frame.Incarnation != s.incarnation || frame.PeerRID != s.peer || frame.Generation != formatUint64(s.binding.Generation) {
		return errors.New("relay v2: invalid ack response")
	}
	return nil
}

func (s *Subscription) Discard(ctx context.Context) (Checkpoint, error) {
	frame, err := s.conn.call(ctx, "DISCARDED", map[string]any{
		"v": 2, "type": "DISCARD", "peer_rid": s.peer, "generation": formatUint64(s.binding.Generation), "incarnation": s.incarnation,
	})
	if err != nil {
		return Checkpoint{}, err
	}
	cursor, err := parseUint64(frame.Cursor)
	if err != nil || !validIncarnation(frame.Incarnation) || frame.Incarnation == s.incarnation || frame.PeerRID != s.peer || frame.Generation != formatUint64(s.binding.Generation) {
		return Checkpoint{}, errors.New("relay v2: invalid discard response")
	}
	return Checkpoint{Incarnation: frame.Incarnation, Cursor: cursor}, nil
}

type PairTransport struct {
	conn      *Conn
	machine   bool
	ceremony  string
	created   chan struct{}
	createOne sync.Once
}

func NewMachinePairTransport(control *Conn) *PairTransport {
	return &PairTransport{conn: control, machine: true, created: make(chan struct{})}
}

func DialPair(ctx context.Context, profile Profile, ceremony string) (*PairTransport, error) {
	if !validCeremony(ceremony) {
		return nil, errors.New("relay v2: invalid ceremony")
	}
	endpoint, err := relayEndpoint(profile.RelayURL, "/v2/pair", url.Values{"ceremony": {ceremony}})
	if err != nil {
		return nil, err
	}
	hc, observer, err := secureHTTPClient(profile.Security, endpoint)
	if err != nil {
		return nil, err
	}
	c, err := dialRaw(ctx, endpoint, hc)
	if err != nil {
		return nil, err
	}
	c.peerSPKI = observer.get()
	return &PairTransport{conn: c, ceremony: ceremony, created: make(chan struct{})}, nil
}

func (p *PairTransport) Created() <-chan struct{} { return p.created }
func (p *PairTransport) Close() {
	if !p.machine {
		p.conn.Close()
	}
}
func (p *PairTransport) PeerSPKI() []byte { return p.conn.PeerSPKI() }

func (p *PairTransport) Create(ctx context.Context, ceremony string) error {
	if !p.machine || !validCeremony(ceremony) {
		return errors.New("relay v2: invalid pair create")
	}
	frame, err := p.conn.call(ctx, "PAIR_CREATED", map[string]any{"v": 2, "type": "PAIR_CREATE", "ceremony": ceremony})
	if err != nil {
		return err
	}
	if frame.Ceremony != ceremony {
		return errors.New("relay v2: pair create mismatch")
	}
	expiresAt, err := parseUint64(frame.ExpiresAt)
	if err != nil || expiresAt <= uint64(time.Now().UnixMilli()) || expiresAt > uint64(time.Now().Add(2*time.Minute).UnixMilli()) {
		return errors.New("relay v2: invalid pair expiry")
	}
	p.ceremony = ceremony
	p.createOne.Do(func() { close(p.created) })
	return nil
}

func (p *PairTransport) Claim(ctx context.Context, ceremony string) error {
	if p.machine || ceremony != p.ceremony || !validCeremony(ceremony) {
		return errors.New("relay v2: invalid pair claim")
	}
	frame, err := p.conn.call(ctx, "PAIR_CLAIMED", map[string]any{"v": 2, "type": "PAIR_CLAIM", "ceremony": ceremony})
	if err != nil {
		return err
	}
	if frame.Ceremony != ceremony {
		return errors.New("relay v2: pair claim mismatch")
	}
	return nil
}

func (p *PairTransport) Send(ctx context.Context, message []byte) error {
	if p.ceremony == "" || len(message) == 0 || len(message) > 256*1024 {
		return errors.New("relay v2: invalid pair send")
	}
	frame, err := p.conn.call(ctx, "PAIR_SENT", map[string]any{
		"v": 2, "type": "PAIR_SEND", "ceremony": p.ceremony, "ciphertext": encode64(message),
	})
	if err != nil {
		return err
	}
	if frame.Ceremony != p.ceremony {
		return errors.New("relay v2: pair send mismatch")
	}
	return nil
}

func (p *PairTransport) Recv(ctx context.Context) ([]byte, error) {
	select {
	case queued := <-p.conn.pairFrames:
		p.conn.releasePair(queued.size)
		frame := queued.frame
		if frame.Ceremony != p.ceremony {
			return nil, errors.New("relay v2: pair frame mismatch")
		}
		message, err := decode64(frame.Ciphertext, -1)
		if err != nil || len(message) == 0 || len(message) > 256*1024 {
			return nil, errors.New("relay v2: invalid pair frame")
		}
		return message, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.conn.done:
		return nil, p.conn.connectionError()
	}
}

func (p *PairTransport) Complete(ctx context.Context, ceremony string) error {
	if !p.machine || ceremony != p.ceremony {
		return errors.New("relay v2: invalid pair finish")
	}
	frame, err := p.conn.call(ctx, "PAIR_FINISHED", map[string]any{"v": 2, "type": "PAIR_FINISH", "ceremony": ceremony})
	if err != nil {
		return err
	}
	if frame.Ceremony != ceremony {
		return errors.New("relay v2: pair finish mismatch")
	}
	return nil
}

func RoutingID(pub ed25519.PublicKey) string {
	r := hkdf.New(sha256.New, pub, []byte("swarm-relay-routing-id-v1"), []byte("routing-id"))
	var out [16]byte
	_, _ = io.ReadFull(r, out[:])
	return hex.EncodeToString(out[:])
}

func HomeID(namespace, machineRID string) string {
	body := appendField(nil, []byte("swarm-relay-home/v1"))
	body = appendField(body, []byte(namespace))
	body = appendField(body, []byte(machineRID))
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func ConsentMessage(ceremony, machineRID string) []byte {
	body := append([]byte(nil), []byte("swarm-relay-consent-v1\x00")...)
	body = appendField(body, []byte(ceremony))
	return append(body, machineRID...)
}

func MarshalConsent(ceremony string, signature []byte) []byte {
	return append(appendField(nil, []byte(ceremony)), signature...)
}

func authMessage(nonce []byte, rid, home string, role Role, purpose Purpose) []byte {
	body := append([]byte(nil), []byte("swarm-relay-auth-v2\x00")...)
	for _, value := range [][]byte{nonce, []byte(rid), []byte(home), []byte(role), []byte(purpose)} {
		body = appendField(body, value)
	}
	return body
}

func appendField(dst, value []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(value)))
	return append(dst, value...)
}

func relayEndpoint(base, path string, query url.Values) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("relay v2: invalid relay URL")
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", errors.New("relay v2: relay URL must use http(s) or ws(s)")
	}
	u.Path, u.RawPath, u.RawQuery = path, "", query.Encode()
	return u.String(), nil
}

type spkiObserver struct {
	mu     sync.Mutex
	digest []byte
}

func (o *spkiObserver) set(digest []byte) {
	o.mu.Lock()
	o.digest = append([]byte(nil), digest...)
	o.mu.Unlock()
}

func (o *spkiObserver) get() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.digest...)
}

func secureHTTPClient(security relay.Security, endpoint string) (*http.Client, *spkiObserver, error) {
	tlsConfig, err := security.Resolve(endpoint)
	if err != nil {
		return nil, nil, err
	}
	observer := &spkiObserver{}
	client := &http.Client{CheckRedirect: func(request *http.Request, _ []*http.Request) error {
		_, err := security.Resolve(request.URL.String())
		return err
	}}
	if tlsConfig != nil {
		config := tlsConfig.Clone()
		prior := config.VerifyConnection
		config.VerifyConnection = func(state tls.ConnectionState) error {
			if prior != nil {
				if err := prior(state); err != nil {
					return err
				}
			}
			if len(state.PeerCertificates) == 0 {
				return errors.New("relay v2: TLS peer sent no certificate")
			}
			digest := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
			observer.set(digest[:])
			return nil
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = config
		client.Transport = transport
	}
	return client, observer, nil
}

func decodeFrame(data []byte) (wireFrame, error) {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return wireFrame{}, errors.New("relay v2: invalid JSON frame")
	}
	raw = make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return wireFrame{}, errors.New("relay v2: invalid JSON field")
		}
		if _, duplicate := raw[key]; duplicate {
			return wireFrame{}, errors.New("relay v2: duplicate JSON field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || bytes.Equal(value, []byte("null")) {
			return wireFrame{}, errors.New("relay v2: invalid JSON field value")
		}
		raw[key] = value
	}
	if closeToken, err := decoder.Token(); err != nil || closeToken != json.Delim('}') {
		return wireFrame{}, errors.New("relay v2: invalid JSON frame")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return wireFrame{}, errors.New("relay v2: trailing JSON")
	}
	var typ string
	if err := json.Unmarshal(raw["type"], &typ); err != nil {
		return wireFrame{}, errors.New("relay v2: missing frame type")
	}
	allowed, ok := responseFields[typ]
	if !ok {
		return wireFrame{}, errors.New("relay v2: unsupported response type")
	}
	for field := range raw {
		if !allowed[field] {
			return wireFrame{}, errors.New("relay v2: unknown response field")
		}
	}
	for field := range requiredResponseFields[typ] {
		if _, ok := raw[field]; !ok {
			return wireFrame{}, errors.New("relay v2: missing response field")
		}
	}
	var frame wireFrame
	if err := json.Unmarshal(data, &frame); err != nil || frame.V != 2 || !token.MatchString(frame.RequestID) {
		return wireFrame{}, errors.New("relay v2: invalid response envelope")
	}
	if frame.Type == "ERROR" && !errorCode.MatchString(frame.Code) {
		return wireFrame{}, errors.New("relay v2: invalid error code")
	}
	return frame, nil
}

func fields(names ...string) map[string]bool {
	m := map[string]bool{"v": true, "type": true, "request_id": true}
	for _, name := range names {
		m[name] = true
	}
	return m
}

var responseFields = map[string]map[string]bool{
	"ERROR": fields("code"), "CHALLENGE": fields("nonce", "home", "expires_at"),
	"AUTHENTICATED": fields("rid", "role", "purpose", "home", "generation"),
	"AUTHORIZED":    fields("phone_rid", "generation"), "APPENDED": fields("peer_rid", "generation", "cursor", "deduped"),
	"SUBSCRIBED": fields("peer_rid", "generation", "incarnation", "after"),
	"DELIVER":    fields("peer_rid", "generation", "incarnation", "cursor", "msg_id", "ciphertext"),
	"ACKED":      fields("peer_rid", "generation", "incarnation", "cursor"),
	"DISCARDED":  fields("peer_rid", "generation", "incarnation", "cursor"), "REVOKED": fields("peer_rid"),
	"PAIR_CREATED": fields("ceremony", "expires_at"), "PAIR_CLAIMED": fields("ceremony"),
	"PAIR_FRAME": fields("ceremony", "ciphertext"), "PAIR_SENT": fields("ceremony"), "PAIR_FINISHED": fields("ceremony"),
}

var requiredResponseFields = map[string]map[string]bool{
	"ERROR": fields("code"), "CHALLENGE": fields("nonce", "home", "expires_at"),
	"AUTHENTICATED": fields("rid", "role", "purpose", "home"),
	"AUTHORIZED":    fields("phone_rid", "generation"), "APPENDED": fields("peer_rid", "generation", "cursor", "deduped"),
	"SUBSCRIBED": fields("peer_rid", "generation", "incarnation", "after"),
	"DELIVER":    fields("peer_rid", "generation", "incarnation", "cursor", "msg_id", "ciphertext"),
	"ACKED":      fields("peer_rid", "generation", "incarnation", "cursor"),
	"DISCARDED":  fields("peer_rid", "generation", "incarnation", "cursor"), "REVOKED": fields("peer_rid"),
	"PAIR_CREATED": fields("ceremony", "expires_at"), "PAIR_CLAIMED": fields("ceremony"),
	"PAIR_FRAME": fields("ceremony", "ciphertext"), "PAIR_SENT": fields("ceremony"), "PAIR_FINISHED": fields("ceremony"),
}

func encode64(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func decode64(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || encode64(decoded) != value || (size >= 0 && len(decoded) != size) {
		return nil, errors.New("invalid canonical base64url")
	}
	return decoded, nil
}

func parseUint64(value string) (uint64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("non-canonical uint64")
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != value {
		return 0, errors.New("invalid uint64")
	}
	return n, nil
}

func formatUint64(value uint64) string { return strconv.FormatUint(value, 10) }
func validRID(value string) bool {
	return len(value) == 32 && strings.ToLower(value) == value && isHex(value)
}
func validCeremony(value string) bool {
	return len(value) == 32 && strings.ToLower(value) == value && isHex(value)
}
func validIncarnation(value string) bool {
	_, err := decode64(value, 16)
	return err == nil
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}
