package accountconfig

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter/claude"
)

// PrepareNative keeps settings in native user/project tiers. Its only Codex
// override is the characterized credential file store; no user defaults are
// promoted over project policy. Old frozen projections retain their contract.
func PrepareNative(stateRoot, provider, profile, cwd string, env, argv []string, prior, nativeModel string, lease func(string, func() error) error, createHistory bool, ownerResume ...bool) (Projection, error) {
	var m manifest
	var err error
	var frozenOrigin manifest
	if prior != "" {
		m, err = readManifest(stateRoot, prior)
		if err != nil {
			return Projection{}, err
		}
		if m.SchemaVersion != 3 {
			return PrepareWithModel(stateRoot, provider, profile, cwd, env, argv, prior, nativeModel)
		}
		if m.Provider != provider || m.Cwd != filepath.Clean(cwd) {
			return Projection{}, Conflict("projection-context-changed")
		}
		frozenOrigin = m
		if len(ownerResume) == 1 && ownerResume[0] && nativeModel == "" {
			prior = ""
			m = manifest{SchemaVersion: 3, Provider: provider, Cwd: filepath.Clean(cwd)}
		}
	} else {
		m = manifest{SchemaVersion: 3, Provider: provider, Cwd: filepath.Clean(cwd)}
	}
	if !cleanAbsolute(cwd) || safePath(cwd, true) != nil {
		return Projection{}, Conflict("unsafe-project-directory")
	}
	if len(argv) == 0 || !cleanAbsolute(profile) || (provider != "codex" && provider != "claude") {
		return Projection{}, Conflict("invalid-launch-context")
	}
	if err = rejectSelectors(env, argv); err != nil {
		return Projection{}, err
	}
	// Schema-3 contexts do not yet freeze the independent XDG profile origin.
	// Historical projections keep their existing recorded XDG contract above.
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "XDG_CONFIG_HOME="); ok && value != "" {
			return Projection{}, Conflict("configuration-origin-not-characterized")
		}
	}
	if err := validateNativeModel(argv, prior, nativeModel); err != nil {
		return Projection{}, err
	}

	contextEnv := append([]string(nil), env...)
	if frozenOrigin.SchemaVersion == 3 {
		selector, origin := "CLAUDE_CONFIG_DIR", ""
		if provider == "codex" {
			contextEnv, err = CodexContextEnvironment(*frozenOrigin.CodexContext, contextEnv)
			if err != nil {
				return Projection{}, err
			}
		} else {
			origin = frozenOrigin.ClaudeContext.SourceProfile.Path
			contextEnv = appendEnvOrigin(contextEnv, selector, origin)
		}
	}
	harmless, err := accounts.ScrubEnvironment(env)
	if err != nil {
		return Projection{}, Conflict("invalid-environment")
	}
	if prior == "" && createHistory {
		origin := originalProfile(provider, contextEnv)
		if !cleanAbsolute(origin) {
			return Projection{}, Conflict("unsafe-source-profile")
		}
		if _, err := os.Lstat(origin); errors.Is(err, os.ErrNotExist) {
			if lease == nil {
				return Projection{}, Conflict("missing-configuration-lease")
			}
			if err := lease("", func() error {
				if safePath(filepath.Dir(origin), true) != nil {
					return Conflict("unsafe-source-profile")
				}
				info, err := os.Lstat(filepath.Dir(origin))
				if err != nil || !claudeOwnedInfo(info, true) {
					return Conflict("unsafe-source-profile")
				}
				if err := os.Mkdir(origin, 0700); err != nil && !errors.Is(err, os.ErrExist) {
					return Conflict("native-history-install")
				}
				if _, err := snapshotClaudeSourceAlias(origin); err != nil {
					return err
				}
				for _, path := range []string{origin, filepath.Dir(origin)} {
					dir, err := os.Open(path)
					if err != nil {
						return Conflict("native-history-install")
					}
					err = dir.Sync()
					_ = dir.Close()
					if err != nil {
						return Conflict("configuration-durability-uncertain")
					}
				}
				return nil
			}); err != nil {
				return Projection{}, err
			}
		}
	}
	if provider == "codex" {
		if err := ValidateCodexNativeArguments(argv); err != nil {
			return Projection{}, err
		}
		projectSources, err := ValidateCodexProjectSettings(cwd, env)
		if err != nil {
			return Projection{}, err
		}
		ctx, err := PrepareCodexContext(profile, cwd, contextEnv, lease, prior == "" && createHistory)
		if err != nil {
			return Projection{}, err
		}
		if prior == "" {
			m.CodexContext, m.CodexProjectSources = &ctx, projectSources
			m.ProjectBoundary, err = discoverProjectBoundary(provider, cwd, env)
			if err != nil {
				return Projection{}, err
			}
		} else if m.CodexContext == nil || m.CodexContext.GlobalGeneration != ctx.GlobalGeneration || !reflect.DeepEqual(m.CodexProjectSources, projectSources) {
			return Projection{}, Conflict("native-configuration-source-changed")
		}
		history, err := PrepareCodexHistoryAlias(ctx, profile, createHistory, lease, prior == "" && createHistory)
		if err != nil {
			return Projection{}, err
		}
		if prior == "" {
			m.CodexHistoryAlias = &history
		} else if m.CodexHistoryAlias == nil || *m.CodexHistoryAlias != history {
			return Projection{}, Conflict("native-history-root-changed")
		}
		if err := revalidateNativeManifest(m, profile); err != nil {
			return Projection{}, err
		}

	} else {
		projectSources, err := ValidateClaudeProjectSettings(cwd, env)
		if err != nil {
			return Projection{}, err
		}
		invocationSources, err := ValidateClaudeInvocationSettings(argv)
		if err != nil {
			return Projection{}, err
		}
		if prior == "" {
			m.ClaudeProjectSources, m.ClaudeInvocationSources = projectSources, invocationSources
		} else if !reflect.DeepEqual(m.ClaudeProjectSources, projectSources) || !reflect.DeepEqual(m.ClaudeInvocationSources, invocationSources) {
			return Projection{}, Conflict("native-configuration-source-changed")
		}
		ctx, err := PrepareClaudeContext(profile, cwd, contextEnv, lease, prior == "" && createHistory)
		if err != nil {
			return Projection{}, err
		}
		if prior != "" && (m.ClaudeContext == nil || m.ClaudeContext.GlobalGeneration != ctx.GlobalGeneration) {
			return Projection{}, Conflict("native-configuration-source-changed")
		}
		if prior == "" {
			m.ClaudeContext = &ctx
			m.ProjectBoundary, err = discoverProjectBoundary(provider, cwd, env)
			if err != nil {
				return Projection{}, err
			}
		}
		if err := validateProjectBoundary(m, env); err != nil {
			return Projection{}, err
		}
		history, err := PrepareClaudeHistoryAlias(ctx, profile, claude.New().ProjectDirName(cwd), createHistory, lease)
		if err != nil {
			return Projection{}, err
		}
		if prior == "" {
			m.ClaudeHistoryAlias = &history
		} else if m.ClaudeHistoryAlias == nil || *m.ClaudeHistoryAlias != history {
			return Projection{}, Conflict("native-history-root-changed")
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return Projection{}, Conflict("projection-encoding")
	}
	digest := sha256.Sum256(raw)
	ref := hex.EncodeToString(digest[:])
	if prior != "" && prior != ref {
		return Projection{}, Conflict("immutable-projection-changed")
	}
	if _, err := writeProjection(stateRoot, ref, raw, nil); err != nil {
		return Projection{}, err
	}
	result := Projection{Ref: ref, Generation: binary.BigEndian.Uint64(digest[:8]), HarmlessEnv: harmless, ProviderCwd: cwd, CLIArgs: append([]string(nil), argv...), NativeContext: true}
	if result.Generation == 0 {
		result.Generation = 1
	}
	if provider == "codex" {
		result.CLIArgs = WithoutSynthesizedNetworkOverride(result.CLIArgs)
		result.BackendArgs = []string{"-c", `cli_auth_credentials_store="file"`}
		if codexResumeNoUpdate(argv) {
			result.BackendArgs = append(result.BackendArgs, "-c", "check_for_update_on_startup=false")
		}
		result.CLIArgs = append(result.CLIArgs, result.BackendArgs...)
	}
	return result, nil
}

