package upgrade

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
)

func TestLoginStatePreventsDowngradeWithoutSessions(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := logins.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	card := CurrentManifest("test")
	if err := loginStateGuard(state, card); err != nil {
		t.Fatal(err)
	}
	card.LoginSchema = 0
	if err := loginStateGuard(state, card); err == nil {
		t.Fatal("old build accepted saved login state")
	}
}

func TestLoginCardDescribesNewWireAndOfflineImport(t *testing.T) {
	card := CurrentManifest("v0.21.0")
	if shimwire.Version != 2 {
		t.Fatal("cutover wire version changed")
	}
	if card.LoginSchema != logins.SchemaVersion || card.Schema != persist.HermesSchemaVersion || card.Shimwire != shimwire.Version {
		t.Fatalf("new contract missing: %+v", card)
	}
	if card.AccountSchema != 1 || card.AccountJobs != 1 || card.AccountRecovery != 3 || card.AccountWorker != 6 || card.AccountShim != 2 || card.AccountConfig != 4 || card.AccountInventory != 1 || card.AccountAuth != 1 {
		t.Fatalf("v0.20 migration input card changed: %+v", card)
	}
}

func TestLoginStateGuardRefusesUnverifiableDirectory(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(state, "logins")); err != nil {
		t.Fatal(err)
	}
	if err := loginStateGuard(state, CurrentManifest("test")); err == nil {
		t.Fatal("symlink login state accepted")
	}
}
