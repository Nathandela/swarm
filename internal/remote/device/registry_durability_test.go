package device

// FAILING-FIRST (TDD RED, GG-5) test for round-4 re-audit finding codex#5 (durability):
// persistLocked now does temp-write -> os.Rename -> fsync-parent-dir, but Add/AddSole/
// Remove treat EVERY persistLocked error as PRE-commit and roll the in-memory map back.
// After a POST-rename failure (the rename SUCCEEDED, only the trailing dir-fsync
// errored) the on-disk roster is the NEW one while the rolled-back daemon holds the OLD
// one -> memory/disk DIVERGENCE (e.g. a just-added device present on disk but absent in
// memory, or the reverse for a removal). The idempotency store (internal/idempotency/
// store.go) already distinguishes pre- from post-rename failure; the registry must too.
//
// The seam is device.syncDir (mirroring idempotency.syncDir): injecting a failure there
// fires AFTER a successful os.Rename, the genuine post-rename case.

import (
	"errors"
	"os"
	"testing"
)

// assertSameRoster asserts the daemon's in-memory registry (mem) and a fresh registry
// opened from disk (disk) hold the identical roster -- the whole point of the finding is
// that these must never diverge. List() is deterministically ordered, so a field-by-field
// compare of the two slices is exact.
func assertSameRoster(t *testing.T, mem, disk *Registry) {
	t.Helper()
	m := mem.List()
	d := disk.List()
	if len(m) != len(d) {
		t.Fatalf("roster size mismatch: memory has %d, disk has %d", len(m), len(d))
	}
	for i := range m {
		recordsEqual(t, d[i], m[i])
	}
}

// TestRegistry_PersistDirSyncFailureAfterRename_NoDivergence pins the finding directly.
// Once os.Rename lands, the on-disk roster IS the new one, so the in-memory change is
// committed even if the trailing dir-fsync fails. Rolling memory back in that case would
// leave the daemon holding the OLD roster while disk holds the NEW one. The test injects
// a post-rename dir-sync failure on an Add and asserts (a) the second device stays in
// memory, (b) it is on disk, and (c) memory == disk exactly (a simulated restart yields
// the roster the daemon holds). On today's roll-back-on-any-error callers this FAILS.
func TestRegistry_PersistDirSyncFailureAfterRename_NoDivergence(t *testing.T) {
	dir := t.TempDir()
	reg, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) error: %v", dir, err)
	}

	// A baseline device, persisted through the real dir-fsync (happy path).
	base := fullRecord(t, 0xC1, CapFull, 1)
	if err := reg.Add(base); err != nil {
		t.Fatalf("Add(base) error: %v", err)
	}

	// Inject a dir-sync failure that fires AFTER the rename has swapped the NEW
	// devices.json (both devices) into place: the rename lands on disk; only the
	// durability barrier reports an error. This is the genuine post-rename failure.
	orig := syncDir
	syncDir = func(string) error { return errors.New("injected dir-sync failure after rename") }
	defer func() { syncDir = orig }()

	second := fullRecord(t, 0xC2, CapReadOnly, 2)
	addErr := reg.Add(second)
	// The dir-sync error is surfaced (durable-enough, just not dir-fsync-confirmed).
	// What must NOT happen is an in-memory rollback: the rename already committed the
	// NEW roster to disk.
	if addErr == nil {
		t.Fatalf("Add with an injected post-rename dir-sync failure returned nil; want the failure surfaced")
	}

	// Restore the real dir-fsync so the reopen-from-disk below is not perturbed.
	syncDir = orig

	// MEMORY must reflect the committed (NEW) roster: BOTH devices present.
	if _, ok := reg.Get(second.DeviceID); !ok {
		t.Fatalf("second device rolled OUT of memory after a post-rename failure, but the rename already committed it to disk -> memory/disk divergence")
	}
	if got := reg.Count(); got != 2 {
		t.Fatalf("in-memory Count = %d after a post-rename failure, want 2 (the committed roster)", got)
	}

	// DISK must hold the same NEW roster: reopening (a simulated restart) yields both.
	reloaded, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after post-rename failure: %v", err)
	}
	if got := reloaded.Count(); got != 2 {
		t.Fatalf("on-disk Count = %d after a post-rename failure, want 2 (rename committed it)", got)
	}

	// The daemon's in-memory roster and the on-disk roster must MATCH exactly.
	assertSameRoster(t, reg, reloaded)
}

// TestRegistry_PersistPreRenameFailure_RollsBack pins that the pre-rename rollback stays
// intact: a genuine failure BEFORE the rename (here, os.CreateTemp failing in an
// unwritable dir) leaves the on-disk roster untouched, so the in-memory change MUST be
// rolled back -- memory == disk == the OLD roster. This behavior must survive the fix.
func TestRegistry_PersistPreRenameFailure_RollsBack(t *testing.T) {
	dir := t.TempDir()
	reg, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) error: %v", dir, err)
	}

	base := fullRecord(t, 0xD1, CapFull, 1)
	if err := reg.Add(base); err != nil {
		t.Fatalf("Add(base) error: %v", err)
	}

	// Force a PRE-rename failure: an unwritable registry dir makes persistLocked's
	// os.CreateTemp fail before anything is written or renamed. The on-disk roster is
	// untouched, so the caller MUST roll back (memory == disk == old).
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	defer os.Chmod(dir, 0o700) // restore so t.TempDir cleanup can remove the dir

	second := fullRecord(t, 0xD2, CapReadOnly, 2)
	if err := reg.Add(second); err == nil {
		t.Fatalf("Add into an unwritable dir = nil error; want a pre-rename failure")
	}

	// Rolled back: memory holds ONLY the baseline device.
	if _, ok := reg.Get(second.DeviceID); ok {
		t.Fatalf("second device left in memory after a pre-rename failure; want rollback")
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("in-memory Count = %d after a pre-rename failure, want 1 (rolled back)", got)
	}

	// Disk is untouched; a simulated restart still holds exactly the baseline roster.
	os.Chmod(dir, 0o700)
	reloaded, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after pre-rename failure: %v", err)
	}
	if got := reloaded.Count(); got != 1 {
		t.Fatalf("on-disk Count = %d after a pre-rename failure, want 1 (disk untouched)", got)
	}
	assertSameRoster(t, reg, reloaded)
}
