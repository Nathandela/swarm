package logins

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/pelletier/go-toml/v2"
)

// ValidateProjectAuth leaves native project settings in place while refusing
// selectors that would move authentication outside the chosen native authority.
func ValidateProjectAuth(provider, cwd string) error {
	if !validProvider(provider) || !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd {
		return ErrIneligible
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		names := []string{filepath.Join(dir, ".codex", "config.toml")}
		if provider == ProviderClaude {
			names = []string{filepath.Join(dir, ".claude", "settings.json"), filepath.Join(dir, ".claude", "settings.local.json")}
		}
		for _, name := range names {
			b, e := readAuthConfig(name)
			if e != nil {
				return e
			}
			if len(b) > 0 {
				if e = ValidateAuthSettings(provider, b); e != nil {
					return e
				}
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if provider == ProviderClaude {
		dirs := []string{"/etc/claude-code"}
		if runtime.GOOS == "darwin" {
			dirs = []string{"/private/etc/claude-code", "/Library/Application Support/ClaudeCode"}
		}
		for _, dir := range dirs {
			b, e := readAuthConfig(filepath.Join(dir, "managed-settings.json"))
			if e != nil {
				return e
			}
			if len(b) > 0 {
				if e = ValidateAuthSettings(provider, b); e != nil {
					return e
				}
			}
			entries, e := os.ReadDir(filepath.Join(dir, "managed-settings.d"))
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				return ErrIneligible
			}
			for _, entry := range entries {
				if !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				b, e = readAuthConfig(filepath.Join(dir, "managed-settings.d", entry.Name()))
				if e != nil {
					return e
				}
				if e = ValidateAuthSettings(provider, b); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

func readAuthConfig(path string) ([]byte, error) {
	before, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil || !before.Mode().IsRegular() || checkAbsolute(path, false) != nil {
		return nil, ErrUnsafePath
	}
	parent, e := os.OpenRoot(filepath.Dir(path))
	if e != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = parent.Close() }()
	f, e := parent.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = f.Close() }()
	opened, e := f.Stat()
	if e != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() > maxCredentialBytes {
		return nil, ErrUnsafePath
	}
	b, e := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if e != nil || len(b) > maxCredentialBytes {
		return nil, ErrUnsafePath
	}
	after, e := parent.Lstat(filepath.Base(path))
	if e != nil || !os.SameFile(after, opened) {
		return nil, ErrUnsafePath
	}
	return b, nil
}

// ValidateAuthSettings checks only authentication selectors; native settings,
// hooks, model options and unrelated tool configuration remain native-owned.
func ValidateAuthSettings(provider string, raw []byte) error {
	if !validProvider(provider) || len(raw) > maxCredentialBytes {
		return ErrIneligible
	}
	if provider == ProviderCodex {
		var m map[string]any
		if toml.Unmarshal(raw, &m) != nil || codexCompeting(m) {
			return ErrIneligible
		}
		if mode, ok := m["cli_auth_credentials_store"]; ok && mode != "file" {
			return ErrIneligible
		}
		return nil
	}
	var doc map[string]json.RawMessage
	if validateJSON(raw) != nil || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return ErrIneligible
	}
	for _, key := range []string{"apiKeyHelper", "primaryApiKey", "awsAuthRefresh", "awsCredentialExport", "forceLoginMethod", "forceLoginOrgUUID"} {
		if value, ok := doc[key]; ok && string(value) != "null" && string(value) != `""` {
			return ErrIneligible
		}
	}
	if value, ok := doc["env"]; ok {
		var env map[string]json.RawMessage
		if json.Unmarshal(value, &env) != nil {
			return ErrIneligible
		}
		for key := range env {
			if competingEnvironment(key) {
				return ErrIneligible
			}
		}
	}
	return nil
}

// ValidateArgsAuth accepts the house model/hook/sandbox arguments and rejects
// native command overrides that could select another credential store or host.
func ValidateArgsAuth(provider string, argv []string) error {
	if !validProvider(provider) {
		return ErrIneligible
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		if provider == ProviderCodex {
			if arg == "--profile" || strings.HasPrefix(arg, "--profile=") || strings.HasPrefix(arg, "-p") || arg == "--oss" || strings.HasPrefix(arg, "--local-provider") || strings.HasPrefix(arg, "--remote") {
				return ErrIneligible
			}
			value := ""
			switch {
			case arg == "-c" || arg == "--config":
				i++
				if i >= len(argv) {
					return ErrIneligible
				}
				value = argv[i]
			case strings.HasPrefix(arg, "--config="):
				value = strings.TrimPrefix(arg, "--config=")
			case strings.HasPrefix(arg, "-c") && len(arg) > 2:
				value = arg[2:]
			default:
				continue
			}
			key, rawValue, ok := strings.Cut(value, "=")
			if !ok {
				return ErrIneligible
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return ErrIneligible
			}
			if key == "profile" || strings.HasPrefix(key, "profiles.") || strings.HasPrefix(key, "model_providers.") {
				return ErrIneligible
			}
			// Codex parses the value alone, falling back to an unquoted string.
			// See utils/cli/src/config_override.rs at rust-v0.163.0.
			var parsed map[string]any
			var setting any
			rawValue = strings.TrimSpace(rawValue)
			if toml.Unmarshal([]byte("_x_ = "+rawValue), &parsed) == nil {
				setting = parsed["_x_"]
			} else {
				setting = strings.Trim(rawValue, "\"'")
			}
			parts := strings.Split(key, ".")
			settings := map[string]any{}
			current := settings
			for _, part := range parts[:len(parts)-1] {
				if part == "" {
					return ErrIneligible
				}
				next := map[string]any{}
				current[part], current = next, next
			}
			if parts[len(parts)-1] == "" {
				return ErrIneligible
			}
			current[parts[len(parts)-1]] = setting
			if codexCompeting(settings) {
				return ErrIneligible
			}
		} else if arg == "--settings" || strings.HasPrefix(arg, "--settings=") {
			value, joined := strings.CutPrefix(arg, "--settings=")
			if !joined {
				i++
				if i >= len(argv) {
					return ErrIneligible
				}
				value = argv[i]
			}
			raw := []byte(value)
			if !strings.HasPrefix(strings.TrimSpace(value), "{") {
				var e error
				raw, e = readAuthConfig(value)
				if e != nil || len(raw) == 0 {
					return ErrIneligible
				}
			}
			if e := ValidateAuthSettings(provider, raw); e != nil {
				return e
			}
		}
	}
	return nil
}
