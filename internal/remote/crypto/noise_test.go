// R-CRY.4-.8 — Noise XX live transport.
//
// Suite Noise_XX_25519_ChaChaPoly_SHA256 (SHA-256 for Swift/CryptoKit interop),
// static s = X25519 Noise-static identity; prologue binds protocol/role/route
// (R-CRY.5); peer static compared to the pinned value and the handshake aborts
// before any transport byte on mismatch (R-CRY.6, authenticated not TOFU);
// per-CipherState monotonic nonces reject replay/reorder and rekey at threshold
// (R-CRY.7); a fresh full handshake with new ephemerals every connection, no
// resumption secret (R-CRY.8).
//
// FROZEN CONTRACT (subset):
//
//	const NoiseSuite = "Noise_XX_25519_ChaChaPoly_SHA256"
//	const RekeyAfterBytes = 1 << 30
//	const RekeyAfterDuration = 15 * time.Minute
//	type NoiseConfig struct { Initiator bool; Static *NoiseStatic; PeerStatic []byte; Prologue []byte; Rand io.Reader }
//	func NewNoise(cfg NoiseConfig) (*NoiseSession, error)
//	func (*NoiseSession) WriteMessage(payload []byte) ([]byte, error)   // handshake
//	func (*NoiseSession) ReadMessage(message []byte) ([]byte, error)    // handshake
//	func (*NoiseSession) HandshakeComplete() bool
//	func (*NoiseSession) PeerStatic() []byte
//	func (*NoiseSession) ChannelBinding() []byte
//	func (*NoiseSession) Suite() string
//	func (*NoiseSession) Encrypt(plaintext []byte) ([]byte, error)      // transport
//	func (*NoiseSession) Decrypt(ciphertext []byte) ([]byte, error)     // transport
//	func (*NoiseSession) Rekey()
//	func LivePrologue(machineRoutingID, deviceRoutingID []byte) []byte
//	func PairPrologue(rendezvousID []byte) []byte
package crypto

import (
	"bytes"
	"reflect"
	"regexp"
	"testing"
	"time"
)

func livePrologue() []byte { return LivePrologue([]byte("machine-route"), []byte("device-route")) }

// TestNoiseXX_HandshakeCompletes drives a full XX handshake between pinned
// peers and confirms both reach transport mode with a working, mutual,
// confidential channel.
func TestNoiseXX_HandshakeCompletes(t *testing.T) {
	a, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity(a): %v", err)
	}
	b, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity(b): %v", err)
	}
	ini, resp := newLivePair(t, a, b, livePrologue())
	if err := driveXX(ini, resp); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if !ini.HandshakeComplete() || !resp.HandshakeComplete() {
		t.Fatal("handshake did not complete on both ends")
	}
	// Each learned the other's real static.
	if !bytes.Equal(ini.PeerStatic(), b.NoiseStaticPublic()) {
		t.Error("initiator learned wrong peer static")
	}
	if !bytes.Equal(resp.PeerStatic(), a.NoiseStaticPublic()) {
		t.Error("responder learned wrong peer static")
	}
	// Transport round-trips both directions, and the ciphertext is not the
	// plaintext.
	msg := []byte("hello over the wire")
	ct, err := ini.Encrypt(msg)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(ct, msg) {
		t.Error("transport ciphertext contains plaintext")
	}
	pt, err := resp.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(pt, msg) {
		t.Errorf("decrypt = %q, want %q", pt, msg)
	}
}

// TestNoiseXX_SuiteExact pins the exact suite string (Swift/CryptoKit interop
// depends on SHA-256, not BLAKE2s), and that a session reports it.
func TestNoiseXX_SuiteExact(t *testing.T) {
	if NoiseSuite != "Noise_XX_25519_ChaChaPoly_SHA256" {
		t.Fatalf("NoiseSuite = %q, want Noise_XX_25519_ChaChaPoly_SHA256", NoiseSuite)
	}
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	ini, _ := newLivePair(t, a, b, livePrologue())
	if got := ini.Suite(); got != NoiseSuite {
		t.Errorf("session Suite() = %q, want %q", got, NoiseSuite)
	}
}

