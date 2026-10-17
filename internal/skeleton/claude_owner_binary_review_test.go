//go:build linux

package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestManagedClaudeActualLaunchUsesTheOwnerBinary(t *testing.T) {
	buildBinaries(t)
	m, env, source, alias := nativeRetentionFixture(t)
	cwd := accountTestProject(t)
	home := strings.TrimPrefix(env[0], "HOME=")
	record := filepath.Join(m.stateRoot, "selected-child.json")
	accountTestPut(t, filepath.Join(home, ".retention-fixture-record"), []byte(record))
	accountTestPut(t, filepath.Join(home, ".claude", "settings.json"), []byte(`{"model":"claude-sonnet-4-6","hooks":{}}`))
	account := accountTestAdmit(t, m, accountTestCandidate(t, m, accounts.ProviderClaude, "owner-actor"))
	if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, accounts.ProviderClaude, true); err != nil {
		t.Fatal(err)
	}
	upgradeOwnerClaude(t, source, alias)
	owner, err := probeCLIIdentity(accounts.ProviderClaude, "", env, cwd)
	if err != nil {
		t.Fatal(err)
	}
	// A cli-refresh owner relaunch with rotation on resolves argv0 through the
	// managed resolver; its probe must equal the refresh's expected identity.
	unbound := runningCodex("ended-owner", identityA, status.TurnIdle, migratedConversationID)
	unbound.AgentType, unbound.Cwd, unbound.Env, unbound.CLIIdentity = accounts.ProviderClaude, cwd, env, &persist.CLIIdentity{Path: alias, Version: "2.1.289", Fingerprint: "older"}
	unbound.Status.Process = status.ProcessExited
	store, err := persist.NewStore(m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(unbound); err != nil {
		t.Fatal(err)
	}
	assembly := &Daemon{accounts: m}
	core := accountTestCore(t, m, func(cfg *daemon.Config) {
		cfg.ShimBinary = swarmBin
		cfg.FinalizeLaunch = assembly.prepareAccountLaunch
	})
	api := newCoreAPI(core, "", testEndpoint)
	api.accounts = m
	t.Cleanup(api.close)
	relaunch := daemon.LaunchSpec{AgentType: accounts.ProviderClaude, Cwd: cwd, ClientEnv: env, Options: map[string]string{protocol.OptionResumeFrom: testEndpoint + "/" + unbound.ID}, ExpectedCLIIdentity: owner, Cols: 80, Rows: 24}
	composed, err := composeLaunchSpec(relaunch, testEndpoint, "", core.Get, api.accountLaunchResolver(relaunch))
	if err != nil {
		t.Fatal(err)
	}
	if relaunched, err := probeCLIIdentity(accounts.ProviderClaude, composed.Argv[0], env, cwd); err != nil || *relaunched != *owner {
		t.Fatalf("owner relaunch with rotation on resolved another CLI: %+v %v", relaunched, err)
	}
	meta, err := api.Launch(daemon.LaunchSpec{AgentType: accounts.ProviderClaude, Cwd: cwd, ClientEnv: env, Options: map[string]string{}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Kill(meta.ID) })
	if meta.CLIIdentity == nil || *meta.CLIIdentity != *owner || meta.AccountBinding == nil || meta.AccountBinding.AccountID != account.ID {
		t.Fatalf("managed launch identity differs from the owner CLI: %+v", meta)
	}
	deadline := time.Now().Add(10 * time.Second)
	var child struct {
		Executable, Argv0, Cwd string
		PID                    int
		StartTime              int64
	}
	for {
		raw, readErr := os.ReadFile(record)
		if readErr == nil && json.Unmarshal(raw, &child) == nil && shim.ReadCLIObservation(filepath.Join(m.stateRoot, meta.ID)) != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner synthetic child or observation did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	resolved, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	observed := shim.ReadCLIObservation(filepath.Join(m.stateRoot, meta.ID))
	if child.Executable != resolved || child.Argv0 != alias || child.Cwd != cwd || observed == nil || *observed != *owner {
		t.Fatalf("child/observation selected another executable: %+v %+v", child, observed)
	}
	if _, err := os.Lstat(filepath.Join(m.stateRoot, "accounts", "native")); err == nil {
		t.Fatal("managed launch retained a private copy")
	}
}

func TestMethods_ClaudeStaysAvailableAfterOwnerAutoUpdate(t *testing.T) {
	m, env, source, alias := nativeRetentionFixture(t)
	if _, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot); err != nil {
		t.Fatal(err)
	}
	upgradeOwnerClaude(t, source, alias)
	if method := m.methods()[accounts.ProviderClaude][0]; !method.Available {
		t.Fatalf("an owner Claude auto-update made native sign-in unavailable: %+v", method)
	}
}

