package accountconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const CodexContextMarker = ".swarm-codex-context.json"

// CodexContext freezes one owner-global user-tier cohort independently of cwd
// and credentials. Project layers remain native. Token/account state is never
// copied; native config writes cause an explicit hold rather than an overwrite.
type CodexContext struct {
	GlobalGeneration      string
	SourceProfile         sourceProfileAlias
	SourceDirectorySHA256 string
	NativeProfile         string
	NativeHome            string
	Settings              source
	SettingsSHA256        string
	Assets                []CodexAssetAlias
	PolicySources         []source
}
type CodexAssetAlias struct{ Name, Target, SHA256 string }

func codexContextHome(env []string) string {
	home := ""
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == "HOME" {
			home = value
		}
	}
	return home
}

// Reconstruct the frozen native selector before source revalidation. Turning
// the implicit default into an explicit CODEX_HOME canonicalizes a symlink and
// changes native source-relative paths and hook keys.
func CodexContextEnvironment(c CodexContext, env []string) ([]string, error) {
	if !validCodexContext(c) || codexContextHome(env) != c.NativeHome {
		return nil, Conflict("configuration-home-context-changed")
	}
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "CODEX_HOME" {
			out = append(out, entry)
		}
	}
	if c.NativeProfile == c.SourceProfile.Path && c.SourceProfile.Path != c.SourceProfile.Canonical {
		if c.SourceProfile.Path != filepath.Join(c.NativeHome, ".codex") {
			return nil, Conflict("invalid-native-codex-context")
		}
	} else {
		out = append(out, "CODEX_HOME="+c.SourceProfile.Path)
	}
	return out, nil
}

func codexContextNames() []string {
	return []string{"skills", "rules", "hooks.json", "AGENTS.md", "plugins", ".tmp/marketplaces"}
}

