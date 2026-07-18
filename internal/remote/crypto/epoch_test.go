// R-CRY.10/.13 (+ D.0-A15) — epoch content keys, EpochGrant, and the wake/
// content key split.
//
// Each epoch delivers TWO independent keys (A15): a WAKE key (after-first-
// unlock, NSE-readable, decrypts ONLY content-free type-0x02 push wakes) and a
// CONTENT key (biometric-gated, NOT NSE-readable, NOT derivable from the wake
// key, decrypts type-0x01 mailbox session content). The grant is sealed with
// box.SealAnonymous (crypto_box_seal-compatible) to the device RECIPIENT X25519
// key (A14), delivered via the relay mailbox so an offline-at-rotation device
// receives it on reconnect (A5).
//
// FROZEN CONTRACT (subset):
//
//	type EpochKeys struct { WakeKey, ContentKey [32]byte }
//	type EpochGrant struct { EpochID uint32; GrantSeq uint64; Sealed []byte }
//	func SealEpochGrant(recipientPub []byte, epochID uint32, grantSeq uint64, keys EpochKeys) (*EpochGrant, error)
//	func OpenEpochGrant(ks KeyStore, g *EpochGrant) (uint32, uint64, EpochKeys, error)
//	func SealToRecipient(recipientPub, plaintext []byte) ([]byte, error)  // crypto_box_seal
package crypto

import (
	"bytes"
	"testing"
)

func testEpochKeys() EpochKeys {
	return EpochKeys{WakeKey: fill(0xE1), ContentKey: fill(0xC2)}
}

// TestEpochGrant_SealOpenRoundTrip pins R-CRY.10: a grant sealed to a device's
// recipient key opens (only) via that device's KeyStore, recovering both epoch
// keys and the epoch/grant coordinates.
func TestEpochGrant_SealOpenRoundTrip(t *testing.T) {
	ks := devKeyStore(t, stdMaterial())
	keys := testEpochKeys()

	grant, err := SealEpochGrant(ks.RecipientPublic(), 5, 1, keys)
	if err != nil {
		t.Fatalf("SealEpochGrant: %v", err)
	}
	if bytes.Contains(grant.Sealed, keys.WakeKey[:]) || bytes.Contains(grant.Sealed, keys.ContentKey[:]) {
		t.Error("sealed grant exposes an epoch key in cleartext")
	}

	epochID, grantSeq, got, err := OpenEpochGrant(ks, grant)
	if err != nil {
		t.Fatalf("OpenEpochGrant: %v", err)
	}
	if epochID != 5 || grantSeq != 1 {
		t.Errorf("opened (epoch=%d, grant_seq=%d), want (5, 1)", epochID, grantSeq)
	}
	if got.WakeKey != keys.WakeKey || got.ContentKey != keys.ContentKey {
		t.Error("opened epoch keys do not match sealed keys")
	}
}

// TestEpochGrant_WrongKeyFails pins R-CRY.10: a different device (different
// recipient key) cannot open the grant.
func TestEpochGrant_WrongKeyFails(t *testing.T) {
	target := devKeyStore(t, stdMaterial())
	other := devKeyStore(t, KeyMaterial{
		NoiseStaticPriv: fill(0x51), RecipientPriv: fill(0x52),
		CommandSignSeed: fill(0x53), RelayAuthSeed: fill(0x54),
	})

	grant, err := SealEpochGrant(target.RecipientPublic(), 5, 1, testEpochKeys())
	if err != nil {
		t.Fatalf("SealEpochGrant: %v", err)
	}
	if _, _, _, err := OpenEpochGrant(other, grant); err == nil {
		t.Fatal("a non-recipient device opened the epoch grant")
	}
}

