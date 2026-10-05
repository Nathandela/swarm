// Package accountconfig preserves the characterized subset of native launch
// configuration across account changes. Unknown settings refuse a managed
// launch; they are never silently dropped or copied into another account.
package accountconfig

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/accounts"
)

const maxSourceBytes = 2 << 20

const CharacterizedCodexVersion = "0.160.0"
const CharacterizedClaudeVersion = "2.1.289"

// SupportedNativeVersion is an exact gate, not a future-version promise. The
// help fixture establishes flags; live effective-configuration acceptance is
// a separate release gate for any newly supported configuration source.
func SupportedNativeVersion(provider, version string) bool {
	if provider == "codex" {
		return version == CharacterizedCodexVersion
	}
	if provider == "claude" {
		return version == CharacterizedClaudeVersion || version == "2.1.288"
	}
	return false
}

// Conflict exposes a stable, nonsecret cause. It never includes a source path,
// source content, executable command, environment value or argv fragment.
type Conflict string

func (c Conflict) Error() string { return "account configuration conflict: " + string(c) }

type Projection struct {
	Ref           string
	Generation    uint64
	HarmlessEnv   []string
	CLIArgs       []string // ephemeral full current argv; never persisted in the manifest
	BackendArgs   []string // additional config overrides, without a user prompt
	ProviderCwd   string
	NativeContext bool
}

type source struct{ Path, SHA256 string }
type sourceProfileAlias struct {
	Path, Canonical string
	Links           []sourceProfileLink
	Absent          bool `json:",omitempty"`
}
type sourceProfileLink struct {
	Path, Target  string
	Device, Inode uint64
}
type manifest struct {
	SchemaVersion           int
	Provider, Cwd           string
	Sources                 []source
	SourceAliases           []sourceProfileAlias `json:",omitempty"`
	Codex                   map[string]string    `json:",omitempty"`
	Claude                  map[string]any       `json:",omitempty"`
	Cohort                  map[string]string
	ProjectBoundary         *projectBoundary    `json:",omitempty"`
	CodexContext            *CodexContext       `json:",omitempty"`
	ClaudeContext           *ClaudeContext      `json:",omitempty"`
	ClaudeProjectSources    []source            `json:",omitempty"`
	CodexProjectSources     []source            `json:",omitempty"`
	ClaudeHistoryAlias      *ClaudeHistoryAlias `json:",omitempty"`
	CodexHistoryAlias       *CodexHistoryAlias  `json:",omitempty"`
	ClaudeInvocationSources []source            `json:",omitempty"`
}

// PrepareWithModel permits only the invocation model authenticated by the
// managed recovery authority. It leaves the frozen manifest and non-model
// policy unchanged; ordinary caller argv must pass an empty nativeModel.
func validateNativeModel(argv []string, prior, nativeModel string) error {
	if nativeModel != "" {
		if prior == "" || len(nativeModel) > 128 || strings.ContainsAny(nativeModel, "\x00\r\n\t ") {
			return Conflict("invalid-native-model-proof")
		}
		switch strings.ToLower(strings.SplitN(strings.SplitN(nativeModel, "[", 2)[0], ":", 2)[0]) {
		case "opus", "opusplan", "sonnet", "haiku", "default", "auto":
			return Conflict("invalid-native-model-proof")
		}
		for _, c := range nativeModel {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("-_.:/[]", c) {
				return Conflict("invalid-native-model-proof")
			}
		}
		models := append(flagValues(argv, "--model"), flagValues(argv, "-m")...)
		if len(models) != 1 || models[0] != nativeModel {
			return Conflict("native-model-proof-mismatch")
		}
	}
	return nil
}