func TestRefreshTargetIdentity_StaleRetainedTargetFollowsTheOwnerCLI(t *testing.T) {
	m, env, source, alias := nativeRetentionFixture(t)
	old, err := probeCLIIdentity(accounts.ProviderClaude, "", env, m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	upgradeOwnerClaude(t, source, alias)
	owner, err := probeCLIIdentity(accounts.ProviderClaude, "", env, m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	session := runningCodex("bound-claude", identityA, status.TurnIdle, migratedConversationID)
	session.AgentType, session.Cwd, session.Env, session.CLIIdentity = accounts.ProviderClaude, accountTestProject(t), env, old
	session.AccountBinding = &accounts.Binding{Provider: accounts.ProviderClaude, AccountID: "bound-account"}
	w := testWatcher(t, newAuthFake(identityA))
	w.state = emptyAuthWatchState()
	w.cliProbe = probeCLIIdentity
	w.accountRotation = &accountRotationManager{w: w, d: &Daemon{accounts: m}}
	retained := persist.CLIIdentity{Path: filepath.Join(m.stateRoot, "accounts", "native", "claude-2.1.289", "claude"), Version: "2.1.289", Fingerprint: "sha256:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64)}
	got, err := w.refreshTargetIdentity(session, retained)
	if err != nil || got == nil || *got != *owner {
		t.Fatalf("a stale retained refresh target did not follow the owner CLI %+v: %+v %v", owner, got, err)
	}
}

func TestAccountRetirement_IneligibleGenerationDoesNotStopTheSweep(t *testing.T) {
	m, f, a, native := retirementFixture(t)
	registry, err := m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var other accounts.Binding
	for id := range registry.Accounts {
		if id != a.AccountBinding.AccountID {
			other, err = m.store.CurrentBinding(id, 1)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := m.store.SetLifecycle(registry.Revision, other.AccountID, accounts.LifecycleRetiring); err != nil {
		t.Fatal(err)
	}
	b := accountTestSource(t, m.w.stateDir, other)
	b.ID, b.Status.Process, b.ShimPID, b.ShimStartTime = "second-source", status.ProcessExited, a.ShimPID, a.ShimStartTime
	if err := os.Mkdir(filepath.Join(m.w.stateDir, b.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	bNative := native
	bNative.Binding = other
	writeAccountProofFixture(t, filepath.Join(m.w.stateDir, b.ID, shim.NativeProcessFile), bNative)
	writeAccountProofFixture(t, filepath.Join(m.w.stateDir, b.ID, shim.NativeStoppedFile), shim.NativeStoppedInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Native: bNative, WritersStopped: true})
	// The account processed first was last written by a CLI later than the
	// characterized erasure inventory; the other one must still retire.
	first, second := a, b
	if other.AccountID < a.AccountBinding.AccountID {
		first, second = b, a
	}
	first.CLIIdentity = &persist.CLIIdentity{Path: "/synthetic-codex", Version: "0.162.1", Fingerprint: "synthetic"}
	persistence, err := persist.NewStore(m.w.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range []persist.Meta{first, second} {
		if err := persistence.Save(meta); err != nil {
			t.Fatal(err)
		}
		f.add(meta)
	}
	m.stepRetirements()
	if g := retirementGeneration(t, m, first); g.CredentialErasing || g.CredentialErased {
		t.Fatal("a later-version generation entered the characterized erasure inventory")
	}
	if g := retirementGeneration(t, m, second); !g.CredentialErased {
		t.Fatal("an ineligible generation on another account stopped the retirement sweep")
	}
}
