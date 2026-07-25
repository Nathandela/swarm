// Shared fixtures for slice S6's FAILING-FIRST tests (TDD RED, GG-5).
//
// Peers are REAL wherever a real peer proves the property: the in-process
// relay.Server serves every happy path, and the crypto package seals every
// envelope. Failure injection is confined to two purpose-built fakes:
//
//   - wireTap, a TCP proxy in front of the real relay. It records what the relay
//     actually receives -- websocket frame payloads, unmasked, so the assertion is
//     on the wire rather than on an interface -- and can cut live connections or
//     refuse new ones, so "flapping relay" is a real transport failure rather than
//     a stubbed error return.
//
//   - hostileRelay, a minimal websocket peer speaking the relay frame protocol.
//     It exists for the two behaviours a correct relay will never exhibit: never
//     answering a request (request-timeout), and claiming has_more forever without
//     advancing the cursor (hostile pagination).
//
// The tests live in package transport_test on purpose: they pin the EXPORTED
// contract a caller depends on, and they leave the relay package's own test binary
// untouched so the other live slices keep a green build.
package transport_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/transport"
)

// testDeadline bounds every round-trip so a hung handshake fails one test instead
// of the whole package.
const testDeadline = 10 * time.Second

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
	t.Cleanup(cancel)
	return ctx
}

// noopAPNs is a push sink that discards: these tests never assert on push, but the
// relay must not be handed a nil sink.
type noopAPNs struct{}

func (noopAPNs) Push(context.Context, string, relay.APNsPayload) error { return nil }

// startRelay boots the REAL relay on 127.0.0.1:0 over plain ws:// and returns it
// with its URL. mut lets a test tighten quotas.
func startRelay(t *testing.T, mut func(*relay.Config)) (*relay.Server, string) {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	if mut != nil {
		mut(&cfg)
	}
	srv, err := relay.New(cfg, relay.WithAPNsSink(noopAPNs{}))
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, srv.URL()
}