// TestNoiseXX_KnownAnswer pins a deterministic XX transcript (fixed statics +
// fixed ephemeral randomness) — the cross-language KAT that keeps the Go and
// Swift stacks byte-compatible. The channel binding is derive-and-pin: the
// implementer records the observed transcript/binding at first green (the KAT
// cannot be computed by hand). The always-on assertions below still fail RED on
// the undefined symbols.
func TestNoiseXX_KnownAnswer(t *testing.T) {
	a := NewIdentityFromMaterial(fill(0x01), fill(0x02))
	b := NewIdentityFromMaterial(fill(0x03), fill(0x04))

	ini, err := NewNoise(NoiseConfig{
		Initiator: true, Static: a.NoiseStatic(), PeerStatic: b.NoiseStaticPublic(),
		Prologue: livePrologue(), Rand: bytes.NewReader(bytes.Repeat([]byte{0x55}, 256)),
	})
	if err != nil {
		t.Fatalf("NewNoise(ini): %v", err)
	}
	resp, err := NewNoise(NoiseConfig{
		Initiator: false, Static: b.NoiseStatic(), PeerStatic: a.NoiseStaticPublic(),
		Prologue: livePrologue(), Rand: bytes.NewReader(bytes.Repeat([]byte{0xAA}, 256)),
	})
	if err != nil {
		t.Fatalf("NewNoise(resp): %v", err)
	}
	if err := driveXX(ini, resp); err != nil {
		t.Fatalf("deterministic handshake: %v", err)
	}
	// Both ends must agree on the channel binding regardless of the pinned
	// value; a clean transcript yields one binding.
	if !bytes.Equal(ini.ChannelBinding(), resp.ChannelBinding()) {
		t.Fatal("channel bindings differ on a clean handshake")
	}
	if len(wantChannelBinding) == 0 {
		t.Log("TODO(impl): pin wantChannelBinding from the observed deterministic transcript at first green")
	} else if !bytes.Equal(ini.ChannelBinding(), wantChannelBinding) {
		t.Errorf("channel binding = %x, want %x", ini.ChannelBinding(), wantChannelBinding)
	}
}

// wantChannelBinding is a derive-and-pin KAT (fixed statics + fixed ephemeral
// randomness above); filled by the implementer at first green.
var wantChannelBinding []byte

// TestNoise_PrologueMismatchAborts pins R-CRY.5: differing prologues abort the
// handshake at the first MAC (downgrade protection), before any transport byte.
func TestNoise_PrologueMismatchAborts(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	ini, err := NewNoise(NoiseConfig{
		Initiator: true, Static: a.NoiseStatic(), PeerStatic: b.NoiseStaticPublic(),
		Prologue: LivePrologue([]byte("machine-route"), []byte("device-route")),
	})
	if err != nil {
		t.Fatalf("NewNoise(ini): %v", err)
	}
	resp, err := NewNoise(NoiseConfig{
		Initiator: false, Static: b.NoiseStatic(), PeerStatic: a.NoiseStaticPublic(),
		Prologue: LivePrologue([]byte("machine-route"), []byte("DIFFERENT-route")),
	})
	if err != nil {
		t.Fatalf("NewNoise(resp): %v", err)
	}
	if err := driveXX(ini, resp); err == nil {
		t.Fatal("handshake completed despite prologue mismatch")
	}
	if resp.HandshakeComplete() {
		t.Error("responder reached transport mode despite prologue mismatch")
	}
}

