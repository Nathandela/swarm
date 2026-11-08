package daemon

import (
	"strings"

	"github.com/Nathandela/swarm/internal/persist"
)

func managerOriginMarker(agent string) string {
	return "SWARM_MANAGER_" + strings.ToUpper(agent) + "_UNSUPPORTED_ORIGIN=1"
}

// ManagerNativeEnvironmentSupported refuses credential/backend substitutions
// before ordinary launch filtering can erase their evidence.
func ManagerNativeEnvironmentSupported(agent string, env []string) bool {
	if agent != "codex" && agent != "claude" {
		return false
	}
	seen := map[string]bool{}
	marker, _, _ := strings.Cut(managerOriginMarker(agent), "=")
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if key == marker {
			return false
		}
		relevant, boolean := key == "HOME", false
		if agent == "claude" {
			if key == "CLAUDE_CONFIG_DIR" {
				relevant = true
			} else if strings.HasPrefix(key, "CLAUDE_CODE_USE_") {
				relevant = true
				boolean = true
			} else {
				switch key {
				case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_PROFILE", "ANTHROPIC_CONFIG_DIR", "AWS_BEARER_TOKEN_BEDROCK", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_ACCOUNT_UUID", "CLAUDE_CODE_API_KEY", "CLAUDE_CODE_API_KEY_HELPER_TTL_MS", "CLAUDE_CODE_PROFILE", "CLAUDE_CODE_DEFAULT_PROFILE", "CLAUDE_CODE_FEDERATION_PROFILE", "CLAUDE_SECURESTORAGE_CONFIG_DIR":
					relevant = true
				}
				relevant = relevant || strings.HasPrefix(key, "CLAUDE_CODE_HOST_") || strings.HasPrefix(key, "CLAUDE_CODE_GATEWAY_")
			}
		} else {
			switch key {
			case "CODEX_HOME", "CODEX_AUTH_JSON", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_ORG_ID", "OPENAI_ORGANIZATION", "OPENAI_PROJECT_ID", "OPENAI_PROJECT", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CODEX_API_ENDPOINT", "CODEX_PROFILE", "CODEX_REFRESH_TOKEN_URL_OVERRIDE", "CODEX_REVOKE_TOKEN_URL_OVERRIDE", "CODEX_CLI_AUTH_CREDENTIALS_STORE":
				relevant = true
			case "CODEX_DISABLE_KEYRING":
				relevant = true
				boolean = true
			}
			relevant = relevant || strings.HasPrefix(key, "CODEX_AUTH_") || strings.HasPrefix(key, "CODEX_REMOTE_")
		}
		if !relevant {
			continue
		}
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		if key == "HOME" || agent == "claude" && key == "CLAUDE_CONFIG_DIR" {
			continue
		}
		if value != "" && (!boolean || value != "false" && value != "0") {
			return false
		}
	}
	return true
}

// ManagerLaunchEnvironment retains safe native selection and refusal evidence,
// without carrying unsupported credential values into private request custody.
func ManagerLaunchEnvironment(agent string, env []string) []string {
	if env == nil {
		return nil
	}
	out := make([]string, 0)
	for _, entry := range persist.FilterEnv(env) {
		key, _, _ := strings.Cut(entry, "=")
		if key != "OPENAI_API_KEY" && key != "ANTHROPIC_API_KEY" {
			out = append(out, entry)
		}
	}
	if agent == "claude" {
		for _, entry := range env {
			if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
				out = append(out, entry)
			}
		}
	}
	if !ManagerNativeEnvironmentSupported(agent, env) {
		out = append(out, managerOriginMarker(agent))
	}
	return out
}

func (d *Daemon) ManagerEnvironment(agent string, env []string) []string {
	if env == nil {
		env = d.savedEnv
		if len(env) == 0 {
			env = daemonEnviron()
		}
	}
	return ManagerLaunchEnvironment(agent, env)
}