func PrepareWithModel(stateRoot, provider, profilePath, cwd string, env, argv []string, priorProjectionRef, nativeModel string) (Projection, error) {
	if err := validateNativeModel(argv, priorProjectionRef, nativeModel); err != nil {
		return Projection{}, err
	}
	if provider != "codex" && provider != "claude" {
		return Projection{}, Conflict("unsupported-provider")
	}
	if !filepath.IsAbs(cwd) || !filepath.IsAbs(profilePath) || len(argv) == 0 {
		return Projection{}, Conflict("invalid-launch-context")
	}
	if err := safePath(cwd, true); err != nil {
		return Projection{}, Conflict("unsafe-project-directory")
	}
	if err := safePath(profilePath, true); err != nil {
		return Projection{}, Conflict("unsafe-account-profile")
	}
	harmless, err := accounts.ScrubEnvironment(env)
	if err != nil {
		return Projection{}, Conflict("invalid-environment")
	}
	if err := rejectSelectors(env, argv); err != nil {
		return Projection{}, err
	}
	var profileSources manifest
	var invocationCodex map[string]string
	if provider == "claude" {
		if err := collectClaudeProfileSources(&profileSources, env); err != nil {
			return Projection{}, err
		}
	}
	var m manifest
	if priorProjectionRef != "" {
		m, err = readManifest(stateRoot, priorProjectionRef)
		if err != nil {
			return Projection{}, err
		}
		if m.Provider != provider || m.Cwd != filepath.Clean(cwd) {
			return Projection{}, Conflict("projection-context-changed")
		}
		if err := validateProjectBoundary(m, env); err != nil {
			return Projection{}, err
		}
		if err := validateSourceAliases(m.SourceAliases); err != nil {
			return Projection{}, err
		}
		if err := validateSources(m.Sources); err != nil {
			return Projection{}, err
		}
	} else {
		m = manifest{SchemaVersion: 1, Provider: provider, Cwd: filepath.Clean(cwd), Cohort: map[string]string{}}
		m.ProjectBoundary, err = discoverProjectBoundary(provider, cwd, env)
		if err != nil {
			return Projection{}, err
		}
		originalProfilePath := originalProfile(provider, env)
		if originalProfilePath == "" {
			return Projection{}, Conflict("missing-original-home")
		}
		alias, err := snapshotSourceAlias(originalProfilePath)
		if err != nil {
			return Projection{}, err
		}
		m.SourceAliases = []sourceProfileAlias{alias}
		m.Sources = append(m.Sources, profileSources.Sources...)
		originalProfile := alias.Canonical
		if provider == "claude" {
			if err := collectClaudeProfilePolicies(&m, originalProfile, "native-managed-policy-not-characterized"); err != nil {
				return Projection{}, err
			}
		}
		if err := collectPoliciesAndProject(&m, provider, cwd, profilePath); err != nil {
			return Projection{}, err
		}
		if provider == "codex" {
			m.Codex, err = readCodexSettings(&m, filepath.Join(originalProfile, "config.toml"))
		} else {
			m.Claude, err = readClaudeSettings(&m, filepath.Join(originalProfile, "settings.json"), false)
		}
		if err != nil {
			return Projection{}, err
		}
		for _, name := range cohortNames(provider) {
			digest, err := cohortDigest(filepath.Join(originalProfile, name))
			if err != nil {
				return Projection{}, err
			}
			if provider == "codex" && name == "hooks.json" && digest != "absent" {
				return Projection{}, Conflict("executable-hooks-not-characterized")
			}
			m.Cohort[name] = digest
		}
		if provider == "claude" {
			fallback := ""
			if filepath.Base(originalProfilePath) == ".claude" {
				fallback = filepath.Join(filepath.Dir(originalProfilePath), ".claude.json")
			}
			digest, err := claudeTrustAndMCPAt(originalProfile, cwd, fallback)
			if err != nil {
				return Projection{}, err
			}
			m.Cohort["trust-and-mcp"] = digest
		}
	}
	// Candidate native settings are validated but never rewritten. Overridable
	// scalar defaults are projected; unrepresentable customization requires the
	// exact same native cohort, which is checked again immediately before spawn.
	if provider == "claude" {
		if priorProjectionRef == "" {
			for _, raw := range flagValues(argv, "--settings") {
				var overlay map[string]any
				if strings.HasPrefix(strings.TrimSpace(raw), "{") {
					if len(raw) > maxSourceBytes || json.Unmarshal([]byte(raw), &overlay) != nil {
						return Projection{}, Conflict("invalid-launch-settings")
					}
					if err := validateClaude(overlay, true); err != nil {
						return Projection{}, err
					}
				} else {
					overlay, err = readClaudeSettings(&m, raw, true)
					if err != nil {
						return Projection{}, err
					}
				}
				mergeClaude(m.Claude, overlay)
			}
		}
	} else {
		// Launch-specific model/sandbox policy is shared by both native processes.
		checked := m.Codex
		if nativeModel != "" {
			invocationCodex = make(map[string]string, len(m.Codex)+1)
			for key, value := range m.Codex {
				invocationCodex[key] = value
			}
			invocationCodex["model"] = strconv.Quote(nativeModel)
			checked = invocationCodex
		}
		if err := projectCodexFlags(checked, argv, priorProjectionRef == ""); err != nil {
			return Projection{}, err
		}
		m.Codex["cli_auth_credentials_store"] = `"file"`
	}
	if err := validateCandidate(m, profilePath); err != nil {
		return Projection{}, err
	}
	if err := validateSourceAliases(m.SourceAliases); err != nil {
		return Projection{}, err
	}
	if provider == "claude" {
		for _, alias := range m.SourceAliases {
			if err := collectClaudeProfilePolicies(&profileSources, alias.Canonical, "native-managed-policy-not-characterized"); err != nil {
				return Projection{}, err
			}
		}
	}
	if err := validateSources(m.Sources); err != nil {
		return Projection{}, err
	}
	if err := validateSources(profileSources.Sources); err != nil {
		return Projection{}, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return Projection{}, Conflict("projection-encoding")
	}
	digest := sha256.Sum256(raw)
	ref := hex.EncodeToString(digest[:])
	if priorProjectionRef != "" && ref != priorProjectionRef {
		return Projection{}, Conflict("immutable-projection-changed")
	}
	dir, err := writeProjection(stateRoot, ref, raw, m.Claude)
	if err != nil {
		return Projection{}, err
	}
	result := Projection{Ref: ref, Generation: binary.BigEndian.Uint64(digest[:8]), HarmlessEnv: harmless, ProviderCwd: cwd}
	if result.Generation == 0 {
		result.Generation = 1
	}
	if provider == "codex" {
		settings := m.Codex
		if invocationCodex != nil {
			settings = invocationCodex
		}
		keys := make([]string, 0, len(settings))
		for key := range settings {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			result.BackendArgs = append(result.BackendArgs, "-c", key+"="+settings[key])
		}
		// The adapter disables update checks for this resume invocation. It is
		// independent of the frozen session defaults and must win in both processes.
		if codexResumeNoUpdate(argv) {
			result.BackendArgs = append(result.BackendArgs, "-c", "check_for_update_on_startup=false")
		}
		// Keep argv[1] == resume for the existing adapter's remote-resume policy
		// projection. Codex's global config flag also parses after the subcommand.
		result.CLIArgs = append(append([]string(nil), argv...), result.BackendArgs...)
	} else {
		result.CLIArgs = removeFlag(argv, "--settings")
		result.CLIArgs = removeFlag(result.CLIArgs, "--setting-sources")
		result.CLIArgs = append([]string{result.CLIArgs[0], "--setting-sources", "", "--settings", filepath.Join(dir, "settings.json")}, result.CLIArgs[1:]...)
	}
	return result, nil
}

