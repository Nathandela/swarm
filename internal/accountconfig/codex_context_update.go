package accountconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
)

const CodexContextUpdateMarker = ".swarm-codex-context-update.json"

// Only provenance and digests are journaled. A retry accepts the exact known
// old/new states, never an unrelated write made by the native client.
type codexContextUpdate struct {
	SchemaVersion  int
	Previous, Next CodexContext
}

func sameCodexContextCustody(a, b CodexContext) bool {
	return reflect.DeepEqual(a.SourceProfile, b.SourceProfile) && a.SourceDirectorySHA256 == b.SourceDirectorySHA256 && a.NativeProfile == b.NativeProfile && a.NativeHome == b.NativeHome
}

func readCodexContextUpdate(profile string) (*codexContextUpdate, error) {
	path := filepath.Join(profile, CodexContextUpdateMarker)
	raw, err := readRegular(path, maxSourceBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var u codexContextUpdate
	if err != nil || privateProjectionFile(path) != nil || json.Unmarshal(raw, &u) != nil || u.SchemaVersion != 1 || !validCodexContext(u.Previous) || !validCodexContext(u.Next) || !sameCodexContextCustody(u.Previous, u.Next) || u.Previous.GlobalGeneration == u.Next.GlobalGeneration {
		return nil, Conflict("invalid-native-configuration-update")
	}
	return &u, nil
}

func syncCodexContextDirectory(profile string) error {
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

func writeCodexContextUpdate(profile string, u codexContextUpdate) error {
	raw, err := json.Marshal(u)
	if err != nil {
		return Conflict("configuration-install")
	}
	if err := writeClaudeContextFile(profile, CodexContextUpdateMarker, raw); err != nil {
		return err
	}
	return syncCodexContextDirectory(profile)
}

func removeCodexContextUpdate(profile string) error {
	if err := os.Remove(filepath.Join(profile, CodexContextUpdateMarker)); err != nil {
		return Conflict("configuration-install")
	}
	return syncCodexContextDirectory(profile)
}

func validateCodexPartialUpdate(u codexContextUpdate, profile string) error {
	if validateCodexInstalledConfig(u.Previous, profile) != nil && validateCodexInstalledConfig(u.Next, profile) != nil {
		return Conflict("candidate-configuration-changed")
	}
	for i, asset := range u.Next.Assets {
		path := filepath.Join(profile, asset.Name)
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && (asset.SHA256 == "absent" || u.Previous.Assets[i].SHA256 == "absent") {
			continue
		}
		target, err := os.Readlink(path)
		if err != nil || target != asset.Target || (asset.SHA256 == "absent" && u.Previous.Assets[i].SHA256 == "absent") {
			return Conflict("candidate-customization-not-context-alias")
		}
	}
	return nil
}

// Read-only prevalidation. Refresh does not create or replace history; it only
// rebases an existing proven generation while preserving source inode custody.
func refreshCodexHistoryProof(previous, next CodexContext, profile string) (*CodexHistoryAlias, error) {
	path := filepath.Join(profile, CodexHistoryMarker)
	raw, err := readRegular(path, maxSourceBytes)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(filepath.Join(profile, "sessions")); !errors.Is(err, os.ErrNotExist) {
			return nil, Conflict("invalid-native-history-inventory")
		}
		return nil, nil
	}
	var proof CodexHistoryAlias
	if err != nil || privateProjectionFile(path) != nil || json.Unmarshal(raw, &proof) != nil || (!ValidCodexHistoryAlias(previous, proof) && !ValidCodexHistoryAlias(next, proof)) {
		return nil, Conflict("invalid-native-history-inventory")
	}
	if err := validateCodexHistoryIdentity(profile, proof); err != nil {
		return nil, err
	}
	proof.GlobalGeneration = next.GlobalGeneration
	return &proof, nil
}
