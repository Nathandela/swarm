package accountconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const ClaudeHistoryMarker = ".swarm-claude-history.json"

type ClaudeHistoryAlias struct {
	GlobalGeneration, ProjectKey, SourcePath string
	Device, Inode                            uint64
}

type claudeHistoryInventory struct {
	SchemaVersion int
	Aliases       map[string]ClaudeHistoryAlias
}

// PrepareClaudeHistoryAlias preserves the original native history and companion
// artifacts. The caller resolves the pinned native project key and excludes
// account writers through the same credential-generation lease as config setup.
// Creating an absent original history directory requires an authorized launch;
// read-only inventory/snapshot callers must leave createSource false.
func PrepareClaudeHistoryAlias(c ClaudeContext, profile, key string, createSource bool, lease func(string, func() error) error) (ClaudeHistoryAlias, error) {
	var proof ClaudeHistoryAlias
	if lease == nil || !validClaudeContext(c) || key == "" || key == "." || key == ".." || filepath.Base(key) != key || !filepath.IsLocal(key) || len(key) > 255 || strings.ContainsRune(key, 0) {
		return proof, Conflict("invalid-native-history-context")
	}
	sourcePath := filepath.Join(c.SourceProfile.Canonical, "projects", key)
	err := lease(c.GlobalGeneration, func() error {
		if err := RevalidateClaudeContext(c, profile); err != nil {
			return err
		}
		inventory, err := readClaudeHistoryInventory(profile)
		if err != nil {
			return err
		}
		projects := filepath.Join(profile, "projects")
		if _, err := os.Lstat(projects); err == nil {
			if err := safePath(projects, true); err != nil {
				return Conflict("unsafe-native-history")
			}
			info, err := os.Lstat(projects)
			if err != nil || !claudeOwnedInfo(info, true) {
				return Conflict("unsafe-native-history")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Conflict("unsafe-native-history")
		}
		destination := filepath.Join(projects, key)
		if target, err := os.Readlink(destination); err == nil {
			if target != sourcePath {
				return Conflict("native-history-destination-differs")
			}
		} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return Conflict("native-history-destination-differs")
		}

		if c.SourceProfile.Absent {
			return Conflict("missing-original-native-history-profile")
		}
		if _, err := os.Lstat(sourcePath); errors.Is(err, os.ErrNotExist) {
			if !createSource {
				return Conflict("original-native-history-missing")
			}
			parent := filepath.Dir(sourcePath)
			if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
				if err := os.Mkdir(parent, 0700); err != nil {
					return Conflict("native-history-install")
				}
			}
			if err := safePath(parent, true); err != nil {
				return Conflict("unsafe-native-history")
			}
			info, err := os.Lstat(parent)
			if err != nil || !claudeOwnedInfo(info, true) {
				return Conflict("unsafe-native-history")
			}
			if err := os.Mkdir(sourcePath, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return Conflict("native-history-install")
			}
		} else if err != nil {
			return Conflict("unsafe-native-history")
		}
		if err := safePath(sourcePath, true); err != nil {
			return Conflict("unsafe-native-history")
		}
		info, err := os.Lstat(sourcePath)
		if err != nil || !claudeOwnedInfo(info, true) {
			return Conflict("unsafe-native-history")
		}
		stat := info.Sys().(*syscall.Stat_t)
		proof = ClaudeHistoryAlias{c.GlobalGeneration, key, sourcePath, uint64(stat.Dev), stat.Ino}
		if createSource {
			for _, path := range []string{sourcePath, filepath.Dir(sourcePath), c.SourceProfile.Canonical} {
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
		}

		for recordedKey, recorded := range inventory.Aliases {
			if recordedKey != recorded.ProjectKey || !ValidClaudeHistoryAlias(c, recorded) {
				return Conflict("invalid-native-history-inventory")
			}
		}
		if prior, ok := inventory.Aliases[key]; ok && prior != proof {
			return Conflict("native-history-root-changed")
		}
		if _, err := os.Lstat(projects); errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(projects, 0700); err != nil {
				return Conflict("native-history-install")
			}
		}
		if err := safePath(projects, true); err != nil {
			return Conflict("unsafe-native-history")
		}
		info, err = os.Lstat(projects)
		if err != nil || !claudeOwnedInfo(info, true) {
			return Conflict("unsafe-native-history")
		}
		if target, err := os.Readlink(destination); err == nil {
			if target != sourcePath {
				return Conflict("native-history-destination-differs")
			}
		} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return Conflict("native-history-destination-differs")
		} else if err := os.Symlink(sourcePath, destination); err != nil {
			return Conflict("native-history-install")
		}
		inventory.Aliases[key] = proof
		raw, err := json.Marshal(inventory)
		if err != nil {
			return Conflict("native-history-install")
		}
		if err := writeClaudeContextFile(profile, ClaudeHistoryMarker, raw); err != nil {
			return err
		}
		dir, err := os.Open(projects)
		if err != nil {
			return Conflict("native-history-install")
		}
		defer func() { _ = dir.Close() }()
		if err := dir.Sync(); err != nil {
			return Conflict("configuration-durability-uncertain")
		}
		parent, err := os.Open(profile)
		if err != nil {
			return Conflict("native-history-install")
		}
		defer func() { _ = parent.Close() }()
		if err := parent.Sync(); err != nil {
			return Conflict("configuration-durability-uncertain")
		}
		return RevalidateClaudeHistoryAlias(c, profile, proof)
	})
	return proof, err
}