// Revalidate runs before either child is spawned. It compares the immutable
// manifest hash, mutable source hashes and the destination's native cohort.
func Revalidate(stateRoot, ref, provider, profilePath, cwd string) error {
	m, err := readManifest(stateRoot, ref)
	if err != nil {
		return err
	}
	if m.Provider != provider || m.Cwd != filepath.Clean(cwd) {
		return Conflict("projection-context-changed")
	}
	if m.SchemaVersion == 3 {
		return revalidateNativeManifest(m, profilePath)
	}
	if err := validateProjectBoundary(m, nil); err != nil {
		return err
	}
	currentPolicies := manifest{ProjectBoundary: m.ProjectBoundary}
	if err := collectPoliciesAndProject(&currentPolicies, provider, cwd, profilePath); err != nil {
		return err
	}
	if provider == "claude" {
		for _, alias := range m.SourceAliases {
			if err := collectClaudeProfilePolicies(&currentPolicies, alias.Canonical, "native-managed-policy-not-characterized"); err != nil {
				return err
			}
		}
	}
	if err := validateSourceAliases(m.SourceAliases); err != nil {
		return err
	}
	if err := validateSources(m.Sources); err != nil {
		return err
	}
	if err := validateSources(currentPolicies.Sources); err != nil {
		return err
	}
	return validateCandidate(m, profilePath)
}

func originalProfile(provider string, env []string) string {
	home, profile := "", ""
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == "HOME" {
			home = value
		}
		if provider == "codex" && key == "CODEX_HOME" || provider == "claude" && key == "CLAUDE_CONFIG_DIR" {
			profile = value
		}
	}
	if profile != "" {
		return profile
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "."+provider)
}

// Only the original ambient profile may be a user-owned alias. Resolve it once
// to a canonical directory, read through that anchor, and retain the alias
// identity so changing its target cannot change this session's configuration.
// Bound private profiles and individual source files still reject symlinks.
func snapshotSourceAlias(path string) (sourceProfileAlias, error) {
	alias := sourceProfileAlias{Path: path}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return alias, Conflict("unsafe-source-profile")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		alias.Canonical = path
		alias.Absent = true
		return alias, nil
	} else if err != nil {
		return alias, Conflict("unsafe-source-profile")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return alias, Conflict("unsafe-source-profile")
	}
	if err := safePath(canonical, true); err != nil {
		return alias, Conflict("unsafe-source-profile")
	}
	info, err := os.Lstat(canonical)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return alias, Conflict("unsafe-source-profile")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return alias, Conflict("unsafe-source-profile")
	}
	alias.Canonical = canonical
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), current) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return alias, Conflict("unsafe-source-profile")
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) {
			return alias, Conflict("unsafe-source-profile")
		}
		target, err := os.Readlink(current)
		if err != nil {
			return alias, Conflict("unsafe-source-profile")
		}
		alias.Links = append(alias.Links, sourceProfileLink{Path: current, Target: target, Device: uint64(stat.Dev), Inode: stat.Ino})
	}
	return alias, nil
}

