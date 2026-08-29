package relay

// Backup/restore for the relay's bbolt store (playbook 6.5). The relay stays an
// untrusted ciphertext mailbox throughout: Backup and Restore move the same
// opaque bytes openStore already persists, decrypt nothing, and add no new
// content the relay must hold.
//
// bbolt gives one process exclusive use of a store file at a time (Open always
// flocks it -- LOCK_EX unless ReadOnly -- for the handle's lifetime, per
// go.etcd.io/bbolt@v1.3.11's bolt_unix.go). A running relay therefore already
// holds the lock this package probes: Backup and Restore both open with a
// short, nonzero Timeout (never the relay's own Timeout: 0, which waits
// forever) so a live relay is detected as bolt.ErrTimeout in well under a
// second rather than hanging the operator's terminal.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// lockProbeTimeout bounds how long Backup/Restore wait to acquire the store's
// file lock before concluding a relay currently holds it. Short enough that a
// refusal is immediate; long enough (well above bbolt's 50ms flock retry) that
// a couple of retries happen before giving up.
const lockProbeTimeout = 300 * time.Millisecond

// ErrRelayRunning is returned by Backup and Restore when the store file is
// currently held open by another process (in production, a running relay).
// Backing up or restoring into a live store risks reading a torn snapshot or
// replacing a file a live writer still has open.
var ErrRelayRunning = errors.New("relay: store is locked by a running relay")

// requiredBuckets are the buckets openStore creates on every store it opens
// (store.go). Restore checks a candidate backup has all of them before ever
// touching the live store.
var requiredBuckets = [][]byte{
	bucketItems, bucketSeq, bucketPairs, bucketRevoked, bucketTokens, bucketConsents, bucketRetired,
}

// syncWriteCloser is backupCreate's seam type: a temp file Backup can fsync
// before renaming into place (mirroring Restore's own tmp.Sync() below), so a
// 'successful' backup is durable across a crash/power loss immediately after
// -- otherwise the file a later Restore reads back can be exactly the
// truncated shape checkBackupNotTruncated exists to catch. Widened from a
// bare io.WriteCloser, which cannot express fsync.
type syncWriteCloser interface {
	io.WriteCloser
	Sync() error
}

// backupCreate creates a fresh, uniquely-named temp file in dir for the
// backup write and returns both the open handle and the path it was created
// at (Backup needs the path back for the rename that follows). It is a seam:
// tests substitute a writer that fails partway through to prove disk-full
// leaves no partial backup file, without needing a real full disk (darwin has
// no simple per-test tmpfs or quota mechanism).
//
// Using os.CreateTemp -- exactly as Restore's own staging file already does
// below -- gives every call a name no concurrent call can collide with.
// Nothing serializes two Backup runs to the same destPath (Backup's ReadOnly
// Open takes a SHARED flock, so a long cron backup overlapping the next tick
// gets the source store simultaneously); a fixed "<dest>.tmp" name plus an
// unconditional os.Remove to clear a stale one -- the previous approach --
// let the SECOND run's Remove unlink the FIRST run's still-in-flight temp
// file out from under it, so whichever run's os.Rename fired last could
// silently publish the other's not-yet-written file over destPath while
// still reporting success. A unique name per call rules that out structurally
// and needs no os.Remove at all -- there is never anything stale to clear.
var backupCreate = func(dir string) (syncWriteCloser, string, error) {
	f, err := os.CreateTemp(dir, ".swarm-relay-backup-*")
	if err != nil {
		return nil, "", err
	}
	return f, f.Name(), nil
}

