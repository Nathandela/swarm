// FAILING-FIRST (TDD RED, GG-5) tests for slice S14a / PB-KEY-9(a): the FAILABILITY half
// of the Go-side custody seam.
//
// These do not compile against today's interface, and that IS the finding. ADR-007 B14
// decided that `crypto.KeyStore` becomes failable and that "the signature change lands in
// the Go core, not the Android slice"; it was never implemented, so
// `SignCommand(msg []byte) []byte`, `SignRelayAuth(challenge []byte) []byte` and
// `NoiseStatic() *NoiseStatic` still cannot fail. PB-KEY-6's acceptance criterion is "a test
// drives an auth-required failure and a key-invalidated failure through every signing path",
// and no test anywhere -- Go or Kotlin -- can drive a failure through a function that has no
// error return. The interface is the gate, so the RED is a build failure at the interface.
//
// SCOPE. ADR-007 B14 is the authorisation for this edit to the FROZEN crypto package, and it
// authorises the SIGNATURES only: "no crypto semantics change". TestS14A_WireOutputIsUnchanged
// pins that with known-answer vectors captured from the pre-change implementation, so a
// signature change that also moved a byte of output fails here rather than on a handset
// (R-CRY.15: a hardware-gated implementation must drop in with bit-identical wire output).

package crypto

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

// s14aMaterial is the fixed device material behind every known-answer vector below. The
// same pattern the android/gate PB-SEC-1 tests use, so a vector can be cross-read.
func s14aMaterial() KeyMaterial {
	var m KeyMaterial
	for i := range m.NoiseStaticPriv {
		m.NoiseStaticPriv[i] = byte(0x10 + i)
		m.RecipientPriv[i] = byte(0x40 + i)
		m.CommandSignSeed[i] = byte(0x70 + i)
		m.RelayAuthSeed[i] = byte(0xA0 + i)
	}
	return m
}

// s14aDrivenKeyStore is a KeyStore whose three failable operations are driven by the test.
// It is the ONLY way PB-KEY-6's criterion can be expressed: an Android-Keystore-backed
// store fails on user-authentication-required and on permanent invalidation after a
// biometric-enrollment change, and neither failure is representable today. Public-key
// accessors and OpenSealedBox delegate to a real software store so the failure under test
// is the only thing that differs from the working path.
type s14aDrivenKeyStore struct {
	inner KeyStore

	signCommandErr   error
	signRelayAuthErr error
	noiseStaticErr   error
}

// Compile-time assertion that the driven store satisfies KeyStore. This single line is what
// fails to build today, and it is the whole of PB-KEY-9(a) stated as a type constraint.
var _ KeyStore = (*s14aDrivenKeyStore)(nil)

func (k *s14aDrivenKeyStore) NoiseStaticPublic() []byte    { return k.inner.NoiseStaticPublic() }
func (k *s14aDrivenKeyStore) RecipientPublic() []byte      { return k.inner.RecipientPublic() }
func (k *s14aDrivenKeyStore) CommandSigningPublic() []byte { return k.inner.CommandSigningPublic() }
func (k *s14aDrivenKeyStore) RelayAuthPublic() []byte      { return k.inner.RelayAuthPublic() }

func (k *s14aDrivenKeyStore) OpenSealedBox(sealed []byte) ([]byte, error) {
	return k.inner.OpenSealedBox(sealed)
}

func (k *s14aDrivenKeyStore) NoiseStatic() (*NoiseStatic, error) {
	if k.noiseStaticErr != nil {
		return nil, k.noiseStaticErr
	}
	return k.inner.NoiseStatic()
}

func (k *s14aDrivenKeyStore) SignCommand(msg []byte) ([]byte, error) {
	if k.signCommandErr != nil {
		return nil, k.signCommandErr
	}
	return k.inner.SignCommand(msg)
}

func (k *s14aDrivenKeyStore) SignRelayAuth(challenge []byte) ([]byte, error) {
	if k.signRelayAuthErr != nil {
		return nil, k.signRelayAuthErr
	}
	return k.inner.SignRelayAuth(challenge)
}

func s14aSoftwareStore(t *testing.T) KeyStore {
	t.Helper()
	ks, err := NewFileKeyStoreFromMaterial(t.TempDir(), s14aMaterial())
	if err != nil {
		t.Fatalf("seeding a software key store: %v", err)
	}
	return ks
}

