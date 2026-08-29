package relay

// Backup/restore for the relay's bbolt store (playbook 6.5, R2 "backup" work
// package). FAILING-FIRST (GG-5): Backup, Restore, and ErrRelayRunning do not
// exist yet, so this file alone should fail to compile.
//
// bbolt's own flock is exclusive per process (confirmed against
// go.etcd.io/bbolt@v1.3.11: Open always flocks the file, LOCK_EX unless
// ReadOnly, and holds it for the DB handle's lifetime) — no two OS processes
// can hold the same store file open at once. That is exactly the mechanism
// these tests exercise as the "relay is up" check: a still-open *bolt.DB in
// this test process (standing in for a live relay) makes a second Open from
// the same process block/timeout precisely as it would across processes,
// because flock is scoped per open-file-description, not per process.

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestBackup_ProducesRestorableSnapshot backs up a store holding a real mailbox
// item and confirms the backup file is itself a valid bbolt store with the
// item readable back out of it (PB-6.5 "consistent hot snapshot via Tx.WriteTo").
func TestBackup_ProducesRestorableSnapshot(t *testing.T) {
	srv, cfg, _, clk := startTestRelay(t, nil)
	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	env := sp.sealMailbox(t, 1, []byte("backed-up"), clk)
	devRID := RoutingID(dPub)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}

	// Release the store's lock before backing it up (see the package-level
	// comment on the disk-full test below for the same story, at CLI level:
	// backing up while the relay holds the file's OS lock cleanly refuses
	// rather than hanging or corrupting anything).
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(cfg.DBPath, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	st, err := openStore(backupPath)
	if err != nil {
		t.Fatalf("openStore(backup): %v", err)
	}
	defer func() { _ = st.close() }()
	items, _, _, err := st.readItemsPage(devRID, 0, 10, 1<<20)
	if err != nil {
		t.Fatalf("readItemsPage on backup: %v", err)
	}
	if len(items) != 1 || !bytes.Equal(items[0].Envelope, env) {
		t.Fatalf("backup did not contain the appended item: %+v", items)
	}
}

// TestBackup_RefusesWhileStoreIsLocked asserts a live store (still open, as a
// running relay would hold it) refuses the backup cleanly rather than hanging
// or copying a torn file.
func TestBackup_RefusesWhileStoreIsLocked(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	st, err := openStore(dbPath)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.close() })

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	err = Backup(dbPath, backupPath)
	if !errors.Is(err, ErrRelayRunning) {
		t.Fatalf("Backup while store is locked: got %v, want ErrRelayRunning", err)
	}
	if _, statErr := os.Stat(backupPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Backup left a file at %s after refusing", backupPath)
	}
}

// TestBackup_MissingSourceIsCleanError asserts backing up a store that does not
// exist is a clean error, not a panic or an empty file.
func TestBackup_MissingSourceIsCleanError(t *testing.T) {
	dir := t.TempDir()
	err := Backup(filepath.Join(dir, "absent.db"), filepath.Join(dir, "out.db"))
	if err == nil {
		t.Fatal("Backup of a missing store returned nil, want an error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "out.db")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("Backup of a missing store left a destination file")
	}
}

// TestBackup_RefusesZeroLengthSource asserts backing up a pre-created but
// empty dbPath is a clean error, not a "successful" backup of nothing.
// checkStoreNotLocked already guards against bolt.Open's own behaviour of
// initializing fresh meta pages into ANY zero-length file it is pointed at,
// even ReadOnly -- Backup needs the same guard, or it silently turns the
// SOURCE into a real (if empty) bbolt file and reports success on a backup
// with no buckets in it (which validateBackup would only catch much later,
// at restore time).
func TestBackup_RefusesZeroLengthSource(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatalf("seed zero-length dbPath: %v", err)
	}
	destPath := filepath.Join(dir, "out.db")

	if err := Backup(dbPath, destPath); err == nil {
		t.Fatal("Backup of a zero-length store returned nil, want an error")
	}
	if _, statErr := os.Stat(destPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("Backup of a zero-length store left a destination file")
	}
	info, statErr := os.Stat(dbPath)
	if statErr != nil {
		t.Fatalf("Stat(dbPath): %v", statErr)
	}
	if info.Size() != 0 {
		t.Fatalf("Backup mutated the zero-length source into a %d-byte file instead of leaving "+
			"it untouched", info.Size())
	}
}