// The lease excludes every account writer even when the cohort already exists.
func PrepareCodexContext(profile, cwd string, env []string, lease func(string, func() error) error, ownerRefresh ...bool) (CodexContext, error) {
	var c CodexContext
	if lease == nil || len(ownerRefresh) > 1 {
		return c, Conflict("missing-configuration-lease")
	}
	if err := safePath(cwd, true); err != nil {
		return c, Conflict("unsafe-project-directory")
	}
	if err := safePath(profile, true); err != nil {
		return c, Conflict("unsafe-account-profile")
	}
	if err := privateDir(profile); err != nil {
		return c, err
	}
	alias, err := snapshotClaudeSourceAlias(originalProfile("codex", env))
	if err != nil {
		return c, err
	}
	if alias.Canonical == profile {
		return c, Conflict("configuration-source-is-account-profile")
	}
	c.NativeHome = codexContextHome(env)
	if !cleanAbsolute(c.NativeHome) {
		return c, Conflict("invalid-native-codex-home")
	}
	c.SourceProfile = alias
	c.NativeProfile = alias.Path
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == "CODEX_HOME" && value != "" {
			c.NativeProfile = alias.Canonical
		}
	}
	c.SourceDirectorySHA256, err = claudeAssetDigest(alias.Canonical)
	if err != nil {
		return c, err
	}
	inventory := manifest{}
	for _, path := range codexContextPolicyPaths(alias.Canonical) {
		if err := absentSource(&inventory, path, "managed-policy-not-characterized"); err != nil {
			return c, err
		}
	}
	c.PolicySources = inventory.Sources
	if err := validateCodexCandidatePolicies(profile); err != nil {
		return c, err
	}
	c.Settings.Path = filepath.Join(alias.Canonical, "config.toml")
	raw, err := readClaudeOwnedSource(c.Settings.Path)
	c.Settings.SHA256 = "absent"
	if errors.Is(err, os.ErrNotExist) {
		raw, err = nil, nil
	} else if err == nil {
		c.Settings.SHA256 = claudeHashBytes(raw)
	}
	if err != nil {
		return c, err
	}
	settings := map[string]any{}
	if len(raw) > 0 && toml.Unmarshal(raw, &settings) != nil {
		return c, Conflict("invalid-native-codex-settings")
	}
	if err := validateCodexNativeAuthRouting(settings); err != nil {
		return c, err
	}
	if err := rebaseCodexGlobalPaths(settings, c.NativeProfile, codexContextHome(env)); err != nil {
		return c, err
	}
	raw, err = toml.Marshal(settings)
	if err != nil {
		return c, Conflict("invalid-native-codex-settings")
	}
	c.SettingsSHA256 = claudeHashBytes(raw)
	if err := remapCodexHookState(settings, c.NativeProfile, profile, false); err != nil {
		return c, err
	}
	raw, err = toml.Marshal(settings)
	if err != nil {
		return c, Conflict("invalid-native-codex-settings")
	}
	for _, name := range codexContextNames() {
		target := filepath.Join(alias.Canonical, name)
		digest, err := claudeAssetDigest(target)
		if err != nil {
			return c, err
		}
		c.Assets = append(c.Assets, CodexAssetAlias{Name: name, Target: target, SHA256: digest})
	}
	c.GlobalGeneration = claudeHash(c)
	err = lease(c.GlobalGeneration, func() error {
		if err := validateCodexContextSource(c); err != nil {
			return err
		}
		if err := validateCodexProfileAnchors(profile); err != nil {
			return err
		}
		markerPath := filepath.Join(profile, CodexContextMarker)
		marker, markerErr := readRegular(markerPath, maxSourceBytes)
		update, err := readCodexContextUpdate(profile)
		if err != nil {
			return err
		}
		refresh := len(ownerRefresh) == 1 && ownerRefresh[0]
		if update != nil && (!refresh || update.Next.GlobalGeneration != c.GlobalGeneration) {
			return Conflict("native-configuration-update-source-changed")
		}
		var history *CodexHistoryAlias
		if markerErr == nil {
			var installed CodexContext
			if privateProjectionFile(markerPath) != nil || json.Unmarshal(marker, &installed) != nil || !validCodexContext(installed) {
				return Conflict("candidate-configuration-marker-changed")
			}
			if update != nil {
				if installed.GlobalGeneration != update.Previous.GlobalGeneration && installed.GlobalGeneration != update.Next.GlobalGeneration {
					return Conflict("invalid-native-configuration-update")
				}
				if err := validateCodexPartialUpdate(*update, profile); err != nil {
					return err
				}
			} else {
				if installed.GlobalGeneration == c.GlobalGeneration {
					return RevalidateCodexContext(c, profile)
				}
				if !refresh {
					return Conflict("account-global-configuration-generation-differs")
				}
				if !sameCodexContextCustody(installed, c) {
					return Conflict("configuration-source-custody-changed")
				}
				if err := validateCodexInstalledSettings(installed, profile); err != nil {
					return err
				}
				update = &codexContextUpdate{SchemaVersion: 1, Previous: installed, Next: c}
			}
			history, err = refreshCodexHistoryProof(update.Previous, c, profile)
			if err != nil {
				return err
			}
		} else if !errors.Is(markerErr, os.ErrNotExist) || update != nil {
			return Conflict("unsafe-account-configuration")
		}
		// Nested asset aliases must not traverse an account-owned alias.
		if _, err := os.Lstat(filepath.Join(profile, ".tmp")); err == nil {
			if err := privateDir(filepath.Join(profile, ".tmp")); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Conflict("unsafe-account-configuration")
		}
		// Validate all destinations before any write. Login-created ordinary config
		// may exist, but an unsupported account/provider route is never replaced.
		existing, err := readRegular(filepath.Join(profile, "config.toml"), maxSourceBytes)
		if err == nil {
			var candidate map[string]any
			if toml.Unmarshal(existing, &candidate) != nil {
				return Conflict("invalid-native-codex-settings")
			}
			if err := validateCodexNativeAuthRouting(candidate); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Conflict("unsafe-account-configuration")
		}
		if update == nil {
			for _, asset := range c.Assets {
				path := filepath.Join(profile, asset.Name)
				if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
					continue
				}
				target, err := os.Readlink(path)
				if err != nil || target != asset.Target || asset.SHA256 == "absent" {
					return Conflict("candidate-customization-not-context-alias")
				}
			}
		} else {
			if err := writeCodexContextUpdate(profile, *update); err != nil {
				return err
			}
			for _, asset := range c.Assets {
				if asset.SHA256 == "absent" {
					if err := os.Remove(filepath.Join(profile, asset.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
						return Conflict("configuration-install")
					}
				}
			}
		}
		if err := writeClaudeContextFile(profile, "config.toml", raw); err != nil {
			return err
		}
		for _, asset := range c.Assets {
			if asset.SHA256 == "absent" {
				continue
			}
			path := filepath.Join(profile, asset.Name)
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					return Conflict("configuration-install")
				}
				if err := os.Symlink(asset.Target, path); err != nil {
					return Conflict("configuration-install")
				}
			}
		}
		if _, err := os.Lstat(filepath.Join(profile, ".tmp")); err == nil {
			if err := syncCodexContextDirectory(filepath.Join(profile, ".tmp")); err != nil {
				return err
			}
		}
		marker, err = json.Marshal(c)
		if err != nil {
			return Conflict("configuration-install")
		}
		if history != nil {
			historyRaw, err := json.Marshal(history)
			if err != nil {
				return Conflict("native-history-install")
			}
			if err := writeClaudeContextFile(profile, CodexHistoryMarker, historyRaw); err != nil {
				return err
			}
		}
		if err := writeClaudeContextFile(profile, CodexContextMarker, marker); err != nil {
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
		if err := RevalidateCodexContext(c, profile); err != nil {
			return err
		}
		if history != nil {
			if err := RevalidateCodexHistoryAlias(c, profile, *history); err != nil {
				return err
			}
		}
		if update != nil {
			return removeCodexContextUpdate(profile)
		}
		return nil
	})
	return c, err
}