// fsyncDir fsyncs a directory's entry table after a rename into it, so the
// new name itself -- not just the file's contents -- survives a crash or
// power loss immediately after. A seam so tests can observe the call.
var fsyncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// Backup writes a consistent snapshot of the bbolt store at dbPath to destPath
// using bolt.Tx.WriteTo inside a read-only transaction (PB-6.5) -- bbolt's own
// documented hot-backup mechanism. The read-only Open above already requires
// the exclusive flock a running relay holds be free (see the package
// comment), so nothing else can be writing dbPath while this runs; Tx.WriteTo's
// role here is producing a self-contained snapshot as of one transaction, not
// defending against a concurrent writer that cannot exist.
//
// destPath is written via a temp file in the same directory, fsynced, and
// renamed into place, so a failure partway through (including a disk-full
// write) never leaves a partial or corrupt file at destPath, and a crash or
// power loss immediately AFTER a 'successful' backup cannot leave a truncated
// file behind either -- the rename is not visible until the fsync ahead of it
// completes. The destination directory is fsynced too, once the rename lands,
// so the directory entry itself is not lost to the same crash.
func Backup(dbPath, destPath string) error {
	// bolt.Open initializes fresh meta pages into ANY zero-length file it is
	// pointed at, even ReadOnly (see checkStoreNotLocked below) -- so without
	// this guard, backing up a pre-created-but-empty dbPath would silently
	// turn the SOURCE into a real (if empty) bbolt file and report success on
	// a backup with no buckets in it.
	if info, err := os.Stat(dbPath); err == nil && info.Size() == 0 {
		return fmt.Errorf("relay: refuse to back up zero-length store %s", dbPath)
	}

	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{ReadOnly: true, Timeout: lockProbeTimeout})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return fmt.Errorf("%w (%s)", ErrRelayRunning, dbPath)
		}
		return fmt.Errorf("relay: open store for backup: %w", err)
	}
	defer func() { _ = db.Close() }()

	dir := filepath.Dir(destPath)
	out, tmp, err := backupCreate(dir)
	if err != nil {
		return fmt.Errorf("relay: create backup file: %w", err)
	}
	writeErr := db.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(out)
		return err
	})
	var syncErr error
	if writeErr == nil {
		syncErr = out.Sync()
	}
	closeErr := out.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		switch {
		case writeErr != nil:
			return fmt.Errorf("relay: write backup: %w", writeErr)
		case syncErr != nil:
			return fmt.Errorf("relay: sync backup file: %w", syncErr)
		default:
			return fmt.Errorf("relay: close backup file: %w", closeErr)
		}
	}
	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("relay: finalize backup: %w", err)
	}
	if err := fsyncDir(dir); err != nil {
		return fmt.Errorf("relay: sync backup directory: %w", err)
	}
	return nil
}

// Restore replaces the store at dbPath with the backup at backupPath. It
// refuses while dbPath is held open by a running relay, validates backupPath
// opens as a bbolt database with every bucket the relay requires, and only
// then replaces dbPath -- via a temp file in the same directory, fsynced and
// renamed into place, so a failure partway through never leaves dbPath
// partially written; the directory is fsynced too, once the rename lands, so
// the directory entry itself is not lost to a crash immediately after.
//
// Restore is a point-in-time rollback of the WHOLE store, which includes the
// revocation state: restoring a backup taken before a `swarm remote revoke`
// brings back the deleted pairing edge and drops the retired-ceremony
// tombstone (ADR-007 B47) that make the revoke durable, so a grantee that
// kept its old consent bytes is accepted again after the restore. This is
// inherent to any point-in-time restore, not a bug in this package -- see
// relay-runbook.md Sec.11's "Restore is a revocation rollback" caveat, and
// re-revoke anything that was revoked after the backup was taken.
func Restore(dbPath, backupPath string) error {
	if err := checkStoreNotLocked(dbPath); err != nil {
		return err
	}
	if err := validateBackup(backupPath); err != nil {
		return fmt.Errorf("relay: invalid backup: %w", err)
	}

	src, err := os.Open(backupPath)
	if err != nil {
		return fmt.Errorf("relay: open backup: %w", err)
	}
	defer func() { _ = src.Close() }()

	dir := filepath.Dir(dbPath)
	tmp, err := os.CreateTemp(dir, ".swarm-relay-restore-*")
	if err != nil {
		return fmt.Errorf("relay: create restore staging file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds

	_, copyErr := io.Copy(tmp, src)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("relay: copy backup into place: %w", copyErr)
	case syncErr != nil:
		return fmt.Errorf("relay: sync restored store: %w", syncErr)
	case closeErr != nil:
		return fmt.Errorf("relay: close restored store: %w", closeErr)
	}
	// A restore is a rollback to another mailbox log even when the backup itself
	// already carries an incarnation. Reusing it would let a consumer cursor from
	// after the snapshot silently skip/purge restored items once H catches up.
	staged, err := bolt.Open(tmpPath, 0o600, &bolt.Options{Timeout: lockProbeTimeout})
	if err != nil {
		return fmt.Errorf("relay: open staged restore for incarnation rotation: %w", err)
	}
	rotateErr := rotateMailboxIncarnation(staged)
	closeErr = staged.Close()
	if rotateErr != nil {
		return fmt.Errorf("relay: rotate restored mailbox incarnation: %w", rotateErr)
	}
	if closeErr != nil {
		return fmt.Errorf("relay: close incarnation-rotated restore: %w", closeErr)
	}
	if err := os.Rename(tmpPath, dbPath); err != nil {
		return fmt.Errorf("relay: finalize restore: %w", err)
	}
	if err := fsyncDir(dir); err != nil {
		return fmt.Errorf("relay: sync restore directory: %w", err)
	}
	return nil
}

// checkStoreNotLocked reports ErrRelayRunning if dbPath exists and is
// currently held open by another process. A dbPath that does not exist yet
// (a fresh restore target) has nothing to hold a lock, so it passes -- and so
// does a dbPath that exists but is zero bytes: bbolt's own Open initializes
// fresh meta pages into ANY zero-length file it is pointed at, readOnly or
// not, which would otherwise turn this check-only probe into a mutation of a
// file Restore is supposed to leave untouched if a later step fails.
//
// The probe itself opens ReadOnly: a shared (not exclusive) flock still
// blocks against a running relay's exclusive one, so detection is unchanged,
// and ReadOnly also skips bolt.Open's freelist-sync write transaction on an
// otherwise-valid store -- this is meant to be read-only, not "read-write but
// happens not to write anything today".
//
// This check is advisory, not load-bearing: it CLOSES the handle immediately
// (releasing the flock) before the copy and rename that follow, so a relay
// started inside that window opens the pre-restore inode and then has it
// shadowed out from under it by Restore's rename -- it serves an unlinked
// file, and everything it writes from then on is silently discarded at its
// next restart. Holding the lock open across the whole Restore would close
// that window but block a relay trying to start mid-restore instead of
// racing it, which is a worse failure mode for an operator to hit
// unexpectedly; the documented procedure (relay-vps-deploy.md Sec.13) avoids
// the window entirely by stopping the unit first.
func checkStoreNotLocked(dbPath string) error {
	info, statErr := os.Stat(dbPath)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return fmt.Errorf("relay: probe store lock: %w", statErr)
	}
	if info.Size() == 0 {
		return nil
	}
	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{ReadOnly: true, Timeout: lockProbeTimeout})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return fmt.Errorf("%w (%s)", ErrRelayRunning, dbPath)
		}
		return fmt.Errorf("relay: probe store lock: %w", err)
	}
	return db.Close()
}