// TestBackup_DiskFullLeavesNoPartialFile simulates ENOSPC on the backup output
// stream via an injectable writer seam (backupCreate). Darwin has no simple
// per-test tmpfs or quota mechanism available without elevated privileges, so
// per the work package's own fallback this wrapper seam is the honest way to
// exercise the write-refusal path: it fails the real io.Writer bbolt's
// Tx.WriteTo writes into, exactly where a real ENOSPC would surface.
func TestBackup_DiskFullLeavesNoPartialFile(t *testing.T) {
	srv, cfg, _, _ := startTestRelay(t, nil)
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	failAfter := 8
	orig := backupCreate
	backupCreate = func(dir string) (syncWriteCloser, string, error) {
		w, path, err := orig(dir)
		if err != nil {
			return nil, "", err
		}
		return &enospcAfter{syncWriteCloser: w, remaining: failAfter}, path, nil
	}
	t.Cleanup(func() { backupCreate = orig })

	destPath := filepath.Join(t.TempDir(), "backup.db")
	err := Backup(cfg.DBPath, destPath)
	if err == nil {
		t.Fatal("Backup with a disk-full output stream returned nil, want an error")
	}
	if _, statErr := os.Stat(destPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Backup left the final destination %s after a write failure", destPath)
	}
	entries, readErr := os.ReadDir(filepath.Dir(destPath))
	if readErr != nil {
		t.Fatalf("ReadDir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("Backup left %d leftover file(s) in the destination directory after disk-full: %v", len(entries), entries)
	}
}

// enospcAfter fails every Write after `remaining` bytes have been accepted,
// with syscall.ENOSPC wrapped exactly as a real full disk would return it.
type enospcAfter struct {
	syncWriteCloser
	remaining int
}

func (w *enospcAfter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, &os.PathError{Op: "write", Path: "backup", Err: syscall.ENOSPC}
	}
	if len(p) <= w.remaining {
		w.remaining -= len(p)
		return len(p), nil
	}
	n := w.remaining
	w.remaining = 0
	return n, &os.PathError{Op: "write", Path: "backup", Err: syscall.ENOSPC}
}

// TestBackupCreate_ConcurrentCallsDoNotCollide is the fix for the reviewer's
// finding: the old backupCreate always wrote to a single FIXED "<dest>.tmp"
// name and unconditionally os.Remove'd whatever was there first, so two
// Backup runs racing on the same destPath -- nothing serializes them,
// since Backup's read-only Open takes a SHARED flock, so a long cron backup
// overlapping the next tick gets the source store simultaneously -- collide
// on that one name: whichever call's os.Remove+create runs second unlinks
// the first call's in-flight temp file out from under it, and the first
// call's later os.Rename can then either fail outright or silently rename
// the second call's fresh, still-empty file over the destination while
// still reporting success (nil). os.CreateTemp gives every call a unique
// name, ruling this out structurally: two overlapping calls can never touch
// the same path.
func TestBackupCreate_ConcurrentCallsDoNotCollide(t *testing.T) {
	dir := t.TempDir()

	w1, path1, err := backupCreate(dir)
	if err != nil {
		t.Fatalf("backupCreate (first): %v", err)
	}
	defer func() { _ = w1.Close() }()
	if _, err := w1.Write([]byte("first backup's bytes")); err != nil {
		t.Fatalf("write first: %v", err)
	}

	// Simulate a second, overlapping Backup run creating its own temp file
	// before the first has synced/renamed anything.
	w2, path2, err := backupCreate(dir)
	if err != nil {
		t.Fatalf("backupCreate (second): %v", err)
	}
	defer func() { _ = w2.Close() }()

	if path1 == path2 {
		t.Fatalf("two overlapping backupCreate calls returned the same path %q -- they will collide", path1)
	}

	// The first call's file and its bytes must still be exactly what it
	// wrote -- untouched by the second, overlapping call.
	got, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("ReadFile(path1) after second backupCreate: %v", err)
	}
	if string(got) != "first backup's bytes" {
		t.Fatalf("first backup's temp file was disturbed by a second, overlapping backupCreate call: got %q", got)
	}
}

