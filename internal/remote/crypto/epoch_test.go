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
// CONTRACT (subset; F3-authenticated grant):
//
//	type EpochKeys struct { WakeKey WakeKey; ContentKey ContentKey }  // distinct named types (F10)
//	type EpochGrant struct { EpochID uint32; GrantSeq uint64; Sealed, Sig []byte }
//	func SealEpochGrant(machinePriv ed25519.PrivateKey, recipientPub []byte, epochID uint32, grantSeq uint64, keys EpochKeys) (*EpochGrant, error)
//	func OpenEpochGrant(ks KeyStore, machinePub ed25519.PublicKey, g *EpochGrant) (uint32, uint64, EpochKeys, error)
//	func SealToRecipient(recipientPub, plaintext []byte) ([]byte, error)  // crypto_box_seal
package crypto

import (
	"bytes"
	"testing"
)

func testEpochKeys() EpochKeys {
	return EpochKeys{WakeKey: WakeKey(fill(0xE1)), ContentKey: ContentKey(fill(0xC2))}
}

func sealedBoxKATPlain() []byte {
	w, c := fill(0x9a), fill(0x9b)
	return append(append([]byte(nil), w[:]...), c[:]...)
}

// TestEpochGrant_SealOpenRoundTrip pins R-CRY.10: a grant sealed to a device's
// recipient key opens (only) via that device's KeyStore, recovering both epoch
// keys and the epoch/grant coordinates.
func TestEpochGrant_SealOpenRoundTrip(t *testing.T) {
	ks := devKeyStore(t, stdMaterial())
	keys := testEpochKeys()
	mPriv, mPub := machineSigner(0x31)

	grant, err := SealEpochGrant(mPriv, ks.RecipientPublic(), 5, 1, keys)
	if err != nil {
		t.Fatalf("SealEpochGrant: %v", err)
	}
	if bytes.Contains(grant.Sealed, keys.WakeKey[:]) || bytes.Contains(grant.Sealed, keys.ContentKey[:]) {
		t.Error("sealed grant exposes an epoch key in cleartext")
	}

	epochID, grantSeq, got, err := OpenEpochGrant(ks, mPub, grant)
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

	mPriv, mPub := machineSigner(0x31)
	grant, err := SealEpochGrant(mPriv, target.RecipientPublic(), 5, 1, testEpochKeys())
	if err != nil {
		t.Fatalf("SealEpochGrant: %v", err)
	}
	if _, _, _, err := OpenEpochGrant(other, mPub, grant); err == nil {
		t.Fatal("a non-recipient device opened the epoch grant")
	}
}

// TestEpochGrant_DeliveredViaMailboxToOfflineDevice pins A5: the grant is an
// async sealed-box artifact, so a device offline during rotation opens it later
// (the sealed bytes survive a store round-trip / serialization).
func TestEpochGrant_DeliveredViaMailboxToOfflineDevice(t *testing.T) {
	ks := devKeyStore(t, stdMaterial())
	keys := testEpochKeys()
	mPriv, mPub := machineSigner(0x31)

	grant, err := SealEpochGrant(mPriv, ks.RecipientPublic(), 9, 3, keys)
	if err != nil {
		t.Fatalf("SealEpochGrant: %v", err)
	}
	// Simulate mailbox transit: the relay stores and later forwards the opaque
	// sealed bytes + signature unchanged.
	transited := &EpochGrant{
		EpochID:  grant.EpochID,
		GrantSeq: grant.GrantSeq,
		Sealed:   append([]byte(nil), grant.Sealed...),
		Sig:      append([]byte(nil), grant.Sig...),
	}

	epochID, grantSeq, got, err := OpenEpochGrant(ks, mPub, transited)
	if err != nil {
		t.Fatalf("OpenEpochGrant after transit: %v", err)
	}
	if epochID != 9 || grantSeq != 3 || got.ContentKey != keys.ContentKey {
		t.Error("grant did not survive mailbox transit intact")
	}
}

// TestCryptoBoxSeal_OpenKAT pins the crypto_box_seal PRIMITIVE (SealToRecipient/
// OpenSealedBox), which the EpochGrant wrapper is built on. F3 wraps grants with
// coordinates + a machine signature, so grant interop is verified elsewhere; the
// value here is the raw sealed-box open path against a fixed vector.
//
// HONESTY (F14): this vector was produced by our own x/crypto box.SealAnonymous,
// which is crypto_box_seal-compatible by construction — it is a REGRESSION pin,
// NOT independent libsodium evidence. Cross-language Swift/CryptoKit/libsodium
// interop of this exact blob is an explicit ON-DEVICE RELEASE GATE, not proven
// here. The open path (recipientPriv -> plaintext) is a genuine round-trip KAT.
func TestCryptoBoxSeal_OpenKAT(t *testing.T) {
	if len(sealedBoxKATCiphertext) == 0 {
		t.Skip("TODO(impl): pin sealedBoxKATCiphertext at first green")
	}
	ks := devKeyStore(t, KeyMaterial{RecipientPriv: sealedBoxKATRecipientPriv})
	plain, err := ks.OpenSealedBox(sealedBoxKATCiphertext)
	if err != nil {
		t.Fatalf("OpenSealedBox(KAT): %v", err)
	}
	if !bytes.Equal(plain, sealedBoxKATPlaintext) {
		t.Errorf("sealed-box KAT plaintext = %x, want %x", plain, sealedBoxKATPlaintext)
	}
}