// TestS14A_EverySigningPathCanFail is PB-KEY-6's criterion, made executable. Each of the
// three operations B14 names must be able to report BOTH an auth-required failure and a
// permanent invalidation, and must return NO usable artifact when it does -- a store that
// returned an error alongside a signature would let a caller ship an unauthorised one.
func TestS14A_EverySigningPathCanFail(t *testing.T) {
	inner := s14aSoftwareStore(t)

	for _, tc := range []struct {
		failure string
		err     error
	}{
		{"auth-required", ErrKeyAuthRequired},
		{"key-invalidated", ErrKeyInvalidated},
	} {
		t.Run(tc.failure, func(t *testing.T) {
			t.Run("SignCommand", func(t *testing.T) {
				ks := &s14aDrivenKeyStore{inner: inner, signCommandErr: tc.err}
				sig, err := ks.SignCommand([]byte("op"))
				if !errors.Is(err, tc.err) {
					t.Errorf("PB-KEY-6: SignCommand under %s returned err %v, want %v", tc.failure, err, tc.err)
				}
				if sig != nil {
					t.Errorf("PB-KEY-6: SignCommand returned %d signature bytes alongside its error; a "+
						"failed signing operation must yield nothing a caller could send", len(sig))
				}
			})
			t.Run("SignRelayAuth", func(t *testing.T) {
				ks := &s14aDrivenKeyStore{inner: inner, signRelayAuthErr: tc.err}
				sig, err := ks.SignRelayAuth([]byte("challenge"))
				if !errors.Is(err, tc.err) {
					t.Errorf("PB-KEY-6: SignRelayAuth under %s returned err %v, want %v", tc.failure, err, tc.err)
				}
				if sig != nil {
					t.Errorf("PB-KEY-6: SignRelayAuth returned %d signature bytes alongside its error", len(sig))
				}
			})
			t.Run("NoiseStatic", func(t *testing.T) {
				ks := &s14aDrivenKeyStore{inner: inner, noiseStaticErr: tc.err}
				h, err := ks.NoiseStatic()
				if !errors.Is(err, tc.err) {
					t.Errorf("PB-KEY-6: NoiseStatic under %s returned err %v, want %v", tc.failure, err, tc.err)
				}
				if h != nil {
					t.Error("PB-KEY-6: NoiseStatic returned a usable handshake handle alongside its error; " +
						"the handshake would proceed on a key custody refused to authorise")
				}
			})
		})
	}
}

// TestS14A_TheTwoFailuresAreDistinguishable. PB-KEY-6 requires an auth-required failure AND a
// key-invalidated failure to be driven -- two named cases, not one. If both collapse to a bare
// `errors.New`, a test asserting `err != nil` cannot tell them apart and the criterion is
// satisfiable while only one is implemented. They also demand OPPOSITE handling: auth-required
// is recoverable by prompting the user, permanent invalidation (biometric re-enrollment) is
// not and forces re-pairing, so a UI that cannot distinguish them cannot be correct.
func TestS14A_TheTwoFailuresAreDistinguishable(t *testing.T) {
	if ErrKeyAuthRequired == nil || ErrKeyInvalidated == nil {
		t.Fatal("PB-KEY-6: both custody sentinels must exist")
	}
	if errors.Is(ErrKeyAuthRequired, ErrKeyInvalidated) || errors.Is(ErrKeyInvalidated, ErrKeyAuthRequired) {
		t.Error("PB-KEY-6: ErrKeyAuthRequired and ErrKeyInvalidated are not distinguishable. A recoverable " +
			"re-prompt and a permanent invalidation requiring re-pairing would be handled identically")
	}
	// They must survive wrapping: an Android implementation reports the platform exception as
	// context, and a call site matching on equality rather than errors.Is would miss it.
	wrapped := fmt.Errorf("keystore: %w", ErrKeyInvalidated)
	if !errors.Is(wrapped, ErrKeyInvalidated) {
		t.Error("PB-KEY-6: the sentinel does not survive wrapping, so a platform error cannot carry context")
	}
	if errors.Is(wrapped, ErrKeyAuthRequired) {
		t.Error("PB-KEY-6: a wrapped ErrKeyInvalidated also matches ErrKeyAuthRequired")
	}
}

