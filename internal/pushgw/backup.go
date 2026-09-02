package pushgw

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	backupFormatVersion = 1
	backupDBName        = "pushgw.db"
	backupKeyName       = "pushgw.key"
	backupManifestName  = "manifest.json"
	backupLockTimeout   = 300 * time.Millisecond
	// Restore is an operator-controlled path, but archives are still untrusted
	// input. Bound both the declared DB member and the complete tar before any
	// extraction can consume unbounded local disk.
	maxBackupDBBytes      int64 = 16 << 30
	maxBackupArchiveBytes int64 = maxBackupDBBytes + 1<<20
)

var ErrGatewayRunning = errors.New("pushgw: database is locked by a running gateway")

type backupManifest struct {
	Version   int    `json:"version"`
	DBSize    int64  `json:"db_size"`
	DBSHA256  string `json:"db_sha256"`
	KeySize   int64  `json:"key_size"`
	KeySHA256 string `json:"key_sha256"`
}

type restorePublication struct {
	Version    int            `json:"version"`
	TargetDB   string         `json:"target_db"`
	StageDir   string         `json:"stage_dir"`
	Manifest   backupManifest `json:"manifest"`
	markerPath string
}

func restoreMarkerPath(dbPath string) string { return dbPath + ".restore-in-progress" }

