//go:build linux

package skeleton

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func enrollmentRig(t *testing.T, provider string) (*authWatcher, *authFake, *accountManager, persist.Meta) {
	t.Helper()
	manager := accountTestManager(t, accountTestState(t))
	accountTestAdmit(t, manager, accountTestCandidate(t, manager, provider, "enrollment-owner"))
	if _, err := manager.store.SetEnabled(accountTestRegistry(t, manager).Revision, provider, true); err != nil {
		t.Fatal(err)
	}
	accountTestPut(t, filepath.Join(manager.stateRoot, "account-onboarding.json"), []byte(fmt.Sprintf(`{%q:true}`, provider)))
	home, cwd, env := accountTestLaunchEnvironment(t, manager.stateRoot)
	native := filepath.Join(manager.stateRoot, "bin", provider)
	version := "codex-cli 0.160.0"
	if provider == "claude" {
		version = "2.1.289 (Claude Code)"
	}
	accountTestPut(t, native, []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", version)))
	if err := os.Chmod(native, 0700); err != nil {
		t.Fatal(err)
	}
	env[1] = "PATH=" + filepath.Dir(native) + ":/usr/bin:/bin"
	identity, err := probeCLIIdentity(provider, native, env, cwd)
	if err != nil {
		t.Fatal(err)
	}
	source := persist.Meta{ID: "legacy-source", AgentType: provider, Cwd: cwd, Env: env, ConversationID: legacyCodexRootID, CreatedAt: legacyCreatedAt, CLIIdentity: identity, Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}}
	if provider == "codex" {
		writeCodexHistory(t, home, source.ConversationID, cwd, legacyCreatedAt, "", "cli", `{"type":"turn_context","payload":{"model":"gpt-fixture"}}`+"\n")
	} else {
		writeClaudeHistory(t, home, source.ConversationID, cwd, legacyCreatedAt, "", "")
	}
	f := newAuthFake("")
	f.add(source)
	w := testWatcher(t, f)
	w.stateDir = manager.stateRoot
	w.state = emptyAuthWatchState()
	w.settled = true
	w.cliObserve = func(string) *persist.CLIIdentity { return identity }
	w.cliProbe = probeCLIIdentity
	w.resolve = lookPathIn
	core := accountTestCore(t, manager, nil)
	d := &Daemon{accounts: manager, core: core}
	d.api = &coreAPI{core: core, accounts: manager, endpointID: w.endpointID, historyResolver: newFilesystemResumeHistoryResolver(home, defaultResumeHistoryLimits)}
	w.accountRotation = &accountRotationManager{d: d, store: manager.store}
	launch := w.launch
	w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		m, err := launch(spec)
		if err != nil {
			return m, err
		}
		m.AccountBinding, m.AccountProjectionRef, m.CLIIdentity = spec.AccountBinding, spec.AccountProjectionRef, spec.ExpectedCLIIdentity
		f.add(m)
		return m, nil
	}
	w.ready = func(persist.Meta) bool { return true }
	return w, f, manager, source
}

func TestAccountEnrollmentUsesExistingRecyclerForBothProviders(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			w, f, _, source := enrollmentRig(t, provider)
			w.discoverAccountEnrollments()
			rec := w.state.CLI[source.ID]
			if rec.Enrollment == nil || len(f.killed) != 0 || f.sessions[source.ID].AccountBinding != nil {
				t.Fatal("enrollment changed live credentials or failed to queue", rec)
			}
			// A restart must preserve the selected account before the first kill.
			loaded, err := loadAuthWatchStateChecked(w.stateDir)
			if err != nil {
				t.Fatal(err)
			}
			w.state = loaded
			for i := 0; i < 4; i++ {
				w.workCLIRefresh(true)
			}
			rec = w.state.CLI[source.ID]
			if len(f.killed) != 1 || len(f.launched) != 1 || rec.State != cliRefreshComplete {
				t.Fatalf("enrollment did not finish once: %+v kills=%v launches=%d", rec, f.killed, len(f.launched))
			}
			child := f.sessions[rec.ReplacementID]
			if child.ConversationID != source.ConversationID || child.AccountBinding == nil || *child.AccountBinding != rec.Enrollment.Binding || f.sessions[source.ID].AccountBinding != nil {
				t.Fatal("enrollment lost conversation or changed old binding")
			}
		})
	}
}

// Rotation alone never onboards running unmanaged discussions: the separate
// daemon-side onboarding switch must be on for that provider (absent = off).
func TestAccountEnrollmentRequiresTheOnboardingSwitch(t *testing.T) {
	for name, raw := range map[string]string{"absent": "", "other-provider": `{"claude":true}`, "off": `{"codex":false}`, "malformed": `{"codex":`, "on": `{"codex":true}`} {
		t.Run(name, func(t *testing.T) {
			w, f, m, source := enrollmentRig(t, "codex")
			path := filepath.Join(m.stateRoot, "account-onboarding.json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if raw != "" {
				accountTestPut(t, path, []byte(raw))
			}
			w.discoverAccountEnrollments()
			queued := w.state.CLI[source.ID].Enrollment != nil
			if queued != (name == "on") || len(f.killed) != 0 {
				t.Fatalf("onboarding %s: queued=%v, rotation is on", name, queued)
			}
		})
	}
}