// WithoutSynthesizedNetworkOverride removes precisely the historical override
// injected by Swarm's adapter. All other explicit permission flags survive.
func WithoutSynthesizedNetworkOverride(argv []string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if (argv[i] == "-c" || argv[i] == "--config") && i+1 < len(argv) && argv[i+1] == "sandbox_workspace_write.network_access=true" {
			i++
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

func revalidateNativeManifest(m manifest, profile string) error {
	if PendingNativeContextUpdate(profile, m.Provider) {
		return Conflict("native-configuration-update-pending")
	}
	if m.SchemaVersion != 3 || (m.Provider == "codex") != (m.CodexContext != nil) || (m.Provider == "claude") != (m.ClaudeContext != nil) {
		return Conflict("projection-corrupt")
	}
	if err := validateProjectBoundary(m, nil); err != nil {
		return err
	}
	if m.Provider == "codex" {
		if err := RevalidateCodexProjectSettings(m.CodexProjectSources); err != nil {
			return err
		}
		if m.CodexHistoryAlias == nil {
			return Conflict("missing-native-history-context")
		}
		return RevalidateCodexHistoryAlias(*m.CodexContext, profile, *m.CodexHistoryAlias)
	}
	if err := RevalidateClaudeProjectSettings(m.ClaudeProjectSources); err != nil {
		return err
	}
	if err := RevalidateClaudeProjectSettings(m.ClaudeInvocationSources); err != nil {
		return err
	}
	if m.ClaudeHistoryAlias == nil {
		return Conflict("missing-native-history-context")
	}
	if err := RevalidateClaudeContext(*m.ClaudeContext, profile); err != nil {
		return err
	}
	return RevalidateClaudeHistoryAlias(*m.ClaudeContext, profile, *m.ClaudeHistoryAlias)
}

// NativeHistoryProfile identifies history authority independently of credentials.
func NativeHistoryProfile(stateRoot, ref string) (string, error) {
	m, err := readManifest(stateRoot, ref)
	if err != nil {
		return "", err
	}
	if m.SchemaVersion != 3 {
		return "", nil
	}
	var alias sourceProfileAlias
	if m.Provider == "codex" {
		alias = m.CodexContext.SourceProfile
	} else {
		alias = m.ClaudeContext.SourceProfile
	}
	if err := validateClaudeSourceAlias(alias); err != nil {
		return "", err
	}
	var proofPath string
	var dev, ino uint64
	if m.Provider == "codex" {
		proofPath, dev, ino = m.CodexHistoryAlias.SourcePath, m.CodexHistoryAlias.Device, m.CodexHistoryAlias.Inode
	} else {
		proofPath, dev, ino = m.ClaudeHistoryAlias.SourcePath, m.ClaudeHistoryAlias.Device, m.ClaudeHistoryAlias.Inode
	}
	if err := safePath(proofPath, true); err != nil {
		return "", Conflict("unsafe-native-history")
	}
	info, err := os.Lstat(proofPath)
	if err != nil || !claudeOwnedInfo(info, true) {
		return "", Conflict("unsafe-native-history")
	}
	stat := info.Sys().(*syscall.Stat_t)
	if uint64(stat.Dev) != dev || stat.Ino != ino {
		return "", Conflict("native-history-root-changed")
	}
	return alias.Canonical, nil
}

func NativeHistoryAuthority(stateRoot, ref, provider, cwd string, generation uint64) (string, error) {
	m, err := readManifest(stateRoot, ref)
	if err != nil {
		return "", err
	}
	if m.SchemaVersion != 3 {
		return "", nil
	}
	raw, _ := hex.DecodeString(ref)
	gen := binary.BigEndian.Uint64(raw[:8])
	if gen == 0 {
		gen = 1
	}
	if generation != gen || provider != m.Provider || !cleanAbsolute(cwd) || cwd != m.Cwd {
		return "", Conflict("native-history-context-mismatch")
	}
	return NativeHistoryProfile(stateRoot, ref)
}

func appendEnvOrigin(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			out = append(out, entry)
		}
	}
	return append(out, key+"="+value)
}