// startTLSRelay fronts the real relay with a TLS terminator holding a self-signed
// certificate, and returns the wss:// URL plus that certificate's DER bytes. It is
// how PB-NET-2's three TLS cases are exercised against a real websocket peer rather
// than a stub: default verification must fail closed against this untrusted cert,
// the matching pin must succeed all the way through the relay-auth handshake, and a
// non-matching pin must be refused.
func startTLSRelay(t *testing.T) (wssURL string, certDER []byte) {
	t.Helper()
	_, plain := startRelay(t, nil)
	target, err := url.Parse(strings.Replace(plain, "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewTLSServer(proxy)
	t.Cleanup(front.Close)
	return strings.Replace(front.URL, "https://", "wss://", 1), front.Certificate().Raw
}

// selfSignedDER mints an unrelated self-signed certificate, used as the WRONG pin.
// It is minted directly with x509.CreateCertificate, NOT taken from a second
// httptest.NewTLSServer: httptest serves one hardcoded certificate for every server
// in the process, so a cert obtained that way is byte-identical to the pinned one
// and the wrong-pin test becomes unwinnable for any implementation.
func selfSignedDER(t *testing.T) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("selfSignedDER key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("selfSignedDER serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "wrong-pin.invalid"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("selfSignedDER create: %v", err)
	}
	return der
}

// --- wireTap: a recording, cuttable TCP proxy -----------------------------

// syncBuffer is a race-safe sink for the tapped byte stream.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

// wsUnmasker turns one proxied client->server TCP stream into the LOGICAL payload
// bytes the relay ends up handling. It exists because RFC 6455 requires a client to
// XOR-mask every frame it sends with a per-frame key carried in the clear beside it:
// searching the raw socket bytes for a plaintext marker would find nothing whether
// or not the transport leaked it, so a masked-bytes assertion is vacuous. Masking is
// not confidentiality, and PB-NET-3 must not be allowed to pass because of it.
type wsUnmasker struct {
	buf        []byte
	headerDone bool
	out        *syncBuffer
}

func (d *wsUnmasker) feed(p []byte) {
	d.buf = append(d.buf, p...)
	if !d.headerDone {
		i := bytes.Index(d.buf, []byte("\r\n\r\n"))
		if i < 0 {
			return
		}
		d.buf = d.buf[i+4:]
		d.headerDone = true
	}
	for {
		n, payload, ok := parseWSFrame(d.buf)
		if !ok {
			return
		}
		_, _ = d.out.Write(payload)
		d.buf = d.buf[n:]
	}
}

// parseWSFrame decodes one websocket frame header, returning the bytes consumed and
// the unmasked payload. Opcode and fragmentation are ignored on purpose: the tap is
// a substring oracle over what the relay receives, not a protocol implementation.
func parseWSFrame(b []byte) (int, []byte, bool) {
	if len(b) < 2 {
		return 0, nil, false
	}
	masked := b[1]&0x80 != 0
	ln := int(b[1] & 0x7f)
	off := 2
	switch ln {
	case 126:
		if len(b) < off+2 {
			return 0, nil, false
		}
		ln = int(binary.BigEndian.Uint16(b[off : off+2]))
		off += 2
	case 127:
		if len(b) < off+8 {
			return 0, nil, false
		}
		ln = int(binary.BigEndian.Uint64(b[off : off+8]))
		off += 8
	}
	var key []byte
	if masked {
		if len(b) < off+4 {
			return 0, nil, false
		}
		key = b[off : off+4]
		off += 4
	}
	if ln < 0 || len(b) < off+ln {
		return 0, nil, false
	}
	payload := append([]byte(nil), b[off:off+ln]...)
	if masked {
		for i := range payload {
			payload[i] ^= key[i%4]
		}
	}
	return off + ln, payload, true
}

// wireTap proxies TCP to an upstream relay while recording, UNMASKED, every frame
// payload the client sends. Cut severs live connections (a flap); Refuse keeps new
// ones from being established (the relay stays down).
type wireTap struct {
	ln       net.Listener
	upstream string
	sent     syncBuffer

	mu     sync.Mutex
	live   []net.Conn
	refuse bool
	closed bool
}

func newWireTap(t *testing.T, upstreamURL string) *wireTap {
	t.Helper()
	host := strings.TrimPrefix(upstreamURL, "ws://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("wireTap listen: %v", err)
	}
	w := &wireTap{ln: ln, upstream: host}
	go w.accept()
	t.Cleanup(w.Close)
	return w
}

func (w *wireTap) URL() string { return "ws://" + w.ln.Addr().String() }

// Sent returns every byte the client has written toward the relay so far.
func (w *wireTap) Sent() []byte { return w.sent.Bytes() }

func (w *wireTap) Refuse(v bool) {
	w.mu.Lock()
	w.refuse = v
	w.mu.Unlock()
}

// Cut closes every live proxied connection, simulating a relay/network flap.
func (w *wireTap) Cut() {
	w.mu.Lock()
	live := w.live
	w.live = nil
	w.mu.Unlock()
	for _, c := range live {
		_ = c.Close()
	}
}

func (w *wireTap) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	_ = w.ln.Close()
	w.Cut()
}

func (w *wireTap) accept() {
	for {
		down, err := w.ln.Accept()
		if err != nil {
			return
		}
		w.mu.Lock()
		refuse := w.refuse
		w.mu.Unlock()
		if refuse {
			_ = down.Close()
			continue
		}
		up, err := net.Dial("tcp", w.upstream)
		if err != nil {
			_ = down.Close()
			continue
		}
		w.mu.Lock()
		w.live = append(w.live, down, up)
		w.mu.Unlock()
		// Client -> relay is forwarded verbatim and, in parallel, unmasked into the
		// recording buffer: that is the stream PB-NET-3 asserts a plaintext marker
		// never appears in. Each proxied connection gets its own decoder, since a
		// reconnect restarts the websocket handshake and the masking keys.
		dec := &wsUnmasker{out: &w.sent}
		go func() {
			buf := make([]byte, 32*1024)
			for {
				n, rerr := down.Read(buf)
				if n > 0 {
					dec.feed(buf[:n])
					if _, werr := up.Write(buf[:n]); werr != nil {
						break
					}
				}
				if rerr != nil {
					break
				}
			}
			_ = up.Close()
		}()
		go func() {
			_, _ = io.Copy(down, up)
			_ = down.Close()
		}()
	}
}

// --- hostileRelay: a relay that answers wrongly, or not at all ------------

// hostileRelay speaks just enough of the relay frame protocol to authenticate a
// client and then misbehave. It never verifies a signature: the point is the
// CLIENT's behaviour against an untrusted peer.
type hostileRelay struct {
	srv *httptest.Server

	mu       sync.Mutex
	silent   bool         // stop replying to everything after authentication
	stuck    bool         // mailbox_read: always has_more, never advances the cursor
	retained []relay.Item // served on every read, and NEVER compacted by an ack
	reads    int          // how many mailbox_read requests were answered
	lastCurs uint64       // the cursor the client last read from
	pub      []byte       // the relay-auth pub presented in auth_init
	cursor   uint64
}

