package e2e

import (
	"os"
	"testing"
)

// TestMain ends every shim and agent a test left behind: shims outlive their daemon by design.
func TestMain(m *testing.M) {
	code := m.Run()
	testBins.Sweep()
	os.Exit(code)
}