// TestNoise_RouteBindingAborts pins R-CRY.5: the route ids are bound into the
// prologue, so a peer bound to a different route (correct protocol, wrong
// route) aborts — the relay cannot cross-wire two sessions.
func TestNoise_RouteBindingAborts(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	ini, _ := NewNoise(NoiseConfig{
		Initiator: true, Static: a.NoiseStatic(), PeerStatic: b.NoiseStaticPublic(),
		Prologue: LivePrologue([]byte("route-A"), []byte("dev-1")),
	})
	resp, _ := NewNoise(NoiseConfig{
		Initiator: false, Static: b.NoiseStatic(), PeerStatic: a.NoiseStaticPublic(),
		Prologue: LivePrologue([]byte("route-B"), []byte("dev-1")),
	})
	if err := driveXX(ini, resp); err == nil {
		t.Fatal("handshake completed across mismatched route binding")
	}
}

// TestLive_PinnedStaticMitmRejected pins R-CRY.6: an initiator pinned to peer A
// that instead reaches an attacker M (its own valid static) aborts before any
// transport byte — XX makes this authenticated, not TOFU.
func TestLive_PinnedStaticMitmRejected(t *testing.T) {
	a, _ := GenerateIdentity()      // honest initiator
	honest, _ := GenerateIdentity() // the peer the initiator pinned
	mitm, _ := GenerateIdentity()   // attacker, a different valid static

	ini, err := NewNoise(NoiseConfig{
		Initiator: true, Static: a.NoiseStatic(), PeerStatic: honest.NoiseStaticPublic(),
		Prologue: livePrologue(),
	})
	if err != nil {
		t.Fatalf("NewNoise(ini): %v", err)
	}
	attacker, err := NewNoise(NoiseConfig{
		Initiator: false, Static: mitm.NoiseStatic(), PeerStatic: a.NoiseStaticPublic(),
		Prologue: livePrologue(),
	})
	if err != nil {
		t.Fatalf("NewNoise(attacker): %v", err)
	}
	if err := driveXX(ini, attacker); err == nil {
		t.Fatal("initiator accepted an unpinned (MITM) static")
	}
	if ini.HandshakeComplete() {
		t.Error("initiator reached transport mode against an unpinned static")
	}
}

// TestLive_UnknownPeerRejected pins R-CRY.6 from the responder side: a
// responder pinned to a known device rejects an initiator whose static is not
// the pinned one.
func TestLive_UnknownPeerRejected(t *testing.T) {
	known, _ := GenerateIdentity()    // the device the responder pinned
	stranger, _ := GenerateIdentity() // an unpaired initiator
	resp, _ := GenerateIdentity()

	ini, _ := NewNoise(NoiseConfig{
		Initiator: true, Static: stranger.NoiseStatic(), PeerStatic: resp.NoiseStaticPublic(),
		Prologue: livePrologue(),
	})
	responder, _ := NewNoise(NoiseConfig{
		Initiator: false, Static: resp.NoiseStatic(), PeerStatic: known.NoiseStaticPublic(),
		Prologue: livePrologue(),
	})
	if err := driveXX(ini, responder); err == nil {
		t.Fatal("responder accepted an unknown (unpinned) peer static")
	}
}