func codexContextPolicyPaths(profile string) []string {
	return []string{"/etc/codex/config.toml", "/etc/codex/requirements.toml", "/etc/codex/managed_config.toml", filepath.Join(profile, "requirements.toml"), filepath.Join(profile, "managed_config.toml")}
}
func validateCodexCandidatePolicies(profile string) error {
	m := manifest{}
	for _, name := range []string{"requirements.toml", "managed_config.toml"} {
		if err := absentSource(&m, filepath.Join(profile, name), "native-managed-policy-not-characterized"); err != nil {
			return err
		}
	}
	return nil
}
func validCodexContext(c CodexContext) bool {
	if !cleanAbsolute(c.NativeHome) || (c.NativeProfile != c.SourceProfile.Path && c.NativeProfile != c.SourceProfile.Canonical) || !cleanAbsolute(c.SourceProfile.Path) || !cleanAbsolute(c.SourceProfile.Canonical) || c.Settings.Path != filepath.Join(c.SourceProfile.Canonical, "config.toml") || len(c.Assets) != len(codexContextNames()) || len(c.PolicySources) != len(codexContextPolicyPaths(c.SourceProfile.Canonical)) {
		return false
	}
	for i, path := range codexContextPolicyPaths(c.SourceProfile.Canonical) {
		if c.PolicySources[i].Path != path || c.PolicySources[i].SHA256 != "absent" {
			return false
		}
	}
	for i, name := range codexContextNames() {
		a := c.Assets[i]
		if a.Name != name || a.Target != filepath.Join(c.SourceProfile.Canonical, name) || !claudeContextDigest(a.SHA256, true) {
			return false
		}
	}
	if !claudeContextDigest(c.SourceDirectorySHA256, true) || !claudeContextDigest(c.Settings.SHA256, true) || !claudeContextDigest(c.SettingsSHA256, false) {
		return false
	}
	generation := c.GlobalGeneration
	c.GlobalGeneration = ""
	return claudeContextDigest(generation, false) && claudeHash(c) == generation
}
func validateCodexContextSource(c CodexContext) error {
	digest, err := claudeAssetDigest(c.SourceProfile.Canonical)
	if err != nil || digest != c.SourceDirectorySHA256 {
		return Conflict("configuration-source-changed")
	}
	if err := validateClaudeSourceAlias(c.SourceProfile); err != nil {
		return err
	}
	if err := validateSources(c.PolicySources); err != nil {
		return err
	}
	if err := RevalidateClaudeProjectSettings([]source{c.Settings}); err != nil {
		return err
	}
	for _, a := range c.Assets {
		digest, err := claudeAssetDigest(a.Target)
		if err != nil || digest != a.SHA256 {
			return Conflict("configuration-source-changed")
		}
	}
	return nil
}
func RevalidateCodexContext(c CodexContext, profile string) error {
	if !validCodexContext(c) {
		return Conflict("invalid-configuration-generation")
	}
	if err := validateCodexProfileAnchors(profile); err != nil {
		return err
	}
	if err := validateCodexCandidatePolicies(profile); err != nil {
		return err
	}
	path := filepath.Join(profile, CodexContextMarker)
	raw, err := readRegular(path, maxSourceBytes)
	var installed CodexContext
	if err != nil || privateProjectionFile(path) != nil || json.Unmarshal(raw, &installed) != nil || !validCodexContext(installed) || installed.GlobalGeneration != c.GlobalGeneration {
		return Conflict("candidate-configuration-marker-changed")
	}
	if err := validateCodexContextSource(c); err != nil {
		return err
	}
	return validateCodexInstalledSettings(c, profile)
}

