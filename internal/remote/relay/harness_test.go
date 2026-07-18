// Shared fixtures + helpers for the relay server's FAILING-FIRST tests (TDD
// RED, GG-5). Every relay.* symbol these tests reference is the frozen contract
// a separate implementer supplies; until then the package does not compile and
// the only errors are "undefined" for the new relay symbols. The crypto package
// (imported here) already exists, so its symbols resolve — the RED is confined
// to the relay surface.
//
// Design constraints these helpers encode (ADR-007 D9/D11, plan R-REL.*):
//   - a real localhost listener + an in-process client for full round-trips,
//   - a mock APNs sink (real APNs deferred, R-REL.5),
//   - an injected clock for every TTL/rate/retention assertion (no real sleeps).
package relay

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// testDeadline bounds every round-trip so a hung handshake fails the test
// instead of the whole package.
const testDeadline = 5 * time.Second

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
	t.Cleanup(cancel)
	return ctx
}

// fakeClock is the single authoritative clock the relay reads for presence
// timeouts, rendezvous TTLs, rate windows, and retention (ADR-007 "every TTL is
// pinned to one authoritative clock"). Advance drives time forward with no real
// sleep.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recordedPush is one delivery the relay handed to the (mock) APNs sink.
type recordedPush struct {
	token   string
	payload APNsPayload
}

// mockAPNs is the deferred-real APNs target (R-REL.5). It records every push so
// a test can assert the outer payload is generic and carries only ciphertext.
type mockAPNs struct {
	mu     sync.Mutex
	pushes []recordedPush
}

func (m *mockAPNs) Push(_ context.Context, token string, p APNsPayload) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushes = append(m.pushes, recordedPush{token: token, payload: p})
	return nil
}

func (m *mockAPNs) all() []recordedPush {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recordedPush(nil), m.pushes...)
}

// startTestRelay boots a relay on 127.0.0.1:0 over plain ws:// with an injected
// clock and mock APNs sink. mut lets a test tighten quotas/timeouts. It returns
// the running server, the resolved config (for DBPath + restart), the sink, and
// the clock.
func startTestRelay(t *testing.T, mut func(*Config)) (*Server, Config, *mockAPNs, *fakeClock) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	if mut != nil {
		mut(&cfg)
	}
	apns := &mockAPNs{}
	clk := newFakeClock()
	srv, err := New(cfg, WithClock(clk), WithAPNsSink(apns))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, cfg, apns, clk
}

// newRelayAuthKey returns a fresh Ed25519 relay-auth keypair (the only key a
// party ever discloses to the untrusted relay, R-CRY.3 / R-REL.2).
func newRelayAuthKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return pub, priv
}

// authFor builds a ClientAuth from a raw relay-auth keypair.
func authFor(pub ed25519.PublicKey, priv ed25519.PrivateKey) ClientAuth {
	return ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(challenge []byte) []byte { return ed25519.Sign(priv, challenge) },
	}
}

// dialAuthed dials and completes the relay-auth challenge/response, failing the
// test if the authenticated connection cannot be established.
func dialAuthed(t *testing.T, url string, auth ClientAuth) *Client {
	t.Helper()
	c, err := Dial(testCtx(t), url, auth)
	if err != nil {
		t.Fatalf("Dial(authed): %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// dialRaw opens an unauthenticated framed connection (pairing rendezvous +
// adversarial framing tests use it — pairing peers are not yet relay-registered).
func dialRaw(t *testing.T, url string) *Conn {
	t.Helper()
	c, err := DialRaw(testCtx(t), url)
	if err != nil {
		t.Fatalf("DialRaw: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// sealParty is a sender's per-epoch content key plus its sealed-box recipient
// key id — enough to produce mailbox envelopes the relay stores opaquely and a
// device opens end to end. The relay never holds ContentKey (R-REL.6/.7).
type sealParty struct {
	keys        crypto.EpochKeys
	senderKeyID [8]byte
	recipKeyID  [8]byte
	epochID     uint32
}

// newSealParty builds a content-key sender/recipient pair with recognizable
// routing key ids.
func newSealParty(t *testing.T, senderPub, recipientPub []byte) sealParty {
	t.Helper()
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("NewEpochKeys: %v", err)
	}
	return sealParty{
		keys:        keys,
		senderKeyID: crypto.KeyID(senderPub),
		recipKeyID:  crypto.KeyID(recipientPub),
		epochID:     7,
	}
}

// sealMailbox produces the byte-exact wire envelope for one session-content
// event at the given authenticated seq, carrying plaintext the relay must never
// be able to read.
func (p sealParty) sealMailbox(t *testing.T, seq uint64, plaintext []byte, clk *fakeClock) []byte {
	t.Helper()
	h := crypto.EnvelopeHeader{
		Version:        crypto.VersionV1,
		EpochID:        p.epochID,
		Seq:            seq,
		RecipientKeyID: p.recipKeyID,
		SenderKeyID:    p.senderKeyID,
		IssuedAt:       clk.Now().UnixMilli(),
	}
	env, err := crypto.SealMailbox(p.keys.ContentKey, h, plaintext)
	if err != nil {
		t.Fatalf("SealMailbox: %v", err)
	}
	return env.Marshal()
}
