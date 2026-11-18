package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

// ManagerNativeIdentityForEnv reads an account/org digest, never native tokens.
// Claude's manager-only preferences identity is not a global worker AuthProbe.
func ManagerNativeIdentityForEnv(agent string, env []string) string {
	if !daemon.ManagerNativeEnvironmentSupported(agent, env) {
		return ""
	}
	if agent != "claude" {
		return AuthIdentityForEnv(agent, env)
	}
	home, config := "", ""
	seen := map[string]bool{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if key != "HOME" && key != "CLAUDE_CONFIG_DIR" {
			continue
		}
		if !ok || seen[key] {
			return ""
		}
		seen[key] = true
		if key == "HOME" {
			home = value
		} else {
			config = value
		}
	}
	if config == "" {
		if home == "" {
			var err error
			home, err = authHomeDir()
			if err != nil {
				return ""
			}
		}
		config = home
	}
	if !filepath.IsAbs(config) || filepath.Clean(config) != config || strings.ContainsFunc(config, unicode.IsControl) {
		return ""
	}
	physical, err := filepath.EvalSymlinks(config)
	if err != nil || physical != config {
		return ""
	}
	dir, err := os.Lstat(config)
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0022 != 0 {
		return ""
	}
	owner, ok := dir.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return ""
	}
	path := filepath.Join(config, ".claude.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return ""
	}
	fileOwner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || fileOwner.Uid != uint32(os.Getuid()) || fileOwner.Nlink != 1 {
		return ""
	}
	raw, err := readCredentials(path)
	if err != nil {
		return ""
	}
	id, err := logins.ClaudeIdentity(raw)
	if err != nil {
		return ""
	}
	return id
}

// ManagerNativeIdentity reuses the exact private native origin saved at launch.
func ManagerNativeIdentity(meta persist.Meta) string {
	if meta.ResumeError != "" {
		return ""
	}
	return ManagerNativeIdentityForEnv(meta.AgentType, managerIdentityEnvironment(meta))
}

func managerIdentityEnvironment(meta persist.Meta) []string {
	env := make([]string, 0, len(meta.Env)+1)
	for _, entry := range meta.Env {
		if meta.AgentType == "claude" && strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
			continue
		}
		env = append(env, entry)
	}
	if meta.AgentType == "claude" && meta.ManagerClaudeConfigDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+meta.ManagerClaudeConfigDir)
	}
	return env
}

func prepareManagerNativeIdentity(spec *daemon.LaunchSpec) error {
	if spec.Manager == nil {
		return nil
	}
	if spec.Manager.UnsupportedOrigin {
		return errors.New("manager native account configuration is unsupported; select the native CLI account without credential or backend overrides")
	}
	if spec.LoginRuntime != nil {
		return nil
	}
	env := append([]string(nil), spec.ClientEnv...)
	if spec.AgentType == "claude" && spec.Manager.ClaudeConfigDir != nil {
		env = managerIdentityEnvironment(persist.Meta{AgentType: spec.AgentType, Env: env, ManagerClaudeConfigDir: *spec.Manager.ClaudeConfigDir})
	}
	id := ManagerNativeIdentityForEnv(spec.AgentType, env)
	if id == "" {
		return errors.New("manager native account identity is unavailable; sign in to the selected native CLI configuration and reconcile the manager before launching")
	}
	spec.AuthIdentity = id
	return nil
}
