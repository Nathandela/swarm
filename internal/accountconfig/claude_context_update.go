package accountconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
)

const ClaudeContextUpdateMarker = ".swarm-claude-context-update.json"

// The intent carries hashes/provenance only. It permits retry after either
// ordinary file write, without treating unrelated private native edits as ours.
type claudeContextUpdate struct {
	SchemaVersion  int
	Previous, Next ClaudeContext
}

func readClaudeContextUpdate(profile string) (*claudeContextUpdate, error) {
	path := filepath.Join(profile, ClaudeContextUpdateMarker)
	raw, err := readRegular(path, maxSourceBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var update claudeContextUpdate
	if err != nil || privateProjectionFile(path) != nil || json.Unmarshal(raw, &update) != nil || update.SchemaVersion != 1 || !validClaudeContext(update.Previous) || !validClaudeContext(update.Next) || !reflect.DeepEqual(update.Previous.SourceProfile, update.Next.SourceProfile) || update.Previous.GlobalGeneration == update.Next.GlobalGeneration {
		return nil, Conflict("invalid-native-configuration-update")
	}
	return &update, nil
}

func validateClaudePartialUpdate(update claudeContextUpdate, profile string) error {
	raw, err := readRegular(filepath.Join(profile, "settings.json"), maxSourceBytes)
	if err != nil {
		return Conflict("candidate-configuration-changed")
	}
	hash := claudeHashBytes(raw)
	if hash != update.Previous.SettingsSHA256 && hash != update.Next.SettingsSHA256 {
		return Conflict("candidate-configuration-changed")
	}
	prefs, err := readClaudePreferences(filepath.Join(profile, ".claude.json"))
	if err != nil {
		return err
	}
	hash = claudeHash(claudeStablePreferences(prefs))
	if hash != update.Previous.PreferencesStableSHA256 && hash != update.Next.PreferencesStableSHA256 {
		return Conflict("candidate-configuration-changed")
	}
	for i, asset := range update.Next.Assets {
		path := filepath.Join(profile, asset.Name)
		if target, err := os.Readlink(path); err == nil {
			if target != asset.Target || asset.SHA256 == "absent" && !claudeRemovedAsset(update.Previous, update.Next, i, target) {
				return Conflict("candidate-customization-not-context-alias")
			}
		} else if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) || asset.SHA256 != "absent" && update.Previous.Assets[i].SHA256 != "absent" {
			return Conflict("candidate-customization-not-context-alias")
		}
	}
	return nil
}

func claudeRemovedAsset(previous, next ClaudeContext, index int, target string) bool {
	old, new := previous.Assets[index], next.Assets[index]
	return new.SHA256 == "absent" && old.SHA256 != "absent" && old.Name == new.Name && old.Target == new.Target && target == old.Target
}

func removeClaudeContextUpdate(profile string) error {
	if err := os.Remove(filepath.Join(profile, ClaudeContextUpdateMarker)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Conflict("configuration-install")
	}
	dir, err := os.Open(profile)
	if err != nil {
		return Conflict("configuration-install")
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return Conflict("configuration-durability-uncertain")
	}
	return nil
}

func writeClaudeContextUpdate(profile string, update claudeContextUpdate) error {
	raw, err := json.Marshal(update)
	if err != nil {
		return Conflict("configuration-install")
	}
	if err := writeClaudeContextFile(profile, ClaudeContextUpdateMarker, raw); err != nil {
		return err
	}
	dir, err := os.Open(profile)
	if err != nil {
		return Conflict("configuration-install")
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return Conflict("configuration-durability-uncertain")
	}
	return nil
}