// Backup writes one self-validating archive containing a consistent bbolt snapshot and
// its inseparable AEAD key. It deliberately refuses while a gateway owns the DB lock; the
// service runbook brackets this command with stop/start.
func Backup(dbPath, destPath string) (retErr error) {
	key, err := os.ReadFile(dbPath + ".key")
	if err != nil {
		return fmt.Errorf("pushgw: read backup key: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("pushgw: backup key has length %d, want 32", len(key))
	}
	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{ReadOnly: true, Timeout: backupLockTimeout})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return fmt.Errorf("%w (%s)", ErrGatewayRunning, dbPath)
		}
		return fmt.Errorf("pushgw: open database for backup: %w", err)
	}
	defer func() { _ = db.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".swarm-pushgw-backup-*")
	if err != nil {
		return fmt.Errorf("pushgw: create backup temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	tw := tar.NewWriter(tmp)
	manifest := backupManifest{Version: backupFormatVersion, KeySize: int64(len(key))}
	err = db.View(func(tx *bolt.Tx) error {
		manifest.DBSize = tx.Size()
		if manifest.DBSize <= 0 || manifest.DBSize > maxBackupDBBytes {
			return fmt.Errorf("database snapshot size %d exceeds backup ceiling %d", manifest.DBSize, maxBackupDBBytes)
		}
		if err := tw.WriteHeader(&tar.Header{Name: backupDBName, Mode: 0o600, Size: manifest.DBSize}); err != nil {
			return err
		}
		h := sha256.New()
		written, err := tx.WriteTo(io.MultiWriter(tw, h))
		if err != nil {
			return err
		}
		if written != manifest.DBSize {
			return fmt.Errorf("database snapshot wrote %d bytes, expected %d", written, manifest.DBSize)
		}
		manifest.DBSHA256 = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		return fmt.Errorf("pushgw: write database snapshot: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: backupKeyName, Mode: 0o600, Size: int64(len(key))}); err != nil {
		return fmt.Errorf("pushgw: write key header: %w", err)
	}
	if _, err := tw.Write(key); err != nil {
		return fmt.Errorf("pushgw: write key: %w", err)
	}
	keySum := sha256.Sum256(key)
	manifest.KeySHA256 = hex.EncodeToString(keySum[:])
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: backupManifestName, Mode: 0o600, Size: int64(len(manifestBytes))}); err != nil {
		return fmt.Errorf("pushgw: write manifest header: %w", err)
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		return fmt.Errorf("pushgw: write manifest: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("pushgw: close backup archive: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("pushgw: sync backup archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("pushgw: close backup file: %w", err)
	}
	// Link is an atomic no-replace publication on the same filesystem. Rename
	// would silently overwrite an existing backup on Unix.
	if err := os.Link(tmpPath, destPath); err != nil {
		return fmt.Errorf("pushgw: publish backup archive: %w", err)
	}
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("pushgw: remove published backup staging link: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destPath)); err != nil {
		return fmt.Errorf("pushgw: sync backup directory: %w", err)
	}
	return nil
}

// Restore validates the entire archive and its required bbolt buckets before publishing
// either target file. The target paths must be absent; restoring over live state is an
// explicit operator error rather than an implicit rollback.
func Restore(dbPath, backupPath string) (retErr error) {
	var err error
	dbPath, err = filepath.Abs(dbPath)
	if err != nil {
		return fmt.Errorf("pushgw: resolve restore target: %w", err)
	}
	if _, err := os.Stat(restoreMarkerPath(dbPath)); err == nil {
		return recoverRestorePublication(dbPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("pushgw: inspect restore marker: %w", err)
	}
	for _, target := range []string{dbPath, dbPath + ".key"} {
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("pushgw: restore target already exists: %s", target)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("pushgw: inspect restore target: %w", err)
		}
	}
	in, err := os.Open(backupPath)
	if err != nil {
		return fmt.Errorf("pushgw: open backup archive: %w", err)
	}
	defer func() { _ = in.Close() }()
	if info, err := in.Stat(); err != nil {
		return fmt.Errorf("pushgw: stat backup archive: %w", err)
	} else if info.Size() <= 0 || info.Size() > maxBackupArchiveBytes {
		return fmt.Errorf("pushgw: backup archive size %d exceeds ceiling %d", info.Size(), maxBackupArchiveBytes)
	}

	dir := filepath.Dir(dbPath)
	tmpDir, err := os.MkdirTemp(dir, ".swarm-pushgw-restore-*")
	if err != nil {
		return fmt.Errorf("pushgw: create restore staging directory: %w", err)
	}
	defer func() {
		if tmpDir != "" {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	tr := tar.NewReader(in)
	entries := make(map[string]string)
	var manifest backupManifest
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("pushgw: read backup archive: %w", err)
		}
		// A zero typeflag is the deprecated tar.TypeRegA spelling for a regular file.
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
			return fmt.Errorf("pushgw: backup entry %q is not a regular file", hdr.Name)
		}
		if err := validateRestoreEntrySize(hdr.Name, hdr.Size); err != nil {
			return err
		}
		switch hdr.Name {
		case backupDBName, backupKeyName:
			if _, duplicate := entries[hdr.Name]; duplicate {
				return fmt.Errorf("pushgw: duplicate backup entry %s", hdr.Name)
			}
			path := filepath.Join(tmpDir, hdr.Name)
			out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(out, tr, hdr.Size)
			syncErr := out.Sync()
			closeErr := out.Close()
			if copyErr != nil || written != hdr.Size || syncErr != nil || closeErr != nil {
				return fmt.Errorf("pushgw: extract %s: copied=%d/%d copy=%v sync=%v close=%v", hdr.Name, written, hdr.Size, copyErr, syncErr, closeErr)
			}
			entries[hdr.Name] = path
		case backupManifestName:
			data, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
			if err != nil || int64(len(data)) != hdr.Size {
				return errors.New("pushgw: truncated backup manifest")
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				return fmt.Errorf("pushgw: decode backup manifest: %w", err)
			}
		default:
			return fmt.Errorf("pushgw: unexpected backup entry %q", hdr.Name)
		}
	}
	if manifest.Version != backupFormatVersion || entries[backupDBName] == "" || entries[backupKeyName] == "" {
		return errors.New("pushgw: incomplete or unsupported backup archive")
	}
	if err := validateBackupFile(entries[backupDBName], manifest.DBSize, manifest.DBSHA256); err != nil {
		return fmt.Errorf("pushgw: validate database snapshot: %w", err)
	}
	if err := validateBackupFile(entries[backupKeyName], manifest.KeySize, manifest.KeySHA256); err != nil {
		return fmt.Errorf("pushgw: validate AEAD key: %w", err)
	}
	if manifest.KeySize != 32 {
		return fmt.Errorf("pushgw: restored AEAD key has length %d, want 32", manifest.KeySize)
	}
	if err := validateBackupDatabase(entries[backupDBName]); err != nil {
		return err
	}

	publication, err := prepareRestorePublication(dbPath, tmpDir, manifest)
	if err != nil {
		return err
	}
	// The durable marker now owns cleanup/recovery of this stage directory.
	tmpDir = ""
	return publishRestorePublication(publication)
}

func validateRestoreEntrySize(name string, size int64) error {
	switch name {
	case backupDBName:
		if size <= 0 || size > maxBackupDBBytes {
			return fmt.Errorf("pushgw: invalid database snapshot size %d (ceiling %d)", size, maxBackupDBBytes)
		}
	case backupKeyName:
		if size != 32 {
			return fmt.Errorf("pushgw: invalid AEAD key entry size %d", size)
		}
	case backupManifestName:
		if size <= 0 || size > 16<<10 {
			return fmt.Errorf("pushgw: invalid backup manifest size %d", size)
		}
	default:
		return fmt.Errorf("pushgw: unexpected backup entry %q", name)
	}
	return nil
}

func manifestForRestoreFiles(dbPath, keyPath string) (backupManifest, error) {
	dbSize, dbSHA, err := fileDigest(dbPath)
	if err != nil {
		return backupManifest{}, err
	}
	keySize, keySHA, err := fileDigest(keyPath)
	if err != nil {
		return backupManifest{}, err
	}
	manifest := backupManifest{
		Version: backupFormatVersion, DBSize: dbSize, DBSHA256: dbSHA,
		KeySize: keySize, KeySHA256: keySHA,
	}
	if err := validateRestoreEntrySize(backupDBName, dbSize); err != nil {
		return backupManifest{}, err
	}
	if err := validateRestoreEntrySize(backupKeyName, keySize); err != nil {
		return backupManifest{}, err
	}
	return manifest, nil
}

func prepareRestorePublication(dbPath, stageDir string, manifest backupManifest) (restorePublication, error) {
	dbPath, err := filepath.Abs(dbPath)
	if err != nil {
		return restorePublication{}, err
	}
	stageDir, err = filepath.Abs(stageDir)
	if err != nil {
		return restorePublication{}, err
	}
	publication := restorePublication{
		Version: backupFormatVersion, TargetDB: dbPath, StageDir: stageDir,
		Manifest: manifest, markerPath: restoreMarkerPath(dbPath),
	}
	if err := validateRestorePublication(publication); err != nil {
		return restorePublication{}, err
	}
	if err := validateBackupFile(filepath.Join(stageDir, backupDBName), manifest.DBSize, manifest.DBSHA256); err != nil {
		return restorePublication{}, fmt.Errorf("pushgw: validate staged database: %w", err)
	}
	if err := validateBackupFile(filepath.Join(stageDir, backupKeyName), manifest.KeySize, manifest.KeySHA256); err != nil {
		return restorePublication{}, fmt.Errorf("pushgw: validate staged key: %w", err)
	}
	if err := validateBackupDatabase(filepath.Join(stageDir, backupDBName)); err != nil {
		return restorePublication{}, err
	}
	data, err := json.Marshal(publication)
	if err != nil {
		return restorePublication{}, err
	}
	f, err := os.OpenFile(publication.markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return restorePublication{}, fmt.Errorf("pushgw: claim restore publication: %w", err)
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(publication.markerPath)
		return restorePublication{}, fmt.Errorf("pushgw: persist restore marker: %w", err)
	}
	if err := syncDirectory(filepath.Dir(dbPath)); err != nil {
		_ = os.Remove(publication.markerPath)
		return restorePublication{}, fmt.Errorf("pushgw: sync restore marker: %w", err)
	}
	return publication, nil
}

func recoverRestorePublication(dbPath string) error {
	dbPath, err := filepath.Abs(dbPath)
	if err != nil {
		return err
	}
	marker := restoreMarkerPath(dbPath)
	data, err := os.ReadFile(marker)
	if err != nil {
		return fmt.Errorf("pushgw: read interrupted restore marker: %w", err)
	}
	if len(data) == 0 || len(data) > 16<<10 {
		return errors.New("pushgw: invalid interrupted restore marker")
	}
	var publication restorePublication
	if err := json.Unmarshal(data, &publication); err != nil {
		return fmt.Errorf("pushgw: decode interrupted restore marker: %w", err)
	}
	publication.markerPath = marker
	if publication.Version != backupFormatVersion || publication.TargetDB != dbPath {
		return errors.New("pushgw: interrupted restore marker does not match target")
	}
	if err := validateRestorePublication(publication); err != nil {
		return err
	}
	return publishRestorePublication(publication)
}

func publishRestorePublication(publication restorePublication) error {
	if err := validateRestorePublication(publication); err != nil {
		return err
	}
	dir := filepath.Dir(publication.TargetDB)
	stageDB := filepath.Join(publication.StageDir, backupDBName)
	stageKey := filepath.Join(publication.StageDir, backupKeyName)
	if err := validateBackupFile(stageDB, publication.Manifest.DBSize, publication.Manifest.DBSHA256); err != nil {
		return fmt.Errorf("pushgw: validate interrupted database stage: %w", err)
	}
	if err := validateBackupFile(stageKey, publication.Manifest.KeySize, publication.Manifest.KeySHA256); err != nil {
		return fmt.Errorf("pushgw: validate interrupted key stage: %w", err)
	}
	for _, pair := range [][2]string{{stageKey, publication.TargetDB + ".key"}, {stageDB, publication.TargetDB}} {
		if err := linkOrVerifyPublication(pair[0], pair[1]); err != nil {
			rollbackRestorePublication(publication)
			return err
		}
		if err := syncDirectory(dir); err != nil {
			return fmt.Errorf("pushgw: sync restore publication: %w", err)
		}
	}
	if err := os.Remove(publication.markerPath); err != nil {
		return fmt.Errorf("pushgw: finalize restore marker: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("pushgw: sync completed restore: %w", err)
	}
	if err := os.RemoveAll(publication.StageDir); err != nil {
		return fmt.Errorf("pushgw: remove completed restore stage: %w", err)
	}
	return nil
}

func validateRestorePublication(publication restorePublication) error {
	target, err := filepath.Abs(publication.TargetDB)
	if err != nil || target != publication.TargetDB {
		return errors.New("pushgw: restore publication target is not canonical")
	}
	stage, err := filepath.Abs(publication.StageDir)
	if err != nil || stage != publication.StageDir || filepath.Dir(stage) != filepath.Dir(target) ||
		!strings.HasPrefix(filepath.Base(stage), ".swarm-pushgw-restore-") {
		return errors.New("pushgw: restore stage is outside target directory")
	}
	if publication.Version != backupFormatVersion || publication.Manifest.Version != backupFormatVersion {
		return errors.New("pushgw: unsupported restore publication version")
	}
	if err := validateRestoreEntrySize(backupDBName, publication.Manifest.DBSize); err != nil {
		return err
	}
	if err := validateRestoreEntrySize(backupKeyName, publication.Manifest.KeySize); err != nil {
		return err
	}
	if publication.markerPath != restoreMarkerPath(target) {
		return errors.New("pushgw: restore publication marker does not match target")
	}
	return nil
}

func linkOrVerifyPublication(stage, target string) error {
	if err := os.Link(stage, target); err == nil {
		return nil
	} else if !os.IsExist(err) {
		return fmt.Errorf("pushgw: publish restored file: %w", err)
	}
	stageInfo, stageErr := os.Stat(stage)
	targetInfo, targetErr := os.Stat(target)
	if stageErr == nil && targetErr == nil && os.SameFile(stageInfo, targetInfo) {
		return nil
	}
	return fmt.Errorf("pushgw: restore target already exists: %s", target)
}

func rollbackRestorePublication(publication restorePublication) {
	for _, pair := range [][2]string{
		{filepath.Join(publication.StageDir, backupDBName), publication.TargetDB},
		{filepath.Join(publication.StageDir, backupKeyName), publication.TargetDB + ".key"},
	} {
		stageInfo, stageErr := os.Stat(pair[0])
		targetInfo, targetErr := os.Stat(pair[1])
		if stageErr == nil && targetErr == nil && os.SameFile(stageInfo, targetInfo) {
			_ = os.Remove(pair[1])
		}
	}
	_ = os.Remove(publication.markerPath)
	_ = os.RemoveAll(publication.StageDir)
	_ = syncDirectory(filepath.Dir(publication.TargetDB))
}

func validateBackupFile(path string, wantSize int64, wantSHA string) error {
	size, sha, err := fileDigest(path)
	if err != nil {
		return err
	}
	if size != wantSize || sha != wantSHA {
		return errors.New("size or SHA-256 mismatch")
	}
	return nil
}

func fileDigest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func validateBackupDatabase(path string) error {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: backupLockTimeout})
	if err != nil {
		return fmt.Errorf("pushgw: open restored database: %w", err)
	}
	defer func() { _ = db.Close() }()
	return db.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketInstallations, bucketAddresses, bucketTombstones} {
			if tx.Bucket(name) == nil {
				return fmt.Errorf("pushgw: restored database missing bucket %q", name)
			}
		}
		return nil
	})
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