// validateBackup opens path as a bbolt database, confirms every bucket the
// relay's store requires is present, and runs bbolt's own consistency check
// (tx.Check) -- all without touching the live store.
func validateBackup(path string) error {
	if err := checkBackupNotTruncated(path); err != nil {
		return err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: lockProbeTimeout})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return fmt.Errorf("%w (%s)", ErrRelayRunning, path)
		}
		return err
	}
	defer func() { _ = db.Close() }()
	return db.View(func(tx *bolt.Tx) error {
		for _, b := range requiredBuckets {
			if tx.Bucket(b) == nil {
				return fmt.Errorf("missing bucket %q", b)
			}
		}
		// tx.Check walks the whole B+tree structure -- key ordering, page
		// reachability, double-frees -- catching corruption bucket presence
		// alone misses (a backup written by some other tool, or plain
		// garbage that happens to be the right size). It does NOT checksum
		// opaque VALUE bytes (bbolt has no per-page data checksum), so a
		// flipped bit purely inside an envelope's ciphertext can still slip
		// through; this closes the structural half of that gap, which is
		// the half bbolt itself is able to detect at all. The channel is
		// drained fully rather than stopping at the first error, so the
		// background goroutine tx.Check starts is never left blocked trying
		// to send to a reader that walked away.
		var checkErrs []error
		for checkErr := range tx.Check() {
			checkErrs = append(checkErrs, checkErr)
		}
		if len(checkErrs) > 0 {
			return fmt.Errorf("backup failed bbolt consistency check (%d issue(s)), first: %w", len(checkErrs), checkErrs[0])
		}
		return nil
	})
}

// errBackupTruncated is checkBackupNotTruncated's refusal: the backup file's
// own bbolt meta page records more pages than the file actually contains --
// exactly the shape a truncated scp/rsync/copy leaves behind. bolt.Open does
// not refuse this cleanly: it mmaps the file at its (short) actual size, and
// the failure surfaces later as an out-of-bounds pointer dereference into
// memory past the mapped region -- a fatal fault (SIGBUS, or an unrecoverable
// runtime assertion panic depending on exactly where the cut falls relative
// to a page boundary) that recover() cannot catch, killing the whole calling
// process. This check runs first, using plain ReadAt calls that can only
// ever return io.EOF short of the file's real end -- never a fault.
var errBackupTruncated = errors.New("relay: backup file is truncated (shorter than its own bbolt metadata declares)")