// TestRestore_RoundTripSeedBackupWipeRestoreServe is the compatibility test the
// work package names explicitly: seed a store, back it up, wipe it, restore it,
// and prove it serves — reading the mailbox item and the cursor it was assigned
// back out through a freshly started relay against the restored file.
func TestRestore_RoundTripSeedBackupWipeRestoreServe(t *testing.T) {
	srv, cfg, apns, clk := startTestRelay(t, nil)
	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("round-trip"))
	env := sp.sealMailbox(t, 1, []byte("survives-restore"), clk)
	if _, err := machine.MailboxAppend(testCtx(t), RoutingID(dPub), env); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(cfg.DBPath, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Wipe the live store, as if it were lost/corrupted.
	if err := os.Remove(cfg.DBPath); err != nil {
		t.Fatalf("wipe: %v", err)
	}

	if err := Restore(cfg.DBPath, backupPath); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	srv2, err := New(cfg, WithClock(clk), WithPushSink(apns))
	if err != nil {
		t.Fatalf("New(restored): %v", err)
	}
	if err := srv2.Start(testCtx(t)); err != nil {
		t.Fatalf("Start(restored): %v", err)
	}
	t.Cleanup(func() { _ = srv2.Close() })

	device := dialAuthed(t, srv2.URL(), authFor(dPub, dPriv))
	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead after restore: %v", err)
	}
	if len(items) != 1 || items[0].Cursor != 1 || !bytes.Equal(items[0].Envelope, env) {
		t.Fatalf("restored store did not serve the seeded item/cursor: %+v", items)
	}

	// A device can still be freshly authorized against the restored pairing
	// graph — bucketPairs round-tripped, not just bucketItems. machine was
	// dialed against the now-closed srv, so this needs a fresh client against
	// the restored server srv2 (the same relay-auth key, still recognized).
	machine2 := dialAuthed(t, srv2.URL(), authFor(mPub, mPriv))
	d2Pub, d2Priv := newRelayAuthKey(t)
	if err := machine2.AuthorizeDevice(testCtx(t), ed25519.PublicKey(d2Pub), consentTo(d2Priv, machine2.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice after restore: %v", err)
	}
}

// TestRestore_RefusesWhileStoreIsLocked mirrors the backup refusal: restoring
// into a store a running relay still holds open must refuse rather than
// replacing a file out from under a live process.
func TestRestore_RefusesWhileStoreIsLocked(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	st, err := openStore(dbPath)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.close() })

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	seedValidBackup(t, backupPath)

	err = Restore(dbPath, backupPath)
	if !errors.Is(err, ErrRelayRunning) {
		t.Fatalf("Restore while store is locked: got %v, want ErrRelayRunning", err)
	}
}

// TestRestore_RejectsUnparseableFile asserts a file that is not a bbolt
// database at all is refused before anything is replaced.
func TestRestore_RejectsUnparseableFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte("not a bbolt file"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	if err := Restore(dbPath, garbage); err == nil {
		t.Fatal("Restore from an unparseable file returned nil, want an error")
	}
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("Restore from an unparseable file created a destination store")
	}
}

// TestRestore_RejectsMissingBucket asserts a syntactically valid bbolt file
// that is nonetheless missing one of the relay's required buckets is refused,
// never partially adopted as the live store.
func TestRestore_RejectsMissingBucket(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	incomplete := filepath.Join(t.TempDir(), "incomplete.db")

	db, err := bolt.Open(incomplete, 0o600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		// Every required bucket except bucketRetired.
		for _, b := range [][]byte{bucketItems, bucketSeq, bucketPairs, bucketRevoked, bucketTokens, bucketConsents} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed incomplete buckets: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close incomplete: %v", err)
	}

	if err := Restore(dbPath, incomplete); err == nil {
		t.Fatal("Restore from a store missing a required bucket returned nil, want an error")
	}
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("Restore from an incomplete store created a destination store")
	}
}

// TestRestore_MissingSourceIsCleanError asserts restoring from a backup path
// that does not exist is a clean error.
func TestRestore_MissingSourceIsCleanError(t *testing.T) {
	dir := t.TempDir()
	err := Restore(filepath.Join(dir, "relay.db"), filepath.Join(dir, "absent-backup.db"))
	if err == nil {
		t.Fatal("Restore from a missing backup returned nil, want an error")
	}
}

