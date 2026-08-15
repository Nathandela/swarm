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
	"crypto/rand"
	"encoding/hex"
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
	payload PushPayload
}

// mockAPNs is the deferred-real APNs target (R-REL.5). It records every push so
// a test can assert the outer payload is generic and carries only ciphertext.
type mockAPNs struct {
	mu     sync.Mutex
	pushes []recordedPush
}

func (m *mockAPNs) Push(_ context.Context, token string, p PushPayload) error {
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
	srv, err := New(cfg, WithClock(clk), WithPushSink(apns))
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

// awaitGatewayDrop blocks until the relay has OBSERVED rid's session drop, i.e.
// until removeConn has cleared the presence entry and stamped disconnectedAt.
//
// A client's Close returning is NOT that observation and never could be: the
// relay learns of a drop on the connection's own read goroutine, which is
// unordered against the client returning from Close. A test that closes and then
// drives a sweep from its own goroutine is therefore asserting against a presence
// entry the relay may still consider connected — the sweep skips it (correctly:
// a connected machine is online), and the entry is then stamped with the clock as
// it stands AFTER the test's Advance, so the elapsed-since-drop the next sweep
// measures is short by exactly the advance. Sequencing the sweep after this
// observation is what makes the drop happen-before the elapsed-time question.
//
// The wall-clock poll here is a synchronization point, not a settle: every TTL
// DECISION under test still reads the injected fakeClock, and the condition is
// the server's own state rather than an elapsed duration.
func awaitGatewayDrop(t *testing.T, s *Server, rid string) {
	t.Helper()
	waitUntil(t, testDeadline, "the relay to observe the gateway drop", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		p := s.presence[rid]
		return p != nil && !p.connected
	})
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
		Sign:         func(challenge []byte) ([]byte, error) { return ed25519.Sign(priv, challenge), nil },
	}
}

// consentTo is the named device's own consent for grantee's routing id — the
// credential handleAuthorizeDevice verifies before it records a pairing (ADR-007
// B27, mandatory since B38). Production obtains it during the SAS-authenticated
// pairing ceremony and carries it in pairing msg4; a test signs the same statement
// with the same key directly, so the wire bytes are identical.
//
// Each call mints a FRESH ceremony id, which is what a real pairing produces (ADR-007
// B47). A test that needs to replay one ceremony's credential, or to supersede it with
// another, names the ceremony itself through consentToCeremony.
func consentTo(priv ed25519.PrivateKey, granteeRID string) []byte {
	return consentToCeremony(priv, newTestCeremonyID(), granteeRID)
}

// consentToCeremony is consentTo with the ceremony named, for the tests whose subject is
// the ceremony binding itself.
func consentToCeremony(priv ed25519.PrivateKey, ceremonyID, granteeRID string) []byte {
	return MarshalConsent(ceremonyID, ed25519.Sign(priv, ConsentMessage(ceremonyID, granteeRID)))
}

// newTestCeremonyID mints a rendezvous-shaped id, the same 16 random bytes hex-encoded
// that internal/skeleton's BeginPairing puts in the QR.
func newTestCeremonyID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("relay test: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// devConsent is consentTo through a real crypto.KeyStore, i.e. through the custody
// boundary the phone actually signs behind (KeyStore.SignRelayAuth). It exists to
// show the consent needs NO new key and no new crypto: the relay-auth key that
// answers the connection challenge is the key that signs the grant, kept apart by
// ConsentMessage's domain separator.
func devConsent(t *testing.T, ks crypto.KeyStore, granteeRID string) []byte {
	t.Helper()
	ceremonyID := newTestCeremonyID()
	sig, err := ks.SignRelayAuth(ConsentMessage(ceremonyID, granteeRID))
	if err != nil {
		t.Fatalf("SignRelayAuth(consent): %v", err)
	}
	return MarshalConsent(ceremonyID, sig)
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

// pushEnvelopeFixture is an opaque envelope of the size the push channel admits, for tests
// whose subject is ROUTING or AUTHORITY rather than the payload. It is deliberately not
// sealed: the relay cannot read a push envelope either way, and PB-PUSH-3's schema is a
// LENGTH. Using it keeps such a test failing for its own reason -- an authority test that
// passed because the envelope was the wrong size would have stopped testing authority.
func pushEnvelopeFixture() []byte { return make([]byte, PushEnvelopeSize) }

// sealPush produces an envelope the PUSH channel admits: PB-PUSH-3's schema is exactly
// PushEnvelopeSize bytes, which is a header over an EMPTY plaintext, because "the provider
// observes size" is a benign disclosure only while size is constant (see PushEnvelopeSize).
//
// IT ASSERTS ITS OWN LENGTH rather than trusting the arithmetic. If the envelope format ever
// grows a field, every push test in this package would otherwise start failing with
// `bad_request` far from the cause; this way the fixture names it.
//
// A push fixture carries NO plaintext, and that is the schema rather than the fixture being
// lazy: the push channel cannot represent session content at all now, so a test that wants a
// sealed sentinel the relay must not read wants sealMailbox and the mailbox path.
func (p sealParty) sealPush(t *testing.T, seq uint64, clk *fakeClock) []byte {
	t.Helper()
	env := p.sealMailbox(t, seq, nil, clk)
	if len(env) != PushEnvelopeSize {
		t.Fatalf("push fixture is %d bytes, but the relay's push schema admits exactly %d: "+
			"the envelope format changed and PushEnvelopeSize no longer describes it",
			len(env), PushEnvelopeSize)
	}
	return env
}
