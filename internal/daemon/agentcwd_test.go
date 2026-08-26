package daemon

// FAILING-FIRST (TDD RED, GG-5) for the launch half of persist.Meta.AgentCwd.
//
// The PreLaunch hook (Epic 12's worktree isolation) already returns the directory the
// AGENT will run in, and launch() already overrides spec.Cwd with it so the shim spawns
// there. What it does NOT do is persist it: the meta keeps the caller's launch cwd, and
// the resolved directory is dropped on the floor the moment the function returns. That
// makes a worktree session's provider history directory uncomputable by anyone, the
// daemon included.
//
// So the value is stamped onto the meta at the one place it exists, immediately after
// the hook resolves, and Meta.Cwd is left exactly as it is -- rollback and delete both
// anchor worktree teardown to it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

// loadPersistedMeta reads the meta.json a launch committed, both as a decoded Meta and as
// the raw JSON object, so a test can assert on the durable key set and not only the
// in-memory value.
func loadPersistedMeta(t *testing.T, cfg Config, id string) (persist.Meta, map[string]json.RawMessage) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfg.StateDir, id, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json for %s: %v", id, err)
	}
	var m persist.Meta
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode meta.json for %s: %v", id, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("meta.json for %s is not a JSON object: %v", id, err)
	}
	return m, raw
}

// TestLaunchPersistsTheResolvedAgentCwd: when the pre-launch hook overrides the working
// directory, the override is committed as agent_cwd while cwd keeps the directory the
// launch was requested in. Both halves matter: the first makes the agent's own history
// findable, the second is what worktree teardown is anchored to.
func TestLaunchPersistsTheResolvedAgentCwd(t *testing.T) {
	repo := t.TempDir()
	agentDir := filepath.Join(repo, ".swarm", "worktrees", "resolved")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}

	cfg := daemonConfig(t)
	cfg.PreLaunch = func(string, LaunchSpec) (string, error) { return agentDir, nil }
	d := openDaemon(t, cfg)

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)
	spec.Cwd = repo
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { killTree(readPIDFile(t, pidFile)); killTree(m.ShimPID) })

	if m.AgentCwd != agentDir {
		t.Errorf("returned meta AgentCwd = %q, want the hook's resolved cwd %q", m.AgentCwd, agentDir)
	}
	if m.Cwd != repo {
		t.Errorf("returned meta Cwd = %q, want the launch cwd %q -- the override must not move it", m.Cwd, repo)
	}

	persisted, _ := loadPersistedMeta(t, cfg, m.ID)
	if persisted.AgentCwd != agentDir {
		t.Errorf("persisted agent_cwd = %q, want %q -- the resolved cwd exists only inside launch() "+
			"unless it is written down", persisted.AgentCwd, agentDir)
	}
	if persisted.Cwd != repo {
		t.Errorf("persisted cwd = %q, want %q", persisted.Cwd, repo)
	}
	if persisted.ProviderCwd() != agentDir {
		t.Errorf("persisted ProviderCwd() = %q, want %q", persisted.ProviderCwd(), agentDir)
	}
}

// TestLaunchWithoutACwdOverrideLeavesAgentCwdEmpty covers the ordinary launch two ways:
// no hook at all, and a registered hook that declines to override (which is exactly what
// preLaunchWorktree does for every session without the worktree flag). Both must leave
// agent_cwd empty -- present, because the on-disk key set is the durable contract, and
// empty, because there was no second directory.
func TestLaunchWithoutACwdOverrideLeavesAgentCwdEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook func(string, LaunchSpec) (string, error)
	}{
		{name: "no hook registered", hook: nil},
		{name: "hook declines to override", hook: func(string, LaunchSpec) (string, error) { return "", nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			cfg := daemonConfig(t)
			cfg.PreLaunch = tc.hook
			d := openDaemon(t, cfg)

			pidFile := filepath.Join(t.TempDir(), "agent.pid")
			spec := announceSpec(t, pidFile)
			spec.Cwd = cwd
			m, err := d.Launch(spec)
			if err != nil {
				t.Fatalf("Launch: %v", err)
			}
			t.Cleanup(func() { killTree(readPIDFile(t, pidFile)); killTree(m.ShimPID) })

			persisted, raw := loadPersistedMeta(t, cfg, m.ID)
			if persisted.AgentCwd != "" {
				t.Errorf("persisted agent_cwd = %q, want empty for a launch with no cwd override", persisted.AgentCwd)
			}
			if persisted.Cwd != cwd {
				t.Errorf("persisted cwd = %q, want %q", persisted.Cwd, cwd)
			}
			if persisted.ProviderCwd() != cwd {
				t.Errorf("persisted ProviderCwd() = %q, want the launch cwd %q", persisted.ProviderCwd(), cwd)
			}
			got, ok := raw["agent_cwd"]
			if !ok {
				t.Fatalf("meta.json missing key %q (the on-disk key set is the durable contract)", "agent_cwd")
			}
			if string(got) != `""` {
				t.Errorf("meta.json agent_cwd = %s, want an empty string", got)
			}
		})
	}
}
