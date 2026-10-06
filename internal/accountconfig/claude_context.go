package accountconfig

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ClaudeContext identifies the ordinary global configuration independently of
// cwd and credentials. Its metadata contains hashes and paths, never settings.
// The interactive profile remains the sole native auth/refresh/state writer.
const ClaudeContextMarker = ".swarm-claude-context.json"

type ClaudeContext struct {
	GlobalGeneration        string
	SourceProfile           sourceProfileAlias
	Settings                source
	SettingsSHA256          string
	PreferencesPath         string
	PreferencesSHA256       string
	PreferencesStableSHA256 string
	Assets                  []ClaudeAssetAlias
	PolicySources           []source
}

type ClaudeAssetAlias struct{ Name, Target, SHA256 string }

// PrepareClaudeContext leaves native project/local layers and argv intact. The
// caller must hold all account writer exclusions through install, and refuse a
// different generation while any CLI/backend/login/quota writer is active.
// The callback is mandatory even for an already prepared native profile.
func PrepareClaudeContext(profile, cwd string, env []string, withLease func(string, func() error) error, ownerRefresh ...bool) (ClaudeContext, error) {
	var c ClaudeContext
	if withLease == nil || !filepath.IsAbs(cwd) {
		return c, Conflict("missing-configuration-lease")
	}
	if err := privateDir(profile); err != nil {
		return c, err
	}
	original := originalProfile("claude", env)
	alias, err := snapshotClaudeSourceAlias(original)
	if err != nil {
		return c, err
	}
	c.SourceProfile = alias
	inventory := manifest{}
	if err := collectClaudeProfileSources(&inventory, env); err != nil {
		return c, err
	}
	if err := collectClaudePolicies(&inventory, "/etc/claude-code", "managed-policy-not-characterized"); err != nil {
		return c, err
	}
	if err := collectClaudeProfilePolicies(&inventory, alias.Canonical, "native-managed-policy-not-characterized"); err != nil {
		return c, err
	}
	c.PolicySources = inventory.Sources
	if err := validateClaudeCandidatePolicies(profile); err != nil {
		return c, err
	}
	if alias.Canonical == profile {
		return c, Conflict("configuration-source-is-account-profile")
	}
	settingsPath := filepath.Join(alias.Canonical, "settings.json")
	raw, err := readClaudeOwnedSource(settingsPath)
	sourceAbsent := errors.Is(err, os.ErrNotExist)
	if sourceAbsent {
		raw, err = nil, nil
	}
	if err != nil {
		return c, err
	}
	settings := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &settings) != nil {
		return c, Conflict("invalid-claude-settings")
	}
	if err := filterClaudeGlobalSettings(settings); err != nil {
		return c, err
	}
	sourceDigest := "absent"
	if !sourceAbsent {
		sourceDigest = claudeHashBytes(raw)
	}
	c.Settings = source{Path: settingsPath, SHA256: sourceDigest}
	raw, err = json.Marshal(settings)
	if err != nil {
		return c, Conflict("invalid-claude-settings")
	}
	c.SettingsSHA256 = claudeHashBytes(raw)
	c.PreferencesPath = filepath.Join(alias.Canonical, ".claude.json")
	if _, err := os.Lstat(filepath.Join(alias.Canonical, ".config.json")); err == nil {
		return c, Conflict("legacy-native-config-not-characterized")
	} else if !errors.Is(err, os.ErrNotExist) {
		return c, Conflict("unsafe-native-trust-config")
	}
	if _, err := os.Lstat(c.PreferencesPath); errors.Is(err, os.ErrNotExist) && filepath.Base(original) == ".claude" {
		c.PreferencesPath = filepath.Join(filepath.Dir(original), ".claude.json")
	}
	preferences, err := readClaudePreferences(c.PreferencesPath)
	if err != nil {
		return c, err
	}
	c.PreferencesSHA256 = claudeHash(preferences)
	c.PreferencesStableSHA256 = claudeHash(claudeStablePreferences(preferences))
	for _, name := range cohortNames("claude") {
		target := filepath.Join(alias.Canonical, name)
		digest, err := claudeAssetDigest(target)
		if err != nil {
			return c, err
		}
		c.Assets = append(c.Assets, ClaudeAssetAlias{Name: name, Target: target, SHA256: digest})
	}
	encoded, _ := json.Marshal(c)
	sum := sha256.Sum256(encoded)
	c.GlobalGeneration = hex.EncodeToString(sum[:])
	err = withLease(c.GlobalGeneration, func() error {
		if err := validateClaudeContextSource(c); err != nil {
			return err
		}
		markerPath := filepath.Join(profile, ClaudeContextMarker)
		marker, markerErr := readRegular(markerPath, maxSourceBytes)
		update, err := readClaudeContextUpdate(profile)
		if err != nil {
			return err
		}
		if update != nil && update.Next.GlobalGeneration != c.GlobalGeneration {
			return Conflict("native-configuration-update-source-changed")
		}
		var refreshedHistory *claudeHistoryInventory
		var previous *ClaudeContext
		if markerErr == nil {
			var installed ClaudeContext
			if privateProjectionFile(markerPath) != nil || json.Unmarshal(marker, &installed) != nil || !validClaudeContext(installed) {
				return Conflict("account-global-configuration-generation-differs")
			}
			if installed.GlobalGeneration == c.GlobalGeneration {
				if err := RevalidateClaudeContext(c, profile); err != nil {
					return err
				}
				if update != nil {
					if _, err := refreshClaudeHistoryInventory(update.Previous, update.Next, profile); err != nil {
						return err
					}
					return removeClaudeContextUpdate(profile)
				}
				return nil
			}
			if len(ownerRefresh) != 1 || !ownerRefresh[0] || !reflect.DeepEqual(installed.SourceProfile, c.SourceProfile) {
				return Conflict("account-global-configuration-generation-differs")
			}
			if update != nil {
				if update.Previous.GlobalGeneration != installed.GlobalGeneration {
					return Conflict("invalid-native-configuration-update")
				}
				if err := validateClaudePartialUpdate(*update, profile); err != nil {
					return err
				}
			} else {
				if err := validateClaudeInstalledContext(installed, profile); err != nil {
					return err
				}
				previous = &installed
			}
			history, err := refreshClaudeHistoryInventory(installed, c, profile)
			if err != nil {
				return err
			}
			refreshedHistory = &history
		}
		if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
			return Conflict("unsafe-account-configuration")
		}
		if errors.Is(markerErr, os.ErrNotExist) && update != nil {
			return Conflict("invalid-native-configuration-update")
		}
		current, err := readClaudeContextState(filepath.Join(profile, ".claude.json"))
		if err != nil {
			return err
		}
		mergeClaudePreferences(current, preferences)
		state, err := json.Marshal(current)
		if err != nil {
			return Conflict("invalid-native-trust-config")
		}
		// Validate every alias destination before the first profile write. Never
		// replace arbitrary native/account artifacts with a configuration alias.
		for i, asset := range c.Assets {
			path := filepath.Join(profile, asset.Name)
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return Conflict("unsafe-native-customization")
			}
			if info.Mode()&os.ModeSymlink == 0 {
				return Conflict("candidate-customization-not-context-alias")
			}
			target, err := os.Readlink(path)
			removed := previous != nil && claudeRemovedAsset(*previous, c, i, target) || update != nil && claudeRemovedAsset(update.Previous, c, i, target)
			if err != nil || target != asset.Target || asset.SHA256 == "absent" && !removed {
				return Conflict("candidate-customization-not-context-alias")
			}
		}
		if previous != nil {
			update = &claudeContextUpdate{SchemaVersion: 1, Previous: *previous, Next: c}
			if err := writeClaudeContextUpdate(profile, *update); err != nil {
				return err
			}
		}
		if err := writeClaudeContextFile(profile, "settings.json", raw); err != nil {
			return err
		}
		if err := writeClaudeContextFile(profile, ".claude.json", state); err != nil {
			return err
		}
		for i, asset := range c.Assets {
			if asset.SHA256 == "absent" {
				if update != nil && claudeRemovedAsset(update.Previous, c, i, asset.Target) {
					if err := os.Remove(filepath.Join(profile, asset.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
						return Conflict("configuration-install")
					}
				}
				continue
			}
			path := filepath.Join(profile, asset.Name)
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				if err := os.Symlink(asset.Target, path); err != nil {
					return Conflict("configuration-install")
				}
			}
		}
		marker, err = json.Marshal(c)
		if err != nil {
			return Conflict("configuration-install")
		}
		if refreshedHistory != nil && len(refreshedHistory.Aliases) > 0 {
			historyRaw, err := json.Marshal(refreshedHistory)
			if err != nil {
				return Conflict("native-history-install")
			}
			if err := writeClaudeContextFile(profile, ClaudeHistoryMarker, historyRaw); err != nil {
				return err
			}
		}
		if err := writeClaudeContextFile(profile, ClaudeContextMarker, marker); err != nil {
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
		if err := RevalidateClaudeContext(c, profile); err != nil {
			return err
		}
		if update != nil {
			return removeClaudeContextUpdate(profile)
		}
		return nil
	})
	return c, err
}

