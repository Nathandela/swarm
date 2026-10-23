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
	return validateProjectAuth(provider, cwd, "")
}

// ValidateProjectAuthEnv recognizes only the owner's native home directory alias
// selected by this launch's HOME. Project and managed configuration remain strict.
func ValidateProjectAuthEnv(provider, cwd string, env []string) error {
	home := ""
	seen := false
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if key != "HOME" {
			continue
		}
		if seen || !ok || value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, '\x00') {
			return ErrIneligible
		}
		home, seen = value, true
	}
	if home == "" {
		return ValidateProjectAuth(provider, cwd)
	}
	name := ".codex"
	if provider == ProviderClaude {
		name = ".claude"
	}
	return validateProjectAuth(provider, cwd, filepath.Join(home, name))
}

func validateProjectAuth(provider, cwd, nativeHome string) error {
	if !validProvider(provider) || !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd {
		return ErrIneligible
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		names := []string{filepath.Join(dir, ".codex", "config.toml")}
		if provider == ProviderClaude {
			names = []string{filepath.Join(dir, ".claude", "settings.json"), filepath.Join(dir, ".claude", "settings.local.json")}
		}
		for _, name := range names {
			b, e := readProjectAuthConfig(name, nativeHome)
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
	return readAuthConfigFile(parent, filepath.Base(path), before, false)
}

func nativeOwned(info os.FileInfo, directory bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode()&os.ModeSymlink == 0
	}
	return info.Mode().IsRegular() && st.Nlink == 1
}

func nativeLinkOwned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode()&os.ModeSymlink != 0 && st.Uid == uint32(os.Getuid()) && st.Nlink == 1
}

func nativeAliasUnchanged(alias string, aliasInfo os.FileInfo, target string, targetInfo os.FileInfo) bool {
	current, e := os.Lstat(alias)
	if e != nil || !nativeLinkOwned(current) || !os.SameFile(current, aliasInfo) {
		return false
	}
	home, e := os.Lstat(filepath.Dir(alias))
	if e != nil || !nativeOwned(home, true) || checkAbsolute(filepath.Dir(alias), true) != nil {
		return false
	}
	current, e = os.Lstat(target)
	if e != nil || !nativeOwned(current, true) || !os.SameFile(current, targetInfo) || checkAbsolute(target, true) != nil {
		return false
	}
	resolved, e := filepath.EvalSymlinks(alias)
	return e == nil && resolved == target
}

func readProjectAuthConfig(path, nativeHome string) ([]byte, error) {
	if nativeHome == "" || filepath.Dir(path) != nativeHome {
		return readAuthConfig(path)
	}
	alias, e := os.Lstat(nativeHome)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, ErrUnsafePath
	}
	if alias.Mode()&os.ModeSymlink == 0 {
		return readAuthConfig(path)
	}
	if !nativeLinkOwned(alias) {
		return nil, ErrUnsafePath
	}
	home, e := os.Lstat(filepath.Dir(nativeHome))
	if e != nil || !nativeOwned(home, true) || checkAbsolute(filepath.Dir(nativeHome), true) != nil {
		return nil, ErrUnsafePath
	}
	target, e := filepath.EvalSymlinks(nativeHome)
	if e != nil || checkAbsolute(target, true) != nil {
		return nil, ErrUnsafePath
	}
	before, e := os.Lstat(target)
	if e != nil || !nativeOwned(before, true) {
		return nil, ErrUnsafePath
	}
	parent, e := os.OpenRoot(target)
	if e != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = parent.Close() }()
	opened, e := parent.Lstat(".")
	if e != nil || !nativeOwned(opened, true) || !os.SameFile(before, opened) || !nativeAliasUnchanged(nativeHome, alias, target, before) {
		return nil, ErrUnsafePath
	}
	name := filepath.Base(path)
	info, e := parent.Lstat(name)
	var data []byte
	if e == nil {
		data, e = readAuthConfigFile(parent, name, info, true)
	} else if errors.Is(e, os.ErrNotExist) {
		e = nil
	}
	if e != nil {
		return nil, e
	}
	if !nativeAliasUnchanged(nativeHome, alias, target, before) {
		return nil, ErrUnsafePath
	}
	return data, nil
}

func readAuthConfigFile(parent *os.Root, name string, before os.FileInfo, native bool) ([]byte, error) {
	if !before.Mode().IsRegular() || native && !nativeOwned(before, false) {
		return nil, ErrUnsafePath
	}
	f, e := parent.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = f.Close() }()
	opened, e := f.Stat()
	if e != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() > maxCredentialBytes || native && !nativeOwned(opened, false) {
		return nil, ErrUnsafePath
	}
	b, e := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if e != nil || len(b) > maxCredentialBytes {
		return nil, ErrUnsafePath
	}
	after, e := parent.Lstat(name)
	if e != nil || !os.SameFile(after, opened) || native && !nativeOwned(after, false) {
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
