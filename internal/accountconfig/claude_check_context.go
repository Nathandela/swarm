package accountconfig

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// PrepareRestrictedClaudeConfig copies selected cached account metadata only.
// It never copies credentials, hooks, plugins, MCP, trust, API keys or owner
// configuration. The exact credential generation remains the secure store.
func PrepareRestrictedClaudeConfig(credentialProfile, configProfile string) error {
	if err := privateDir(credentialProfile); err != nil {
		return err
	}
	if err := privateDir(configProfile); err != nil {
		return err
	}
	if credentialProfile == configProfile {
		return Conflict("check-context-is-credential-profile")
	}
	selected, err := readClaudeContextState(filepath.Join(credentialProfile, ".claude.json"))
	if err != nil {
		return err
	}
	metadata := map[string]any{}
	for _, key := range []string{"oauthAccount", "userID", "hasCompletedOnboarding"} {
		if value, ok := selected[key]; ok {
			metadata[key] = value
		}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return Conflict("invalid-native-trust-config")
	}
	if err := writeClaudeContextFile(configProfile, "settings.json", []byte("{}")); err != nil {
		return err
	}
	return writeClaudeContextFile(configProfile, ".claude.json", raw)
}

func ValidateRestrictedClaudeCheck(stateRoot, credentialProfile, configProfile, cwd string, env []string) error {
	if err := privateDir(credentialProfile); err != nil {
		return err
	}
	if filepath.Dir(credentialProfile) != filepath.Join(stateRoot, "accounts", "profiles") {
		return Conflict("availability-profile-outside-account-store")
	}
	actual := false
	checked := append([]string(nil), env...)
	for i, entry := range checked {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return Conflict("availability-invalid-environment")
		}
		if key == "CLAUDE_SECURESTORAGE_CONFIG_DIR" {
			if actual || value != credentialProfile {
				return Conflict("availability-alternate-profile")
			}
			actual = true
			checked[i] = key + "=" + configProfile
		}
	}
	if !actual {
		return Conflict("availability-missing-selected-profile")
	}
	return ValidateAvailabilityCheck(stateRoot, configProfile, cwd, checked)
}