func validateClaudeContextSource(c ClaudeContext) error {
	if err := validateSources(c.PolicySources); err != nil {
		return err
	}
	if err := validateClaudeSourceAlias(c.SourceProfile); err != nil {
		return err
	}
	if err := validateClaudeProjectSources([]source{c.Settings}); err != nil {
		return err
	}
	preferences, err := readClaudePreferences(c.PreferencesPath)
	if err != nil || claudeHash(preferences) != c.PreferencesSHA256 {
		return Conflict("configuration-source-changed")
	}
	for _, asset := range c.Assets {
		digest, err := claudeAssetDigest(asset.Target)
		if err != nil || digest != asset.SHA256 {
			return Conflict("configuration-source-changed")
		}
	}
	return nil
}

func RevalidateClaudeContext(c ClaudeContext, profile string) error {
	if !validClaudeContext(c) {
		return Conflict("invalid-configuration-generation")
	}
	if err := validateClaudeCandidatePolicies(profile); err != nil {
		return err
	}
	if err := privateDir(profile); err != nil {
		return err
	}
	markerPath := filepath.Join(profile, ClaudeContextMarker)
	marker, err := readRegular(markerPath, maxSourceBytes)
	var installed ClaudeContext
	if err != nil || privateProjectionFile(markerPath) != nil || json.Unmarshal(marker, &installed) != nil || !validClaudeContext(installed) || installed.GlobalGeneration != c.GlobalGeneration {
		return Conflict("candidate-configuration-marker-changed")
	}
	if err := validateClaudeContextSource(c); err != nil {
		return err
	}
	return validateClaudeInstalledContext(c, profile)
}