// TestRestore_RejectsTruncatedBackup reproduces the reviewer's SIGBUS finding
// deterministically: a real multi-page backup truncated to half its size used
// to make validateBackup's tx.Bucket(b) fault-crash the whole process (bbolt
// mmaps the file at its short actual size, then dereferences a page past that
// mapping via unsafe.Pointer arithmetic bolt.Open never bounds-checks) -- a
// SIGBUS fatal fault, not a recoverable Go panic. Restore must refuse this
// cleanly, before ever calling bolt.Open on the truncated file.
func TestRestore_RejectsTruncatedBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "source.db")
	st, err := openStore(dbPath)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	// Enough real data to span multiple bbolt pages, so truncating the
	// resulting backup in half cuts off pages its own meta page still
	// declares -- exactly the shape a truncated scp/rsync leaves behind.
	for i := 0; i < 200; i++ {
		rid := fmt.Sprintf("rid-%d", i%5)
		if _, err := st.appendItem(rid, "src", bytes.Repeat([]byte{0xAB}, 256), int64(i)); err != nil {
			t.Fatalf("appendItem: %v", err)
		}
	}
	if err := st.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(dbPath, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	full, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(full) < 4096*4 {
		t.Fatalf("backup is only %d bytes, too small to reliably span multiple pages for this test", len(full))
	}
	truncatedPath := filepath.Join(t.TempDir(), "truncated.db")
	if err := os.WriteFile(truncatedPath, full[:len(full)/2], 0o600); err != nil {
		t.Fatalf("write truncated backup: %v", err)
	}

	restoreDBPath := filepath.Join(t.TempDir(), "relay.db")
	err = Restore(restoreDBPath, truncatedPath)
	if err == nil {
		t.Fatal("Restore from a truncated backup returned nil, want a clean error")
	}
	if !errors.Is(err, errBackupTruncated) {
		t.Fatalf("Restore from a truncated backup returned %v, want errBackupTruncated", err)
	}
	if _, statErr := os.Stat(restoreDBPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("Restore from a truncated backup created a destination store")
	}
}

// TestRestore_RejectsTruncatedRawCopyWhenPageOneMetaIsActive reproduces the
// reviewer's finding that checkBackupNotTruncated used to parse only page
// 0's meta, but bbolt's ACTIVE meta -- the one bolt.Open actually uses -- is
// whichever of page 0 / page 1 validates with the higher txid, alternating
// which page that is by txid parity (bbolt db.go: p.id = txid % 2). A file
// this package's own Backup produces is immune (Tx.WriteTo always writes
// page 0's meta with the higher txid, so page 0 always wins), which is why
// TestRestore_RejectsTruncatedBackup above only ever exercises that shape.
// A raw file-level copy of a LIVE store -- an interrupted scp/rsync, or a
// plain `cp` of the .db file, which relay-runbook.md Sec.11 promises this
// guard against -- is not: page 1 is active roughly half the time.
//
// This seeds a store into exactly that state: one appendItem after
// openStore leaves the store's last commit on an odd txid, so page 1 (not
// page 0) is active, with a LARGER declared extent than page 0's now-stale
// meta. Truncating the raw file to page 0's (smaller) extent used to satisfy
// the old page-0-only guard while still being short of what the real active
// meta (page 1) requires -- exactly the gap that let a truncated file reach
// bolt.Open and fault (see docs/verification/r2-red for the captured panic).
func TestRestore_RejectsTruncatedRawCopyWhenPageOneMetaIsActive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "source.db")
	st, err := openStore(dbPath)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if _, err := st.appendItem("rid", "src", []byte("payload"), 1); err != nil {
		t.Fatalf("appendItem: %v", err)
	}
	if err := st.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	need := bboltMetaOffset + bboltMetaChecksummedLen + 8
	pageSize, pgid0, _, ok0 := parseBoltMeta(raw[0:need])
	if !ok0 || pageSize == 0 || uint64(len(raw)) < uint64(pageSize)+uint64(need) {
		t.Fatalf("page 0 meta must locate a complete page 1: pageSize=%d len=%d ok0=%v", pageSize, len(raw), ok0)
	}
	_, pgid1, _, ok1 := parseBoltMeta(raw[int(pageSize) : int(pageSize)+need])
	if !ok0 || !ok1 {
		t.Fatalf("both meta pages must parse as valid bbolt meta: ok0=%v ok1=%v", ok0, ok1)
	}
	if pgid1 <= pgid0 {
		t.Fatalf("test precondition broken: expected page 1's meta to declare a LARGER extent than "+
			"page 0's (page 1 active with room having grown since page 0 was last written), got "+
			"pgid0=%d pgid1=%d -- the seeding shape above needs adjusting", pgid0, pgid1)
	}

	truncated := raw[:pgid0*uint64(pageSize)]
	truncatedPath := filepath.Join(t.TempDir(), "truncated-raw-copy.db")
	if err := os.WriteFile(truncatedPath, truncated, 0o600); err != nil {
		t.Fatalf("write truncated raw copy: %v", err)
	}

	restoreDBPath := filepath.Join(t.TempDir(), "relay.db")
	err = Restore(restoreDBPath, truncatedPath)
	if err == nil {
		t.Fatal("Restore from a raw copy truncated to page 0's stale extent returned nil, want a clean error")
	}
	if !errors.Is(err, errBackupTruncated) {
		t.Fatalf("Restore from a raw copy truncated between page 0 and page 1's extents returned "+
			"%v, want errBackupTruncated", err)
	}
	if _, statErr := os.Stat(restoreDBPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("Restore from a truncated raw copy created a destination store")
	}
}