func newHostileRelay(t *testing.T) *hostileRelay {
	t.Helper()
	h := &hostileRelay{}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hostileRelay) URL() string { return strings.Replace(h.srv.URL, "http://", "ws://", 1) }

func (h *hostileRelay) setSilent(v bool) { h.mu.Lock(); h.silent = v; h.mu.Unlock() }
func (h *hostileRelay) setStuck(v bool)  { h.mu.Lock(); h.stuck = v; h.mu.Unlock() }

// setRetained makes this relay a RETAINING one: it serves the same items forever
// and ignores acks. That is the adversary PB-NET-6 is about -- a relay that keeps
// a copy and re-serves it after the phone process dies.
func (h *hostileRelay) setRetained(items []relay.Item) {
	h.mu.Lock()
	h.retained = items
	h.mu.Unlock()
}

// lastReadCursor is the cursor the client most recently asked to read from.
func (h *hostileRelay) lastReadCursor() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastCurs
}

func (h *hostileRelay) readCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reads
}

func (h *hostileRelay) serve(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(relay.MaxFrame + 64)
	ctx := r.Context()
	for {
		mt, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if mt != websocket.MessageBinary {
			return
		}
		tag, payload, err := relay.ReadFrame(bytes.NewReader(data))
		if err != nil {
			return
		}
		reply, send := h.answer(tag, payload)
		if !send {
			continue
		}
		var buf bytes.Buffer
		if err := relay.WriteFrame(&buf, relay.MsgOK, reply); err != nil {
			return
		}
		if err := ws.Write(ctx, websocket.MessageBinary, buf.Bytes()); err != nil {
			return
		}
	}
}

func (h *hostileRelay) answer(tag relay.MsgType, payload []byte) ([]byte, bool) {
	var req struct {
		Op   string `json:"op"`
		Pub  []byte `json:"relay_auth_pub"`
		Curs uint64 `json:"cursor"`
	}
	_ = json.Unmarshal(payload, &req)

	h.mu.Lock()
	defer h.mu.Unlock()

	switch {
	case req.Op == "hello":
		return mustJSON(map[string]any{"version": relay.ProtocolVersion, "caps": []string{}}), true
	case req.Op == "auth_init":
		h.pub = append([]byte(nil), req.Pub...)
		nonce := make([]byte, 16)
		_, _ = rand.Read(nonce)
		return mustJSON(map[string]any{"nonce": nonce}), true
	case req.Op == "auth_resp":
		return mustJSON(map[string]any{"routing_id": relay.RoutingID(ed25519.PublicKey(h.pub))}), true
	}
	if h.silent {
		return nil, false
	}
	switch {
	case req.Op == "mailbox_read":
		h.reads++
		h.lastCurs = req.Curs
		if h.stuck {
			// A page that claims more remains while every item sits at or behind the
			// caller's cursor: trusting has_more here spins forever (phonesim's
			// errStuckPage, codex#7). The client must terminate instead.
			return mustJSON(map[string]any{
				"items":    []relay.Item{{Cursor: req.Curs, Envelope: []byte("stuck")}},
				"has_more": true,
			}), true
		}
		page := []relay.Item{}
		for _, it := range h.retained {
			if it.Cursor > req.Curs {
				page = append(page, it)
			}
		}
		return mustJSON(map[string]any{"items": page, "has_more": false}), true
	case req.Op == "mailbox_ack":
		return mustJSON(map[string]any{}), true
	case tag == relay.MsgMailboxAppend:
		h.cursor++
		return mustJSON(map[string]any{"cursor": h.cursor}), true
	}
	return mustJSON(map[string]any{}), true
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// --- keys, seals, sessions ------------------------------------------------

func newRelayAuthKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return pub, priv
}

func authFor(pub ed25519.PublicKey, priv ed25519.PrivateKey) relay.ClientAuth {
	return relay.ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(challenge []byte) ([]byte, error) { return ed25519.Sign(priv, challenge), nil },
	}
}

// peers wires an authenticated machine (a plain relay.Client, the sender) and an
// authorized device identity (the transport.Session under test consumes it). It
// mirrors the relay package's own mailboxFixture so the adversary tests run against
// the same shape Phase A proved.
type peers struct {
	machine    *relay.Client
	machineRID string
	deviceAuth relay.ClientAuth
	deviceRID  string
	party      sealParty
}