func validateCodexProfileAnchors(profile string) error {
	if err := safePath(profile, true); err != nil {
		return Conflict("unsafe-account-profile")
	}
	if err := privateDir(profile); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(profile, ".tmp")); err == nil {
		if err := safePath(filepath.Join(profile, ".tmp"), true); err != nil {
			return Conflict("unsafe-account-configuration")
		}
		if err := privateDir(filepath.Join(profile, ".tmp")); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Conflict("unsafe-account-configuration")
	}
	return nil
}

func validateCodexInstalledConfig(c CodexContext, profile string) error {
	path := filepath.Join(profile, "config.toml")
	raw, err := readRegular(path, maxSourceBytes)
	if err != nil || privateProjectionFile(path) != nil {
		return Conflict("candidate-configuration-changed")
	}
	var settings map[string]any
	if toml.Unmarshal(raw, &settings) != nil || remapCodexHookState(settings, c.NativeProfile, profile, true) != nil {
		return Conflict("candidate-configuration-changed")
	}
	normalized, err := toml.Marshal(settings)
	if err != nil || claudeHashBytes(normalized) != c.SettingsSHA256 {
		return Conflict("candidate-configuration-changed")
	}
	return nil
}
func validateCodexInstalledSettings(c CodexContext, profile string) error {
	if err := validateCodexInstalledConfig(c, profile); err != nil {
		return err
	}
	for _, a := range c.Assets {
		path := filepath.Join(profile, a.Name)
		if a.SHA256 == "absent" {
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Conflict("candidate-customization-not-context-alias")
		}
		target, err := os.Readlink(path)
		if err != nil || target != a.Target {
			return Conflict("candidate-customization-not-context-alias")
		}
	}
	return nil
}

func ValidateCodexProjectSettings(cwd string, env []string) ([]source, error) {
	boundary, err := discoverProjectBoundary("codex", cwd, env)
	if err != nil {
		return nil, err
	}
	var sources []source
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, name := range []string{"config.toml", "hooks.json"} {
			path := filepath.Join(dir, ".codex", name)
			digest, err := codexProjectSourceDigest(path)
			if err != nil {
				return nil, err
			}
			sources = append(sources, source{Path: path, SHA256: digest})
		}
		if dir == boundary.Root {
			break
		}
	}
	if boundary.MainRoot != "" {
		path := filepath.Join(boundary.MainRoot, ".codex", "hooks.json")
		digest, err := codexProjectSourceDigest(path)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source{Path: path, SHA256: digest})
	}
	return sources, nil
}
func codexProjectSourceDigest(path string) (string, error) {
	raw, err := readClaudeOwnedSource(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", Conflict("unsafe-native-configuration-source")
	}
	if filepath.Ext(path) == ".toml" {
		var settings map[string]any
		if toml.Unmarshal(raw, &settings) != nil {
			return "", Conflict("invalid-native-codex-settings")
		}
		if err := validateCodexNativeAuthRouting(settings); err != nil {
			return "", err
		}
	}
	return claudeHashBytes(raw), nil
}
func RevalidateCodexProjectSettings(sources []source) error {
	for _, s := range sources {
		digest, err := codexProjectSourceDigest(s.Path)
		if err != nil || digest != s.SHA256 {
			return Conflict("configuration-source-changed")
		}
	}
	return nil
}

