package skeleton

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/setuptrust"
)

// After worktree resolution the path in spec.Cwd is the provider's actual cwd.
func (d *Daemon) finalizeAgentLaunch(id string, spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
	spec, err := d.prepareLoginLaunch(id, spec)
	if err != nil || spec.AgentType != "agy" || spec.CLIIdentity == nil || spec.CLIIdentity.Version != "1.5.13" {
		return spec, err
	}
	if cwd, enabled := setuptrust.Workspace(spec.Cwd); enabled {
		spec.Argv = agyInlineSetting(spec.Argv, "trustedWorkspaces", []string{cwd})
	}
	return spec, nil
}

// The shim has not released its TUI yet. Native Codex owns config writes and
// evaluates hashes again when loading a hook, including after a concurrent edit.
func (d *Daemon) prepareCodexLaunchTrust(id, instance, userAgent string, conn backendConn) {
	if version, ok := codex.New().ParseVersion(userAgent); d.core == nil || !ok || version != "0.171.0" || instance == "" {
		return
	}
	meta, ok := d.core.Get(id)
	if !ok {
		return
	}
	cwd, ok := setuptrust.Workspace(meta.ProviderCwd())
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	valid := func() bool {
		current, live := d.sessionInstance(id)
		_, enabled := setuptrust.Workspace(cwd)
		return enabled && live && current == instance
	}
	if err := applyCodexLaunchTrust(ctx, conn, cwd, valid); err != nil {
		log.Printf("skeleton: Codex setup for %s needs attention. Open the session to finish setup.", id)
	}
}

type launchHook struct {
	Key         string
	Source      string
	CurrentHash string
	TrustStatus string
	Enabled     bool
}

func applyCodexLaunchTrust(ctx context.Context, conn backendConn, cwd string, valid func() bool) error {
	var config struct {
		Config struct {
			Projects map[string]struct {
				TrustLevel string `json:"trust_level"`
			}
		}
	}
	if err := conn.Call(ctx, "config/read", map[string]any{"cwd": cwd, "includeLayers": false}, &config); err != nil {
		return err
	}
	write := func(key string, value any) error {
		if !valid() {
			return errors.New("workspace setup consent changed")
		}
		var result struct{ Status string }
		err := conn.Call(ctx, "config/batchWrite", map[string]any{
			"edits":            []map[string]any{{"keyPath": key, "value": value, "mergeStrategy": "upsert"}},
			"reloadUserConfig": true,
		}, &result)
		if err == nil && result.Status != "ok" {
			return errors.New("Codex could not apply startup settings")
		}
		return err
	}
	if config.Config.Projects[cwd].TrustLevel != "trusted" {
		if err := write("projects", map[string]any{cwd: map[string]string{"trust_level": "trusted"}}); err != nil {
			return err
		}
	}
	list := func() ([]launchHook, error) {
		var result struct {
			Data []struct {
				Cwd    string
				Hooks  []launchHook
				Errors []json.RawMessage
			}
		}
		if err := conn.Call(ctx, "hooks/list", map[string]any{"cwds": []string{cwd}}, &result); err != nil {
			return nil, err
		}
		if len(result.Data) != 1 || result.Data[0].Cwd != cwd || len(result.Data[0].Errors) != 0 || result.Data[0].Hooks == nil || len(result.Data[0].Hooks) > 256 {
			return nil, errors.New("Codex hook inventory could not be verified")
		}
		return result.Data[0].Hooks, nil
	}
	hooks, err := list()
	if err != nil {
		return err
	}
	updates := make(map[string]map[string]string)
	for _, h := range hooks {
		if !h.Enabled || h.TrustStatus != "untrusted" && h.TrustStatus != "modified" {
			continue
		}
		switch h.Source {
		case "user", "project", "plugin":
		default:
			return errors.New("unknown hook source")
		}
		hash, err := hex.DecodeString(strings.TrimPrefix(h.CurrentHash, "sha256:"))
		if h.Key == "" || len(h.Key) > 4096 || !strings.HasPrefix(h.CurrentHash, "sha256:") || err != nil || len(hash) != 32 || updates[h.Key] != nil {
			return errors.New("invalid hook identity")
		}
		updates[h.Key] = map[string]string{"trusted_hash": h.CurrentHash}
	}
	if len(updates) == 0 {
		return nil
	}
	if err := write("hooks.state", updates); err != nil {
		return err
	}
	reloaded, err := list()
	if err != nil {
		return err
	}
	for _, h := range reloaded {
		if identity := updates[h.Key]; identity != nil && identity["trusted_hash"] == h.CurrentHash && h.TrustStatus == "trusted" {
			delete(updates, h.Key)
		}
	}
	if len(updates) != 0 {
		return errors.New("hook changed during startup")
	}
	return nil
}
