package crypto

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrUnsafeKeyFile is returned when a key path is a symlink (a symlink-swap
// attack could redirect the write) — Save refuses rather than write through it.
var ErrUnsafeKeyFile = errors.New("crypto: refusing to write key material through a symlink")

// writeSecretFile writes private key material at exactly 0600 (F8). os.WriteFile
// does NOT tighten an already-existing file's mode, so a pre-existing 0644 key
// file would stay world-readable. This writes a fresh 0600 temp file in the same
// directory, fsyncs it, and atomically renames it over the target — the result
// is always 0600 regardless of any prior mode. A symlink at the target is
// refused outright.
func writeSecretFile(path string, data []byte) error {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrUnsafeKeyFile, path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".keytmp-*") // O_CREATE|O_EXCL, 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
