package gate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// PB-SEC-1 -- "Key material at rest is sealed under an Android-Keystore-backed KEK per the
// PB-KEY-1 custody contract and the PB-KEY-2 tier split."
//
// Criterion: "Persisted blob is not the raw key and does not decrypt without the keystore
// key."
//
// WHY THIS TEST IS IN GO AND NOT IN KOTLIN. The Kotlin custody layer can declare where key
// material lives and what seals it, and android/app/src/test/.../SealedStoreTest asserts
// against that declaration. A declaration can be wrong. These two tests read the bytes the
// phone core actually writes into the app's private data directory, so they cannot be
// satisfied by declaring anything -- which is what PB-SEC-1's criterion asks for.
//
// Both are expected to FAIL until the phone core's state directory is sealed. That is the
// finding, not a defect in the tests: nothing in the Android slice's scope can seal a file
// that Go opens for itself at Resume, and the facade (frozen since S8, pinned by
// mobile/testdata/exported_surface.golden) has no verb for it. S15's PB-STATE-6 needs the
// same mechanism, so it wants deciding once.

// TestPBSEC1_DeviceRoleKeysAreNotPersistedInTheClear. crypto.NewFileKeyStore writes the four
// device private scalars to <stateDir>/device.key as 128 raw bytes, 0600
// (internal/remote/crypto/keystore.go). 0600 is a filesystem permission, not a seal: it is
// worth nothing against ADB backup, a restored image, or any path that reads the app's data
// directory. PB-KEY-5 assigns each of these four roles a custody tier precisely so that they
// are NOT all reachable at once.
func TestPBSEC1_DeviceRoleKeysAreNotPersistedInTheClear(t *testing.T) {
	dir := t.TempDir()

	// Deterministic material, so the assertion is exact rather than a guess about entropy.
	var m crypto.KeyMaterial
	for i := range m.NoiseStaticPriv {
		m.NoiseStaticPriv[i] = byte(0x10 + i)
		m.RecipientPriv[i] = byte(0x40 + i)
		m.CommandSignSeed[i] = byte(0x70 + i)
		m.RelayAuthSeed[i] = byte(0xA0 + i)
	}
	if _, err := crypto.NewFileKeyStoreFromMaterial(dir, m); err != nil {
		t.Fatalf("seeding the device key store: %v", err)
	}

	core, err := phonecore.Resume(phonecore.Config{Dir: dir, Machine: "m"})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	_ = core

	body, err := os.ReadFile(filepath.Join(dir, "device.key"))
	if err != nil {
		t.Fatalf("PB-SEC-1: reading the persisted device keys: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("PB-SEC-1: device.key is empty; every assertion below would pass vacuously")
	}

	for _, role := range []struct {
		name  string
		bytes []byte
		tier  string
	}{
		{"NoiseStatic", m.NoiseStaticPriv[:], "content"},
		{"Recipient", m.RecipientPriv[:], "content"},
		{"CommandSign", m.CommandSignSeed[:], "content"},
		{"RelayAuth", m.RelayAuthSeed[:], "wake"},
	} {
		if bytes.Contains(body, role.bytes) {
			t.Errorf("PB-SEC-1: the %s private key (PB-KEY-5 %s tier) sits verbatim in "+
				"%s. At rest it must be sealed under an Android-Keystore-backed KEK; "+
				"0600 is a filesystem permission, not a seal",
				role.name, role.tier, filepath.Join("<stateDir>", "device.key"))
		}
	}
}

// TestPBSEC1_EpochKeysAreNotPersistedInTheClear. App.InstallContentKey copies the unwrapped
// content key into the durable State and calls Save (mobile/app.go), and the state blob is
// plain JSON with `wake_key` and `content_key` as base64 fields
// (internal/phonecore/state.go:176-177, 409-410). So the one artifact the whole two-tier
// design is built around is written to disk in the clear, both tiers together in one file --
// which also collapses PB-KEY-2's split at rest, since a single file cannot be gated two
// ways.
func TestPBSEC1_EpochKeysAreNotPersistedInTheClear(t *testing.T) {
	dir := t.TempDir()
	core, err := phonecore.Resume(phonecore.Config{Dir: dir, Machine: "m"})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}

	st := core.State()
	for i := range st.Keys.WakeKey {
		st.Keys.WakeKey[i] = byte(0x11 + i)
		st.Keys.ContentKey[i] = byte(0x55 + i)
	}
	if err := core.Save(st); err != nil {
		t.Fatalf("saving state: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, phonecore.StateFileName))
	if err != nil {
		t.Fatalf("PB-SEC-1: reading the persisted state blob: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("PB-SEC-1: the state blob is empty; the assertions below would pass vacuously")
	}

	// JSON encodes []byte as base64, so search for both forms rather than assuming one.
	for _, key := range []struct {
		name  string
		bytes []byte
		tier  string
	}{
		{"WakeKey", st.Keys.WakeKey[:], "wake"},
		{"ContentKey", st.Keys.ContentKey[:], "content"},
	} {
		if bytes.Contains(body, key.bytes) || bytes.Contains(body, []byte(base64Std(key.bytes))) {
			t.Errorf("PB-SEC-1: the epoch %s (PB-KEY-2 %s tier) is recoverable verbatim from "+
				"%s. PB-KEY-2 gates the content tier behind user authentication; a plaintext "+
				"copy on disk is reachable without it",
				key.name, key.tier, filepath.Join("<stateDir>", phonecore.StateFileName))
		}
	}
}