func validateSourceAliases(aliases []sourceProfileAlias) error {
	for _, expected := range aliases {
		actual, err := snapshotSourceAlias(expected.Path)
		if err != nil || !reflect.DeepEqual(expected, actual) {
			return Conflict("source-profile-alias-changed")
		}
	}
	return nil
}

func rejectSelectors(env, argv []string) error {
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		// These values name paths, so "false" and "0" are real selectors too.
		if value != "" {
			switch key {
			case "CLAUDE_CODE_MANAGED_SETTINGS_PATH", "CLAUDE_CODE_REMOTE_SETTINGS_PATH":
				return Conflict("provider-or-profile-selector")
			}
		}
		if value == "" || value == "0" || value == "false" {
			continue
		}
		switch key {
		case "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD", "CLAUDE_CODE_USE_MANTLE":
			return Conflict("provider-or-profile-selector")
		case "CODEX_PROFILE", "ANTHROPIC_PROFILE", "ANTHROPIC_CONFIG_DIR", "CLAUDE_CODE_PROFILE", "CLAUDE_CODE_DEFAULT_PROFILE", "CLAUDE_CODE_FEDERATION_PROFILE", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_GATEWAY", "CLAUDE_CODE_GATEWAY_URL", "CLAUDE_CODE_HOST_GATEWAY_LINEAGE":
			return Conflict("provider-or-profile-selector")
		}
	}
	for _, arg := range argv {
		key, _, _ := strings.Cut(arg, "=")
		switch key {
		case "--profile", "-p", "--oss", "--local-provider", "--auth-token", "--api-key", "--remote-host", "--bare", "--safe-mode":
			return Conflict("unsupported-launch-selector")
		}
	}
	return nil
}

// The pinned Claude CLI's named OAuth profile store is independent of
// CLAUDE_CONFIG_DIR. Keep ordinary HOME for tools, but refuse this alternate
// authentication source by presence alone, without reading its credentials.
func collectClaudeProfileSources(m *manifest, env []string) error {
	paths := map[string]bool{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if value == "" || key != "HOME" && key != "XDG_CONFIG_HOME" {
			continue
		}
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return Conflict("unsafe-alternate-native-profile-path")
		}
		path := filepath.Join(value, "anthropic")
		if key == "HOME" {
			path = filepath.Join(value, ".config", "anthropic")
		}
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		if err := absentSource(m, path, "alternate-native-profile-store"); err != nil {
			return err
		}
	}
	return nil
}

func collectPoliciesAndProject(m *manifest, provider, cwd, profile string) error {
	// Policies remain native and higher priority. Their raw contents are not
	// projected. Until effective managed precedence is characterized, a present
	// policy is a refusal, including unreadable/malformed documents.
	if provider == "claude" {
		if err := collectClaudePolicies(m, "/etc/claude-code", "managed-policy-not-characterized"); err != nil {
			return err
		}
	} else {
		for _, path := range []string{"/etc/codex/config.toml", "/etc/codex/requirements.toml", "/etc/codex/managed_config.toml"} {
			if err := absentSource(m, path, "managed-policy-not-characterized"); err != nil {
				return err
			}
		}
	}
	return collectProjectSources(m, provider, cwd, profile)
}

func collectClaudePolicies(m *manifest, directory, reason string) error {
	for _, name := range []string{"managed-settings.json", "managed-settings.d", "managed-mcp.json"} {
		if err := absentSource(m, filepath.Join(directory, name), reason); err != nil {
			return err
		}
	}
	return nil
}

func collectClaudeProfilePolicies(m *manifest, profile, reason string) error {
	if err := collectClaudePolicies(m, profile, reason); err != nil {
		return err
	}
	for _, name := range []string{"remote-settings.json", "remote-settings-consent.json", "remote-settings-helper-consent"} {
		if err := absentSource(m, filepath.Join(profile, name), reason); err != nil {
			return err
		}
	}
	return nil
}

