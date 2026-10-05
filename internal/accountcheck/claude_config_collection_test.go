//go:build linux

package accountcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectionRetainsPrivateClaudeConfigCustody(t *testing.T) {
	state, root, binding := collectionFixture(t)
	ref := stoppedCollectionFixture(t, root, binding, 41)
	path := filepath.Join(state, "accounts", "checks", ref.Generation)
	for _, dir := range []string{"native-cwd", "native-config", "native-config/backups"} {
		if err := os.Mkdir(filepath.Join(path, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(path, "native-config/backups"), 0775); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"native-config/.claude.json", "native-config/settings.json", "native-config/backups/.claude.json.backup.1791058214651"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	collector := NewCollector(state)
	defer collector.Close()
	if n := collectFixture(t, collector, nil); n != 1 {
		t.Fatal("stopped private Claude config could not be collected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stopped private Claude config remains")
	}
}
