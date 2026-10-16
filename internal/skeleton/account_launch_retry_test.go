//go:build linux

package skeleton

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

// accountRetryFixture is an enabled Codex pool of labelled accounts behind the real
// assembly launch path. refuse decides, per attempted account, whether the
// account-specific preparation fails before the real one runs.
type accountRetryFixture struct {
	api      *coreAPI
	manager  *accountManager
	env      []string
	cwd      string
	labels   map[string]string // account id -> label
	attempts []string          // account ids reaching account preparation, in order
	refuse   func(accounts.Binding) error
}

func newAccountRetryFixture(t *testing.T, labels ...string) *accountRetryFixture {
	t.Helper()
	buildBinaries(t)
	m := accountTestManager(t, accountTestState(t))
	f := &accountRetryFixture{manager: m, labels: map[string]string{}, refuse: func(accounts.Binding) error { return nil }}
	for _, label := range labels {
		_, account, err := m.store.Admit(accountTestRegistry(t, m).Revision, accountTestCandidate(t, m, "codex", "retry-"+label), label)
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

func (f *accountRetryFixture) launch(t *testing.T, name string) (persist.Meta, error) {
	t.Helper()
	f.attempts = nil
	meta, err := f.api.Launch(daemon.LaunchSpec{AgentType: "codex", Name: name, Cwd: f.cwd, ClientEnv: f.env, Options: map[string]string{}, Cols: 80, Rows: 24})
	if err == nil {
		t.Cleanup(func() { _ = f.api.core.Kill(meta.ID) })
	}
	return meta, err
}

func TestBindAccountLaunch_RetriesTheNextAccountAfterAnAccountConfigurationConflict(t *testing.T) {
	sink := captureLog(t)
	f := newAccountRetryFixture(t, "Work", "Personal")
	f.refuse = func(accounts.Binding) error {
		if len(f.attempts) == 1 {
			return accountconfig.Conflict("unsafe-account-profile")
		}
		return nil
	}
	meta, err := f.launch(t, "fix-login")
	if err != nil {
		t.Fatalf("launch refused although a second account was eligible: %v", err)
	}
	if len(f.attempts) != 2 || f.attempts[0] == f.attempts[1] {
		t.Fatalf("attempted accounts %v, want the refused account then a different one", f.attempts)
	}
	if meta.AccountBinding == nil || meta.AccountBinding.AccountID != f.attempts[1] {
		t.Fatalf("discussion bound to %+v, want the second account %s", meta.AccountBinding, f.attempts[1])
	}
	refused := f.attempts[0]
	got := sink.String()
	for _, want := range []string{refused, fmt.Sprintf("%q", f.labels[refused]), "unsafe-account-profile"} {
		if !strings.Contains(got, want) {
			t.Errorf("daemon log lacks %q for the refused account:\n%s", want, got)
		}
	}
}

// Refusals scoped to the bound account's own profile or credentials are retried on
// the next account, which another account's profile would not share.
func TestBindAccountLaunch_RetriesEveryProfileScopedRefusal(t *testing.T) {
	for code, failure := range map[string]error{
		"candidate-configuration-marker-changed": accountconfig.Conflict("candidate-configuration-marker-changed"),
		"unsafe-account-profile":                 accountconfig.Conflict("unsafe-account-profile"),
		"unsafe-account-configuration":           accountconfig.Conflict("unsafe-account-configuration"),
		"native-configuration-update-pending":    accountconfig.Conflict("native-configuration-update-pending"),
		"invalid-credentials":                    accounts.ErrInvalidCredentials,
	} {
		t.Run(code, func(t *testing.T) {
			f := newAccountRetryFixture(t, "Work", "Personal")
			f.refuse = func(accounts.Binding) error {
				if len(f.attempts) == 1 {
					return failure
				}
				return nil
			}
			if _, err := f.launch(t, "fix-login"); err != nil {
				t.Fatalf("launch refused although a second account was eligible: %v", err)
			}
			if len(f.attempts) != 2 || f.attempts[0] == f.attempts[1] {
				t.Fatalf("attempted accounts %v, want the refused account then a different one", f.attempts)
			}
		})
	}
}

func TestBindAccountLaunch_DoesNotRetryALaunchContextFailure(t *testing.T) {
	for _, failure := range []error{accountconfig.Conflict("unsafe-project-directory"), accountconfig.Conflict("unsupported-git-layout"), errors.New("daemon: spawn shim: no such file or directory")} {
		t.Run(failure.Error(), func(t *testing.T) {
			f := newAccountRetryFixture(t, "Work", "Personal")
			f.refuse = func(accounts.Binding) error { return failure }
			if _, err := f.launch(t, "fix-login"); err == nil || !strings.Contains(err.Error(), failure.Error()) {
				t.Fatalf("launch error = %v, want the launch failure itself", err)
			}
			if len(f.attempts) != 1 {
				t.Fatalf("attempted accounts %v; a failure every account shares must not be retried", f.attempts)
			}
		})
	}
}

func TestBindAccountLaunch_NamesEveryAccountTriedWhenNoneCanServe(t *testing.T) {
	sink := captureLog(t)
	f := newAccountRetryFixture(t, "Work", "Personal", "Spare")
	var spare string
	for id, label := range f.labels {
		if label == "Spare" {
			spare = id
		}
	}
	if _, err := f.manager.store.SetAuth(accountTestRegistry(t, f.manager).Revision, spare, accounts.AuthNeedsLogin); err != nil {
		t.Fatal(err)
	}
	_, err := f.launch(t, "fix-login")
	if err != nil {
		t.Fatal(err)
	}
	f.refuse = func(accounts.Binding) error { return accounts.ErrInvalidCredentials }
	_, err = f.launch(t, "second")
	if err == nil {
		t.Fatal("launch succeeded although every account refused")
	}
	if len(f.attempts) != 2 {
		t.Fatalf("attempted accounts %v, want each signed-in account exactly once", f.attempts)
	}
	msg := err.Error()
	for _, want := range []string{
		`"Work" invalid-credentials - sign in again`, `"Personal" invalid-credentials - sign in again`,
		`"Spare" signed-out - sign in again`, "codex rotation off",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "private account or configuration is unavailable") {
		t.Errorf("refusal %q still carries the opaque account message", msg)
	}
	for _, id := range f.attempts {
		if !strings.Contains(sink.String(), id) {
			t.Errorf("daemon log does not name refused account %s:\n%s", id, sink.String())
		}
	}
}

// A launch whose binding is frozen (a bound resume, enrollment, recovery) cannot move
// to another account, so its own refusal is returned, not a pool-wide summary.
func TestBindAccountLaunch_AFrozenBindingReturnsItsOwnRefusal(t *testing.T) {
	f := newAccountRetryFixture(t, "Work", "Personal")
	var frozen accounts.Binding
	for id := range f.labels {
		frozen = accountTestBinding(t, f.manager, accounts.Account{ID: id})
		break
	}
	f.refuse = func(accounts.Binding) error { return accountconfig.Conflict("unsafe-account-profile") }
	_, err := f.api.Launch(daemon.LaunchSpec{AgentType: "codex", Name: "bound", Cwd: f.cwd, ClientEnv: f.env, Options: map[string]string{}, AccountBinding: &frozen, Cols: 80, Rows: 24})
	if !errors.Is(err, accountconfig.Conflict("unsafe-account-profile")) || strings.Contains(err.Error(), "rotation off") {
		t.Fatalf("frozen launch error = %v, want the account's own unsafe-account-profile refusal", err)
	}
	if len(f.attempts) != 1 || f.attempts[0] != frozen.AccountID {
		t.Fatalf("attempted accounts %v, want only the frozen account %s", f.attempts, frozen.AccountID)
	}
}
