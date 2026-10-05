package accounts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func nativeContextUpdatePending(profile *os.Root, provider string) bool {
	name := ".swarm-codex-context-update.json"
	if provider == ProviderClaude {
		name = ".swarm-claude-context-update.json"
	}
	_, err := profile.Lstat(name)
	return !errors.Is(err, os.ErrNotExist)
}

// Retirement retains this exact history alias without reading a transcript or
// following the alias for deletion. Both the shared root and session directory
// must still have the custody recorded by the installed context and proof.
func validCodexHistoryInventory(profile *os.Root) bool {
	marker, err := readPrivate(profile, ".swarm-codex-context.json", 2<<20)
	if err != nil {
		return false
	}
	var context struct {
		GlobalGeneration, SourceDirectorySHA256 string
		SourceProfile                           struct {
			Canonical string
			Absent    bool
		}
	}
	if json.Unmarshal(marker, &context) != nil || len(context.GlobalGeneration) != 64 || context.SourceProfile.Absent || !filepath.IsAbs(context.SourceProfile.Canonical) || filepath.Clean(context.SourceProfile.Canonical) != context.SourceProfile.Canonical {
		return false
	}
	prefix := []byte(`{"GlobalGeneration":"` + context.GlobalGeneration + `",`)
	if !bytes.HasPrefix(marker, prefix) {
		return false
	}
	empty := append([]byte(`{"GlobalGeneration":"",`), marker[len(prefix):]...)
	sum := sha256.Sum256(empty)
	if hex.EncodeToString(sum[:]) != context.GlobalGeneration {
		return false
	}
	info, err := os.Lstat(context.SourceProfile.Canonical)
	if err != nil || !codexHistoryOwnedDirectory(info) {
		return false
	}
	stat := info.Sys().(*syscall.Stat_t)
	rootIdentity, _ := json.Marshal(struct {
		Path          string
		Device, Inode uint64
		Owner         uint32
	}{context.SourceProfile.Canonical, uint64(stat.Dev), stat.Ino, stat.Uid})
	sum = sha256.Sum256(rootIdentity)
	if hex.EncodeToString(sum[:]) != context.SourceDirectorySHA256 {
		return false
	}
	raw, err := readPrivate(profile, ".swarm-codex-history.json", 2<<20)
	var proof struct {
		GlobalGeneration, SourcePath string
		Device, Inode                uint64
	}
	if err != nil || json.Unmarshal(raw, &proof) != nil || proof.GlobalGeneration != context.GlobalGeneration || proof.SourcePath != filepath.Join(context.SourceProfile.Canonical, "sessions") || proof.Device == 0 || proof.Inode == 0 {
		return false
	}
	target, err := profile.Readlink("sessions")
	if err != nil || target != proof.SourcePath {
		return false
	}
	info, err = os.Lstat(proof.SourcePath)
	if err != nil || !codexHistoryOwnedDirectory(info) {
		return false
	}
	stat = info.Sys().(*syscall.Stat_t)
	return uint64(stat.Dev) == proof.Device && stat.Ino == proof.Inode
}

func codexHistoryOwnedDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.IsDir() && info.Mode().Perm()&0002 == 0 && ok && stat.Uid == uint32(os.Getuid())
}
