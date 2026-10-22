package loginmigration

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/persist"
)

func readMigration(state string) (migration, error) {
	var plan migration
	raw, err := readFile(filepath.Join(state, "logins", "migration.json"), maxJSON, true)
	if err != nil {
		return plan, err
	}
	if validJSON(raw) != nil {
		return plan, ErrUnsupported
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&plan) != nil {
		return plan, ErrUnsupported
	}
	return plan, validateMigration(state, plan)
}
func localPath(path string) bool {
	return filepath.IsLocal(path) && filepath.Clean(path) == path && path != "."
}
func profileFile(state, path string) bool {
	relative, err := filepath.Rel(state, path)
	if err != nil || !localPath(relative) || filepath.Join(state, relative) != path {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 4 || parts[0] != "accounts" || parts[1] != "profiles" || !hexID(parts[2], 32) {
		return false
	}
	return parts[3] == "auth.json" || parts[3] == ".credentials.json" || parts[3] == ".claude.json"
}
func metadataPath(path string) bool {
	parts := strings.Split(path, string(filepath.Separator))
	return path == "auth-watch-state.json" || len(parts) == 2 && persist.ValidID(parts[0]) && parts[1] == "meta.json"
}
func proofPath(path string) bool {
	parts := strings.Split(path, string(filepath.Separator))
	return len(parts) == 2 && persist.ValidID(parts[0]) && (parts[1] == "native-process.json" || parts[1] == "native-writers-stopped.json")
}
func validateMigration(state string, plan migration) error {
	if plan.SchemaVersion != 1 || (plan.Phase != "planned" && plan.Phase != "cleanup" && plan.Phase != "complete") {
		return ErrUnsupported
	}
	if len(plan.Imports) > 100000 || len(plan.Rewrites)+len(plan.Remove)+len(plan.Secrets) > 300000 || len(plan.Contexts) > 2 {
		return ErrUnsupported
	}
	secrets := map[string]bool{}
	ids := map[string]bool{}
	for _, item := range plan.Imports {
		l := item.Login
		if !hexID(l.ID, 32) || ids[l.ID] || (l.Provider != "claude" && l.Provider != "codex") || !hexID(l.Identity, 64) || !profileFile(state, item.AuthPath) {
			return ErrUnsupported
		}
		ids[l.ID] = true
		expected := "auth.json"
		if l.Provider == "claude" {
			expected = ".credentials.json"
		}
		if filepath.Base(item.AuthPath) != expected {
			return ErrUnsupported
		}
		if item.IdentityPath != "" && (l.Provider != "claude" || item.IdentityPath != filepath.Join(filepath.Dir(item.AuthPath), ".claude.json")) {
			return ErrUnsupported
		}
		secrets[item.AuthPath] = true
		if item.IdentityPath != "" {
			secrets[item.IdentityPath] = true
		}
	}
	for provider, sources := range plan.Contexts {
		if provider != "claude" && provider != "codex" || len(sources) > 100000 {
			return ErrUnsupported
		}
		for _, source := range sources {
			if !filepath.IsAbs(source.Home) || filepath.Clean(source.Home) != source.Home || len(source.Home) > 4096 {
				return ErrUnsupported
			}
			if source.Conversation != "" && uuidRE.FindString(source.Conversation) != source.Conversation {
				return ErrUnsupported
			}
			if source.Cwd != "" && (!filepath.IsAbs(source.Cwd) || filepath.Clean(source.Cwd) != source.Cwd) {
				return ErrUnsupported
			}
		}
	}
	seen := map[string]bool{}
	for _, item := range plan.Rewrites {
		if !localPath(item.Path) || !metadataPath(item.Path) || seen[item.Path] || !hexID(item.Before, 64) || !hexID(item.After, 64) || item.Stage != filepath.Join("logins", "migration-stage", digest([]byte(item.Path))+".json") {
			return ErrUnsupported
		}
		seen[item.Path] = true
	}
	for _, item := range plan.Secrets {
		if !localPath(item.Path) || !secrets[filepath.Join(state, item.Path)] || seen[item.Path] || !hexID(item.Before, 64) || !hexID(item.After, 64) || item.Stage != filepath.Join("logins", "migration-stage", digest([]byte("credential-cleanup:"+item.Path))+".json") {
			return ErrUnsupported
		}
		seen[item.Path] = true
	}
	for _, item := range plan.Remove {
		if !localPath(item.Path) || !proofPath(item.Path) || seen[item.Path] || !hexID(item.Before, 64) || item.Stage != "" || item.After != "" {
			return ErrUnsupported
		}
		seen[item.Path] = true
	}
	return nil
}
