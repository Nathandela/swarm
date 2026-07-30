package relay_test

// Live-side fixtures for the relay TRANSPORT-SECURITY fences (PB-NET-2), relocated here from
// internal/remote/transport by ADR-007 B98.
//
// WHY THEY MOVED. The fences these serve -- TLS policy, pin renewal, platform pinning, the
// release-build probes, and productiondial's "no production caller may reach relay.Dial" --
// have relay's own exported surface as their subject and never touched transport.Session. They
// were only ever housed next to it. Leaving them there would have made deleting the dead
// transport package delete live fences over live code, which is B98's finding.
//
// THEY ARE package relay_test, NOT package relay, and that is deliberate: they pin the
// EXPORTED contract an outside caller depends on, which is the property they were written to
// hold. The relay package's own 38 internal test files are untouched.
//
//   - wireTap is a TCP proxy in front of the real relay. It records what the relay actually
//     receives -- websocket frame payloads, unmasked, so the assertion is on the wire rather
//     than on an interface -- and can cut live connections or refuse new ones.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
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

func (noopAPNs) Push(context.Context, string, relay.PushPayload) error { return nil }

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
	srv, err := relay.New(cfg, relay.WithPushSink(noopAPNs{}))
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
