package phonecore

import (
	"os"
	"strings"
	"testing"
)

// TestRegistryOnly_ObsoleteSingleMachineManagerDoesNotReturn keeps the v2 manager
// surface registry-only. The old one-entry manager and its adapter were compatibility
// scaffolding, not a second implementation to preserve.
func TestRegistryOnly_ObsoleteSingleMachineManagerDoesNotReturn(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	obsolete := []string{
		"SingleMachineAdapter",
		"NewSingleMachineAdapter",
		"SingleMachineManager",
		"NewSingleMachineManager",
		"ErrMultiMachineNotImplemented",
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		source, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, symbol := range obsolete {
			if strings.Contains(string(source), symbol) {
				t.Errorf("obsolete singleton compatibility symbol %q remains in %s", symbol, entry.Name())
			}
		}
	}
}
