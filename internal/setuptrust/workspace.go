// Package setuptrust records explicit, machine-local consent to native CLI onboarding.
package setuptrust

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type Consent struct {
	Workspace    string
	ProfileHooks bool
}

// Get matches both the original path and the identity of the enrolled directory.
func Get(path string) (Consent, bool) {
	cwd, marker, err := location(path)
	if err != nil {
		return Consent{}, false
	}
	identity, err := directoryIdentity(cwd)
	if err != nil {
		return Consent{}, false
	}
	consent := Consent{Workspace: cwd}
	if !privateDirectory(filepath.Dir(marker)) {
		return consent, false
	}
	f, err := os.OpenFile(marker, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return consent, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return consent, false
	}
	data, err := io.ReadAll(io.LimitReader(f, 256))
	if err != nil {
		return consent, false
	}
	switch string(data) {
	case identity + " workspace\n":
		return consent, true
	case identity + " profile\n":
		consent.ProfileHooks = true
		return consent, true
	default:
		return consent, false
	}
}

func Set(path string, enabled, profileHooks bool) (string, error) {
	cwd, marker, err := location(path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(marker)
	if !enabled {
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			return cwd, nil
		}
		if !privateDirectory(dir) {
			return "", errors.New("setup trust: config directory must be private and owned by you")
		}
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return cwd, nil
	}
	identity, err := directoryIdentity(cwd)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if !privateDirectory(dir) {
		return "", errors.New("setup trust: config directory must be private and owned by you")
	}
	f, err := os.CreateTemp(dir, ".enroll-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	scope := "workspace"
	if profileHooks {
		scope = "profile"
	}
	if _, err := fmt.Fprintln(f, identity, scope); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), marker); err != nil {
		return "", err
	}
	return cwd, nil
}

func privateDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode().Perm()&0077 == 0 && owned(info)
}
func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func location(path string) (cwd, marker string, err error) {
	if !filepath.IsAbs(path) {
		return "", "", errors.New("setup trust: specify an absolute workspace path")
	}
	cwd, err = filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		cwd = filepath.Clean(path)
	} else if err != nil {
		return "", "", err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	key := sha256.Sum256([]byte(cwd))
	return cwd, filepath.Join(dir, "swarm", "setup-trust", fmt.Sprintf("%x", key)), nil
}