// NativeConfigurationOrigin resolves ordinary profile custody without reading auth.
func NativeConfigurationOrigin(provider string, env []string) (string, error) {
	if provider != "codex" && provider != "claude" {
		return "", Conflict("unsupported-provider")
	}
	alias, err := snapshotClaudeSourceAlias(originalProfile(provider, env))
	if err != nil || alias.Absent {
		return "", Conflict("unsafe-source-profile")
	}
	return alias.Canonical, nil
}

func HasNativeContext(stateRoot, ref string) (bool, error) {
	m, err := readManifest(stateRoot, ref)
	return m.SchemaVersion == 3, err
}

// MatchingInstalledContext permits read-only reuse alongside isolated quota
// workers. The caller still serializes profile configuration and revalidates
// the complete installed context before executing a native process.
func MatchingInstalledContext(profile, provider, generation string) bool {
	if PendingNativeContextUpdate(profile, provider) {
		return false
	}
	name := ClaudeContextMarker
	if provider == "codex" {
		name = CodexContextMarker
	}
	path := filepath.Join(profile, name)
	raw, err := readRegular(path, maxSourceBytes)
	if err != nil || privateProjectionFile(path) != nil {
		return false
	}
	if provider == "codex" {
		var c CodexContext
		return json.Unmarshal(raw, &c) == nil && validCodexContext(c) && c.GlobalGeneration == generation
	}
	if provider == "claude" {
		var c ClaudeContext
		return json.Unmarshal(raw, &c) == nil && validClaudeContext(c) && c.GlobalGeneration == generation
	}
	return false
}

func PendingNativeContextUpdate(profile, provider string) bool {
	name := ".swarm-claude-context-update.json"
	if provider == "codex" {
		name = ".swarm-codex-context-update.json"
		if _, err := os.Lstat(filepath.Join(profile, ".swarm-codex-stock-skills-update.json")); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	_, err := os.Lstat(filepath.Join(profile, name))
	return !errors.Is(err, os.ErrNotExist)
}