func collectProjectSources(m *manifest, provider, cwd, profile string) error {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		names := []string{filepath.Join(".codex", "config.toml")}
		if provider == "claude" {
			names = []string{filepath.Join(".claude", "settings.json"), filepath.Join(".claude", "settings.local.json"), ".mcp.json"}
		}
		for _, name := range names {
			if m.ProjectBoundary != nil {
				if provider == "claude" && dir == m.ProjectBoundary.Home && name != ".mcp.json" {
					continue
				}
				if provider == "codex" && filepath.Join(dir, ".codex") == profile {
					continue
				}
			}
			if err := absentSource(m, filepath.Join(dir, name), "project-settings-not-characterized"); err != nil {
				return err
			}
		}
		if provider == "codex" && m.ProjectBoundary != nil {
			if main := m.ProjectBoundary.MainRoot; main != "" {
				rel, err := filepath.Rel(m.ProjectBoundary.Root, dir)
				if err != nil || !withinDirectory(m.ProjectBoundary.Root, dir) {
					return Conflict("project-boundary-changed")
				}
				if err := absentSource(m, filepath.Join(main, rel, ".codex", "config.toml"), "project-settings-not-characterized"); err != nil {
					return err
				}
			}
			if dir == m.ProjectBoundary.Root {
				break
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return nil
}

func absentSource(m *manifest, path, reason string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		m.Sources = append(m.Sources, source{Path: path, SHA256: "absent"})
		return nil
	}
	return Conflict(reason)
}

func readSource(m *manifest, path string) ([]byte, error) {
	raw, err := readRegular(path, maxSourceBytes)
	if errors.Is(err, os.ErrNotExist) {
		m.Sources = append(m.Sources, source{Path: path, SHA256: "absent"})
		return nil, nil
	}
	if err != nil {
		return nil, Conflict("unsafe-or-unreadable-settings")
	}
	digest := sha256.Sum256(raw)
	m.Sources = append(m.Sources, source{Path: path, SHA256: hex.EncodeToString(digest[:])})
	return raw, nil
}

func validateSources(sources []source) error {
	for _, s := range sources {
		raw, err := readRegular(s.Path, maxSourceBytes)
		if s.SHA256 == "absent" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Conflict("configuration-source-changed")
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != s.SHA256 {
			return Conflict("configuration-source-changed")
		}
	}
	return nil
}

func validateCandidate(m manifest, profile string) error {
	if err := safePath(profile, true); err != nil {
		return Conflict("unsafe-account-profile")
	}
	temporary := manifest{}
	if m.Provider == "codex" {
		candidate, err := readCodexSettings(&temporary, filepath.Join(profile, "config.toml"))
		if err != nil {
			return err
		}
		for key := range candidate {
			if _, projected := m.Codex[key]; !projected {
				return Conflict("candidate-settings-not-projected")
			}
		}
	} else {
		if err := collectClaudeProfilePolicies(&temporary, profile, "native-managed-policy-not-characterized"); err != nil {
			return err
		}
		if _, err := readClaudeSettings(&temporary, filepath.Join(profile, "settings.json"), false); err != nil {
			return err
		}
	}
	for name, expected := range m.Cohort {
		var actual string
		var err error
		if name == "trust-and-mcp" {
			actual, err = claudeTrustAndMCP(profile, m.Cwd)
		} else {
			actual, err = cohortDigest(filepath.Join(profile, name))
		}
		if err != nil {
			return err
		}
		if actual != expected {
			return Conflict("native-customization-cohort-differs")
		}
	}
	return validateSources(temporary.Sources)
}

// Native identity fields in this configuration are never returned or copied.
// Preserve only the selected project's trust/tool decisions and MCP cohort by
// digest. Account-dependent identity/cost/onboarding fields are irrelevant.
func claudeTrustAndMCP(profile, cwd string) (string, error) {
	fallback := ""
	if filepath.Base(profile) == ".claude" {
		fallback = filepath.Join(filepath.Dir(profile), ".claude.json")
	}
	return claudeTrustAndMCPAt(profile, cwd, fallback)
}

func claudeTrustAndMCPAt(profile, cwd, fallback string) (string, error) {
	path := filepath.Join(profile, ".claude.json")
	raw, err := readRegular(path, maxSourceBytes)
	if errors.Is(err, os.ErrNotExist) && fallback != "" {
		path = fallback
		raw, err = readRegular(path, maxSourceBytes)
	}
	settings := map[string]any{}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", Conflict("unsafe-native-trust-config")
	}
	if len(raw) > 0 && json.Unmarshal(raw, &settings) != nil {
		return "", Conflict("invalid-native-trust-config")
	}
	cohort := map[string]any{}
	if servers, ok := settings["mcpServers"].(map[string]any); ok && len(servers) > 0 {
		cohort["mcpServers"] = servers
	}
	if projects, ok := settings["projects"].(map[string]any); ok {
		if project, ok := projects[cwd].(map[string]any); ok {
			selected := map[string]any{}
			for _, key := range []string{"hasTrustDialogAccepted", "allowedTools", "mcpServers", "enabledMcpjsonServers", "disabledMcpjsonServers", "ignorePatterns"} {
				if value, present := project[key]; present {
					selected[key] = value
				}
			}
			if len(selected) > 0 {
				cohort["project"] = selected
			}
		}
	}
	encoded, err := json.Marshal(cohort)
	if err != nil {
		return "", Conflict("invalid-native-trust-config")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cohortNames(provider string) []string {
	if provider == "codex" {
		return []string{"skills", "rules", "hooks.json", "AGENTS.md"}
	}
	return []string{"skills", "agents", "commands", "plugins", "CLAUDE.md"}
}

func cohortDigest(path string) (string, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	} else if err != nil {
		return "", Conflict("unsafe-native-customization")
	}
	h := sha256.New()
	count, total := 0, 0
	err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			return Conflict("unsafe-native-customization")
		}
		if entry.IsDir() {
			return nil
		}
		count++
		if count > 256 {
			return Conflict("native-customization-too-large")
		}
		raw, err := readRegular(current, maxSourceBytes)
		if err != nil {
			return Conflict("unsafe-native-customization")
		}
		total += len(raw)
		if total > 8<<20 {
			return Conflict("native-customization-too-large")
		}
		rel, _ := filepath.Rel(path, current)
		_, _ = io.WriteString(h, rel+"\x00")
		_, _ = h.Write(raw)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readClaudeSettings(m *manifest, path string, launch bool) (map[string]any, error) {
	raw, err := readSource(m, path)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &out) != nil {
		return nil, Conflict("invalid-claude-settings")
	}
	if err := validateClaude(out, launch); err != nil {
		return nil, err
	}
	return out, nil
}