func TestAccountEnrollmentDefersUnsafeSourcesAndRechecksConfigurationBeforeKill(t *testing.T) {
	for _, reason := range []string{"active", "interaction", "controller", "unsupported", "downgrade", "unobserved", "disabled", "missing-history", "destination-denied"} {
		t.Run(reason, func(t *testing.T) {
			w, f, m, source := enrollmentRig(t, "codex")
			switch reason {
			case "active":
				f.sessions[source.ID].Status.Turn = status.TurnActive
			case "interaction":
				f.sessions[source.ID].Status.Interaction = status.InteractionPermission
			case "controller":
				w.unsafe = func(string) bool { return true }
			case "unsupported":
				id := *source.CLIIdentity
				id.Version = "0.159.9"
				f.sessions[source.ID].CLIIdentity = &id
			case "downgrade":
				id := *source.CLIIdentity
				id.Version = "0.162.1"
				f.sessions[source.ID].CLIIdentity = &id
				w.cliObserve = func(string) *persist.CLIIdentity { return &id }
			case "unobserved":
				w.cliObserve = func(string) *persist.CLIIdentity { return nil }
			case "disabled":
				_, _ = m.store.SetEnabled(accountTestRegistry(t, m).Revision, "codex", false)
			case "missing-history":
				for _, item := range source.Env {
					if len(item) > 5 && item[:5] == "HOME=" {
						if err := os.RemoveAll(filepath.Join(item[5:], ".codex", "sessions")); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			w.discoverAccountEnrollments()
			if reason == "destination-denied" {
				plan := w.state.CLI[source.ID].Enrollment
				if plan == nil {
					t.Fatal("fixture did not queue")
				}
				registry := accountTestRegistry(t, m)
				quota := registry.Accounts[plan.Binding.AccountID].Quota
				observation := accounts.Observation{FeedGeneration: quota.FeedGeneration + 1, Sequence: 1, EventID: "destination-denied", ReceivedAt: w.clock(), Terminal: true, Scopes: []accounts.ScopeObservation{{Scope: accounts.ScopeGlobal, Authority: accounts.AuthorityDenied}}}
				if _, err := m.store.Observe(registry.Revision, plan.Binding, observation); err != nil {
					t.Fatal(err)
				}
			}
			w.workCLIRefresh(true)
			if len(f.killed) != 0 || len(f.launched) != 0 {
				t.Fatal("unsafe source was interrupted", reason)
			}
		})
	}
}

// Owner configuration drift after the preflight no longer defers onboarding:
// the recheck follows the owner configuration and the source is enrolled.
func TestAccountEnrollmentFollowsOwnerConfigurationChangeBeforeKill(t *testing.T) {
	w, f, _, source := enrollmentRig(t, "codex")
	w.discoverAccountEnrollments()
	if w.state.CLI[source.ID].Enrollment == nil {
		t.Fatal("fixture did not queue")
	}
	for _, item := range source.Env {
		if len(item) > 5 && item[:5] == "HOME=" {
			accountTestPut(t, filepath.Join(item[5:], ".codex", "config.toml"), []byte("model=\"changed\"\n"))
		}
	}
	w.workCLIRefresh(true)
	if len(f.killed) != 1 {
		t.Fatal("owner configuration drift deferred onboarding")
	}
}

func TestAccountEnrollmentUpgradesPendingCLIRefreshAndHonorsDisableBeforeKill(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(fmt.Sprint(disable), func(t *testing.T) {
			w, f, m, source := enrollmentRig(t, "codex")
			// A CLI update was discovered before the owner enabled account rotation.
			target := *source.CLIIdentity
			target.Version = "0.162.1"
			w.state.CLI[source.ID] = cliRefreshRecord{AgentType: "codex", Target: target, State: cliRefreshPending}
			if !w.prepareCLIRefreshTarget(source) || w.state.CLI[source.ID].Enrollment == nil {
				t.Fatal("existing refresh did not prepare managed successor")
			}
			if disable {
				if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, "codex", false); err != nil {
					t.Fatal(err)
				}
			}
			w.workCLIRefresh(false) // Pool enrollment works independently of CLI-refresh opt-out.
			if disable && len(f.killed) != 0 {
				t.Fatal("disabled pool interrupted source")
			}
			if !disable && (len(f.killed) != 1 || len(f.launched) != 1) {
				t.Fatalf("enrollment did not reuse pending refresh: %v %d", f.killed, len(f.launched))
			}
		})
	}
}

func TestAccountEnrollmentAcceptsNewerCodexSourceAndTarget(t *testing.T) {
	w, f, _, source := enrollmentRig(t, "codex")
	path := source.CLIIdentity.Path
	accountTestPut(t, path, []byte("#!/bin/sh\nprintf 'codex-cli 0.162.1\\n'\n"))
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := probeCLIIdentity("codex", path, source.Env, source.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	f.sessions[source.ID].CLIIdentity = identity
	w.cliObserve = func(string) *persist.CLIIdentity { return identity }
	w.discoverAccountEnrollments()
	rec := w.state.CLI[source.ID]
	if rec.Enrollment == nil || rec.Target.Version != "0.162.1" || len(f.killed) != 0 {
		t.Fatalf("newer Codex was not safely queued: %+v", rec)
	}
	for i := 0; i < 4; i++ {
		w.workCLIRefresh(true)
	}
	rec = w.state.CLI[source.ID]
	child := f.sessions[rec.ReplacementID]
	if len(f.killed) != 1 || len(f.launched) != 1 || rec.State != cliRefreshComplete || child.ConversationID != source.ConversationID || child.AccountBinding == nil || child.CLIIdentity.Version != "0.162.1" {
		t.Fatalf("newer Codex enrollment lost continuity: %+v", rec)
	}
}