// TestRestore_RejectsBackupWithCorruptedKeyOrder proves validateBackup runs
// bbolt's own tx.Check() rather than only confirming the required buckets are
// present. It corrupts one byte of one key in a bucket large enough to spill
// past bbolt's inline-bucket threshold (pageSize/4) onto real leaf pages, so
// the corruption breaks the B+tree's key ordering -- the one class of
// corruption tx.Check is documented to catch, and the one this test can
// inject with zero out-of-bounds risk (only a KEY byte is touched, never a
// length, offset, or page-type field).
func TestRestore_RejectsBackupWithCorruptedKeyOrder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "source.db")
	st, err := openStore(dbPath)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	// Written in ONE transaction (not separate st.putToken calls): bbolt's
	// copy-on-write leaves each committed transaction's superseded pages in
	// the file as unreferenced garbage, and a search for a literal key byte
	// string can find stale leftover copies of it in exactly that garbage --
	// a single transaction has nothing earlier of its own to leave behind.
	if err := st.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTokens)
		// Scale with the host page size. Apple Silicon uses 16 KiB pages, so the
		// old fixed 64 records still fit bbolt's pageSize/4 inline-bucket threshold;
		// tx.Check walks spilled B+tree pages, not an inline bucket embedded as a
		// parent value. This count exceeds the threshold on both 4 KiB and 16 KiB
		// hosts while remaining tiny.
		entries := os.Getpagesize() / 32
		if entries < 64 {
			entries = 64
		}
		for i := 0; i < entries; i++ {
			rid := fmt.Sprintf("probe-token-rid-%08d", i)
			if err := b.Put([]byte(rid), []byte("tok")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed tokens: %v", err)
	}
	if err := st.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(dbPath, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	raw, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Replace one key with a later key's value -- a duplicate/out-of-order key
	// in the leaf's physical ordering.
	entries := os.Getpagesize() / 32
	if entries < 64 {
		entries = 64
	}
	targetIndex := entries / 2
	target := []byte(fmt.Sprintf("probe-token-rid-%08d", targetIndex))
	replacement := []byte(fmt.Sprintf("probe-token-rid-%08d", targetIndex+7))
	if n := bytes.Count(raw, target); n != 1 {
		t.Fatalf("probe key appears %d time(s) in the backup, want exactly 1", n)
	}
	idx := bytes.Index(raw, target)
	corrupted := append([]byte(nil), raw...)
	copy(corrupted[idx:idx+len(target)], replacement)
	corruptedPath := filepath.Join(t.TempDir(), "corrupted.db")
	if err := os.WriteFile(corruptedPath, corrupted, 0o600); err != nil {
		t.Fatalf("write corrupted backup: %v", err)
	}

	restoreDBPath := filepath.Join(t.TempDir(), "relay.db")
	err = Restore(restoreDBPath, corruptedPath)
	if err == nil {
		t.Fatal("Restore accepted a backup with a corrupted B+tree key order, want a consistency-check error")
	}
	if _, statErr := os.Stat(restoreDBPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("Restore from a structurally corrupted backup created a destination store")
	}
}

// TestRestore_LeavesExistingStoreUntouchedOnInvalidBackup closes the
// assertion gap the review flagged: the existing unparseable/missing-bucket
// tests only ever target a NONEXISTENT dbPath, so the runbook's central
// safety promise -- "leaves the previous file, if any, untouched rather than
// half-overwritten" -- was never asserted against a real, existing store.
func TestRestore_LeavesExistingStoreUntouchedOnInvalidBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	st, err := openStore(dbPath)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if _, err := st.appendItem("some-rid", "some-source", []byte("must-survive"), 1); err != nil {
		t.Fatalf("appendItem: %v", err)
	}
	if err := st.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile(before): %v", err)
	}

	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte("not a bbolt file"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	if err := Restore(dbPath, garbage); err == nil {
		t.Fatal("Restore from an unparseable file into an existing store returned nil, want an error")
	}

	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile(after): %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("Restore from an unparseable file modified the existing store instead of leaving it untouched")
	}
}