func validateClaude(settings map[string]any, launch bool) error {
	for key, value := range settings {
		switch key {
		case "model", "effortLevel", "language":
			if _, ok := value.(string); !ok {
				return Conflict("invalid-claude-scalar")
			}
		case "permissions":
			permissions, ok := value.(map[string]any)
			if !ok {
				return Conflict("invalid-claude-permissions")
			}
			for name, rules := range permissions {
				if name != "allow" && name != "deny" && name != "ask" {
					return Conflict("unsupported-claude-permissions")
				}
				list, ok := rules.([]any)
				if !ok {
					return Conflict("invalid-claude-permissions")
				}
				for _, rule := range list {
					if _, ok := rule.(string); !ok {
						return Conflict("invalid-claude-permissions")
					}
				}
			}
		case "hooks":
			if !launch || !swarmHooks(value) {
				return Conflict("executable-hooks-not-characterized")
			}
		case "apiKeyHelper", "env", "awsAuthRefresh", "awsCredentialExport", "forceLoginMethod", "forceLoginOrgUUID":
			return Conflict("credential-or-provider-settings")
		default:
			return Conflict("unsupported-claude-setting")
		}
	}
	return nil
}

func swarmHooks(value any) bool {
	hooks, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for event, matchers := range hooks {
		switch event {
		case "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SubagentStart", "SubagentStop", "PermissionRequest", "SessionStart", "StopFailure":
		default:
			return false
		}
		list, ok := matchers.([]any)
		if !ok {
			return false
		}
		for _, rawMatcher := range list {
			matcher, ok := rawMatcher.(map[string]any)
			if !ok {
				return false
			}
			for key := range matcher {
				if key != "hooks" && key != "matcher" {
					return false
				}
			}
			commands, ok := matcher["hooks"].([]any)
			if !ok {
				return false
			}
			for _, rawCommand := range commands {
				command, ok := rawCommand.(map[string]any)
				if !ok || len(command) != 2 || command["type"] != "command" {
					return false
				}
				text, ok := command["command"].(string)
				if !ok {
					return false
				}
				parts := strings.Fields(text)
				if len(parts) != 3 || parts[0] != "swarm" || parts[1] != "hook" || parts[2] != event {
					return false
				}
			}
		}
	}
	return true
}

func mergeClaude(destination, overlay map[string]any) {
	for key, value := range overlay {
		if nested, ok := value.(map[string]any); ok {
			existing, ok := destination[key].(map[string]any)
			if !ok {
				existing = map[string]any{}
				destination[key] = existing
			}
			mergeClaude(existing, nested)
			continue
		}
		if list, ok := value.([]any); ok {
			existing, _ := destination[key].([]any)
			for _, item := range list {
				found := false
				for _, old := range existing {
					if reflect.DeepEqual(old, item) {
						found = true
						break
					}
				}
				if !found {
					existing = append(existing, item)
				}
			}
			destination[key] = existing
			continue
		}
		destination[key] = value
	}
}