// Native hook state keys include the literal user source file path. Their
// hash covers handler semantics, not that path. Rewrite only those two exact
// prefixes; project/plugin keys and every suffix/value remain untouched.
func remapCodexHookState(settings map[string]any, source, profile string, inverse bool) error {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return nil
	}
	value, present := hooks["state"]
	if !present {
		return nil
	}
	state, ok := value.(map[string]any)
	if !ok {
		return Conflict("invalid-native-codex-hook-state")
	}
	out := make(map[string]any, len(state))
	for key, value := range state {
		replacement := key
		for _, name := range []string{"config.toml", "hooks.json"} {
			original := filepath.Join(source, name) + ":"
			private := filepath.Join(profile, name) + ":"
			from, to := original, private
			if inverse {
				if source != profile && strings.HasPrefix(key, original) {
					return Conflict("candidate-hook-source-differs")
				}
				from, to = private, original
			}
			if suffix, ok := strings.CutPrefix(key, from); ok {
				replacement = to + suffix
				break
			}
		}
		if _, exists := out[replacement]; exists {
			return Conflict("native-hook-state-key-collision")
		}
		out[replacement] = value
	}
	hooks["state"] = out
	return nil
}

// rust-v0.160.0 ConfigToml uses AbsolutePathBuf for these exact schema fields.
// The native resolver anchors them to the source layer, not the invocation cwd.
// Permissions, MCP cwd, marketplace source, commands and shell text are opaque
// strings and deliberately retain their native HOME/cwd interpretation.
func rebaseCodexGlobalPaths(settings map[string]any, origin, home string) error {
	rebase := func(table map[string]any, key string, array bool) error {
		value, present := table[key]
		if !present {
			return nil
		}
		path := func(value any) (string, error) {
			s, ok := value.(string)
			if !ok || strings.ContainsRune(s, 0) {
				return "", Conflict("invalid-native-codex-path")
			}
			if s == "~" || strings.HasPrefix(s, "~/") {
				if !cleanAbsolute(home) {
					return "", Conflict("invalid-native-codex-home")
				}
				s = filepath.Join(home, strings.TrimLeft(strings.TrimPrefix(s, "~"), "/"))
			}
			if !filepath.IsAbs(s) {
				s = filepath.Join(origin, s)
			}
			return filepath.Clean(s), nil
		}
		if !array {
			p, err := path(value)
			if err == nil {
				table[key] = p
			}
			return err
		}
		values, ok := value.([]any)
		if !ok {
			return Conflict("invalid-native-codex-path")
		}
		for i, value := range values {
			p, err := path(value)
			if err != nil {
				return err
			}
			values[i] = p
		}
		return nil
	}
	for _, key := range []string{"model_instructions_file", "js_repl_node_path", "sqlite_home", "log_dir", "model_catalog_json", "experimental_compact_prompt_file"} {
		if err := rebase(settings, key, false); err != nil {
			return err
		}
	}
	if err := rebase(settings, "js_repl_node_module_dirs", true); err != nil {
		return err
	}
	if sandbox, ok := settings["sandbox_workspace_write"].(map[string]any); ok {
		if err := rebase(sandbox, "writable_roots", true); err != nil {
			return err
		}
	}
	if skills, ok := settings["skills"].(map[string]any); ok {
		if entries, ok := skills["config"].([]any); ok {
			for _, entry := range entries {
				if table, ok := entry.(map[string]any); ok {
					if err := rebase(table, "path", false); err != nil {
						return err
					}
				}
			}
		}
	}
	if otel, ok := settings["otel"].(map[string]any); ok {
		for _, key := range []string{"exporter", "trace_exporter", "metrics_exporter"} {
			if exporter, ok := otel[key].(map[string]any); ok {
				for _, kind := range []string{"otlp-http", "otlp-grpc"} {
					if transport, ok := exporter[kind].(map[string]any); ok {
						if tls, ok := transport["tls"].(map[string]any); ok {
							for _, key := range []string{"ca-certificate", "client-certificate", "client-private-key"} {
								if err := rebase(tls, key, false); err != nil {
									return err
								}
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// ValidateCodexNativeArguments checks explicit invocation overrides without
// promoting native defaults or reinterpreting ordinary configuration values.
func ValidateCodexNativeArguments(argv []string) error {
	for i := 1; i < len(argv); i++ {
		flag, value, joined := strings.Cut(argv[i], "=")
		if strings.HasPrefix(flag, "-p") && !strings.HasPrefix(flag, "--") {
			return Conflict("unsupported-launch-selector")
		}
		if strings.HasPrefix(flag, "-c") && flag != "-c" && !strings.HasPrefix(flag, "--") {
			value, flag, joined = strings.TrimPrefix(argv[i], "-c"), "-c", true
		}
		switch flag {
		case "--profile", "-p", "--oss", "--local-provider", "--remote", "--remote-auth-token-env":
			return Conflict("unsupported-launch-selector")
		case "-c", "--config":
			if !joined {
				i++
				if i >= len(argv) {
					return Conflict("invalid-codex-launch-setting")
				}
				value = argv[i]
			}
			var settings map[string]any
			if toml.Unmarshal([]byte(value), &settings) != nil {
				// Native -c treats a non-TOML value as a literal string.
				key, raw, ok := strings.Cut(value, "=")
				if !ok || toml.Unmarshal([]byte(key+"="+strconv.Quote(strings.TrimSpace(raw))), &settings) != nil {
					return Conflict("invalid-codex-launch-setting")
				}
			}
			if err := validateCodexNativeAuthRouting(settings); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCodexNativeAuthRouting(settings map[string]any) error {
	if features, ok := settings["features"].(map[string]any); ok {
		if proxy, ok := features["network_proxy"].(map[string]any); ok {
			for _, key := range []string{"credential_broker", "credentials"} {
				if _, present := proxy[key]; present {
					return Conflict("native-auth-route-not-characterized")
				}
			}
		}
	}

	for key, allowed := range map[string]string{
		"model_provider": "openai", "cli_auth_credentials_store": "file", "forced_login_method": "chatgpt",
	} {
		if value, present := settings[key]; present {
			text, ok := value.(string)
			if !ok || text != allowed {
				return Conflict("native-auth-route-not-characterized")
			}
		}
	}
	// These selectors may choose another endpoint, secret source or profile.
	// MCP tool authentication is intentionally outside this root-key list.
	for _, key := range []string{
		"model_providers", "profile", "profiles", "openai_base_url", "chatgpt_base_url",
		"api_key", "env_key", "experimental_bearer_token", "auth", "auth_keyring_backend",
		"forced_chatgpt_workspace_id", "credential_broker",
	} {
		if _, present := settings[key]; present {
			return Conflict("native-auth-route-not-characterized")
		}
	}
	if _, present := settings["project_root_markers"]; present {
		return Conflict("native-project-discovery-not-characterized")
	}
	if methods, present := settings["allowed_login_methods"]; present {
		values, ok := methods.([]any)
		allowed := false
		for _, value := range values {
			allowed = allowed || value == "chatgpt"
		}
		if !ok || !allowed {
			return Conflict("native-auth-route-not-characterized")
		}
	}
	// A role-specific configuration can change the model provider independently
	// of the main layer. Admit it only after that separate route is characterized.
	if agents, ok := settings["agents"].(map[string]any); ok {
		for _, value := range agents {
			if role, ok := value.(map[string]any); ok {
				if _, present := role["config_file"]; present {
					return Conflict("nested-native-auth-context-not-characterized")
				}
			}
		}
	}
	return nil
}
