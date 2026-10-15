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
	"github.com/Nathandela/swarm/internal/shim"
)

// ownerAttempt marks an unbound (owner login) launch in fixture attempts.
const ownerAttempt = "owner"

// newOwnerFallbackFixture is the account retry fixture whose preparation hook
// also records owner (unbound) launches, which never reach account preparation.
func newOwnerFallbackFixture(t *testing.T, labels ...string) *accountRetryFixture {
	t.Helper()
	buildBinaries(t)
	m := accountTestManager(t, accountTestState(t))
	f := &accountRetryFixture{manager: m, labels: map[string]string{}, refuse: func(accounts.Binding) error { return nil }}
	for _, label := range labels {
		_, account, err := m.store.Admit(accountTestRegistry(t, m).Revision, accountTestCandidate(t, m, "codex", "fallback-"+label), label)
		if err != nil {
			t.Fatal(err)
		}
		f.labels[account.ID] = label
	}
	if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, "codex", true); err != nil {
		t.Fatal(err)
	}
	_, cwd, env := accountTestLaunchEnvironment(t, m.stateRoot)
	// The executable only reports a supported version and idles; it never
	// authenticates or contacts a provider.
	bin := filepath.Join(m.stateRoot, "codex")
	accountTestPut(t, bin, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'codex-cli 0.160.0'; exit; fi\nexec /bin/sleep 60\n"))
	if err := os.Chmod(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	env[1] = "PATH=" + m.stateRoot + ":/usr/bin:/bin"
	f.env, f.cwd = env, cwd
	assembly := &Daemon{accounts: m}
	core := accountTestCore(t, m, func(cfg *daemon.Config) {
		cfg.ShimBinary = swarmBin
		cfg.FinalizeLaunch = func(id string, spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
			if spec.AccountBinding == nil {
				f.attempts = append(f.attempts, ownerAttempt)
				return spec, nil
			}
			f.attempts = append(f.attempts, spec.AccountBinding.AccountID)
			if err := f.refuse(*spec.AccountBinding); err != nil {
				return spec, err
			}
			return assembly.prepareAccountLaunch(id, spec)
		}
	})
	f.api = newCoreAPI(core, "", testEndpoint)
	f.api.accounts = m
	t.Cleanup(f.api.close)
	return f
}

// A binding the selector chose that fails in the rotation machinery itself (no
// account-scoped code) launches on the owner login instead of failing.
func TestLaunch_SelectorChosenBindingFallsBackToOwnerOnManagedFailure(t *testing.T) {
	f := newOwnerFallbackFixture(t, "Work")
	f.refuse = func(accounts.Binding) error { return errAccountLaunch }
	meta, err := f.launch(t, "fix-login")
	if err != nil {
		t.Fatalf("launch refused although the owner login could take it: %v", err)
	}
	if meta.AccountBinding != nil {
		t.Fatalf("fallback launch is bound to %+v, want an owner launch", meta.AccountBinding)
	}
	if len(f.attempts) != 2 || f.attempts[0] == ownerAttempt || f.attempts[1] != ownerAttempt {
		t.Fatalf("attempts %v, want the selected account then the owner login", f.attempts)
	}
}

func TestLaunch_OwnerFallbackIsLoggedAndShown(t *testing.T) {
	sink := captureLog(t)
	f := newOwnerFallbackFixture(t, "Work")
	f.refuse = func(accounts.Binding) error { return errAccountLaunch }
	meta, err := f.launch(t, "fix-login")
	if err != nil {
		t.Fatal(err)
	}
	if want := `accounts: codex launch "fix-login" fell back to the owner login after account "Work" refused: ` + errAccountLaunch.Error(); !strings.Contains(sink.String(), want) {
		t.Errorf("daemon log lacks %q:\n%s", want, sink.String())
	}
	for _, want := range []string{"owner login", `"Work"`, errAccountLaunch.Error()} {
		if !strings.Contains(meta.LaunchNotice, want) {
			t.Errorf("launch notice %q lacks %q", meta.LaunchNotice, want)
		}
	}
}

// A frozen binding (here an explicit one, as enrollment and recovery pass) keeps
// its account: its refusal is returned and the owner login is never tried.
func TestLaunch_FrozenBindingDoesNotFallBack(t *testing.T) {
	f := newOwnerFallbackFixture(t, "Work")
	var frozen accounts.Binding
	for id := range f.labels {
		frozen = accountTestBinding(t, f.manager, accounts.Account{ID: id})
	}
	f.refuse = func(accounts.Binding) error { return errAccountLaunch }
	_, err := f.api.Launch(daemon.LaunchSpec{AgentType: "codex", Name: "bound", Cwd: f.cwd, ClientEnv: f.env, Options: map[string]string{}, AccountBinding: &frozen, Cols: 80, Rows: 24})
	if !errors.Is(err, errAccountLaunch) {
		t.Fatalf("frozen launch error = %v, want the account's own refusal", err)
	}
	if len(f.attempts) != 1 || f.attempts[0] != frozen.AccountID {
		t.Fatalf("attempts %v, want only the frozen account %s", f.attempts, frozen.AccountID)
	}
}

// A resume of a discussion that was already bound keeps its account.
func TestLaunch_FrozenBindingDoesNotFallBack_BoundResume(t *testing.T) {
	f := manualResumeFixture(t)
	f.stop = errAccountLaunch
	api := f.api(t)
	dir := filepath.Join(f.manager.stateRoot, f.source.ID)
	manualResumeProof(t, filepath.Join(dir, shim.NativeProcessFile), f.native)
	manualResumeProof(t, filepath.Join(dir, shim.NativeStoppedFile), shim.NativeStoppedInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Native: f.native, WritersStopped: true})
	_, err := api.Launch(f.request())
	if !errors.Is(err, errAccountLaunch) {
		t.Fatalf("bound resume error = %v, want the account's own refusal", err)
	}
	if !f.finalized || f.accepted.AccountBinding == nil || *f.accepted.AccountBinding != *f.source.AccountBinding {
		t.Fatalf("bound resume last prepared %+v, want only its frozen account", f.accepted.AccountBinding)
	}
}

// Launch-context failures every account and the owner would share still abort.
func TestLaunch_LaunchContextFailureStillAborts(t *testing.T) {
	for _, failure := range []error{accountconfig.Conflict("unsafe-project-directory"), errors.New("daemon: spawn shim: no such file or directory")} {
		t.Run(failure.Error(), func(t *testing.T) {
			f := newOwnerFallbackFixture(t, "Work")
			f.refuse = func(accounts.Binding) error { return failure }
			if _, err := f.launch(t, "fix-login"); err == nil || !strings.Contains(err.Error(), failure.Error()) {
				t.Fatalf("launch error = %v, want the launch failure itself", err)
			}
			if len(f.attempts) != 1 || f.attempts[0] == ownerAttempt {
				t.Fatalf("attempts %v; a launch-context failure must not fall back to the owner", f.attempts)
			}
		})
	}
}