func readClaudeHistoryInventory(profile string) (claudeHistoryInventory, error) {
	inventory := claudeHistoryInventory{SchemaVersion: 1, Aliases: map[string]ClaudeHistoryAlias{}}
	path := filepath.Join(profile, ClaudeHistoryMarker)
	raw, err := readRegular(path, maxSourceBytes)
	if errors.Is(err, os.ErrNotExist) {
		return inventory, nil
	}
	if err != nil || privateProjectionFile(path) != nil || json.Unmarshal(raw, &inventory) != nil || inventory.SchemaVersion != 1 || inventory.Aliases == nil || len(inventory.Aliases) > 4096 {
		return inventory, Conflict("invalid-native-history-inventory")
	}
	return inventory, nil
}

func refreshClaudeHistoryInventory(installed, next ClaudeContext, profile string) (claudeHistoryInventory, error) {
	inventory, err := readClaudeHistoryInventory(profile)
	if err != nil {
		return inventory, err
	}
	for key, proof := range inventory.Aliases {
		if key != proof.ProjectKey || (!ValidClaudeHistoryAlias(installed, proof) && !ValidClaudeHistoryAlias(next, proof)) || safePath(proof.SourcePath, true) != nil {
			return inventory, Conflict("invalid-native-history-inventory")
		}
		info, err := os.Lstat(proof.SourcePath)
		if err != nil || !claudeOwnedInfo(info, true) {
			return inventory, Conflict("unsafe-native-history")
		}
		stat := info.Sys().(*syscall.Stat_t)
		target, err := os.Readlink(filepath.Join(profile, "projects", key))
		if err != nil || target != proof.SourcePath || uint64(stat.Dev) != proof.Device || stat.Ino != proof.Inode {
			return inventory, Conflict("native-history-root-changed")
		}
		proof.GlobalGeneration = next.GlobalGeneration
		if !ValidClaudeHistoryAlias(next, proof) {
			return inventory, Conflict("invalid-native-history-context")
		}
		inventory.Aliases[key] = proof
	}
	return inventory, nil
}

func RevalidateClaudeHistoryAlias(c ClaudeContext, profile string, proof ClaudeHistoryAlias) error {
	if !ValidClaudeHistoryAlias(c, proof) {
		return Conflict("invalid-native-history-context")
	}
	if err := RevalidateClaudeContext(c, profile); err != nil {
		return err
	}
	inventory, err := readClaudeHistoryInventory(profile)
	if err != nil || inventory.Aliases[proof.ProjectKey] != proof {
		return Conflict("invalid-native-history-inventory")
	}
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
	target, err := os.Readlink(filepath.Join(profile, "projects", proof.ProjectKey))
	if err != nil || target != proof.SourcePath {
		return Conflict("native-history-destination-differs")
	}
	return nil
}

// ValidClaudeHistoryAlias checks intrinsic projection metadata only. Mutable
// filesystem custody is checked separately immediately before either spawn.
func ValidClaudeHistoryAlias(c ClaudeContext, proof ClaudeHistoryAlias) bool {
	key := proof.ProjectKey
	return validClaudeContext(c) && proof.GlobalGeneration == c.GlobalGeneration && key != "" && key != "." && key != ".." && filepath.Base(key) == key && filepath.IsLocal(key) && len(key) <= 255 && !strings.ContainsRune(key, 0) && proof.SourcePath == filepath.Join(c.SourceProfile.Canonical, "projects", key) && proof.Device != 0 && proof.Inode != 0
}
