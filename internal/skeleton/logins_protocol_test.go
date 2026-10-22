package skeleton

import (
	"encoding/json"
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
	if err := a.completeLoginJob(c); err != nil {
		t.Fatal(err)
	}
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
	a, c, _ := readyLoginJob(t)
	if _, err := a.Logins(protocol.LoginsReq{Action: "complete", JobID: c.JobID}); err != nil {
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
			if err := a.completeLoginJob(c); err != nil {
				t.Fatal(err)
			}
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
