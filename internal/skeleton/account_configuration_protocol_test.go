//go:build linux

package skeleton

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func configurationOwnerClient(t *testing.T, api *coreAPI, root string) *protocol.Client {
	t.Helper()
	server := protocol.NewServer(api, testEndpoint)
	t.Cleanup(func() { _ = server.Close() })
	socket := filepath.Join(root, "o.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		if conn, err := listener.Accept(); err == nil {
			server.ServeConn(conn)
		}
	}()
	owner, err := protocol.Dial(socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	return owner
}

func configurationCustomOrigin(t *testing.T, provider, root string) (string, string) {
	t.Helper()
	origin := filepath.Join(root, "custom-"+provider)
	if err := os.MkdirAll(origin, 0700); err != nil {
		t.Fatal(err)
	}
	selector, filename, content := "CODEX_HOME", "config.toml", "model_reasoning_effort = \"medium\"\n"
	if provider == "claude" {
		selector, filename, content = "CLAUDE_CONFIG_DIR", "settings.json", `{"model":"owner-custom-model"}`
	}
	accountTestPut(t, filepath.Join(origin, filename), []byte(content))
	return origin, selector + "=" + origin
}

func configurationLaunchedMeta(t *testing.T, f directoryLaunchFixture, id string) persist.Meta {
	t.Helper()
	_, local, ok := protocol.ParseID(id)
	if !ok {
		t.Fatal("invalid protocol session id")
	}
	meta, ok := f.api.core.Get(local)
	if !ok {
		t.Fatal("protocol launch did not persist a session")
	}
	t.Cleanup(func() { _ = f.api.core.Delete(meta.ID) })
	return meta
}

func configurationCheckOrigin(t *testing.T, f directoryLaunchFixture, meta persist.Meta, origin string) {
	t.Helper()
	profile, err := f.manager.store.ProfilePath(*meta.AccountBinding)
	if err != nil {
		t.Fatal(err)
	}
	marker := accountconfig.CodexContextMarker
	if meta.AgentType == "claude" {
		marker = accountconfig.ClaudeContextMarker
	}
	raw, err := os.ReadFile(filepath.Join(profile, marker))
	var context struct {
		SourceProfile struct{ Path, Canonical string }
	}
	if err != nil || json.Unmarshal(raw, &context) != nil || context.SourceProfile.Path != origin || context.SourceProfile.Canonical != origin {
		t.Fatalf("assembled launch lost native custom origin: %v, recorded=%q want=%q", err, context.SourceProfile.Path, origin)
	}
	// Only the frozen projection records the source origin. The persisted launch
	// and shim configuration carry the selected account, never the transient input.
	for _, name := range []string{"meta.json", "shim-launch.json"} {
		raw, err := os.ReadFile(filepath.Join(f.manager.stateRoot, meta.ID, name))
		if err != nil || strings.Contains(string(raw), "AccountOriginalConfigurationEnv") || strings.Contains(string(raw), "CODEX_HOME="+origin) || strings.Contains(string(raw), "CLAUDE_CONFIG_DIR="+origin) {
			t.Fatalf("transient source selector reached %s: %v", name, err)
		}
	}
}

