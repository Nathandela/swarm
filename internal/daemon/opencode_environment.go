package daemon

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Nathandela/swarm/internal/persist"
)

// Preserve refusal across daemon restart without saving opaque config or credentials.
const openCodeUnsupportedOrigin = "SWARM_OPENCODE_UNSUPPORTED_ORIGIN=1"

func openCodeBoolean(key string) bool {
	switch key {
	case "OPENCODE_PURE", "OPENCODE_DISABLE_AUTOUPDATE", "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_DISABLE_DEFAULT_PLUGINS", "OPENCODE_DISABLE_MODELS_FETCH", "OPENCODE_DISABLE_TERMINAL_TITLE":
		return true
	default:
		return false
	}
}

func openCodeSavedEnvironment(env []string) []string {
	var out []string
	unsupported := false
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key == "HOME" || key == "XDG_CONFIG_HOME" {
			continue
		} // already saved by the ordinary/native allowlists
		if persist.OpenCodeProfileKey(key) || openCodeBoolean(key) {
			out = append(out, entry)
		} else if strings.HasPrefix(key, "OPENCODE_") || entry == openCodeUnsupportedOrigin {
			unsupported = true
		}
	}
	if unsupported {
		out = append(out, openCodeUnsupportedOrigin)
	}
	return out
}

// OpenCodeEnvironment preserves supported native sources before the ordinary child allowlist runs.
func (d *Daemon) OpenCodeEnvironment(env []string) ([]string, error) {
	if env == nil {
		if len(d.savedEnv) > 0 {
			env = d.savedEnv
		} else {
			env = daemonEnviron()
		}
	}
	out := make([]string, 0)
	seen := map[string]bool{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if entry == openCodeUnsupportedOrigin {
			return nil, errors.New("OpenCode runtime: unsupported native configuration origin")
		}
		profile, flag := persist.OpenCodeProfileKey(key), openCodeBoolean(key)
		if !profile && !flag {
			if strings.HasPrefix(key, "OPENCODE_") {
				return nil, errors.New("OpenCode runtime: unsupported native configuration selector")
			}
			continue
		}
		if !ok || seen[key] || len(value) > 4096 || strings.ContainsFunc(value, unicode.IsControl) {
			return nil, errors.New("OpenCode runtime: ambiguous native configuration origin")
		}
		seen[key] = true
		if key == "OPENCODE_TUI_CONFIG" {
			return nil, errors.New("OpenCode runtime: custom TUI configuration requires an explicit integration")
		}
		if profile && (!filepath.IsAbs(value) || filepath.Clean(value) != value) {
			return nil, errors.New("OpenCode runtime: native configuration paths must be canonical absolute paths")
		}
		if flag {
			if value != "true" && value != "false" && value != "0" && value != "1" {
				return nil, errors.New("OpenCode runtime: unsupported native boolean selector")
			}
			if key == "OPENCODE_PURE" && (value == "true" || value == "1") {
				return nil, errors.New("OpenCode runtime: pure mode disables the required native bridge")
			}
		}
		out = append(out, entry)
	}
	return out, nil
}
