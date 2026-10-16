package tui

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	gateBins.Sweep()
	os.Exit(code)
}
