package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// E6.9 (supplementary) — a lightweight import-boundary tripwire complementing the
// precomputed-Group tests: the client-path source must not call status.Derive.
// The authoritative boundary check lands in Epic 7 (the TUI package), but this
// catches an accidental client-side re-derivation early. It follows the daemon
// package's convention that client.go is the client side.
//
// This scans package source (non-test .go files). At GREEN it verifies the
// server derives groups while the client only displays the precomputed field.
func TestBoundary_ClientDoesNotReDeriveStatus(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	sawDerive := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(src)
		if strings.Contains(body, "status.Derive") {
			sawDerive = true
			if strings.HasPrefix(name, "client") {
				t.Errorf("%s calls status.Derive: the client path must display the server-computed Group, not re-derive it (E6.9)", name)
			}
		}
	}
	if !sawDerive {
		t.Errorf("no package source calls status.Derive: the server must compute the display Group (E6.9)")
	}
}