// Private native edits are never overwritten by owner-source refresh. This
// comparison intentionally excludes the owner source, which may have changed.
func validateClaudeInstalledContext(c ClaudeContext, profile string) error {
	raw, err := readRegular(filepath.Join(profile, "settings.json"), maxSourceBytes)
	if err != nil {
		return Conflict("configuration-source-changed")
	}
	hash, err := claudeSettingsDigest(raw)
	if err != nil || hash != c.SettingsSHA256 {
		return Conflict("candidate-configuration-changed")
	}
	preferences, err := readClaudePreferences(filepath.Join(profile, ".claude.json"))
	if err != nil || claudeHash(claudeStablePreferences(preferences)) != c.PreferencesStableSHA256 {
		return Conflict("candidate-configuration-changed")
	}
	for _, asset := range c.Assets {
		path := filepath.Join(profile, asset.Name)
		target, err := os.Readlink(path)
		if asset.SHA256 == "absent" {
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Conflict("candidate-customization-not-context-alias")
		}
		if err != nil || target != asset.Target {
			return Conflict("candidate-customization-not-context-alias")
		}
	}
	return nil
}

// The installed marker hashes json.Marshal(map), so whitespace, object order
// and string escapes are presentation only. Preserve numeric spelling instead
// of rounding through float64: noncanonical numbers conservatively hold.
func claudeSettingsDigest(raw []byte) (string, error) {
	if !validClaudeSettingsEncoding(raw) || !validClaudeJSON(raw, false) {
		return "", Conflict("candidate-configuration-changed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var settings map[string]any
	if decoder.Decode(&settings) != nil || settings == nil {
		return "", Conflict("candidate-configuration-changed")
	}
	return claudeHash(settings), nil
}

// encoding/json replaces invalid UTF-8 and unpaired surrogate escapes. Refuse
// those inputs rather than equating changed settings with a literal U+FFFD.
func validClaudeSettingsEncoding(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil || value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		i += 4
		if value >= 0xd800 && value <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func readClaudeContextState(path string) (map[string]any, error) {
	raw, err := readRegular(path, maxSourceBytes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, Conflict("unsafe-native-trust-config")
	}
	state := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &state) != nil {
		return nil, Conflict("invalid-native-trust-config")
	}
	return state, nil
}

// Account/OAuth/API-key/organization and volatile state are never imported.
func readClaudePreferences(path string) (map[string]any, error) {
	raw, err := readClaudeOwnedSource(path)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = nil, nil
	}
	state := map[string]any{}
	if err == nil && len(raw) > 0 && json.Unmarshal(raw, &state) != nil {
		err = Conflict("invalid-native-trust-config")
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if value, ok := state["mcpServers"]; ok {
		if _, valid := value.(map[string]any); !valid {
			return nil, Conflict("invalid-native-trust-config")
		}
		out["mcpServers"] = value
	}
	if projects, ok := state["projects"].(map[string]any); ok {
		selected := map[string]any{}
		for path, raw := range projects {
			project, ok := raw.(map[string]any)
			if !ok {
				return nil, Conflict("invalid-native-trust-config")
			}
			ordinary := map[string]any{}
			for _, key := range []string{"hasTrustDialogAccepted", "allowedTools", "mcpServers", "enabledMcpjsonServers", "disabledMcpjsonServers", "ignorePatterns", "hasClaudeMdExternalIncludesApproved", "hasClaudeMdExternalIncludesWarningShown"} {
				if value, ok := project[key]; ok {
					for _, trust := range claudeMutableTrustKeys {
						if key == trust {
							if _, valid := value.(bool); !valid {
								return nil, Conflict("invalid-native-trust-config")
							}
						}
					}
					ordinary[key] = value
				}
			}
			if len(ordinary) > 0 {
				selected[path] = ordinary
			}
		}
		if len(selected) > 0 {
			out["projects"] = selected
		}
	}
	return out, nil
}

// Legacy settings files can contain native global-state fields. They are not
// transferable configuration: retain only ordinary settings, never owner
// identity, feature/account cache, usage, migrations or per-project state.
func filterClaudeGlobalSettings(settings map[string]any) error {
	for _, key := range []string{"autoCompactWindow", "cachedDynamicConfigs", "cachedGrowthBookFeatures", "cachedStatsigGates", "changelogLastFetched", "claudeCodeFirstTokenDate", "firstStartTime", "githubRepoPaths", "hasSeenTasksHint", "installMethod", "numStartups", "oauthAccount", "opus45MigrationComplete", "projects", "promptQueueUseCount", "shiftEnterKeyBindingInstalled", "sonnet45MigrationComplete", "thinkingMigrationComplete", "tipsHistory", "userID"} {
		delete(settings, key)
	}
	for key := range settings {
		if filepath.IsAbs(key) {
			delete(settings, key)
		}
	}
	return validateClaudeContextSettings(settings)
}

func validateClaudeContextSettings(settings map[string]any) error {
	for key := range settings {
		switch key {
		case "worktree", "agentPushNotifEnabled", "autoCompactEnabled", "autoMode", "autoUpdates", "diffTool", "preferredNotifChannel", "remoteControlAtStartup", "skipAutoPermissionPrompt", "skipWorkflowUsageWarning", "theme", "useAutoModeDuringPlan", "$schema", "model", "effortLevel", "language", "permissions", "hooks", "enabledPlugins", "extraKnownMarketplaces", "alwaysThinkingEnabled", "outputStyle", "statusLine", "attribution", "spinnerTipsEnabled", "includeGitInstructions", "plansDirectory", "prefersReducedMotion", "sandbox", "cleanupPeriodDays", "autoUpdatesChannel", "companyAnnouncements", "disableAllHooks", "respectGitignore", "spinnerVerbs", "fileSuggestion", "enableAllProjectMcpServers", "enabledMcpjsonServers", "disabledMcpjsonServers", "allowedMcpServers", "deniedMcpServers":
		default:
			return Conflict("unsupported-claude-context-setting")
		}
	}
	return nil
}

func validClaudeContext(c ClaudeContext) bool {
	generation := c.GlobalGeneration
	if !cleanAbsolute(c.SourceProfile.Path) || !cleanAbsolute(c.SourceProfile.Canonical) || c.Settings.Path != filepath.Join(c.SourceProfile.Canonical, "settings.json") || len(c.Assets) != len(cohortNames("claude")) {
		return false
	}
	if c.PreferencesPath != filepath.Join(c.SourceProfile.Canonical, ".claude.json") && (filepath.Base(c.SourceProfile.Path) != ".claude" || c.PreferencesPath != filepath.Join(filepath.Dir(c.SourceProfile.Path), ".claude.json")) {
		return false
	}
	for i, name := range cohortNames("claude") {
		asset := c.Assets[i]
		if asset.Name != name || asset.Target != filepath.Join(c.SourceProfile.Canonical, name) || !claudeContextDigest(asset.SHA256, true) {
			return false
		}
	}
	if !claudeContextDigest(c.Settings.SHA256, true) || !claudeContextDigest(c.SettingsSHA256, false) || !claudeContextDigest(c.PreferencesSHA256, false) || !claudeContextDigest(c.PreferencesStableSHA256, false) {
		return false
	}
	policyPaths := []string{
		"/etc/claude-code/managed-settings.json", "/etc/claude-code/managed-settings.d", "/etc/claude-code/managed-mcp.json",
		filepath.Join(c.SourceProfile.Canonical, "managed-settings.json"), filepath.Join(c.SourceProfile.Canonical, "managed-settings.d"), filepath.Join(c.SourceProfile.Canonical, "managed-mcp.json"),
		filepath.Join(c.SourceProfile.Canonical, "remote-settings.json"), filepath.Join(c.SourceProfile.Canonical, "remote-settings-consent.json"), filepath.Join(c.SourceProfile.Canonical, "remote-settings-helper-consent"),
	}
	profileStoreCount := len(c.PolicySources) - len(policyPaths)
	if profileStoreCount < 0 || profileStoreCount > 2 {
		return false
	}
	for i, expected := range c.PolicySources {
		if expected.SHA256 != "absent" || !cleanAbsolute(expected.Path) {
			return false
		}
		if i < profileStoreCount {
			if !strings.HasSuffix(expected.Path, "/anthropic") || i > 0 && c.PolicySources[i-1].Path >= expected.Path {
				return false
			}
		} else if expected.Path != policyPaths[i-profileStoreCount] {
			return false
		}
	}
	c.GlobalGeneration = ""
	raw, err := json.Marshal(c)
	return err == nil && len(generation) == 64 && claudeHashBytes(raw) == generation
}

func claudeContextDigest(value string, absent bool) bool {
	if absent && value == "absent" {
		return true
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size
}

func claudeHash(value any) string       { raw, _ := json.Marshal(value); return claudeHashBytes(raw) }
func claudeHashBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func writeClaudeContextFile(profile, name string, raw []byte) error {
	if existing, err := readRegular(filepath.Join(profile, name), maxSourceBytes); err == nil && string(existing) == string(raw) {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Conflict("unsafe-account-configuration")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Conflict("configuration-install")
	}
	root, err := os.OpenRoot(profile)
	if err != nil {
		return Conflict("configuration-install")
	}
	defer func() { _ = root.Close() }()
	stage := ".account-tmp-" + hex.EncodeToString(nonce[:])
	file, err := root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Conflict("configuration-install")
	}
	defer func() { _ = root.Remove(stage) }()
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return Conflict("configuration-install")
	}
	if err := root.Rename(stage, name); err != nil {
		return Conflict("configuration-install")
	}
	return nil
}

func validateClaudeCandidatePolicies(profile string) error {
	inventory := manifest{}
	if err := absentSource(&inventory, filepath.Join(profile, ".config.json"), "legacy-native-config-not-characterized"); err != nil {
		return err
	}
	if err := collectClaudeProfilePolicies(&inventory, profile, "native-managed-policy-not-characterized"); err != nil {
		return err
	}
	return validateSources(inventory.Sources)
}
