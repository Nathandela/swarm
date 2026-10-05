package persist

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CLIIdentity is an observation of the selected installation, not process attestation.
// Ordinary wrapper dependencies and metadata-preserving edits are not covered;
// retained executable fingerprints additionally cover their complete bytes.
type CLIIdentity struct {
	Path        string `json:"path"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

// CLIContentFingerprint extends the opaque installation stamp for a retained,
// owner-private executable. Old actors cannot mistake it for a metadata stamp.
func CLIContentFingerprint(path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || canonical != path {
		return "", fmt.Errorf("retained CLI path is not canonical")
	}
	before, err := CLIFingerprint(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() || fi.Size() <= 0 || fi.Size() > 1<<30 || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 || fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("retained CLI is not private")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(fi, opened) {
		return "", fmt.Errorf("retained CLI changed")
	}
	h := sha256.New()
	if count, err := io.Copy(h, io.LimitReader(f, 1<<30+1)); err != nil || count != fi.Size() {
		return "", fmt.Errorf("retained CLI content changed")
	}
	after, err := CLIFingerprint(path)
	if err != nil || before != after {
		return "", fmt.Errorf("retained CLI changed")
	}
	return fmt.Sprintf("sha256:%x:%s", h.Sum(nil), after), nil
}

// MatchCLIFingerprint preserves metadata observations for ordinary launchers
// and verifies the complete content of retained executable observations.
func MatchCLIFingerprint(path, expected string) bool {
	var actual string
	var err error
	if strings.HasPrefix(expected, "sha256:") {
		if !IsCLIContentFingerprint(expected) {
			return false
		}
		actual, err = CLIContentFingerprint(path)
	} else {
		actual, err = CLIFingerprint(path)
	}
	return err == nil && actual == expected
}

// IsCLIContentFingerprint validates a metadata-only compatibility reference.
func IsCLIContentFingerprint(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "sha256" {
		return false
	}
	for _, part := range parts[1:] {
		raw, err := hex.DecodeString(part)
		if err != nil || len(raw) != sha256.Size || part != strings.ToLower(part) {
			return false
		}
	}
	return true
}

// CLIFingerprint notices symlink retargets and ordinary atomic/in-place updates.
// It deliberately does not copy or rewrite launchers, preserving argv[0] semantics.
func CLIFingerprint(path string) (string, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("CLI is not executable")
	}
	var dev, ino uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		dev, ino = uint64(st.Dev), st.Ino
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%d:%d:%d", target, dev, ino, fi.Size(), fi.ModTime().UnixNano(), fi.Mode())))), nil
}