func readCodexSettings(m *manifest, path string) (map[string]string, error) {
	raw, err := readSource(m, path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	section := ""
	// This is deliberately a restricted, fixture-backed scalar grammar, not a
	// permissive TOML parser. Unsupported multiline/tables/quoted keys refuse.
	for _, rawLine := range strings.Split(string(raw), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if line != "[sandbox_workspace_write]" {
				return nil, Conflict("unsupported-codex-table")
			}
			section = "sandbox_workspace_write."
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, Conflict("invalid-codex-settings")
		}
		key = section + strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if _, duplicate := out[key]; duplicate {
			return nil, Conflict("duplicate-codex-setting")
		}
		canonical, err := codexValue(key, value)
		if err != nil {
			return nil, err
		}
		out[key] = canonical
	}
	return out, nil
}

func codexValue(key, value string) (string, error) {
	switch key {
	case "model", "model_reasoning_effort", "model_reasoning_summary", "approval_policy", "sandbox_mode", "personality", "cli_auth_credentials_store":
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", Conflict("unsupported-codex-scalar-syntax")
		}
		if key == "cli_auth_credentials_store" && decoded != "file" {
			return "", Conflict("credential-store-policy")
		}
		if key == "sandbox_mode" && decoded != "read-only" && decoded != "workspace-write" && decoded != "danger-full-access" {
			return "", Conflict("unsupported-codex-sandbox")
		}
		return strconv.Quote(decoded), nil
	case "sandbox_workspace_write.network_access", "sandbox_workspace_write.exclude_tmpdir_env_var", "sandbox_workspace_write.exclude_slash_tmp", "check_for_update_on_startup":
		if value != "true" && value != "false" {
			return "", Conflict("unsupported-codex-scalar-syntax")
		}
		return value, nil
	case "model_context_window", "model_auto_compact_token_limit":
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			return "", Conflict("unsupported-codex-scalar-syntax")
		}
		return value, nil
	case "model_provider", "model_providers", "openai_base_url", "chatgpt_base_url", "profile", "profiles", "api_key", "experimental_bearer_token":
		return "", Conflict("credential-or-provider-settings")
	default:
		return "", Conflict("unsupported-codex-setting")
	}
}

func projectCodexFlags(settings map[string]string, argv []string, admit bool) error {
	for i := 1; i < len(argv); i++ {
		flag, value, joined := strings.Cut(argv[i], "=")
		switch flag {
		case "-c", "--config", "--model", "-m", "--sandbox", "-s", "--ask-for-approval", "-a":
			if !joined {
				i++
				if i >= len(argv) {
					return Conflict("invalid-codex-launch-setting")
				}
				value = argv[i]
			}
			key := ""
			switch flag {
			case "-c", "--config":
				var ok bool
				key, value, ok = strings.Cut(value, "=")
				if !ok {
					return Conflict("invalid-codex-launch-setting")
				}
			case "--model", "-m":
				key = "model"
				value = strconv.Quote(value)
			case "--sandbox", "-s":
				key = "sandbox_mode"
				value = strconv.Quote(value)
			case "--ask-for-approval", "-a":
				key = "approval_policy"
				value = strconv.Quote(value)
			}
			canonical, err := codexValue(key, value)
			if err != nil {
				return err
			}
			if len(argv) >= 3 && argv[1] == "resume" && key == "check_for_update_on_startup" && canonical == "false" {
				continue
			}
			if admit {
				settings[key] = canonical
			} else if original, ok := settings[key]; !ok || original != canonical {
				return Conflict("resume-launch-setting-changed")
			}
		}
	}
	return nil
}

func codexResumeNoUpdate(argv []string) bool {
	if len(argv) < 3 || argv[1] != "resume" {
		return false
	}
	for _, flag := range []string{"-c", "--config"} {
		for _, value := range flagValues(argv, flag) {
			if value == "check_for_update_on_startup=false" {
				return true
			}
		}
	}
	return false
}

func flagValues(argv []string, flag string) []string {
	var values []string
	for i := 1; i < len(argv); i++ {
		if argv[i] == flag && i+1 < len(argv) {
			i++
			values = append(values, argv[i])
		} else if value, ok := strings.CutPrefix(argv[i], flag+"="); ok {
			values = append(values, value)
		}
	}
	return values
}

func removeFlag(argv []string, flag string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if argv[i] == flag {
			i++
			continue
		}
		if strings.HasPrefix(argv[i], flag+"=") {
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

func safePath(path string, directory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Conflict("unsafe-configuration-path")
	}
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, current), current)
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 || directory) && !info.IsDir() {
			return Conflict("unsafe-configuration-path")
		}
	}
	return nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	return readBoundedRegular(path, limit, 0o022)
}

func readBoundedRegular(path string, limit int64, forbiddenWrite os.FileMode) ([]byte, error) {
	if err := safePath(path, false); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit || before.Mode().Perm()&forbiddenWrite != 0 {
		return nil, Conflict("unsafe-configuration-file")
	}
	anchor, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = anchor.Close() }()
	f, err := anchor.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, Conflict("configuration-source-changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, Conflict("unsafe-configuration-file")
	}
	return raw, nil
}

