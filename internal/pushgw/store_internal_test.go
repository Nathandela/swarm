package pushgw

// Internal (package pushgw, not pushgw_test) storage-level tests for two fixes that are
// properties of the STORE's own bbolt transaction boundaries, not of the HTTP handlers
// layered over them -- the same convention internal/remote/relay uses for its own
// trustedproxy_test.go: reach the unexported store type directly rather than trying to
// force a single-transaction guarantee through the public HTTP surface, where it is
// unobservable by construction once the fix is in place.

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *store {
	t.Helper()
	dir := t.TempDir()
	st, err := openStore(filepath.Join(dir, "pushgw.db"), filepath.Join(dir, "pushgw.key"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.close() })
	return st
}

// TestStore_DeleteAddressAndTombstone_LeavesTombstonePresentIffAddressGone is the HIGH
// finding's crash-shaped probe (revoke.go): revokeByMachineCapability used to delete the
// address and write its tombstone as TWO separate db.Update calls, so a process exit or a
// putTombstone error between them left the address gone with NO tombstone -- every later
// durable retry (PG-REV-2) then saw 401 forever instead of the idempotent 204 the tombstone
// exists to guarantee. deleteAddressAndTombstone now does both inside ONE transaction; this
// pins that after a single call, "tombstone present" and "address gone" are the same fact,
// never observed apart -- which is only true if bbolt committed them together.
func TestStore_DeleteAddressAndTombstone_LeavesTombstonePresentIffAddressGone(t *testing.T) {
	st := newTestStore(t)
	rec := addressRecord{
		InstallationID:    "inst-atomic",
		SubmitCapHash:     hashSecret("submit-atomic"),
		MachineRevokeHash: hashSecret("machine-atomic"),
		CreatedAtMs:       1000,
		Bound:             true,
	}
	if err := st.putAddress("addr-atomic", rec); err != nil {
		t.Fatalf("putAddress: %v", err)
	}

	if err := st.deleteAddressAndTombstone("addr-atomic", rec, 2000); err != nil {
		t.Fatalf("deleteAddressAndTombstone: %v", err)
	}

	if _, found, err := st.getAddress("addr-atomic"); err != nil {
		t.Fatalf("getAddress: %v", err)
	} else if found {
		t.Fatalf("address still present after deleteAddressAndTombstone")
	}
	tomb, found, err := st.getTombstone(rec.MachineRevokeHash)
	if err != nil {
		t.Fatalf("getTombstone: %v", err)
	}
	if !found {
		t.Fatalf("tombstone absent after deleteAddressAndTombstone: the address is gone with no tombstone, exactly the finding this fix closes")
	}
	if tomb.RevokedAtMs != 2000 {
		t.Fatalf("tombstone RevokedAtMs = %d, want 2000", tomb.RevokedAtMs)
	}
}

// TestStore_UpdateInstallationIfPresent_MutatesExisting is the ordinary path: fn's
// mutation is applied and persisted for a present installation.
func TestStore_UpdateInstallationIfPresent_MutatesExisting(t *testing.T) {
	st := newTestStore(t)
	if err := st.putInstallation("inst-1", installationRecord{LastActiveMs: 1}); err != nil {
		t.Fatalf("putInstallation: %v", err)
	}
	updated, err := st.updateInstallationIfPresent("inst-1", func(rec *installationRecord) {
		rec.TokenDead = true
	})
	if err != nil {
		t.Fatalf("updateInstallationIfPresent: %v", err)
	}
	if !updated {
		t.Fatalf("updateInstallationIfPresent reported no update for a present installation")
	}
	got, found, err := st.getInstallation("inst-1")
	if err != nil {
		t.Fatalf("getInstallation: %v", err)
	}
	if !found || !got.TokenDead {
		t.Fatalf("got %+v found=%v, want TokenDead=true", got, found)
	}
}

// TestStore_UpdateInstallationIfPresent_NoopWhenAbsent is the LOW finding's fix
// (rotate.go): rotate.go used to blind-write the WHOLE installationRecord it had read at
// the top of the handler, so a 180-day inactivity sweep deleting the row in between made
// that final write silently RE-CREATE it -- token and all, minus its addresses, undoing
// §8.1 row 3's deletion. updateInstallationIfPresent must never do that: fn must not even
// run, and no row may exist afterward, for an id the store does not hold.
func TestStore_UpdateInstallationIfPresent_NoopWhenAbsent(t *testing.T) {
	st := newTestStore(t)
	called := false
	updated, err := st.updateInstallationIfPresent("inst-swept", func(rec *installationRecord) {
		called = true
		rec.TokenDead = true
	})
	if err != nil {
		t.Fatalf("updateInstallationIfPresent: %v", err)
	}
	if updated {
		t.Fatalf("updateInstallationIfPresent reported an update for an absent installation")
	}
	if called {
		t.Fatalf("mutation fn ran against an absent installation -- it must never be invoked")
	}
	if _, found, err := st.getInstallation("inst-swept"); err != nil {
		t.Fatalf("getInstallation: %v", err)
	} else if found {
		t.Fatalf("updateInstallationIfPresent RESURRECTED a deleted/absent installation")
	}
}
