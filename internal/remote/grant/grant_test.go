package grant

// FAILING-FIRST (TDD RED, GG-5) tests for the sealed-EpochGrant sidecar + mailbox
// bootstrap frame (ADR-007 amendment 2026-07-24, decision C5). The daemon persists a
// device's initial sealed grant addressable by device id; the gateway loads it and
// appends it to the device mailbox as a TAGGED PLAINTEXT bootstrap frame the phone
// consumes before it can build a ContentKey-keyed router. Every symbol referenced here
// is the frozen contract GREEN supplies; until then the package does not compile.

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// seededGrant seals a real EpochGrant to a fresh device recipient key, signed by a
// fresh machine grant-signing key -- exactly the artifact enroll.Enroll produces.
func seededGrant(t *testing.T) *crypto.EpochGrant {
	t.Helper()
	_, signPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("machine grant key: %v", err)
	}
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	g, err := crypto.SealEpochGrant(signPriv, ks.RecipientPublic(), 7, 3, keys)
	if err != nil {
		t.Fatalf("seal grant: %v", err)
	}
	return g
}

func grantsEqual(a, b *crypto.EpochGrant) bool {
	return a != nil && b != nil &&
		a.EpochID == b.EpochID && a.GrantSeq == b.GrantSeq &&
		string(a.Sealed) == string(b.Sealed) && string(a.Sig) == string(b.Sig)
}

// TestSaveLoad_RoundTrips proves a persisted grant reloads byte-identically and lands
// at a 0600 sidecar under <registryDir>/grants/<deviceID>.json.
func TestSaveLoad_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	g := seededGrant(t)
	const deviceID = "abc123"

	if err := Save(dir, deviceID, g); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(Path(dir, deviceID))
	if err != nil {
		t.Fatalf("sidecar not written at Path: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("sidecar mode = %v, want 0600 (opaque-at-rest but owner-private)", info.Mode().Perm())
	}
	if got := Path(dir, deviceID); got != filepath.Join(dir, "grants", deviceID+".json") {
		t.Errorf("Path = %q, want <dir>/grants/<id>.json", got)
	}

	loaded, err := Load(dir, deviceID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !grantsEqual(loaded, g) {
		t.Fatalf("loaded grant != saved grant")
	}
}

// TestLoad_AbsentIsNotAnError: a device with no persisted grant yields (nil, nil), so a
// gateway assembled for a pre-grant pairing does not fail closed -- it simply has
// nothing to bootstrap.
func TestLoad_AbsentIsNotAnError(t *testing.T) {
	g, err := Load(t.TempDir(), "no-such-device")
	if err != nil {
		t.Fatalf("Load(absent) err = %v, want nil", err)
	}
	if g != nil {
		t.Fatalf("Load(absent) = %v, want nil", g)
	}
}

// TestDelete_RemovesSidecar (re-audit finding C4) proves Delete removes the persisted
// sidecar so a revoke-then-repair does not leak the file, and that deleting an ABSENT
// sidecar is not an error (idempotent, like device.Registry.Remove).
func TestDelete_RemovesSidecar(t *testing.T) {
	dir := t.TempDir()
	g := seededGrant(t)
	const deviceID = "abc123"

	if err := Save(dir, deviceID, g); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(Path(dir, deviceID)); err != nil {
		t.Fatalf("precondition: sidecar not written: %v", err)
	}

	if err := Delete(dir, deviceID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(Path(dir, deviceID)); !os.IsNotExist(err) {
		t.Fatalf("sidecar still present after Delete (stat err = %v); want gone", err)
	}

	// Idempotent: deleting an already-absent sidecar is a no-op, not an error.
	if err := Delete(dir, deviceID); err != nil {
		t.Fatalf("Delete(absent) err = %v, want nil", err)
	}
}

// TestBootstrapFrame_RoundTrips proves the tagged plaintext wire frame the gateway
// appends parses back to the exact grant, and that ParseBootstrap rejects non-bootstrap
// items (so the phone can skip ContentKey-sealed mailbox items while scanning).
func TestBootstrapFrame_RoundTrips(t *testing.T) {
	g := seededGrant(t)
	frame, err := MarshalBootstrap(g)
	if err != nil {
		t.Fatalf("MarshalBootstrap: %v", err)
	}

	got, ok := ParseBootstrap(frame)
	if !ok {
		t.Fatal("ParseBootstrap rejected a well-formed bootstrap frame")
	}
	if !grantsEqual(got, g) {
		t.Fatal("parsed grant != marshalled grant")
	}

	if _, ok := ParseBootstrap([]byte(`{"kind":"something_else"}`)); ok {
		t.Error("ParseBootstrap accepted a non-bootstrap frame")
	}
	if _, ok := ParseBootstrap([]byte("not json")); ok {
		t.Error("ParseBootstrap accepted non-JSON")
	}
}
