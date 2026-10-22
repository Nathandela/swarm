package upgrade

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAstraLegacyOnlyStateAllowsCompatibleRollback(t *testing.T) {
	state, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(state, "accounts"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(state, "accounts", "registry.json"), []byte(`{"schema_version":1,"accounts":{}}`), 0600); e != nil {
		t.Fatal(e)
	}
	card := CurrentManifest("v0.20.0")
	card.LoginSchema = 0
	card.Shimwire = 1
	card.Schema = 2
	if e = loginStateGuard(state, card); e != nil {
		t.Fatalf("pure legacy state blocks returning to build that understands all retained account formats: %v", e)
	}
}