// TestS14A_SoftwareStoreStillSucceeds. The desktop/test path has no auth gate, so the
// failable signatures must not make it fail. Without this, every assertion above is
// satisfiable by a store that fails unconditionally -- the "assertion unsatisfiable by any
// input" class, inverted.
func TestS14A_SoftwareStoreStillSucceeds(t *testing.T) {
	ks := s14aSoftwareStore(t)

	sig, err := ks.SignCommand([]byte("op"))
	if err != nil || len(sig) == 0 {
		t.Errorf("software SignCommand: sig %d bytes, err %v; want a signature and no error", len(sig), err)
	}
	if sig, err := ks.SignRelayAuth([]byte("challenge")); err != nil || len(sig) == 0 {
		t.Errorf("software SignRelayAuth: sig %d bytes, err %v; want a signature and no error", len(sig), err)
	}
	h, err := ks.NoiseStatic()
	if err != nil || h == nil {
		t.Errorf("software NoiseStatic: handle %v, err %v; want a handle and no error", h, err)
	}
}

// TestS14A_WireOutputIsUnchanged pins B14's "no crypto semantics change" and R-CRY.15's
// "bit-identical wire output" with known-answer vectors captured from the implementation as
// it stood BEFORE the signature change. Ed25519 is deterministic and X25519 public-key
// derivation is a pure function, so every one of these is a fixed byte string for fixed
// material: any drift -- a re-derived seed, a reordered file layout, a domain-separation
// string quietly added while "just adding an error" -- changes a vector here.
//
// This is the check that keeps the frozen-package edit inside what B14 authorised.
func TestS14A_WireOutputIsUnchanged(t *testing.T) {
	ks := s14aSoftwareStore(t)

	for _, tc := range []struct {
		name string
		got  []byte
		want string
	}{
		{"NoiseStaticPublic", ks.NoiseStaticPublic(),
			"d89e3bad79437dbed9f843418304f460ff05c7fe81fe4a9577a804cb9367ff66"},
		{"RecipientPublic", ks.RecipientPublic(),
			"79a631eede1bf9c98f12032cdeadd0e7a079398fc786b88cc846ec89af85a51a"},
		{"CommandSigningPublic", ks.CommandSigningPublic(),
			"1ce56a48c82ff99162a14bc544612674e5d61fb9317e65d4055780fdbcb4dc35"},
		{"RelayAuthPublic", ks.RelayAuthPublic(),
			"4fd099ccd47d7893dfe9ec24414ecb0d9b5420232aad30d91c465be33cbe65c4"},
	} {
		want, err := hex.DecodeString(tc.want)
		if err != nil {
			t.Fatalf("vector %s is malformed: %v", tc.name, err)
		}
		if !bytes.Equal(tc.got, want) {
			t.Errorf("R-CRY.15: %s changed across the B14 signature change.\n got %x\nwant %x",
				tc.name, tc.got, want)
		}
	}

	sig, err := ks.SignCommand([]byte("s14a-command-known-answer"))
	if err != nil {
		t.Fatalf("SignCommand on software material: %v", err)
	}
	wantCmd, _ := hex.DecodeString(
		"24886d47f25bf4e3696daa530354c7c22b7009cd354da38541dc93308df238dc" +
			"98ac3389cf5837957456e63739183d38ca320d4d4da575dff3822f6312de8d0f")
	if !bytes.Equal(sig, wantCmd) {
		t.Errorf("R-CRY.15: the command signature changed across the B14 signature change.\n got %x\nwant %x",
			sig, wantCmd)
	}

	rsig, err := ks.SignRelayAuth([]byte("s14a-relay-known-answer"))
	if err != nil {
		t.Fatalf("SignRelayAuth on software material: %v", err)
	}
	wantRelay, _ := hex.DecodeString(
		"58fa068fb0eccbec0d32636d71575f9e6ba7ffb79478cb1f27791875b0734cca" +
			"aee47b7506a001d7c116ae0529b4097a7b93c68d61711fc0cfdd9197e0ae0602")
	if !bytes.Equal(rsig, wantRelay) {
		t.Errorf("R-CRY.15: the relay-auth signature changed across the B14 signature change.\n got %x\nwant %x",
			rsig, wantRelay)
	}

	// A signature that still verifies against the store's own public key is necessary but not
	// sufficient (any consistent re-keying would pass); the vectors above are what pin it to
	// the Phase A bytes. Both are asserted so a vector refreshed to match a drifted
	// implementation still has to face this.
	if err := VerifyCommandSig(ks.CommandSigningPublic(), []byte("s14a-command-known-answer"), sig); err != nil {
		t.Errorf("R-CRY.15: the pinned command signature no longer verifies: %v", err)
	}
}