func TestAccountConfigurationOwnerProtocolPreservesCustomOriginAndResume(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			f := newDirectoryLaunchFixture(t, provider)
			childEnv := f.record + ".environment"
			version := "codex-cli 0.160.0"
			if provider == "claude" {
				version = "2.1.289 (Claude Code)"
			}
			native := filepath.Join(f.manager.stateRoot, provider)
			accountTestPut(t, native, []byte(fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%%s\\n' %q; exit; fi\nprintf '%%s\\n' \"$CODEX_HOME\" \"$CLAUDE_CONFIG_DIR\" \"$CLAUDE_CODE_NO_MODEL_FALLBACK\" > %q\nprintf '%%s\\n' \"$PWD\" > %q\nexec /bin/sleep 60\n", version, childEnv, f.record)))
			origin, selector := configurationCustomOrigin(t, provider, f.manager.stateRoot)
			env := append(append([]string(nil), f.env...), selector)
			if provider == "claude" {
				env = append(env, "CLAUDE_CODE_NO_MODEL_FALLBACK=false")
			}
			owner := configurationOwnerClient(t, f.api, f.manager.stateRoot)
			cwd := filepath.Join(f.manager.stateRoot, "project")
			id, _, err := owner.Launch(protocol.LaunchReq{Agent: provider, Cwd: cwd + "/", Env: env, Cols: 80, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			meta := configurationLaunchedMeta(t, f, id)
			f.check(t, meta, cwd, false)
			configurationCheckOrigin(t, f, meta, origin)
			configurationCheckChildProfile(t, f, meta, childEnv)
			if err := f.api.core.SetConversationID(meta.ID, migratedConversationID); err != nil {
				t.Fatal(err)
			}
			if err := f.api.core.Kill(meta.ID); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				stopped, ok := f.api.core.Get(meta.ID)
				if ok && stopped.Status.Process != status.ProcessRunning && verifyAccountWritersStopped(f.manager.stateRoot, stopped) == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("source fake native writers did not stop")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := os.Remove(f.record); err != nil {
				t.Fatal(err)
			}
			// Incoming origins/policy must lose to the source's persisted environment,
			// typed owner policy and frozen source context when resuming.
			incoming := append(append([]string(nil), f.env...), "CODEX_HOME=/incoming-unavailable", "CLAUDE_CONFIG_DIR=/incoming-unavailable", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=0", "CLAUDE_CODE_NO_MODEL_FALLBACK=true")
			id, _, err = owner.Launch(protocol.LaunchReq{Agent: provider, Cwd: cwd, Env: incoming, Options: map[string]string{protocol.OptionResumeFrom: id, "model": "gpt-explicit-fixture"}, Cols: 80, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			resumed := configurationLaunchedMeta(t, f, id)
			f.check(t, resumed, cwd, false)
			configurationCheckOrigin(t, f, resumed, origin)
			configurationCheckChildProfile(t, f, resumed, childEnv)
			if resumed.ResumedFrom != meta.ID {
				t.Fatal("resume lost source lineage")
			}
			if provider == "claude" && (resumed.AccountClaudeFallback == nil || resumed.AccountClaudeFallback.OwnerValue == nil || *resumed.AccountClaudeFallback.OwnerValue != "false") {
				t.Fatal("incoming fallback policy replaced the source owner policy")
			}
		})
	}
}

func configurationCheckChildProfile(t *testing.T, f directoryLaunchFixture, meta persist.Meta, path string) {
	t.Helper()
	profile, err := f.manager.store.ProfilePath(*meta.AccountBinding)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	index := 0
	if meta.AgentType == "claude" {
		index = 1
	}
	if err != nil || len(lines) != 3 || lines[index] != profile {
		t.Fatalf("assembled fake child lost selected private account profile: %v", err)
	}
	if meta.AgentType == "claude" && lines[2] != "false" {
		t.Fatal("source owner fallback did not reach the assembled fake child")
	}
}

func TestAccountConfigurationOwnerProtocolLegacyResumeUsesSourceCustomOrigin(t *testing.T) {
	f := manualResumeFixture(t)
	origin, _ := configurationCustomOrigin(t, "codex", f.manager.stateRoot)
	accountTestRollout(t, f.manager.store, *f.source.AccountBinding, f.source.Cwd, `{"type":"turn_context","payload":{"model":"gpt-source-resume-model"}}`+"\n")
	if err := os.Rename(filepath.Join(f.profile, "sessions"), filepath.Join(origin, "sessions")); err != nil {
		t.Fatal(err)
	}
	// Legacy metadata retains HOME, not CODEX_HOME. A native default-profile
	// alias is therefore the recoverable custom origin for this old discussion.
	alias := filepath.Join(filepath.Dir(filepath.Dir(f.source.CLIIdentity.Path)), "isolated-home", ".codex")
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(origin, alias); err != nil {
		t.Fatal(err)
	}
	f.source.AccountBinding, f.source.AccountProjectionRef = nil, ""
	f.source.LaunchOptions = nil
	api := f.api(t)
	owner := configurationOwnerClient(t, api, f.manager.stateRoot)
	_, _, err := owner.Launch(protocol.LaunchReq{Agent: "codex", Cwd: f.source.Cwd, Env: []string{"CODEX_HOME=/incoming-unavailable", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=0"}, Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, f.source.ID)}, Cols: 80, Rows: 24})
	if err == nil || !strings.Contains(err.Error(), f.stop.Error()) || !f.finalized {
		t.Fatalf("legacy resume did not reach managed preparation from source custom history: %v", err)
	}
	if f.accepted.Options["model"] != "gpt-source-resume-model" || f.accepted.AccountBinding == nil || !f.accepted.AccountNativeContext {
		t.Fatal("legacy resume lost source-native model or selected account")
	}
	raw, err := os.ReadFile(filepath.Join(f.profile, accountconfig.CodexContextMarker))
	var context struct {
		SourceProfile struct{ Path, Canonical string }
	}
	if err != nil || json.Unmarshal(raw, &context) != nil || context.SourceProfile.Path != alias || context.SourceProfile.Canonical != origin {
		t.Fatal("legacy source custom origin changed during managed enrollment")
	}
}

func TestAccountConfigurationOwnerProtocolRefusesSelectorsBeforeDestinationWrites(t *testing.T) {
	for _, selector := range []string{"XDG_CONFIG_HOME=false", "CLAUDE_CODE_REMOTE_SETTINGS_PATH=false", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=0", "CLAUDE_CODE_USE_ANTHROPIC_AWS=true", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD=true", "CLAUDE_CODE_USE_MANTLE=true"} {
		t.Run(strings.SplitN(selector, "=", 2)[0], func(t *testing.T) {
			f := newDirectoryLaunchFixture(t, "claude")
			t.Cleanup(func() {
				for _, meta := range f.api.core.List() {
					_ = f.api.core.Delete(meta.ID)
				}
			})
			owner := configurationOwnerClient(t, f.api, f.manager.stateRoot)
			profiles := filepath.Join(f.manager.stateRoot, "accounts", "profiles")
			projections := filepath.Join(f.manager.stateRoot, "accounts", "configurations")
			before, beforeProjections := configurationFenceSnapshot(t, profiles), configurationFenceSnapshot(t, projections)
			_, _, err := owner.Launch(protocol.LaunchReq{Agent: "claude", Cwd: filepath.Join(f.manager.stateRoot, "project"), Env: append(append([]string(nil), f.env...), selector), Cols: 80, Rows: 24})
			if err == nil || !strings.Contains(err.Error(), "provider-or-profile-selector") && !strings.Contains(err.Error(), "configuration-origin-not-characterized") {
				t.Fatalf("owner selector disappeared before managed admission: %v", err)
			}
			if len(f.api.core.List()) != 0 || !reflect.DeepEqual(before, configurationFenceSnapshot(t, profiles)) || !reflect.DeepEqual(beforeProjections, configurationFenceSnapshot(t, projections)) {
				t.Fatal("unsupported source selector wrote a destination profile or reserved a session")
			}
			if _, err := os.Stat(f.record); !os.IsNotExist(err) {
				t.Fatal("unsupported source selector started the fake provider")
			}
		})
	}
}

func TestAccountConfigurationOwnerProtocolNilUsesSavedOriginEmptyDoesNot(t *testing.T) {
	root := accountTestState(t)
	home, bin := filepath.Join(root, "saved-home"), filepath.Join(root, "saved-bin")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	origin, selector := configurationCustomOrigin(t, "codex", root)
	accountTestPut(t, filepath.Join(bin, "codex"), []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'codex-cli 0.160.0\\n'; exit; fi\nexec /bin/sleep 60\n"))
	if err := os.Chmod(filepath.Join(bin, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", strings.TrimPrefix(selector, "CODEX_HOME="))
	f := newDirectoryLaunchFixture(t, "codex")
	// Change live environment after Open: only the daemon's saved origin is valid.
	t.Setenv("CODEX_HOME", "/live-unavailable")
	owner := configurationOwnerClient(t, f.api, f.manager.stateRoot)
	cwd := filepath.Join(f.manager.stateRoot, "project")
	id, _, err := owner.Launch(protocol.LaunchReq{Agent: "codex", Cwd: cwd, Env: nil, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("nil owner environment did not use saved daemon origin: %v", err)
	}
	meta := configurationLaunchedMeta(t, f, id)
	t.Cleanup(func() { _ = f.api.core.Kill(meta.ID) })
	configurationCheckOrigin(t, f, meta, origin)
	before := configurationFenceSnapshot(t, filepath.Join(f.manager.stateRoot, "accounts"))
	_, _, err = owner.Launch(protocol.LaunchReq{Agent: "codex", Cwd: cwd, Env: []string{}, Cols: 80, Rows: 24})
	if err == nil || len(f.api.core.List()) != 1 || !reflect.DeepEqual(before, configurationFenceSnapshot(t, filepath.Join(f.manager.stateRoot, "accounts"))) {
		t.Fatalf("explicit empty environment inherited daemon source: %v", err)
	}
}
