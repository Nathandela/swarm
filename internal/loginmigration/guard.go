package loginmigration

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
)

var environmentWriters = processcontain.EnvironmentWriters

// Writer custody is re-established on every unfinished run, including cleanup.
// Login-only state has the same credential hazard as retained conversations.
func revalidateMigrationWriters(state string, plan *migration) error {
	providers := map[string]bool{}
	homes := []string{}
	sessions := []string{}
	var registry oldRegistry
	if err := readJSON(filepath.Join(state, "accounts", "registry.json"), &registry); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, account := range registry.Accounts {
		if account.Provider != "claude" && account.Provider != "codex" {
			return ErrUnsupported
		}
		providers[account.Provider] = true
		for _, generation := range account.Generations {
			if !hexID(generation.Profile, 32) {
				return ErrUnsupported
			}
			homes = append(homes, filepath.Join(state, "accounts", "profiles", generation.Profile))
		}
	}
	if plan != nil {
		for _, item := range plan.Imports {
			providers[item.Login.Provider] = true
			homes = append(homes, filepath.Dir(item.AuthPath))
		}
		for provider, sources := range plan.Contexts {
			providers[provider] = true
			homes = append(homes, providerHome(state, provider))
			for _, source := range sources {
				homes = append(homes, source.Home)
			}
		}
	}
	// Worker inventory covers registry entries and orphan job directories, even
	// when their sign-in never admitted an account to the registry.
	workerProviders, workerHomes, err := inventoryEnrollmentWorkers(state)
	if err != nil {
		return err
	}
	for provider := range workerProviders {
		providers[provider] = true
	}
	homes = append(homes, workerHomes...)
	if err = inventoryCheckWorkers(state); err != nil {
		return err
	}
	if len(providers) == 0 {
		return nil
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !persist.ValidID(entry.Name()) {
			continue
		}
		path := filepath.Join(state, entry.Name(), "meta.json")
		raw, readErr := readFile(path, maxJSON, true)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		var meta persist.Meta
		if validJSON(raw) != nil || json.Unmarshal(raw, &meta) != nil {
			return ErrUnsupported
		}
		if !providers[meta.AgentType] {
			continue
		}
		if meta.ID != entry.Name() {
			return ErrUnsupported
		}
		if live(meta.ShimPID, meta.ShimStartTime) {
			return ErrLiveWriter
		}
		if err = inventoryProof(state, meta.ID, &migration{}); err != nil {
			return err
		}
		sessions = append(sessions, meta.ID)
		home, resolveErr := sourceHome(meta.AgentType, meta.Env)
		if resolveErr != nil {
			return resolveErr
		}
		homes = append(homes, home)
	}
	for provider := range providers {
		if err = checkExternalWriters(provider); err != nil {
			return err
		}
	}
	// Native environment paths may spell either the logical alias or its target.
	for _, home := range homes {
		canonical, resolveErr := filepath.EvalSymlinks(home)
		if resolveErr != nil && !errors.Is(resolveErr, os.ErrNotExist) {
			return resolveErr
		}
		if resolveErr == nil && canonical != home {
			homes = append(homes, canonical)
		}
	}
	writers, err := environmentWriters(homes, sessions)
	if err != nil || len(writers) != 0 {
		return ErrLiveWriter
	}
	return nil
}
