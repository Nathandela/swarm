// R-CRY.2 — device key via a KeyStore interface (file impl now); A14 — the
// device holds TWO X25519 keys (Noise-static + sealed-box recipient) behind the
// store. R-CRY.3 — a separate Ed25519 relay-auth key, distinct from identity,
// and the X25519 identity is never disclosed to the relay.
//
// FROZEN CONTRACT (subset):
//
//	type KeyStore interface {
//	    NoiseStaticPublic() []byte
//	    RecipientPublic() []byte
//	    NoiseStatic() *NoiseStatic          // opaque handshake handle, not raw export
//	    OpenSealedBox(sealed []byte) ([]byte, error)
//	    SignCommand(msg []byte) []byte      // Ed25519 device command signature (D4)
//	    CommandSigningPublic() []byte
//	    SignRelayAuth(challenge []byte) []byte
//	    RelayAuthPublic() []byte
//	}
//	type KeyMaterial struct{ NoiseStaticPriv, RecipientPriv, CommandSignSeed, RelayAuthSeed [32]byte }
//	func NewFileKeyStore(dir string) (KeyStore, error)
//	func OpenFileKeyStore(dir string) (KeyStore, error)
//	func NewFileKeyStoreFromMaterial(dir string, m KeyMaterial) (KeyStore, error)
//
// The store exposes DH (via the Noise-static handle), sealed-box open, and SIGN
// — but NEVER the raw private scalar. A hardware-gated impl must drop in with
// identical wire output (R-CRY.15 proxy).
package crypto

import (
	"bytes"
	"reflect"
	"regexp"
	"testing"
)

// TestKeyStore_NoPrivateExport pins that no KeyStore method returns a raw
// private scalar (the Noise-static or recipient private, or an Ed25519 seed) —
// neither by a suspiciously named exporter (type check) nor by any no-arg
// accessor (behavioral reflection sweep).
func TestKeyStore_NoPrivateExport(t *testing.T) {
	m := stdMaterial()
	ks := devKeyStore(t, m)

	privates := [][]byte{
		m.NoiseStaticPriv[:], m.RecipientPriv[:],
		m.CommandSignSeed[:], m.RelayAuthSeed[:],
	}

	// Type check: no exported method name advertises a private export.
	banned := regexp.MustCompile(`(?i)(private|secret|seed|scalar|export|raw)`)
	v := reflect.ValueOf(ks)
	typ := v.Type()
	for i := 0; i < typ.NumMethod(); i++ {
		if banned.MatchString(typ.Method(i).Name) {
			t.Errorf("KeyStore exposes a private-looking method %q", typ.Method(i).Name)
		}
	}

	// Behavioral sweep: call every zero-arg method and assert no returned
	// []byte contains a private scalar. Methods requiring args are exercised
	// separately below with benign inputs.
	for i := 0; i < typ.NumMethod(); i++ {
		mt := typ.Method(i)
		if mt.Type.NumIn() != 1 { // receiver only
			continue
		}
		for _, out := range v.Method(i).Call(nil) {
			assertNoPrivate(t, mt.Name, out, privates)
		}
	}

	// Methods that take input still must not echo a private scalar out.
	assertNoPrivateBytes(t, "SignCommand", ks.SignCommand([]byte("op")), privates)
	assertNoPrivateBytes(t, "SignRelayAuth", ks.SignRelayAuth([]byte("challenge")), privates)
}

func assertNoPrivate(t *testing.T, name string, out reflect.Value, privates [][]byte) {
	t.Helper()
	if out.Kind() != reflect.Slice || out.Type().Elem().Kind() != reflect.Uint8 {
		return
	}
	assertNoPrivateBytes(t, name, out.Bytes(), privates)
}

func assertNoPrivateBytes(t *testing.T, name string, got []byte, privates [][]byte) {
	t.Helper()
	for _, p := range privates {
		if bytes.Contains(got, p) {
			t.Errorf("method %s leaks a private scalar in its return value", name)
		}
	}
}

