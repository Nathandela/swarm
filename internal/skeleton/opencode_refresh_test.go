package skeleton

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func nativeRefreshRig(t *testing.T) (*authWatcher, *authFake, *persist.CLIIdentity) {
	t.Helper()
	w, f, old, target := acceptanceRefreshRig(t)
	old.Version = "1.19.3"
	old.Path = "/native/opencode"
	target.Version = "1.20.0"
	target.Path = old.Path
	m := f.sessions["source"]
	m.AgentType = "opencode"
	m.RequiredRuntime = persist.RequiredRuntimeOpenCode
	m.ConversationID = "ses_" + strings.Repeat("a", 32)
	m.AgentCwd = "/physical/worktree ü"
	m.OpenCodeBinding = &persist.OpenCodeBinding{Generation: strings.Repeat("b", 32), NativeCreatedAtMS: 123, Phase: persist.OpenCodePhaseReady}
	m.LaunchOptions = map[string]string{protocol.OptionWorktree: "true"}
	w.sessionIdentity = nil
	w.openCodeEnv = func(string) ([]string, error) { return []string{"XDG_CONFIG_HOME=/native/profile"}, nil }
	return w, f, target
}

func TestOpenCodeRefreshReportsUnsupportedInstallWithoutKilling(t *testing.T) {
	w, f, target := nativeRefreshRig(t)
	for i := 0; i < 4; i++ {
		w.tickCLIRefresh(true)
	}
	rec, ok := w.state.CLI["source"]
	if !ok || rec.State != cliRefreshBlocked || rec.Target != *target || !strings.Contains(rec.LastError, "1.19.3") {
		t.Fatalf("native installed change was silent: %+v", rec)
	}
	report, err := CLIRefreshStatus(w.stateDir)
	if err != nil || report.Frozen || len(report.Records) != 1 || report.Records[0].State != cliRefreshBlocked {
		t.Fatalf("blocked native target is not durable/readable: %+v %v", report, err)
	}
	if len(f.killed) != 0 || len(f.launched) != 0 || len(f.deleted) != 0 || f.sessions["source"].AgentCwd != "/physical/worktree ü" {
		t.Fatal("unsupported refresh touched live native worker or checkout")
	}
}

func TestOpenCodeRefreshRestoredClaimCannotKillOrRelaunch(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		w, f, target := nativeRefreshRig(t)
		w.state.CLI["source"] = cliRefreshRecord{AgentType: "opencode", Target: *target, State: cliRefreshPending}
		w.state.Killed["source"] = claimed
		w.workCLIRefresh(true)
		if len(f.killed) != 0 || len(f.launched) != 0 || w.state.CLI["source"].State != cliRefreshBlocked {
			t.Fatal("restored native refresh crossed unsupported lifecycle boundary")
		}
	}
}

func TestOpenCodeRefreshFinalFenceRefusesEvenWithoutRecord(t *testing.T) {
	w, f, _ := nativeRefreshRig(t)
	if err := w.validateCLIRefreshTarget(*f.sessions["source"]); err == nil {
		t.Fatal("native lifecycle has no final refusal")
	}
	w.recycle("opencode", *f.sessions["source"])
	if len(f.killed) != 0 || w.state.Killed["source"] {
		t.Fatal("final native fence allowed kill claim")
	}
}

func TestOpenCodeLegacyRefreshDoesNotProbeOwnerProfile(t *testing.T) {
	w, f, _ := nativeRefreshRig(t)
	f.sessions["source"].RequiredRuntime = ""
	w.cliProbe = func(string, string, []string, string) (*persist.CLIIdentity, error) {
		t.Fatal("unmanaged native row probed")
		return nil, errors.New("unexpected")
	}
	w.tickCLIRefresh(true)
	if len(w.state.CLI) != 0 {
		t.Fatal("legacy native worker acquired managed refresh state")
	}
}

func TestOpenCodeRefreshProbesOnlyCapturedProfileAndPhysicalCheckout(t *testing.T) {
	w, f, target := nativeRefreshRig(t)
	f.sessions["source"].Env = []string{"PATH=/native/bin", "XDG_CONFIG_HOME=/wrong/profile"}
	w.openCodeEnv = func(id string) ([]string, error) {
		if id != "source" {
			t.Fatal("wrong worker profile")
		}
		return []string{"XDG_CONFIG_HOME=/private/native/profile"}, nil
	}
	called := false
	w.cliProbe = func(agent, path string, env []string, cwd string) (*persist.CLIIdentity, error) {
		called = true
		if agent != "opencode" || path != "" || cwd != "/physical/worktree ü" || strings.Join(env, "|") != "PATH=/native/bin|XDG_CONFIG_HOME=/private/native/profile" {
			t.Fatal("native refresh probe escaped captured origin")
		}
		return target, nil
	}
	w.discoverCLIRefreshes()
	if !called {
		t.Fatal("native installation was not checked")
	}
}

func TestOpenCodeRefreshUnconfirmedProfileBlocksBeforeProbe(t *testing.T) {
	w, _, _ := nativeRefreshRig(t)
	w.openCodeEnv = func(string) ([]string, error) { return nil, errors.New("private details must not leak") }
	w.cliProbe = func(string, string, []string, string) (*persist.CLIIdentity, error) {
		t.Fatal("unconfirmed profile executed native CLI")
		return nil, nil
	}
	w.discoverCLIRefreshes()
	report, err := CLIRefreshStatus(w.stateDir)
	if err != nil || len(report.Records) != 1 || report.Records[0].State != cliRefreshBlocked || strings.Contains(report.Records[0].LastError, "private details") {
		t.Fatalf("profile refusal missing or leaked: %+v %v", report, err)
	}
}
