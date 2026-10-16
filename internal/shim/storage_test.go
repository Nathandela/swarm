package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertNoArchivedCapture(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, SnapshotFile)); !os.IsNotExist(err) {
		t.Errorf("ended shim retained a screen snapshot: %v", err)
	}
	archives, err := filepath.Glob(filepath.Join(dir, TranscriptFile) + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) != 0 {
		t.Errorf("shim created terminal archives: %v", archives)
	}
}

func TestRun_RemovesOnlyKnownLegacyCaptures(t *testing.T) {
	cfg := fakeAgentConfig(t, "print fresh-session\nexit 0\n")
	legacy := []string{SnapshotFile, TranscriptFile, TranscriptFile + ".1", TranscriptFile + ".12"}
	kept := []string{TranscriptFile + ".keep", "agent-work.txt"}
	for _, name := range append(legacy, kept...) {
		if err := os.WriteFile(filepath.Join(cfg.SessionDir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if r := waitRun(t, runShimAsync(cfg), 15*time.Second); r.err != nil || r.exit != 0 {
		t.Fatalf("new shim = %+v", r)
	}
	for _, name := range legacy {
		if _, err := os.Lstat(filepath.Join(cfg.SessionDir, name)); !os.IsNotExist(err) {
			t.Errorf("new shim retained legacy capture %s: %v", name, err)
		}
	}
	for _, name := range kept {
		raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, name))
		if err != nil || string(raw) != name {
			t.Errorf("new shim changed unrelated file %s: %q (%v)", name, raw, err)
		}
	}
}

func TestRun_LegacyCaptureCleanupFailureRefusesSetup(t *testing.T) {
	for _, name := range []string{SnapshotFile, TranscriptFile} {
		t.Run(name, func(t *testing.T) {
			cfg := fakeAgentConfig(t, "print SHOULD-NOT-RUN\nexit 0\n")
			path := filepath.Join(cfg.SessionDir, name)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "keep"), []byte("valuable"), 0o600); err != nil {
				t.Fatal(err)
			}
			r := waitRun(t, runShimAsync(cfg), 5*time.Second)
			if r.err == nil || !strings.Contains(r.err.Error(), "remove legacy capture") {
				t.Fatalf("Run = %+v, want a legacy cleanup setup refusal", r)
			}
			if _, err := os.Stat(filepath.Join(cfg.SessionDir, ExitFile)); !os.IsNotExist(err) {
				t.Errorf("setup refusal published an exit marker: %v", err)
			}
			if tail := readTranscript(t, cfg.SessionDir); tail != "" {
				t.Errorf("agent ran despite failed setup: %q", tail)
			}
			raw, err := os.ReadFile(filepath.Join(path, "keep"))
			if err != nil || string(raw) != "valuable" {
				t.Errorf("setup refusal changed the obstructing directory: %q (%v)", raw, err)
			}
		})
	}
}
