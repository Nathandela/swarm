package loginmigration

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/logins"
)

func redactCredential(path string, raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if validJSON(raw) != nil || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, ErrUnsupported
	}
	var keys []string
	switch filepath.Base(path) {
	case "auth.json":
		keys = []string{"auth_mode", "tokens", "last_refresh", "OPENAI_API_KEY"}
	case ".credentials.json":
		keys = []string{"claudeAiOauth"}
	case ".claude.json":
		keys = []string{"oauthAccount", "userID"}
	default:
		return nil, ErrUnsupported
	}
	for _, key := range keys {
		delete(fields, key)
	}
	return json.Marshal(fields)
}
func verifyCleanup(state string, store *logins.Store, registry logins.Registry, plan migration) error {
	for _, item := range plan.Imports {
		found := false
		for _, login := range registry.Logins {
			if login.ID == item.Login.ID {
				if login.Provider != item.Login.Provider || login.Identity != item.Login.Identity {
					return ErrUnsupported
				}
				found = true
				break
			}
		}
		if !found {
			return ErrUnsupported
		}
		if err := store.VerifySaved(item.Login.ID); err != nil {
			return err
		}
	}
	for _, item := range plan.Rewrites {
		raw, err := readFile(filepath.Join(state, item.Path), maxJSON, true)
		if err != nil {
			return err
		}
		if digest(raw) != item.After {
			return ErrUnsupported
		}
	}
	for provider, sources := range plan.Contexts {
		var marker contextPlan
		home := providerHome(state, provider)
		if err := readJSON(filepath.Join(home, contextMarker), &marker); err != nil {
			return err
		}
		if marker.Provider != provider {
			return ErrUnsupported
		}
		if err := verifyConversations(home, provider, sources); err != nil {
			return err
		}
		for _, file := range marker.Files {
			if !localPath(file.Target) || file.Mode != 0600 && file.Mode != 0700 || !hexID(file.SHA256, 64) {
				return ErrUnsupported
			}
			raw, err := readFile(filepath.Join(home, file.Target), maxFile, true)
			if err != nil {
				return err
			}
			if digest(raw) != file.SHA256 {
				return ErrHistoryConflict
			}
			info, err := os.Stat(filepath.Join(home, file.Target))
			if err != nil || info.Mode().Perm() != file.Mode {
				return ErrHistoryConflict
			}
		}
	}
	return nil
}
