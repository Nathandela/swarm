package shim

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
)

func TestResolveLoginEnvironmentUsesInheritedOwnerHomeForProjectAlias(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	state, home, target, cwd := filepath.Join(root, "state"), filepath.Join(root, "home"), filepath.Join(root, "native"), filepath.Join(root, "home", "project")
	for _, dir := range []string{state, cwd, target} {
		if e = os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	store, e := logins.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = store.Close() }()
	r, e := store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	_, login, e := store.Add(r.Revision, logins.ProviderCodex, "synthetic", logins.Credential{Auth: []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"alias-fixture","access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`)})
	if e != nil {
		t.Fatal(e)
	}
	r, e = store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	op, e := store.BeginSwitch(r.Revision, logins.ProviderCodex, login.ID, "fixture", nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, phase := range []string{"stopping", "stopped"} {
		op.Phase = phase
		if e = store.SaveSwitch(op); e != nil {
			t.Fatal(e)
		}
	}
	if e = store.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = store.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	rt, e := store.CommitSwitch(op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(target, "config.toml"), []byte("model = \"gpt-6-luna\"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(target, filepath.Join(home, ".codex")); e != nil {
		t.Fatal(e)
	}
	cfg := Config{LoginRuntime: &rt, LoginStateRoot: state, NativeProvider: logins.ProviderCodex, Argv: []string{"codex"}, Cwd: cwd, Env: []string{"HOME=" + home}}
	if e = resolveLoginEnvironment(&cfg); e != nil {
		t.Fatalf("shim refused inherited owner native alias: %v", e)
	}
	cfg.Env = nil
	if e = resolveLoginEnvironment(&cfg); e == nil {
		t.Fatal("shim authorized native alias without inherited HOME")
	}
}
