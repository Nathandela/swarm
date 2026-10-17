//go:build linux

package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// upgradeOwnerClaude points the owner's claude alias at a newer synthetic release.
func upgradeOwnerClaude(t *testing.T, source, alias string) {
	t.Helper()
	newer := filepath.Join(filepath.Dir(source), "2.1.290")
	copyRetentionELF(t, newer)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newer, alias); err != nil {
		t.Fatal(err)
	}
}

func TestManagedNative_ClaudeUsesTheOwnerBinary(t *testing.T) {
	m, env, source, alias := nativeRetentionFixture(t)
	upgradeOwnerClaude(t, source, alias)
	owner, err := probeCLIIdentity(accounts.ProviderClaude, "", env, m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
	if err != nil || *selected != *owner || selected.Path != alias || selected.Version != "2.1.290" {
		t.Fatalf("managed Claude did not use the owner's installed binary: %+v owner=%+v %v", selected, owner, err)
	}
	if _, err := os.Lstat(filepath.Join(m.stateRoot, "accounts", "native")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed Claude retained a private copy: %v", err)
	}
	if method := m.methods()[accounts.ProviderClaude][0]; !method.Available {
		t.Fatalf("native sign-in refused the installed Claude: %+v", method)
	}
}

func TestNewClaudeNativeContextAcceptsNewerVersion(t *testing.T) {
	prepare := func(version string) error {
		m := accountTestManager(t, accountTestState(t))
		candidate := accountTestCandidate(t, m, accounts.ProviderClaude, "native-version")
		binding := accountTestBinding(t, m, accountTestAdmit(t, m, candidate))
		_, cwd, env := accountTestLaunchEnvironment(t, m.stateRoot)
		d := &Daemon{accounts: m}
		_, err := d.prepareAccountLaunch("new", daemon.LaunchSpec{AgentType: "claude", Argv: []string{"/fixture/claude", "--settings", `{"hooks":{}}`}, Cwd: cwd, ClientEnv: env, AccountBinding: &binding, CLIIdentity: &persist.CLIIdentity{Version: version, Path: "/fixture/claude"}})
		return err
	}
	characterized, newer := prepare(accountconfig.CharacterizedClaudeVersion), prepare("2.1.300")
	if (characterized == nil) != (newer == nil) || characterized != nil && characterized.Error() != newer.Error() {
		t.Fatalf("a Claude newer than the characterized native context is treated differently: characterized=%v newer=%v", characterized, newer)
	}
}

func TestBoundResume_NeverDowngradesBelowSourceCLI(t *testing.T) {
	m, env, _, alias := nativeRetentionFixture(t)
	account := accountTestAdmit(t, m, accountTestCandidate(t, m, accounts.ProviderClaude, "downgrade-account"))
	binding := accountTestBinding(t, m, account)
	source := runningCodex("newer-source", identityA, status.TurnIdle, migratedConversationID)
	source.AgentType, source.Cwd, source.Env, source.AccountBinding = accounts.ProviderClaude, accountTestProject(t), env, &binding
	source.CLIIdentity = &persist.CLIIdentity{Path: alias, Version: "2.1.290", Fingerprint: "newer-writer"}
	source.Status.Process, source.AccountProjectionRef = status.ProcessExited, strings.Repeat("a", 64)
	store, err := persist.NewStore(m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(source); err != nil {
		t.Fatal(err)
	}
	core := accountTestCore(t, m, nil)
	api := &coreAPI{core: core, accounts: m, endpointID: testEndpoint}
	before := len(core.List())
	meta, err := api.launchResolved(daemon.LaunchSpec{AgentType: accounts.ProviderClaude, Argv: []string{alias}, Cwd: source.Cwd, ClientEnv: env, AccountBinding: &binding, Options: map[string]string{protocol.OptionResumeFrom: testEndpoint + "/" + source.ID}, Cols: 80, Rows: 24})
	if err == nil {
		_ = core.Kill(meta.ID)
	}
	if err == nil || !strings.Contains(err.Error(), "cli-older-than-source") || len(core.List()) != before {
		t.Fatalf("bound resume ran Claude 2.1.289 on a discussion last written by 2.1.290: %v", err)
	}
}

func TestCLIRefresh_BoundClaudeWithNewerOwnerCLIGetsASupportedTarget(t *testing.T) {
	m, env, source, alias := nativeRetentionFixture(t)
	old, err := probeCLIIdentity(accounts.ProviderClaude, "", env, m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	upgradeOwnerClaude(t, source, alias)
	target, err := probeCLIIdentity(accounts.ProviderClaude, "", env, m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	session := runningCodex("bound-claude", identityA, status.TurnIdle, migratedConversationID)
	session.AgentType, session.Cwd, session.Env, session.CLIIdentity = accounts.ProviderClaude, accountTestProject(t), env, old
	session.AccountBinding = &accounts.Binding{Provider: accounts.ProviderClaude, AccountID: "bound-account"}
	f := newAuthFake(identityA)
	f.add(session)
	w := testWatcher(t, f)
	w.state = emptyAuthWatchState()
	w.cliProbe = probeCLIIdentity
	w.cliObserve = func(string) *persist.CLIIdentity { return old }
	w.accountRotation = &accountRotationManager{w: w, d: &Daemon{accounts: m}}
	w.discoverCLIRefreshes()
	if candidate, ok := w.state.CLICandidates[session.ID]; !ok || candidate.Identity != *target {
		t.Fatalf("bound Claude session got no supported refresh target %+v: %+v", target, w.state.CLICandidates)
	}
}
