package persist

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// CLIIdentity is an observation of the selected installation, not process attestation.
// Wrapper dependencies and in-place changes that preserve metadata are not covered.
type CLIIdentity struct {
	Path        string `json:"path"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
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
