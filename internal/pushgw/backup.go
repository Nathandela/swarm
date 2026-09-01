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
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	backupFormatVersion = 1
	backupDBName        = "pushgw.db"
	backupKeyName       = "pushgw.key"
	backupManifestName  = "manifest.json"
	backupLockTimeout   = 300 * time.Millisecond
)

var ErrGatewayRunning = errors.New("pushgw: database is locked by a running gateway")

type backupManifest struct {
	Version   int    `json:"version"`
	DBSize    int64  `json:"db_size"`
	DBSHA256  string `json:"db_sha256"`
	KeySize   int64  `json:"key_size"`
	KeySHA256 string `json:"key_sha256"`
}

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
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("pushgw: publish backup archive: %w", err)
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

	dir := filepath.Dir(dbPath)
	tmpDir, err := os.MkdirTemp(dir, ".swarm-pushgw-restore-*")
	if err != nil {
		return fmt.Errorf("pushgw: create restore staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

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
			if hdr.Size <= 0 || hdr.Size > 16<<10 {
				return errors.New("pushgw: invalid backup manifest size")
			}
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

	if err := os.Rename(entries[backupDBName], dbPath); err != nil {
		return fmt.Errorf("pushgw: publish restored database: %w", err)
	}
	if err := os.Rename(entries[backupKeyName], dbPath+".key"); err != nil {
		_ = os.Remove(dbPath)
		return fmt.Errorf("pushgw: publish restored key: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("pushgw: sync restore directory: %w", err)
	}
	return nil
}

func validateBackupFile(path string, wantSize int64, wantSHA string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != wantSize || hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return errors.New("size or SHA-256 mismatch")
	}
	return nil
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
