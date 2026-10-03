package upgrade

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
)

func TestAccountGuardChecksRecoveryWithoutRegistryOrSessions(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, "auth-watch-state.json")
	if err := os.WriteFile(path, []byte(`{"account_schema_version":1,"account_rotations":{"incident":{"phase":"committed"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, CompatManifest{}); err == nil {
		t.Fatal("old build accepted managed recovery without registry")
	}
	if err := accountStateGuard(state, CompatManifest{AccountRecovery: 1, AccountShim: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"account_schema_version":2,"account_rotations":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, CurrentManifest("v0.15.0")); err == nil {
		t.Fatal("future recovery schema accepted")
	}
}

func TestAccountGuardChecksEnrollmentOnlyAndUnsafeWorkerInventory(t *testing.T) {
	state := t.TempDir()
	_ = os.Chmod(state, 0o700)
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	jobs := filepath.Join(state, "accounts", "enrollment.json")
	if err := os.WriteFile(jobs, []byte(`{"schema_version":1,"revision":1,"jobs":{"candidate":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, CompatManifest{}); err == nil {
		t.Fatal("old build accepted enrollment-only state")
	}
	if err := accountStateGuard(state, CurrentManifest("v0.15.0")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(state, "accounts", "jobs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(state, "accounts", "jobs")); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, CurrentManifest("v0.15.0")); err == nil {
		t.Fatal("symlink worker inventory accepted")
	}
}
