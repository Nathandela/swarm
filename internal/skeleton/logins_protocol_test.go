package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/protocol"
)

func readyLoginJob(t *testing.T) (*coreAPI, loginworker.Config, logins.Credential) {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := logins.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c := loginworker.Config{SchemaVersion: 1, StateRoot: state, JobID: "0123456789abcdef0123456789abcdef", Generation: 1, Provider: "codex", Label: "Saved", Deadline: time.Now().Add(time.Minute)}
	home := loginworker.NativeHome(state, c.JobID)
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(home), name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(loginworker.ConfigFile, c)
	write(loginworker.ProgressFile, loginworker.Progress{SchemaVersion: 1, JobID: c.JobID, Generation: 1, Phase: loginworker.PhaseReady, WritersStopped: true, NativeWritersStopped: true})
	credential := logins.Credential{Auth: json.RawMessage(`{"auth_mode":"chatgpt","tokens":{"account_id":"fixture-login","access_token":"fixture-access","refresh_token":"fixture-old-refresh"}}`)}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), credential.Auth, 0600); err != nil {
		t.Fatal(err)
	}
	return &coreAPI{logins: &loginManager{store: store}}, c, credential
}

// Like watchLoginJobs, retry only unavailable custody and resume cleanup for
// terminal jobs. Every attempt still runs the real stopped-writer checks.
func completeLoginFixture(t *testing.T, a *coreAPI, c loginworker.Config) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err := retryLoginFixtureUnavailable(ctx, func() error {
		p, progressErr := loginworker.ReadProgress(c.StateRoot, c.JobID)
		if progressErr == nil && (p.Phase == loginworker.PhaseFailed || p.Phase == loginworker.PhaseCanceled) {
			return cleanupLoginJob(c)
		}
		return a.completeLoginJob(c)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func retryLoginFixtureUnavailable(ctx context.Context, attempt func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := attempt()
		if !errors.Is(err, loginworker.ErrUnavailable) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestLoginFixtureUnavailableRetryPreservesRealCompletion(t *testing.T) {
	a, c, _ := readyLoginJob(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	attempts := 0
	err := retryLoginFixtureUnavailable(ctx, func() error {
		attempts++
		if attempts == 1 {
			return errors.Join(loginworker.ErrUnavailable, errors.New("synthetic unsettled inventory"))
		}
		return a.completeLoginJob(c)
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatal("unavailable custody did not retry")
	}
	if done, err := loginworker.Completed(c); err != nil || !done {
		t.Fatalf("real completion missing: %v", err)
	}
	if cleaned, err := loginworker.Cleaned(c); err != nil || !cleaned {
		t.Fatalf("real cleanup missing: %v", err)
	}
}

func TestLoginCompletionCrashDoesNotReplayOldGrant(t *testing.T) {
	a, c, credential := readyLoginJob(t)
	r, err := a.logins.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, _, err = a.logins.store.Import(r.Revision, logins.Login{ID: c.JobID, Provider: c.Provider, Label: c.Label, LastLoginJobID: c.JobID}, credential)
	if err != nil {
		t.Fatal(err)
	}
	credential.Auth = json.RawMessage(strings.ReplaceAll(string(credential.Auth), "fixture-old-refresh", "fixture-new-refresh"))
	if _, _, err := a.logins.store.Relogin(r.Revision, c.JobID, credential); err != nil {
		t.Fatal(err)
	}
	completeLoginFixture(t, a, c)
	raw, err := os.ReadFile(filepath.Join(c.StateRoot, "logins", "saved", c.JobID, "credential.json"))
	if err != nil || !strings.Contains(string(raw), "fixture-new-refresh") || strings.Contains(string(raw), "fixture-old-refresh") {
		t.Fatal("old enrollment replayed over renewed credential")
	}
	if done, err := loginworker.Completed(c); err != nil || !done {
		t.Fatalf("completion missing: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(loginworker.NativeHome(c.StateRoot, c.JobID), "auth.json")); err == nil && strings.Contains(string(raw), "fixture-old-refresh") {
		t.Fatal("temporary grant retained")
	}
}

func TestLoginViewsNeverContainCredentials(t *testing.T) {
	a, c, credential := readyLoginJob(t)
	// Redaction is independent of the host's transient native-writer inventory.
	r, err := a.logins.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.logins.store.Import(r.Revision, logins.Login{ID: c.JobID, Provider: c.Provider, Label: c.Label, LastLoginJobID: c.JobID}, credential); err != nil {
		t.Fatal(err)
	}
	view, err := a.Logins(protocol.LoginsReq{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	for _, secret := range []string{"fixture-access", "fixture-old-refresh", "fixture-login", c.StateRoot} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("private credential data in view")
		}
	}
	if len(view.Logins) != 1 || view.Logins[0].Active {
		t.Fatalf("saving login unexpectedly activated it: %+v", view.Logins)
	}
}

func TestDuplicateAndWrongLoginLeaveExistingGrantUntouched(t *testing.T) {
	for _, kind := range []string{"duplicate", "wrong"} {
		t.Run(kind, func(t *testing.T) {
			a, c, original := readyLoginJob(t)
			r, err := a.logins.store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			saved := original
			if kind == "wrong" {
				saved.Auth = json.RawMessage(strings.ReplaceAll(string(original.Auth), "fixture-login", "different-account"))
			}
			_, login, err := a.logins.store.Add(r.Revision, c.Provider, "existing", saved)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "wrong" {
				c.LoginID = login.ID
			}
			path := filepath.Join(c.StateRoot, "logins", "saved", login.ID, "credential.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			completeLoginFixture(t, a, c)
			p, err := loginworker.ReadProgress(c.StateRoot, c.JobID)
			if err != nil || p.Phase != loginworker.PhaseFailed || p.ErrorCode != kind+"_login" {
				t.Fatalf("wrong rejection: %+v %v", p, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("existing credential changed on refused sign-in")
			}
			if cleaned, err := loginworker.Cleaned(c); err != nil || !cleaned {
				t.Fatalf("temporary refused grant retained: %v", err)
			}
		})
	}
}

func TestCodexEnrollmentUnsafeLauncherIsActionableBeforeAnyJob(t *testing.T) {
	f := newLoginFixture(t)
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "codex")
	script := []byte("#!/bin/sh\nprintf '%s\\n' 'codex-cli 0.163.0'\n")
	if e = os.WriteFile(path, script, 0775); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(path, 0775); e != nil {
		t.Fatal(e)
	}
	before, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", root)
	r, e := f.m.store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	for _, action := range []string{"start", "relogin"} {
		request := protocol.LoginsReq{Action: action, Provider: "codex", Label: "synthetic", ExpectedRevision: r.Revision}
		if action == "relogin" {
			request.LoginID = f.first.ID
		}
		if _, e = f.m.d.api.Logins(request); !errors.Is(e, protocol.ErrLoginsUnsafeNativeCLI) {
			t.Fatalf("%s unsafe launcher error=%v", action, e)
		}
	}
	jobs, e := loginworker.Jobs(f.m.store.StateRoot())
	if e != nil || len(jobs) != 0 {
		t.Fatal("unsafe executable created an enrollment job")
	}
	if e = os.Chmod(path, 0755); e != nil {
		t.Fatal(e)
	}
	cfg, e := newLoginWorkerConfig(f.m.store.StateRoot(), "codex", "synthetic", "")
	if e != nil || cfg.NativeVersion != "0.163.0" || cfg.NativePath != path {
		t.Fatalf("same launcher with safe permissions refused: %v", e)
	}
	after, e := os.Stat(path)
	if e != nil || !os.SameFile(before, after) {
		t.Fatal("fixture repair replaced executable")
	}
	raw, e := os.ReadFile(path)
	if e != nil || string(raw) != string(script) {
		t.Fatal("fixture repair changed executable bytes")
	}
}

func TestLoginFixtureRetryDoesNotHideOtherErrors(t *testing.T) {
	for _, failure := range []error{loginworker.ErrInvalid, loginworker.ErrUnsafe, errors.New(loginworker.ErrUnavailable.Error())} {
		attempts := 0
		err := retryLoginFixtureUnavailable(t.Context(), func() error { attempts++; return failure })
		if err != failure || attempts != 1 {
			t.Fatalf("non-retryable error hidden: attempts=%d err=%v", attempts, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts := 0
	err := retryLoginFixtureUnavailable(ctx, func() error {
		attempts++
		if attempts == 3 {
			cancel()
		}
		return loginworker.ErrUnavailable
	})
	if !errors.Is(err, context.Canceled) || attempts != 3 {
		t.Fatalf("unavailable retry ignored bound: attempts=%d err=%v", attempts, err)
	}
}

func TestLoginFixtureResumesTerminalJobCleanup(t *testing.T) {
	a, c, _ := readyLoginJob(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := retryLoginFixtureUnavailable(ctx, func() error { return loginworker.Reject(c, "duplicate_login") }); err != nil {
		t.Fatal(err)
	}
	completeLoginFixture(t, a, c)
	progress, err := loginworker.ReadProgress(c.StateRoot, c.JobID)
	if err != nil || progress.Phase != loginworker.PhaseFailed || progress.ErrorCode != "duplicate_login" {
		t.Fatalf("terminal evidence changed: %v", err)
	}
	if cleaned, err := loginworker.Cleaned(c); err != nil || !cleaned {
		t.Fatalf("terminal job cleanup missing: %v", err)
	}
	r, err := a.logins.store.Snapshot()
	if err != nil || len(r.Logins) != 0 {
		t.Fatalf("rejected grant was imported: %v", err)
	}
}
