package daemon

import (
	"path/filepath"
	"testing"
)

// TestBackendLaunch_AnUnmanagedBackendRunsInTheSessionCwdNotTheStateDir pins where the session
// backend runs. Codex records the app-server's process cwd as the thread's cwd and roots the
// workspace-write sandbox there, so a backend left in <stateDir>/<id> makes every unmanaged
// rollout name the state dir (91 of 92 live rollouts), lets the agent write its own meta.json,
// and defeats resume-history's cwd match. Managed launches already carry spec.Cwd; unmanaged
// ones must too.
func TestBackendLaunch_AnUnmanagedBackendRunsInTheSessionCwdNotTheStateDir(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)
	spec.AgentType = "codex"
	spec.Backend = r7BackendSpec(filepath.Join(t.TempDir(), "codex.sock"))
	if spec.LoginRuntime != nil {
		t.Fatal("fixture must be an unmanaged launch")
	}

	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// The backend program does not exist, so the shim never execs the agent before cleanup.
	t.Cleanup(func() { killTree(m.ShimPID) })

	raw := r7ReadLaunchConfig(t, d, m.ID)
	if got, _ := raw["backend_cwd"].(string); got != spec.Cwd {
		t.Errorf("backend_cwd = %q, want the explicit session cwd %q rather than depending on legacy fallback from %q",
			got, spec.Cwd, d.sessionDir(m.ID))
	}
}

// TestBackendLaunch_AManagedCodexBackendKeepsTheSwarmSocketReachable pins ADR-010 F2 for
// account-managed sessions too: without -c sandbox_workspace_write.network_access=true the
// workspace-write sandbox blocks connect() to the daemon socket, so `swarm handoff`, `swarm
// spawn` and passive supervision fail inside every rotator-managed Codex discussion (A4,
// measured with codex 0.162.1).
func TestBackendLaunch_AManagedCodexBackendKeepsTheSwarmSocketReachable(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.AgentType = "codex"
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