// TestEpochGrant_DeliveredViaMailboxToOfflineDevice pins A5: the grant is an
// async sealed-box artifact, so a device offline during rotation opens it later
// (the sealed bytes survive a store round-trip / serialization).
func TestEpochGrant_DeliveredViaMailboxToOfflineDevice(t *testing.T) {
	ks := devKeyStore(t, stdMaterial())
	keys := testEpochKeys()

	grant, err := SealEpochGrant(ks.RecipientPublic(), 9, 3, keys)
	if err != nil {
		t.Fatalf("SealEpochGrant: %v", err)
	}
	// Simulate mailbox transit: the relay stores and later forwards the opaque
	// sealed bytes unchanged.
	transited := &EpochGrant{EpochID: grant.EpochID, GrantSeq: grant.GrantSeq, Sealed: append([]byte(nil), grant.Sealed...)}

	epochID, grantSeq, got, err := OpenEpochGrant(ks, transited)
	if err != nil {
		t.Fatalf("OpenEpochGrant after transit: %v", err)
	}
	if epochID != 9 || grantSeq != 3 || got.ContentKey != keys.ContentKey {
		t.Error("grant did not survive mailbox transit intact")
	}
}

// TestEpochGrant_LibsodiumKAT pins crypto_box_seal interop: an EpochGrant sealed
// by an external libsodium under a fixed recipient key opens to the known epoch
// keys. Derive-and-pin — the implementer records libsodiumGrantSealed +
// libsodiumGrantKeys from a real libsodium at first green (not from our own
// impl, which would be circular).
func TestEpochGrant_LibsodiumKAT(t *testing.T) {
	if len(libsodiumGrantSealed) == 0 {
		t.Skip("TODO(impl): pin libsodiumGrantSealed (external crypto_box_seal KAT) at first green")
	}
	ks := devKeyStore(t, KeyMaterial{RecipientPriv: libsodiumGrantRecipientPriv})
	_, _, got, err := OpenEpochGrant(ks, &EpochGrant{EpochID: 1, GrantSeq: 1, Sealed: libsodiumGrantSealed})
	if err != nil {
		t.Fatalf("OpenEpochGrant(libsodium KAT): %v", err)
	}
	if got != libsodiumGrantKeys {
		t.Errorf("libsodium KAT epoch keys = %x/%x, want pinned", got.WakeKey, got.ContentKey)
	}
}

var (
	libsodiumGrantSealed        []byte
	libsodiumGrantRecipientPriv [32]byte
	libsodiumGrantKeys          EpochKeys
)

// TestNSE_WakeKeyDecryptsNoSessionContent pins A15/R-CRY.13: the wake key opens
// content-free type-0x02 push wakes but cannot open type-0x01 session content
// (that is under the separate content key). A once-unlocked phone, whose NSE
// holds only the wake key, yields no session history.
func TestNSE_WakeKeyDecryptsNoSessionContent(t *testing.T) {
	keys := testEpochKeys()

	wakeHdr := testHeader()
	wakeHdr.Type = TypePushWake
	wakeEnv, err := Seal(keys.WakeKey, wakeHdr, []byte("activity on machine X"))
	if err != nil {
		t.Fatalf("Seal(wake): %v", err)
	}
	if _, err := wakeEnv.Open(keys.WakeKey); err != nil {
		t.Errorf("NSE wake key failed to open a push-wake payload: %v", err)
	}

	contentHdr := testHeader()
	contentHdr.Type = TypeMailbox
	contentEnv, err := Seal(keys.ContentKey, contentHdr, []byte("secret session transcript"))
	if err != nil {
		t.Fatalf("Seal(content): %v", err)
	}
	if _, err := contentEnv.Open(keys.WakeKey); err == nil {
		t.Fatal("the NSE-readable wake key decrypted session content")
	}
}

// TestContentKey_BiometricGatedNotNSEReadable pins A15: the content key is
// independent of the wake key (not derivable from it), so holding the wake key
// grants no access to content. (The at-rest biometric-gating / two-store
// separation is DEFERRED-VERIFICATION; proxy: distinct keys + failed cross-open.)
func TestContentKey_BiometricGatedNotNSEReadable(t *testing.T) {
	keys := testEpochKeys()
	if keys.WakeKey == keys.ContentKey {
		t.Fatal("wake and content keys must be independent, not equal")
	}

	// A content envelope must not open under the wake key even if an attacker
	// relabels its type to 0x02 to fool the NSE.
	h := testHeader()
	h.Type = TypeMailbox
	env, err := Seal(keys.ContentKey, h, []byte("transcript"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	env.Header.Type = TypePushWake
	if _, err := env.Open(keys.WakeKey); err == nil {
		t.Fatal("wake key opened relabelled content; content key is derivable/shared")
	}
}