// bboltMagic and bboltMetaVersion mirror go.etcd.io/bbolt@v1.3.11's own
// private meta.magic/version constants (db.go) -- duplicated here because
// they are unexported, and this package has no other way to recognize "this
// is a bbolt meta page" before ever calling bolt.Open on untrusted input.
//
// bboltMetaOffset is where a meta record begins within a bbolt page: right
// after the fixed 16-byte page header (id uint64, flags uint16, count
// uint16, overflow uint32 -- bbolt's page.go). bboltMetaChecksummedLen is
// how many meta bytes bbolt's own checksum covers -- everything up to (not
// including) the checksum field itself: magic(4) + version(4) + pageSize(4)
// + flags(4) + root bucket(16) + freelist(8) + pgid(8) + txid(8) = 56 bytes,
// immediately followed by an 8-byte checksum. bboltMetaPgidOffset and
// bboltMetaTxidOffset locate those two fields within that same record.
const (
	bboltMagic              = 0xED0CDAED
	bboltMetaVersion        = 2
	bboltMetaOffset         = 16
	bboltMetaChecksummedLen = 56
	bboltMetaPgidOffset     = 40
	bboltMetaTxidOffset     = 48
)

// checkBackupNotTruncated reports errBackupTruncated if path's ACTIVE meta
// page -- the one bolt.Open will actually use -- declares more pages than
// the file's actual size can hold. bbolt keeps two meta pages, at page 0 and
// page 1 (byte offset 0 and pageSize), and always uses whichever one
// validates with the HIGHER txid (db.go's db.meta()), alternating which page
// that is by txid parity (meta.write: p.id = txid % 2). Checking only page 0
// -- as this function used to -- misses every store whose last commit landed
// on page 1: a file this package's own Backup produced is immune (Tx.WriteTo
// always writes page 0's meta with the higher txid, page 1 with txid-1), but
// a raw file-level copy of a LIVE store -- an interrupted scp/rsync, or
// simply `cp` on the .db file -- is not; page 1 is active roughly half the
// time.
//
// It parses page 0's meta first, both to validate it and to learn the page
// size (the one piece of information needed to even locate page 1), then
// parses page 1 at that offset. A meta that fails to validate (bad
// magic/version/checksum, or the file is too short to contain it at all) is
// simply dropped from consideration -- exactly the shape a truncation leaves
// the STALE meta page in, which is not by itself an error. If page 0 does
// not validate, this function still declines to judge (as before): a file
// whose very first meta is unreadable is left for bolt.Open to report in its
// own words.
func checkBackupNotTruncated(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return nil // let bolt.Open report this in its own words
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil
	}

	need := bboltMetaOffset + bboltMetaChecksummedLen + 8
	buf0 := make([]byte, need)
	n0, err := f.ReadAt(buf0, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	pageSize, pgid0, txid0, ok := parseBoltMeta(buf0[:n0])
	if !ok {
		return nil
	}
	activePgid := pgid0

	buf1 := make([]byte, need)
	n1, err := f.ReadAt(buf1, int64(pageSize))
	if err == nil || errors.Is(err, io.EOF) {
		if _, pgid1, txid1, ok1 := parseBoltMeta(buf1[:n1]); ok1 && txid1 > txid0 {
			activePgid = pgid1
		}
	}

	want := activePgid * uint64(pageSize)
	if uint64(info.Size()) < want {
		return fmt.Errorf("%w: file is %d bytes, its meta page declares %d", errBackupTruncated, info.Size(), want)
	}
	return nil
}

// parseBoltMeta reads the pageSize, pgid, and txid go.etcd.io/bbolt would
// read from a meta page occupying the leading bytes of page, validating the
// same magic, version, and FNV-1a-64 checksum bbolt's own (private)
// meta.validate does -- so this only accepts what bbolt itself would accept
// as a meta page.
//
// Field layout and byte order match go.etcd.io/bbolt@v1.3.11's meta struct
// (db.go) exactly as bbolt writes it: a raw unsafe.Pointer cast into the
// mmapped page, native machine byte order, not encoding/binary. That is
// little-endian on every platform this project ships to (amd64, arm64).
func parseBoltMeta(page []byte) (pageSize uint32, pgid uint64, txid uint64, ok bool) {
	need := bboltMetaOffset + bboltMetaChecksummedLen + 8
	if len(page) < need {
		return 0, 0, 0, false
	}
	m := page[bboltMetaOffset:need]
	if binary.LittleEndian.Uint32(m[0:4]) != bboltMagic {
		return 0, 0, 0, false
	}
	if binary.LittleEndian.Uint32(m[4:8]) != bboltMetaVersion {
		return 0, 0, 0, false
	}
	checksum := binary.LittleEndian.Uint64(m[bboltMetaChecksummedLen : bboltMetaChecksummedLen+8])
	h := fnv.New64a()
	_, _ = h.Write(m[:bboltMetaChecksummedLen])
	if h.Sum64() != checksum {
		return 0, 0, 0, false
	}
	return binary.LittleEndian.Uint32(m[8:12]),
		binary.LittleEndian.Uint64(m[bboltMetaPgidOffset : bboltMetaPgidOffset+8]),
		binary.LittleEndian.Uint64(m[bboltMetaTxidOffset : bboltMetaTxidOffset+8]),
		true
}