func readManifest(stateRoot, ref string) (manifest, error) {
	var m manifest
	if len(ref) != 64 {
		return m, Conflict("invalid-projection-reference")
	}
	if _, err := hex.DecodeString(ref); err != nil {
		return m, Conflict("invalid-projection-reference")
	}
	for _, dir := range []string{stateRoot, filepath.Join(stateRoot, "accounts"), filepath.Join(stateRoot, "accounts", "configurations"), filepath.Join(stateRoot, "accounts", "configurations", ref)} {
		if err := privateDir(dir); err != nil {
			return m, err
		}
	}
	raw, err := readRegular(filepath.Join(stateRoot, "accounts", "configurations", ref, "projection.json"), maxSourceBytes)
	if err != nil {
		return m, Conflict("projection-unavailable")
	}
	if err := privateProjectionFile(filepath.Join(stateRoot, "accounts", "configurations", ref, "projection.json")); err != nil {
		return m, err
	}
	digest := sha256.Sum256(raw)
	_, metadataErr := ProjectionCompatibility(raw)
	if hex.EncodeToString(digest[:]) != ref || metadataErr != nil || json.Unmarshal(raw, &m) != nil {
		return m, Conflict("projection-corrupt")
	}
	if m.Claude != nil {
		expected, err := json.Marshal(m.Claude)
		actual, readErr := readRegular(filepath.Join(stateRoot, "accounts", "configurations", ref, "settings.json"), maxSourceBytes)
		if err != nil || readErr != nil || string(expected) != string(actual) {
			return m, Conflict("projection-corrupt")
		}
		if err := privateProjectionFile(filepath.Join(stateRoot, "accounts", "configurations", ref, "settings.json")); err != nil {
			return m, err
		}
	}
	return m, nil
}

func writeProjection(stateRoot, ref string, raw []byte, settings map[string]any) (string, error) {
	if err := safePath(stateRoot, true); err != nil {
		return "", Conflict("unsafe-state-root")
	}
	if err := privateDir(stateRoot); err != nil {
		return "", err
	}
	stateAnchor, err := os.OpenRoot(stateRoot)
	if err != nil {
		return "", Conflict("unsafe-state-root")
	}
	defer func() { _ = stateAnchor.Close() }()
	if err := stateAnchor.Mkdir("accounts", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", Conflict("projection-storage")
	}
	if err := privateDir(filepath.Join(stateRoot, "accounts")); err != nil {
		return "", err
	}
	base := filepath.Join(stateRoot, "accounts", "configurations")
	if err := stateAnchor.Mkdir(filepath.Join("accounts", "configurations"), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", Conflict("projection-storage")
	}
	if err := safePath(base, true); err != nil {
		return "", Conflict("unsafe-projection-directory")
	}
	if err := privateDir(filepath.Join(stateRoot, "accounts")); err != nil {
		return "", err
	}
	if err := privateDir(base); err != nil {
		return "", err
	}
	baseAnchor, err := os.OpenRoot(base)
	if err != nil {
		return "", Conflict("unsafe-projection-directory")
	}
	defer func() { _ = baseAnchor.Close() }()
	dir := filepath.Join(base, ref)
	if err := baseAnchor.Mkdir(ref, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", Conflict("projection-storage")
	}
	if err := safePath(dir, true); err != nil {
		return "", Conflict("unsafe-projection-directory")
	}
	if err := privateDir(dir); err != nil {
		return "", err
	}
	files := map[string][]byte{"projection.json": raw}
	if settings != nil {
		encoded, err := json.Marshal(settings)
		if err != nil {
			return "", Conflict("projection-encoding")
		}
		files["settings.json"] = encoded
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		f, err := baseAnchor.OpenFile(filepath.Join(ref, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if errors.Is(err, os.ErrExist) {
			old, readErr := readRegular(path, maxSourceBytes)
			if readErr != nil || string(old) != string(data) {
				return "", Conflict("projection-corrupt")
			}
			if err := privateProjectionFile(path); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", Conflict("projection-storage")
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return "", Conflict("projection-storage")
		}
	}
	for _, path := range []string{dir, base, filepath.Join(stateRoot, "accounts"), stateRoot} {
		f, err := os.Open(path)
		if err != nil {
			return "", Conflict("projection-storage")
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return "", Conflict("projection-durability-uncertain")
		}
	}
	return dir, nil
}

func privateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return Conflict("unsafe-projection-directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return Conflict("unsafe-projection-directory")
	}
	return nil
}

func privateProjectionFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return Conflict("unsafe-projection-file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return Conflict("unsafe-projection-file")
	}
	return nil
}
