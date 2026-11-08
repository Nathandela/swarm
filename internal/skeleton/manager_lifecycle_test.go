package skeleton

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func managerLifecycleMeta(m persist.Meta) persist.Meta {
	m.Role = "manager"
	m.ManagerConversationID = "manager-conversation"
	m.ManagerBridgeSocket = "/private/bridge"
	m.ManagerAuthFile = "/private/auth"
	return m
}

func TestManagerLifecycleAuthChangeDefersBeforeKill(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			f := newAuthFake(identityB)
			m := managerLifecycleMeta(runningCodex("manager", identityA, status.TurnIdle, migratedConversationID))
			m.AgentType = provider
			f.add(m)
			w := testWatcher(t, f)
			w.agents = []string{provider}
			w.state.Identities[provider] = identityA
			w.tick()
			if len(f.killed) != 0 || len(f.launched) != 0 || w.state.Killed[m.ID] || f.sessions[m.ID].Status.Process != status.ProcessRunning {
				t.Fatal("automatic account recovery destroyed or replaced manager")
			}
			if !w.authPending(provider, m.ID) {
				t.Fatal("stale manager was reported reconciled")
			}
		})
	}
}

func TestManagerLifecycleCLIRefreshDefersIncludingUnqualifiedClaude(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			w, f, old, next := acceptanceRefreshRig(t)
			m := managerLifecycleMeta(*f.sessions["source"])
			m.AgentType = provider
			if provider == "claude" {
				old.Version = "2.1.319"
				next.Version = "2.1.320"
			}
			f.add(m)
			w.agents = []string{provider}
			for i := 0; i < 6; i++ {
				w.tick()
			}
			if len(f.killed) != 0 || len(f.launched) != 0 || w.state.Killed[m.ID] {
				t.Fatal("CLI refresh stopped a manager without current controller authority")
			}
			rec := w.state.CLI[m.ID]
			if rec.State != cliRefreshBlocked || !strings.Contains(rec.LastError, "controller reconciliation") {
				t.Fatalf("manager refresh hold is invisible: %+v", rec)
			}
		})
	}
}

func TestManagerLifecycleLoginSwitchPreflightsWholeFleet(t *testing.T) {
	f := newLoginFixture(t)
	f.rows["source-2"] = managerLifecycleMeta(f.rows["source-2"])
	before, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); err == nil || !strings.Contains(err.Error(), "controller reconciliation") {
		t.Fatalf("manager fleet switch was not deferred: %v", err)
	}
	for i := 0; i < 4; i++ {
		f.m.step()
	}
	after, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || len(after.Switches) != 0 || after.Providers[logins.ProviderCodex].Epoch != before.Providers[logins.ProviderCodex].Epoch {
		t.Fatal("deferred switch mutated account state")
	}
	for _, m := range f.rows {
		if m.Status.Process != status.ProcessRunning {
			t.Fatal("fleet member stopped before manager preflight")
		}
	}
}

func TestManagerLifecycleRestoredSwitchDefersBeforeAnyFleetStop(t *testing.T) {
	f := newLoginFixture(t)
	op, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit")
	if err != nil {
		t.Fatal(err)
	}
	f.rows["source-2"] = managerLifecycleMeta(f.rows["source-2"])
	for i := 0; i < 4; i++ {
		f.m.step()
	}
	after, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Providers[op.Provider].Epoch != f.runtime.Epoch || !strings.Contains(after.Switches[op.Provider].LastError, "controller reconciliation") {
		t.Fatal("restored manager switch was not visibly held")
	}
	for _, m := range f.rows {
		if m.Status.Process != status.ProcessRunning {
			t.Fatal("restored switch stopped another fleet member first")
		}
	}
}

func TestManagerLifecycleInputRequiresCurrentLoginBinding(t *testing.T) {
	f := newLoginFixture(t)
	meta := managerLifecycleMeta(f.rows["source-1"])
	meta.LoginRuntime = nil
	meta.AuthIdentity = identityA
	f.rows[meta.ID] = meta
	f.m.d.authw.sessionIdentity = func(string, []string) string { return identityA }
	if err := f.m.inputGate(meta.ID); err != nil {
		t.Fatalf("current native manager refused: %v", err)
	}
	f.m.d.authw.sessionIdentity = func(string, []string) string { return identityB }
	if err := f.m.inputGate(meta.ID); err == nil || !strings.Contains(err.Error(), "controller reconciliation") {
		t.Fatalf("stale native manager accepted dispatch: %v", err)
	}
	f.m.d.authw.sessionIdentity = func(string, []string) string { return identityA }
	if err := f.m.inputGate(meta.ID); err == nil {
		t.Fatal("returning account silently renewed old manager grant")
	}
	meta.ID = "fresh-manager"
	runtime := f.runtime
	runtime.Epoch++
	meta.LoginRuntime = &runtime
	f.rows[meta.ID] = meta
	if err := f.m.inputGate(meta.ID); err == nil {
		t.Fatal("old managed manager epoch accepted dispatch")
	}
	meta.Role = ""
	meta.ID = "ordinary-worker"
	meta.LoginRuntime = nil
	meta.AuthIdentity = identityA
	f.rows[meta.ID] = meta
	f.m.d.authw.sessionIdentity = func(string, []string) string { return identityB }
	if err := f.m.inputGate(meta.ID); err != nil {
		t.Fatalf("ordinary worker admission changed: %v", err)
	}
}

func TestManagerLifecycleAuthQuarantineDoesNotReuseGrantWhenAccountReturns(t *testing.T) {
	f := newLoginFixture(t)
	meta := managerLifecycleMeta(f.rows["source-1"])
	meta.LoginRuntime = nil
	meta.AuthIdentity = identityA
	f.rows[meta.ID] = meta
	w := f.m.d.authw
	w.warned = map[string]bool{}
	if !w.holdManagerRestart(meta, true) || f.rows[meta.ID].ResumeError == "" {
		t.Fatal("manager quarantine was not retained in its status")
	}
	w.sessionIdentity = func(string, []string) string { return identityA }
	if err := f.m.inputGate(meta.ID); err == nil {
		t.Fatal("returning account implicitly renewed stale manager authority")
	}
}

func TestManagerLifecycleDoesNotBlockOtherProviderSwitch(t *testing.T) {
	f := newLoginFixture(t)
	other := managerLifecycleMeta(f.rows["source-1"])
	other.ID = "other-provider-manager"
	other.AgentType = "claude"
	other.LoginRuntime = nil
	f.rows[other.ID] = other
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); err != nil {
		t.Fatalf("unrelated provider blocked: %v", err)
	}
}
