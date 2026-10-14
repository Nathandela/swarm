package daemon

import (
	"path/filepath"
	"testing"
)

// TestBackendLaunch_AManagedCodexBackendKeepsTheSwarmSocketReachable pins ADR-010 F2 for
// account-managed sessions too: without -c sandbox_workspace_write.network_access=true the
// workspace-write sandbox blocks connect() to the daemon socket, so `swarm handoff`, `swarm
// spawn` and passive supervision fail inside every rotator-managed Codex discussion (A4,
// measured with codex 0.162.1).
// POLICY CONFLICT: internal/skeleton/accounts_launch_test.go:248 pins the opposite (v0.16.0,
// "network access follows native project/user policy"). Needs an ADR decision.
func TestBackendLaunch_AManagedCodexBackendKeepsTheSwarmSocketReachable(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.AgentType = "codex"
	spec.AccountNativeContext = true
	spec.Backend = r7BackendSpec(filepath.Join(t.TempDir(), "codex.sock"))
	spec.Backend.Args = append(spec.Backend.Args, "-c", "sandbox_workspace_write.network_access=true")

	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { killTree(m.ShimPID) })

	args, _ := r7ReadLaunchConfig(t, d, m.ID)["backend_args"].([]any)
	for _, a := range args {
		if a == "sandbox_workspace_write.network_access=true" {
			return
		}
	}
	t.Errorf("backend_args = %v: the managed app-server lost the network override, so the agent's "+
		"sandbox cannot reach the daemon socket", args)
}