// crypto_box_seal regression vector (see TestCryptoBoxSeal_OpenKAT for the
// honesty caveat: self-generated by x/crypto, on-device libsodium interop is a
// release gate). The plaintext is the 64-byte wake||content the old grant format
// sealed; the open path stays a valid primitive round-trip.
var (
	sealedBoxKATRecipientPriv = fill(0x77)
	sealedBoxKATPlaintext     = sealedBoxKATPlain()
	sealedBoxKATCiphertext    = []byte{
		0x7f, 0x56, 0x66, 0x88, 0x44, 0xe3, 0xf9, 0xd2, 0xac, 0xcc, 0x23, 0xdf,
		0x4f, 0xde, 0xb2, 0x04, 0x34, 0xd5, 0xd5, 0x1b, 0xeb, 0xfc, 0xd0, 0x32,
		0xb6, 0x4b, 0x71, 0xad, 0x6f, 0xb3, 0x4b, 0x76, 0x16, 0xc7, 0x4e, 0x47,
		0x83, 0xf3, 0x42, 0x8e, 0x00, 0x62, 0x3a, 0x97, 0x54, 0x7a, 0x81, 0xf5,
		0x18, 0x00, 0xea, 0x65, 0x84, 0x90, 0xa3, 0x97, 0x3c, 0x79, 0x8f, 0x50,
		0x7b, 0x84, 0x62, 0xdc, 0x53, 0x74, 0x28, 0xa9, 0x5b, 0xaa, 0x50, 0x6c,
		0xea, 0x83, 0x08, 0xe4, 0x08, 0x37, 0xfd, 0x11, 0xdb, 0x0b, 0x34, 0x40,
		0x3f, 0x8c, 0x17, 0xb0, 0x9b, 0x12, 0x15, 0xd9, 0x78, 0xae, 0xd3, 0xb5,
		0x02, 0x78, 0xee, 0xea, 0xb8, 0x64, 0x6a, 0x3b, 0xc7, 0x98, 0x42, 0x16,
		0x77, 0xd2, 0x38, 0x3c,
	}
)

// TestNSE_WakeKeyDecryptsNoSessionContent pins A15/R-CRY.13: the wake key opens
// content-free type-0x02 push wakes but cannot open type-0x01 session content
// (that is under the separate content key). A once-unlocked phone, whose NSE
// holds only the wake key, yields no session history.
func TestNSE_WakeKeyDecryptsNoSessionContent(t *testing.T) {
	keys := testEpochKeys()

	wakeHdr := testHeader()
	wakeHdr.Type = TypePushWake
	wakeEnv, err := seal(keys.WakeKey, wakeHdr, []byte("activity on machine X"))
	if err != nil {
		t.Fatalf("seal(wake): %v", err)
	}
	if _, err := wakeEnv.open(keys.WakeKey); err != nil {
		t.Errorf("NSE wake key failed to open a push-wake payload: %v", err)
	}

	contentHdr := testHeader()
	contentHdr.Type = TypeMailbox
	contentEnv, err := seal(keys.ContentKey, contentHdr, []byte("secret session transcript"))
	if err != nil {
		t.Fatalf("seal(content): %v", err)
	}
	if _, err := contentEnv.open(keys.WakeKey); err == nil {
		t.Fatal("the NSE-readable wake key decrypted session content")
	}
}

// TestContentKey_BiometricGatedNotNSEReadable pins A15: the content key is
// independent of the wake key (not derivable from it), so holding the wake key
// grants no access to content. (The at-rest biometric-gating / two-store
// separation is DEFERRED-VERIFICATION; proxy: distinct keys + failed cross-open.)
func TestContentKey_BiometricGatedNotNSEReadable(t *testing.T) {
	keys := testEpochKeys()
	if [32]byte(keys.WakeKey) == [32]byte(keys.ContentKey) {
		t.Fatal("wake and content keys must be independent, not equal")
	}

	// A content envelope must not open under the wake key even if an attacker
	// relabels its type to 0x02 to fool the NSE.
	h := testHeader()
	h.Type = TypeMailbox
	env, err := seal(keys.ContentKey, h, []byte("transcript"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	env.Header.Type = TypePushWake
	if _, err := env.open(keys.WakeKey); err == nil {
		t.Fatal("wake key opened relabelled content; content key is derivable/shared")
	}
}
