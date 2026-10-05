package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if err := accountStateGuard(state, CurrentManifest("v0.15.0")); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, CompatManifest{AccountRecovery: 1, AccountShim: 1}); err == nil {
		t.Fatal("legacy managed build accepted new recovery and writer contract")
	}
	if err := os.WriteFile(path, []byte(`{"account_schema_version":2,"account_rotations":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, CompatManifest{AccountRecovery: 1, AccountShim: 2}); err == nil {
		t.Fatal("empty promoted journal accepted an older recovery reader")
	}
	if err := accountStateGuard(state, CurrentManifest("v0.15.0")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"account_schema_version":%d,"account_rotations":{}}`, accounts.RecoverySchemaVersion+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, CurrentManifest("v0.15.0")); err == nil {
		t.Fatal("future recovery schema accepted")
	}
}

func TestAccountGuardObservationHoldWithoutRegistryOrJournal(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "managed-session")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "account-observation-hold.json")
	data := []byte(`{"schema_version":1,"kind":"hold","local":"managed-session"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	card := CurrentManifest("v0.15.0")
	legacy := card
	legacy.AccountRecovery = 1
	if err := accountStateGuard(state, legacy); err == nil {
		t.Fatal("hold-only state accepted a recovery reader without durable observation holds")
	}
	if err := accountStateGuard(state, card); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{
		`{"schema_version":2,"kind":"hold","local":"managed-session"}`,
		`{"schema_version":1,"kind":"model","local":"managed-session"}`,
		`{"schema_version":1,"kind":"hold","local":"other-session"}`,
	} {
		if err := os.WriteFile(path, []byte(malformed), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := accountStateGuard(state, card); err == nil {
			t.Fatal("unverifiable observation hold accepted")
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(state, "hold-alias.json")); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, card); err == nil {
		t.Fatal("hardlinked observation hold accepted")
	}
}

func TestAccountGuardLegacyDirectoryWithoutObservationHold(t *testing.T) {
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "legacy-session"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, CurrentManifest("v0.15.0")); err != nil {
		t.Fatal(err)
	}
}

func TestAccountGuardPendingInboxWithoutRecoveryJournal(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	dir := filepath.Join(state, "accounts", "recovery-inbox")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pending.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	card := CurrentManifest("v0.15.0")
	legacy := card
	legacy.AccountRecovery = 1
	if err := accountStateGuard(state, legacy); err == nil {
		t.Fatal("pending critical event was hidden by an empty registry and missing journal")
	}
	if err := accountStateGuard(state, card); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, card); err == nil {
		t.Fatal("unknown future inbox schema accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(state, "accounts", "registry.json"), path); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, card); err == nil {
		t.Fatal("symlink inbox document accepted")
	}
}

func TestAccountGuardCheckCustodyWithoutRecoveryJournalOrEnrollment(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	dir := filepath.Join(state, "accounts", "checks", strings.Repeat("a", 32))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "worker.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), ".lock-"+strings.Repeat("b", 64)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	card := CurrentManifest("v0.15.0")
	legacy := card
	legacy.AccountWorker = 1
	if err := accountStateGuard(state, legacy); err == nil {
		t.Fatal("persisted check was hidden by empty registry/jobs and missing journal")
	}
	if err := accountStateGuard(state, card); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(state, "worker-alias.json")); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, card); err == nil {
		t.Fatal("hardlinked check custody accepted")
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