// seal produces a machine->device envelope at seq, sealed under a content key the
// transport never sees.
func (p peers) seal(t *testing.T, seq uint64, plaintext []byte) []byte {
	t.Helper()
	return p.party.seal(t, seq, plaintext)
}

func newPeers(t *testing.T, srv *relay.Server) peers {
	t.Helper()
	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine, err := relay.Dial(testCtx(t), srv.URL(), authFor(mPub, mPriv))
	if err != nil {
		t.Fatalf("relay.Dial(machine): %v", err)
	}
	t.Cleanup(func() { _ = machine.Close() })
	if err := machine.AuthorizeDevice(testCtx(t), dPub); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	return peers{
		machine:    machine,
		machineRID: relay.RoutingID(mPub),
		deviceAuth: authFor(dPub, dPriv),
		deviceRID:  relay.RoutingID(dPub),
		party:      newSealParty(t),
	}
}

// sealParty holds the content key the MACHINE seals with. It lives in the test, not
// in the transport: PB-NET-3's whole point is that the transport never sees it.
type sealParty struct {
	keys    crypto.EpochKeys
	sender  [8]byte
	recip   [8]byte
	epochID uint32
}

func newSealParty(t *testing.T) sealParty {
	t.Helper()
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("NewEpochKeys: %v", err)
	}
	return sealParty{
		keys:    keys,
		sender:  crypto.KeyID([]byte("machine-sender-pub-000000000000x")),
		recip:   crypto.KeyID([]byte("device-recipient-pub-0000000000x")),
		epochID: 7,
	}
}

func (p sealParty) seal(t *testing.T, seq uint64, plaintext []byte) []byte {
	t.Helper()
	env, err := crypto.SealMailbox(p.keys.ContentKey, crypto.EnvelopeHeader{
		Version:        crypto.VersionV1,
		EpochID:        p.epochID,
		Seq:            seq,
		RecipientKeyID: p.recip,
		SenderKeyID:    p.sender,
		IssuedAt:       time.Now().UnixMilli(),
	}, plaintext)
	if err != nil {
		t.Fatalf("SealMailbox: %v", err)
	}
	return env.Marshal()
}

// devSession opens the transport session under test against url with the device's
// relay-auth identity. Loopback cleartext is the narrowly-scoped carve-out
// PB-NET-2 permits, and it is honoured only in a test binary.
func devSession(t *testing.T, url string, p peers, mut func(*transport.Options)) *transport.Session {
	t.Helper()
	opts := transport.Options{
		URL:      url,
		Auth:     p.deviceAuth,
		Security: relay.Security{AllowLoopbackCleartext: true},
	}
	if mut != nil {
		mut(&opts)
	}
	s, err := transport.Dial(testCtx(t), opts)
	if err != nil {
		t.Fatalf("transport.Dial: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// waitState polls until the session reaches want, so a test never has to guess how
// long an automatic reconnect takes.
func waitState(t *testing.T, s *transport.Session, want transport.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// Sleep BEFORE the first check, not after: right after tap.Cut() the session
		// still reads as the awaited state for ~100us (the peer close has not reached
		// the client's netpoller yet), so a check-first loop returns immediately and
		// never waits for the reconnect this helper promises to wait for.
		time.Sleep(5 * time.Millisecond)
		if s.State() == want {
			return
		}
	}
	t.Fatalf("session never reached state %q (still %q)", want, s.State())
}

// recordingSleep is the reconnect-delay seam: it captures the schedule the session
// asks for and returns almost immediately, so the backoff numbers are asserted
// without spending 30 real seconds.
type recordingSleep struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (r *recordingSleep) fn(ctx context.Context, d time.Duration) error {
	r.mu.Lock()
	r.delays = append(r.delays, d)
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Millisecond):
		return nil
	}
}

func (r *recordingSleep) all() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.delays...)
}

// --- goroutine accounting -------------------------------------------------

// settledGoroutines waits for the runtime's goroutine count to stop moving, so a
// baseline is not taken mid-teardown.
func settledGoroutines() int {
	prev := -1
	for i := 0; i < 100; i++ {
		runtime.Gosched()
		n := runtime.NumGoroutine()
		if n == prev {
			return n
		}
		prev = n
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// assertNoLeak fails with the full goroutine dump if the count does not return to
// the baseline within a bound (PB-NET-7).
func assertNoLeak(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= baseline {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutine leak: %d live, baseline %d\n%s", n, baseline, buf)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