// TestRestore_LeavesZeroLengthDBPathUntouchedWhenBackupInvalid guards
// checkStoreNotLocked's own side effect: bbolt's Open initializes fresh meta
// pages into ANY zero-length file it is pointed at, readOnly or not, so a
// dbPath that exists but is empty (e.g. pre-created by deploy tooling) must
// not be silently turned into a real (if empty) bbolt file by the lock-check
// probe alone when the restore as a whole is going to fail anyway.
func TestRestore_LeavesZeroLengthDBPathUntouchedWhenBackupInvalid(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatalf("seed zero-length dbPath: %v", err)
	}
	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte("not a bbolt file"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	if err := Restore(dbPath, garbage); err == nil {
		t.Fatal("Restore with an invalid backup returned nil, want an error")
	}

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat(dbPath) after a failed restore: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("Restore's own lock-check probe initialized the zero-length dbPath into a real "+
			"bbolt file (now %d bytes) even though the overall restore failed and must leave it untouched",
			info.Size())
	}
}

// TestBackup_StaleTempFileDoesNotBlockNextBackup asserts a ".tmp" left behind
// by a previously killed backup does not permanently block every later
// backup to that destination -- the file existing at all is not evidence of
// a concurrent writer, since Backup already refuses to proceed while dbPath
// is locked by a running relay before it ever touches destPath.
func TestBackup_StaleTempFileDoesNotBlockNextBackup(t *testing.T) {
	srv, cfg, _, _ := startTestRelay(t, nil)
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	destPath := filepath.Join(t.TempDir(), "backup.db")
	if err := os.WriteFile(destPath+".tmp", []byte("stale partial data from a killed backup"), 0o600); err != nil {
		t.Fatalf("seed stale .tmp: %v", err)
	}

	if err := Backup(cfg.DBPath, destPath); err != nil {
		t.Fatalf("Backup with a stale .tmp already at the destination: %v, want it to proceed", err)
	}
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("Backup did not produce %s despite the stale .tmp being cleared: %v", destPath, err)
	}
}

// syncTrackingWriter wraps a syncWriteCloser to record whether Backup calls
// Sync before Close, proving the durability fix -- fsync before rename -- is
// actually wired in, not merely that the seam's type grew a Sync method.
type syncTrackingWriter struct {
	syncWriteCloser
	syncCalled            bool
	syncCalledBeforeClose bool
}

func (w *syncTrackingWriter) Sync() error {
	w.syncCalled = true
	return w.syncWriteCloser.Sync()
}

func (w *syncTrackingWriter) Close() error {
	w.syncCalledBeforeClose = w.syncCalled
	return w.syncWriteCloser.Close()
}

