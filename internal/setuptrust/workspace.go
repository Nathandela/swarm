// Package setuptrust records explicit, machine-local consent to native CLI onboarding.
package setuptrust

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Workspace returns the physical directory authorized for new launches. Neither
// descendants nor a replacement checkout at the same path inherit its marker.
func Workspace(path string) (string, bool) {
	cwd, marker, err := location(path)
	if err != nil {
		return "", false
	}
	return cwd, ownedPrivateDirectory(filepath.Dir(marker)) && ownedPrivateDirectory(marker)
}

func Set(path string, enabled bool) (string, error) {
	cwd, marker, err := location(path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(marker)
	if !enabled {
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			return cwd, nil
		}
	}
	if enabled {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
	}
	if !ownedPrivateDirectory(dir) {
		return "", errors.New("setup trust: configuration directory must be private and owned by you")
	}
	if enabled {
		if err := os.Mkdir(marker, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if !ownedPrivateDirectory(marker) {
			return "", errors.New("setup trust: invalid workspace record")
		}
	} else if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return cwd, nil
}

func ownedPrivateDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func location(path string) (cwd, marker string, err error) {
	if !filepath.IsAbs(path) {
		return "", "", errors.New("setup trust: specify an absolute workspace path")
	}
	cwd, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("setup trust: workspace does not exist or is not a directory")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	stat := info.Sys().(*syscall.Stat_t)
	key := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d:%d", cwd, stat.Dev, stat.Ino)))
	return cwd, filepath.Join(dir, "swarm", "setup-trust", fmt.Sprintf("%x", key)), nil
}
