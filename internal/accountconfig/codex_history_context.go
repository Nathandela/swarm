package accountconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const CodexHistoryMarker = ".swarm-codex-history.json"

type CodexHistoryAlias struct {
	GlobalGeneration, SourcePath string
	Device, Inode                uint64
}

// Source creation is restricted to an authorized launch under the account
// generation's writer lease. Existing private history is never replaced.
func PrepareCodexHistoryAlias(c CodexContext, profile string, createSource bool, lease func(string, func() error) error, ownerRefresh ...bool) (CodexHistoryAlias, error) {
	var proof CodexHistoryAlias
	if lease == nil || len(ownerRefresh) > 1 || !validCodexContext(c) {
		return proof, Conflict("invalid-native-history-context")
	}
	err := lease(c.GlobalGeneration, func() error {
		if err := RevalidateCodexContext(c, profile); err != nil {
			return err
		}
		if c.SourceProfile.Absent {
			return Conflict("missing-original-native-history-profile")
		}
		source := filepath.Join(c.SourceProfile.Canonical, "sessions")
		destination := filepath.Join(profile, "sessions")
		if target, err := os.Readlink(destination); err == nil {
			if target != source {
				return Conflict("native-history-destination-differs")
			}
		} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return Conflict("native-history-destination-differs")
		}
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			if !createSource {
				return Conflict("original-native-history-missing")
			}
			if err := os.Mkdir(source, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return Conflict("native-history-install")
			}
		} else if err != nil {
			return Conflict("unsafe-native-history")
		}
		if err := safePath(source, true); err != nil {
			return Conflict("unsafe-native-history")
		}
		info, err := os.Lstat(source)
		if err != nil || !claudeOwnedInfo(info, true) {
			return Conflict("unsafe-native-history")
		}
		stat := info.Sys().(*syscall.Stat_t)
		proof = CodexHistoryAlias{c.GlobalGeneration, source, uint64(stat.Dev), stat.Ino}
		marker := filepath.Join(profile, CodexHistoryMarker)
		raw, err := readRegular(marker, maxSourceBytes)
		if err == nil {
			var installed CodexHistoryAlias
			if privateProjectionFile(marker) != nil || json.Unmarshal(raw, &installed) != nil {
				return Conflict("native-history-root-changed")
			}
			if installed != proof && (len(ownerRefresh) != 1 || !ownerRefresh[0] || !claudeContextDigest(installed.GlobalGeneration, false) || installed.SourcePath != proof.SourcePath || installed.Device != proof.Device || installed.Inode != proof.Inode) {
				return Conflict("native-history-root-changed")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Conflict("invalid-native-history-inventory")
		}
		if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
			if err := os.Symlink(source, destination); err != nil {
				return Conflict("native-history-install")
			}
		}
		raw, err = json.Marshal(proof)
		if err != nil {
			return Conflict("native-history-install")
		}
		if err := writeClaudeContextFile(profile, CodexHistoryMarker, raw); err != nil {
			return err
		}
		for _, path := range []string{source, c.SourceProfile.Canonical, profile} {
			dir, err := os.Open(path)
			if err != nil {
				return Conflict("native-history-install")
			}
			syncErr := dir.Sync()
			_ = dir.Close()
			if syncErr != nil {
				return Conflict("configuration-durability-uncertain")
			}
		}
		return RevalidateCodexHistoryAlias(c, profile, proof)
	})
	return proof, err
}
func ValidCodexHistoryAlias(c CodexContext, proof CodexHistoryAlias) bool {
	return validCodexContext(c) && !c.SourceProfile.Absent && proof.GlobalGeneration == c.GlobalGeneration && proof.SourcePath == filepath.Join(c.SourceProfile.Canonical, "sessions") && proof.Device != 0 && proof.Inode != 0
}
func RevalidateCodexHistoryAlias(c CodexContext, profile string, proof CodexHistoryAlias) error {
	if !ValidCodexHistoryAlias(c, proof) {
		return Conflict("invalid-native-history-context")
	}
	if err := RevalidateCodexContext(c, profile); err != nil {
		return err
	}
	raw, err := readRegular(filepath.Join(profile, CodexHistoryMarker), maxSourceBytes)
	var installed CodexHistoryAlias
	if err != nil || privateProjectionFile(filepath.Join(profile, CodexHistoryMarker)) != nil || json.Unmarshal(raw, &installed) != nil || installed != proof {
		return Conflict("invalid-native-history-inventory")
	}
	return validateCodexHistoryIdentity(profile, proof)
}
func validateCodexHistoryIdentity(profile string, proof CodexHistoryAlias) error {
	if err := safePath(proof.SourcePath, true); err != nil {
		return Conflict("unsafe-native-history")
	}
	info, err := os.Lstat(proof.SourcePath)
	if err != nil || !claudeOwnedInfo(info, true) {
		return Conflict("unsafe-native-history")
	}
	stat := info.Sys().(*syscall.Stat_t)
	if uint64(stat.Dev) != proof.Device || stat.Ino != proof.Inode {
		return Conflict("native-history-root-changed")
	}
	target, err := os.Readlink(filepath.Join(profile, "sessions"))
	if err != nil || target != proof.SourcePath {
		return Conflict("native-history-destination-differs")
	}
	return nil
}
