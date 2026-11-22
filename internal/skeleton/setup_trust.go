package skeleton

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/setuptrust"
)

// After worktree resolution the path in spec.Cwd is the provider's actual cwd.
func (d *Daemon) finalizeAgentLaunch(id string, spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
	spec, err := d.prepareLoginLaunch(id, spec)
	if err != nil || spec.AgentType != "agy" || spec.CLIIdentity == nil || spec.CLIIdentity.Version != "1.5.13" {
		return spec, err
	}
	if consent, enabled := setuptrust.Get(spec.Cwd); enabled {
		spec.Argv = agyInlineSetting(spec.Argv, "trustedWorkspaces", []string{consent.Workspace})
	}
	return spec, nil
}

// The shim has not released its TUI yet. Native Codex owns config writes and
// evaluates hashes again when loading a hook, including after a concurrent edit.
func (d *Daemon) prepareCodexLaunchTrust(id, instance, userAgent string, conn backendConn) {
	if d.core == nil || instance == "" {
		return
	}
	meta, ok := d.core.Get(id)
	if !ok {
		return
	}
	consent, ok := setuptrust.Get(meta.ProviderCwd())
	if !ok {
		return
	}
	if !qualifiedCodexSetup(userAgent) {
		log.Printf("skeleton: Open session %s to finish Codex setup. Automatic setup is unavailable for this version.", id)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	valid := func() bool {
		current, live := d.sessionInstance(id)
		now, enabled := setuptrust.Get(consent.Workspace)
		return enabled && now == consent && live && current == instance
	}
	if err := applyCodexLaunchTrust(ctx, conn, consent, valid); err != nil {
		log.Printf("skeleton: Codex setup for %s: %v. Open the session to finish setup.", id, err)
	}
}

// initialize names this client "swarm". Later user-agent fields describe the OS
// and terminal and cannot qualify the running app-server.
func qualifiedCodexSetup(userAgent string) bool {
	product, _, _ := strings.Cut(userAgent, " ")
	return product == "swarm/0.171.0"
}

type launchHook struct {
	Key         string
	Source      string
	SourcePath  string
	CurrentHash string
	TrustStatus string
	Enabled     bool
}

func applyCodexLaunchTrust(ctx context.Context, conn backendConn, consent setuptrust.Consent, valid func() bool) error {
	cwd := consent.Workspace
	var config struct {
		Config struct {
			Projects map[string]struct {
				TrustLevel string `json:"trust_level"`
			}
		}
	}
	if err := conn.Call(ctx, "config/read", map[string]any{"cwd": cwd, "includeLayers": false}, &config); err != nil {
		return errors.New("could not read workspace settings")
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
		if err != nil || result.Status != "ok" {
			return errors.New("could not apply Codex startup settings")
		}
		return nil
	}
	if config.Config.Projects[cwd].TrustLevel != "trusted" {
		if err := write("projects."+strconv.Quote(cwd)+".trust_level", "trusted"); err != nil {
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
			return nil, errors.New("could not check startup hooks")
		}
		if len(result.Data) != 1 || result.Data[0].Cwd != cwd || len(result.Data[0].Errors) != 0 || result.Data[0].Hooks == nil || len(result.Data[0].Hooks) > 256 {
			return nil, errors.New("could not verify Codex hook inventory")
		}
		return result.Data[0].Hooks, nil
	}
	hooks, err := list()
	if err != nil {
		return err
	}
	var pending error
	updates := make(map[string]map[string]string)
	for _, h := range hooks {
		if !h.Enabled {
			continue
		}
		switch h.TrustStatus {
		case "trusted", "managed":
			continue
		case "modified", "untrusted":
			// The current hook fingerprint must be checked below.
		default:
			return errors.New("unrecognized hook status needs your review")
		}
		switch h.Source {
		case "user", "plugin":
			if !consent.ProfileHooks {
				pending = errors.New("profile hooks need your review")
				continue
			}
		case "project":
			sourcePath, err := filepath.EvalSymlinks(h.SourcePath)
			if err != nil || !filepath.IsAbs(sourcePath) {
				return errors.New("hook source could not be verified")
			}
			rel, err := filepath.Rel(cwd, sourcePath)
			if err != nil || !filepath.IsLocal(rel) {
				pending = errors.New("a hook outside this workspace needs your review")
				continue
			}
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
		return pending
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
	return pending
}