// TestKeyStore_FileImplConformance pins that the file-backed impl is
// deterministic given fixed material: reloaded and re-created stores agree on
// every public key, on deterministic Ed25519 signatures, and on sealed-box
// opens — the property a hardware KeyStore must match bit-for-bit (R-CRY.15).
func TestKeyStore_FileImplConformance(t *testing.T) {
	m := stdMaterial()
	a := devKeyStore(t, m)

	// Reload a persisted store and re-create from the same material: all three
	// must produce identical wire output.
	dir := t.TempDir()
	b, err := NewFileKeyStoreFromMaterial(dir, m)
	if err != nil {
		t.Fatalf("NewFileKeyStoreFromMaterial: %v", err)
	}
	c, err := OpenFileKeyStore(dir)
	if err != nil {
		t.Fatalf("OpenFileKeyStore: %v", err)
	}

	for _, pair := range []struct {
		name   string
		getter func(KeyStore) []byte
	}{
		{"NoiseStaticPublic", KeyStore.NoiseStaticPublic},
		{"RecipientPublic", KeyStore.RecipientPublic},
		{"CommandSigningPublic", KeyStore.CommandSigningPublic},
		{"RelayAuthPublic", KeyStore.RelayAuthPublic},
	} {
		if len(pair.getter(a)) != 32 {
			t.Errorf("%s must be 32 bytes, got %d", pair.name, len(pair.getter(a)))
		}
		if !bytes.Equal(pair.getter(a), pair.getter(b)) || !bytes.Equal(pair.getter(b), pair.getter(c)) {
			t.Errorf("%s differs across identical-material stores", pair.name)
		}
	}

	// Ed25519 is deterministic: identical material -> identical signature.
	msg := []byte("canonical-command-bytes")
	if !bytes.Equal(a.SignCommand(msg), c.SignCommand(msg)) {
		t.Error("SignCommand not deterministic across identical-material stores")
	}
	if err := VerifyCommandSig(a.CommandSigningPublic(), msg, a.SignCommand(msg)); err != nil {
		t.Errorf("self-produced command signature failed verify: %v", err)
	}

	// Sealed-box round-trip: seal to the store's recipient key, open via the
	// store; the two stores share a recipient key so either opens it.
	sealed, err := SealToRecipient(a.RecipientPublic(), []byte("epoch-secret"))
	if err != nil {
		t.Fatalf("SealToRecipient: %v", err)
	}
	got, err := c.OpenSealedBox(sealed)
	if err != nil {
		t.Fatalf("OpenSealedBox: %v", err)
	}
	if string(got) != "epoch-secret" {
		t.Errorf("sealed-box round-trip = %q, want %q", got, "epoch-secret")
	}
	// The external libsodium crypto_box_seal interop KAT lives in epoch_test.go
	// (TestEpochGrant_LibsodiumKAT), the canonical sealed-box user.
}

// TestRelayAuth_DistinctFromIdentity pins R-CRY.3: the Ed25519 relay-auth key
// is a separate key, not any X25519 identity key.
func TestRelayAuth_DistinctFromIdentity(t *testing.T) {
	ks := devKeyStore(t, stdMaterial())
	ra := ks.RelayAuthPublic()
	if len(ra) != 32 {
		t.Errorf("Ed25519 relay-auth public must be 32 bytes, got %d", len(ra))
	}
	if bytes.Equal(ra, ks.NoiseStaticPublic()) || bytes.Equal(ra, ks.RecipientPublic()) {
		t.Error("relay-auth key must be distinct from the X25519 identity keys")
	}
	if bytes.Equal(ra, ks.CommandSigningPublic()) {
		t.Error("relay-auth key must be distinct from the command-signing key")
	}
}

// TestRelayAuth_IdentityNeverOnWire pins R-CRY.3: what the device presents to
// the relay (relay-auth public + a signed challenge) never carries the X25519
// identity keys. The relay authenticates the routing id without learning
// identity.
func TestRelayAuth_IdentityNeverOnWire(t *testing.T) {
	ks := devKeyStore(t, stdMaterial())
	wire := bytes.Join([][]byte{
		ks.RelayAuthPublic(),
		ks.SignRelayAuth([]byte("relay-challenge-nonce||ctx")),
	}, nil)
	for _, id := range [][]byte{ks.NoiseStaticPublic(), ks.RecipientPublic()} {
		if bytes.Contains(wire, id) {
			t.Error("X25519 identity key appears in the relay-auth wire bytes")
		}
	}
}
