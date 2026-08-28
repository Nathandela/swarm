package detect

// EnvHost resolves against a SUPPLIED environment's PATH and never executes.
// It exists for `swarm doctor`'s split-brain check (lifecycle plan R1): compare
// what the daemon's saved environment resolves against what the shell resolves.

import (
	"os"
	"path/filepath"
	"testing"
)

func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvHostResolvesOnTheSuppliedPATHOnly(t *testing.T) {
	dir := t.TempDir()
	want := writeExecutable(t, dir, "codex")
	// The PROCESS PATH deliberately does not carry the binary: resolution must
	// come from the supplied env alone, or the check silently answers for the
	// wrong environment -- the exact confusion it exists to expose.
	t.Setenv("PATH", t.TempDir())

	h := EnvHost{Env: []string{"PATH=" + dir}}
	got, err := h.LookPath("codex")
	if err != nil {
		t.Fatalf("LookPath on the supplied PATH: %v", err)
	}
	if got != want {
		t.Errorf("LookPath = %q, want %q", got, want)
	}

	if _, err := (EnvHost{Env: []string{"PATH=" + t.TempDir()}}).LookPath("codex"); err == nil {
		t.Error("LookPath resolved a binary the supplied PATH does not carry")
	}
}

func TestEnvHostNeverRuns(t *testing.T) {
	if _, err := (EnvHost{}).Run("/bin/sh", nil); err == nil {
		t.Error("Run must refuse: resolution against somebody else's environment never executes")
	}
}