// TestBackup_SyncsOutputBeforeRenamingIntoPlace asserts Backup fsyncs its
// output before renaming it into place, mirroring Restore's own tmp.Sync()
// (backup.go). Without this, a power loss right after a 'successful' backup
// can leave exactly the truncated file TestRestore_RejectsTruncatedBackup
// above exists to catch on the next restore.
func TestBackup_SyncsOutputBeforeRenamingIntoPlace(t *testing.T) {
	srv, cfg, _, _ := startTestRelay(t, nil)
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var tracker *syncTrackingWriter
	orig := backupCreate
	backupCreate = func(dir string) (syncWriteCloser, string, error) {
		w, path, err := orig(dir)
		if err != nil {
			return nil, "", err
		}
		tracker = &syncTrackingWriter{syncWriteCloser: w}
		return tracker, path, nil
	}
	t.Cleanup(func() { backupCreate = orig })

	destPath := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(cfg.DBPath, destPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if tracker == nil {
		t.Fatal("backupCreate seam was never invoked")
	}
	if !tracker.syncCalled {
		t.Fatal("Backup did not fsync its output before renaming it into place")
	}
	if !tracker.syncCalledBeforeClose {
		t.Fatal("Backup called Sync after Close instead of before")
	}
}

// TestBackup_FsyncsDestinationDirectoryAfterRename asserts Backup fsyncs the
// destination directory once its rename lands, so the DIRECTORY ENTRY the
// rename created -- not just the file's contents -- survives a crash or
// power loss immediately after a "successful" backup. Without this, the
// file's bytes are durable (the earlier fsync-before-rename fix) but the
// name pointing at them might not be.
func TestBackup_FsyncsDestinationDirectoryAfterRename(t *testing.T) {
	srv, cfg, _, _ := startTestRelay(t, nil)
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var syncedDir string
	orig := fsyncDir
	fsyncDir = func(dir string) error {
		syncedDir = dir
		return orig(dir)
	}
	t.Cleanup(func() { fsyncDir = orig })

	destPath := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(cfg.DBPath, destPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if want := filepath.Dir(destPath); syncedDir != want {
		t.Fatalf("Backup did not fsync the destination directory: got %q, want %q", syncedDir, want)
	}
}

// TestRestore_FsyncsDestinationDirectoryAfterRename mirrors the Backup case
// above for Restore's own rename into dbPath's directory.
func TestRestore_FsyncsDestinationDirectoryAfterRename(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	backupPath := filepath.Join(t.TempDir(), "backup.db")
	seedValidBackup(t, backupPath)

	var syncedDir string
	orig := fsyncDir
	fsyncDir = func(dir string) error {
		syncedDir = dir
		return orig(dir)
	}
	t.Cleanup(func() { fsyncDir = orig })

	if err := Restore(dbPath, backupPath); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if want := filepath.Dir(dbPath); syncedDir != want {
		t.Fatalf("Restore did not fsync the destination directory: got %q, want %q", syncedDir, want)
	}
}

func TestRestoreRotatesMailboxIncarnation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	backup := filepath.Join(dir, "backup.db")
	target := filepath.Join(dir, "target.db")
	st, err := openStore(source)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err := st.db.View(func(tx *bolt.Tx) error { before = mailboxIncarnation(tx); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}
	if err := Backup(source, backup); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := Restore(target, backup); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restored, err := openStore(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.close() }()
	var after string
	if err := restored.db.View(func(tx *bolt.Tx) error { after = mailboxIncarnation(tx); return nil }); err != nil {
		t.Fatal(err)
	}
	if before == "" || after == "" || before == after {
		t.Fatalf("restore mailbox incarnations before=%q after=%q, want two non-empty distinct values", before, after)
	}
}

func TestRestoreAcceptsLegacyBackupAndMintsNewMailboxIncarnation(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy-backup.db")
	target := filepath.Join(dir, "target.db")
	seedValidBackup(t, legacy)

	db, err := bolt.Open(legacy, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket(bucketMeta) }); err != nil {
		_ = db.Close()
		t.Fatalf("remove post-legacy meta bucket: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Restore(target, legacy); err != nil {
		t.Fatalf("Restore legacy backup: %v", err)
	}
	restored, err := openStore(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.close() }()
	var incarnation string
	if err := restored.db.View(func(tx *bolt.Tx) error { incarnation = mailboxIncarnation(tx); return nil }); err != nil {
		t.Fatal(err)
	}
	if incarnation == "" {
		t.Fatal("restored legacy backup has no mailbox incarnation")
	}
}

// seedValidBackup writes a well-formed relay store (every required bucket) at
// path, standing in for a real backup file in tests that only need it to pass
// validation, not carry data.
func seedValidBackup(t *testing.T, path string) {
	t.Helper()
	st, err := openStore(path)
	if err != nil {
		t.Fatalf("seedValidBackup openStore: %v", err)
	}
	if err := st.close(); err != nil {
		t.Fatalf("seedValidBackup close: %v", err)
	}
}
