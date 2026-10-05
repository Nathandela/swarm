package accounts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Known ordinary configuration aliases are retained during credential erasure.
// Do not follow the alias or touch its owner-global target. Every other alias
// refuses the entire inventory before any credential is removed.
func validNativeContextAssetAlias(profile *os.Root, provider, name string) bool {
	names := []string{"skills", "agents", "commands", "plugins", "CLAUDE.md"}
	marker := ".swarm-claude-context.json"
	if provider == ProviderCodex {
		names = []string{"skills", "rules", "hooks.json", "AGENTS.md", "plugins", ".tmp/marketplaces"}
		marker = ".swarm-codex-context.json"
	}
	known := false
	for _, expected := range names {
		if name == expected {
			known = true
		}
	}
	if !known {
		return false
	}
	raw, err := readPrivate(profile, marker, 2<<20)
	if err != nil {
		return false
	}
	var context struct {
		GlobalGeneration string
		SourceProfile    struct{ Path, Canonical string }
		Assets           []struct{ Name, Target, SHA256 string }
	}
	if json.Unmarshal(raw, &context) != nil || len(context.GlobalGeneration) != 64 || len(context.Assets) != len(names) || !filepath.IsAbs(context.SourceProfile.Canonical) || filepath.Clean(context.SourceProfile.Canonical) != context.SourceProfile.Canonical {
		return false
	}
	prefix := []byte(`{"GlobalGeneration":"` + context.GlobalGeneration + `",`)
	if !bytes.HasPrefix(raw, prefix) {
		return false
	}
	empty := append([]byte(`{"GlobalGeneration":"",`), raw[len(prefix):]...)
	sum := sha256.Sum256(empty)
	if hex.EncodeToString(sum[:]) != context.GlobalGeneration {
		return false
	}
	for i, asset := range context.Assets {
		if asset.Name != names[i] || asset.Target != filepath.Join(context.SourceProfile.Canonical, asset.Name) {
			return false
		}
	}
	for _, asset := range context.Assets {
		if asset.Name == name {
			if asset.SHA256 == "absent" {
				return false
			}
			actual, err := profile.Readlink(name)
			return err == nil && actual == asset.Target
		}
	}
	return false
}

func validCodexContextConfiguration(profile *os.Root, raw []byte) bool {
	marker, err := readPrivate(profile, ".swarm-codex-context.json", 2<<20)
	if err != nil {
		return false
	}
	var context struct {
		GlobalGeneration, SettingsSHA256 string
		NativeProfile                    string
		SourceProfile                    struct{ Path, Canonical string }
	}
	if json.Unmarshal(marker, &context) != nil || len(context.GlobalGeneration) != 64 {
		return false
	}
	prefix := []byte(`{"GlobalGeneration":"` + context.GlobalGeneration + `",`)
	if !bytes.HasPrefix(marker, prefix) {
		return false
	}
	empty := append([]byte(`{"GlobalGeneration":"",`), marker[len(prefix):]...)
	sum := sha256.Sum256(empty)
	if !filepath.IsAbs(context.SourceProfile.Canonical) || filepath.Clean(context.SourceProfile.Canonical) != context.SourceProfile.Canonical {
		return false
	}
	if (context.NativeProfile != context.SourceProfile.Path && context.NativeProfile != context.SourceProfile.Canonical) || !filepath.IsAbs(context.NativeProfile) || filepath.Clean(context.NativeProfile) != context.NativeProfile {
		return false
	}
	var parsed map[string]any
	if toml.Unmarshal(raw, &parsed) != nil {
		return false
	}
	if hooks, ok := parsed["hooks"].(map[string]any); ok {
		if stateValue, present := hooks["state"]; present {
			state, ok := stateValue.(map[string]any)
			if !ok {
				return false
			}
			originalState := map[string]any{}
			for key, value := range state {
				replacement := key
				for _, name := range []string{"config.toml", "hooks.json"} {
					original := filepath.Join(context.NativeProfile, name) + ":"
					private := filepath.Join(profile.Name(), name) + ":"
					if strings.HasPrefix(key, original) && context.NativeProfile != profile.Name() {
						return false
					}
					if suffix, ok := strings.CutPrefix(key, private); ok {
						replacement = original + suffix
						break
					}
				}
				if _, exists := originalState[replacement]; exists {
					return false
				}
				originalState[replacement] = value
			}
			hooks["state"] = originalState
		}
	}
	normalized, err := toml.Marshal(parsed)
	if err != nil {
		return false
	}
	settings := sha256.Sum256(normalized)
	return hex.EncodeToString(sum[:]) == context.GlobalGeneration && hex.EncodeToString(settings[:]) == context.SettingsSHA256
}

func validCodexMarketplaceInventory(profile *os.Root) bool {
	directory, err := profile.Open(".tmp")
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(4097)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 4096 {
		return false
	}
	for _, entry := range entries {
		info, err := profile.Lstat(filepath.Join(".tmp", entry.Name()))
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 && !validNativeContextAssetAlias(profile, ProviderCodex, filepath.Join(".tmp", entry.Name())) {
			return false
		}
	}
	return true
}

func validClaudeHistoryAliases(profile *os.Root) bool {
	directory, err := profile.Open("projects")
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(4097)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 4096 {
		return false
	}
	var aliases []string
	for _, entry := range entries {
		info, err := profile.Lstat(filepath.Join("projects", entry.Name()))
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			aliases = append(aliases, entry.Name())
		}
	}
	if len(aliases) == 0 {
		return true
	}
	raw, err := readPrivate(profile, ".swarm-claude-history.json", 2<<20)
	if err != nil {
		return false
	}
	var history struct {
		SchemaVersion int
		Aliases       map[string]struct {
			GlobalGeneration, ProjectKey, SourcePath string
			Device, Inode                            uint64
		}
	}
	if json.Unmarshal(raw, &history) != nil || history.SchemaVersion != 1 || history.Aliases == nil || len(history.Aliases) > 4096 {
		return false
	}
	marker, err := readPrivate(profile, ".swarm-claude-context.json", 2<<20)
	if err != nil {
		return false
	}
	var context struct {
		GlobalGeneration string
		SourceProfile    struct{ Canonical string }
	}
	if json.Unmarshal(marker, &context) != nil || len(context.GlobalGeneration) != 64 || !filepath.IsAbs(context.SourceProfile.Canonical) || filepath.Clean(context.SourceProfile.Canonical) != context.SourceProfile.Canonical {
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
	for key, proof := range history.Aliases {
		if key == "" || key == "." || key == ".." || filepath.Base(key) != key || !filepath.IsLocal(key) || proof.ProjectKey != key || proof.GlobalGeneration != context.GlobalGeneration || proof.Device == 0 || proof.Inode == 0 || proof.SourcePath != filepath.Join(context.SourceProfile.Canonical, "projects", key) {
			return false
		}
	}
	for _, name := range aliases {
		proof, ok := history.Aliases[name]
		if !ok {
			return false
		}
		target, err := profile.Readlink(filepath.Join("projects", name))
		if err != nil || target != proof.SourcePath {
			return false
		}
	}
	return true
}
