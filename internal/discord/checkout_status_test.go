package discord

import (
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestCheckoutInventoryReadersDoNotInitializeOrHardenState(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing")
	if _, err := InspectConfig(missing); !os.IsNotExist(err) {
		t.Fatalf("missing config: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("inspect config created directory: %v", err)
	}
	path := filepath.Join(base, "discord-active.json")
	if err := os.WriteFile(path, []byte(`"/tmp/existing-private-state"`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := InspectSelectedState(base); err != nil || got != "/tmp/existing-private-state" {
		t.Fatalf("selection = %q: %v", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("read-only selection changed file mode: %v %v", info, err)
	}
	db, err := bolt.Open(filepath.Join(base, "discord.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucket([]byte(teammateActiveBindingBucket))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectCheckoutStatus(base, "install"); err == nil {
		t.Fatal("partial connector ledger was treated as complete")
	}
}
