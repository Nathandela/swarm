//go:build linux || darwin

package loginworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExpiredDeadlineStartsNoWriters(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	cfg.Deadline = time.Now().Add(-time.Second)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), executable, cfg); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired Start accepted: %v", err)
	}
	files, err := openJob(cfg.StateRoot, cfg.JobID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	if err := files.write(ConfigFile, cfg); err != nil {
		t.Fatal(err)
	}
	if err := RunConfig(context.Background(), filepath.Join(files.path, ConfigFile)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired RunConfig accepted: %v", err)
	}
	if _, err := ReadProgress(cfg.StateRoot, cfg.JobID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired config reached worker progress: %v", err)
	}
	entries, err := os.ReadDir(NativeHome(cfg.StateRoot, cfg.JobID))
	if err != nil || len(entries) != 0 {
		t.Fatalf("expired config started native work: entries=%d err=%v", len(entries), err)
	}
	assertNoJobSecrets(t, cfg)
}