// TestTransport_ReplayFrameRejected pins R-CRY.7: a replayed transport frame
// fails AEAD (the receiving CipherState nonce already advanced).
func TestTransport_ReplayFrameRejected(t *testing.T) {
	ini, resp := completedPair(t)
	ct, err := ini.Encrypt([]byte("frame-1"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := resp.Decrypt(ct); err != nil {
		t.Fatalf("first Decrypt: %v", err)
	}
	if _, err := resp.Decrypt(ct); err == nil {
		t.Fatal("replayed frame accepted; nonce reuse not rejected")
	}
}

// TestTransport_ReorderRejected pins R-CRY.7: delivering frame 2 before frame 1
// fails AEAD (out-of-order nonce).
func TestTransport_ReorderRejected(t *testing.T) {
	ini, resp := completedPair(t)
	_, err := ini.Encrypt([]byte("frame-1"))
	if err != nil {
		t.Fatalf("Encrypt(1): %v", err)
	}
	ct2, err := ini.Encrypt([]byte("frame-2"))
	if err != nil {
		t.Fatalf("Encrypt(2): %v", err)
	}
	if _, err := resp.Decrypt(ct2); err == nil {
		t.Fatal("out-of-order frame accepted; reorder not rejected")
	}
}

// TestTransport_RekeyThreshold pins R-CRY.7: the rekey thresholds are the
// specified 15 min / 1 GiB, and a coordinated Rekey() rotates the keystream
// (same plaintext -> different ciphertext) while transport still round-trips.
func TestTransport_RekeyThreshold(t *testing.T) {
	if RekeyAfterBytes != 1<<30 {
		t.Errorf("RekeyAfterBytes = %d, want %d (1 GiB)", RekeyAfterBytes, 1<<30)
	}
	if RekeyAfterDuration != 15*time.Minute {
		t.Errorf("RekeyAfterDuration = %v, want 15m", RekeyAfterDuration)
	}
	ini, resp := completedPair(t)
	msg := []byte("same-plaintext")
	before, err := ini.Encrypt(msg)
	if err != nil {
		t.Fatalf("Encrypt(before): %v", err)
	}
	if _, err := resp.Decrypt(before); err != nil {
		t.Fatalf("Decrypt(before): %v", err)
	}
	ini.Rekey()
	resp.Rekey()
	after, err := ini.Encrypt(msg)
	if err != nil {
		t.Fatalf("Encrypt(after): %v", err)
	}
	if bytes.Equal(before, after) {
		t.Error("Rekey did not rotate the keystream")
	}
	if _, err := resp.Decrypt(after); err != nil {
		t.Errorf("Decrypt after coordinated rekey failed: %v", err)
	}
}

// TestTransport_FreshEphemeralsPerConn pins R-CRY.8: two independent
// connections use fresh ephemerals, so their first handshake messages differ.
func TestTransport_FreshEphemeralsPerConn(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()

	first := func() []byte {
		ini, _ := NewNoise(NoiseConfig{
			Initiator: true, Static: a.NoiseStatic(), PeerStatic: b.NoiseStaticPublic(),
			Prologue: livePrologue(),
		})
		m1, err := ini.WriteMessage(nil)
		if err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}
		return m1
	}
	if bytes.Equal(first(), first()) {
		t.Fatal("two connections produced identical first messages; ephemerals reused")
	}
}

// TestTransport_NoResumptionState pins R-CRY.8: no session-resumption surface
// exists (no exported Export/Resume/Ticket/State method), and two fresh
// handshakes derive different channel bindings (no shared resumption secret).
func TestTransport_NoResumptionState(t *testing.T) {
	banned := regexp.MustCompile(`(?i)(resum|ticket|export|serialize|marshalstate|savestate)`)
	typ := reflect.TypeOf(&NoiseSession{})
	for i := 0; i < typ.NumMethod(); i++ {
		if banned.MatchString(typ.Method(i).Name) {
			t.Errorf("NoiseSession exposes a resumption-like method %q", typ.Method(i).Name)
		}
	}

	binding := func() []byte {
		a, _ := GenerateIdentity()
		b, _ := GenerateIdentity()
		ini, resp := newLivePair(t, a, b, livePrologue())
		if err := driveXX(ini, resp); err != nil {
			t.Fatalf("handshake: %v", err)
		}
		return ini.ChannelBinding()
	}
	if bytes.Equal(binding(), binding()) {
		t.Fatal("two fresh handshakes share a channel binding; resumption secret leaked")
	}
}

// completedPair returns two pinned XX sessions that have completed the
// handshake and are in transport mode.
func completedPair(t *testing.T) (*NoiseSession, *NoiseSession) {
	t.Helper()
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	ini, resp := newLivePair(t, a, b, livePrologue())
	if err := driveXX(ini, resp); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	return ini, resp
}
