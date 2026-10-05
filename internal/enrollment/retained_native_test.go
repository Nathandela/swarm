//go:build linux

package enrollment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestRetainedEnrollmentConfigSchemaAndTamperBeforeWrites(t *testing.T) {
	cfg := testConfig(t, "claude", "success")
	cfg.SchemaVersion = RetainedNativeConfigSchemaVersion
	cfg.NativeVersion = "2.1.289"
	cfg.NativePath = filepath.Join(cfg.StateRoot, "accounts", "native", "claude-"+cfg.NativeVersion, "claude")
	if err := os.MkdirAll(filepath.Dir(cfg.NativePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.NativePath, []byte("synthetic native bytes"), 0o500); err != nil {
		t.Fatal(err)
	}
	var err error
	cfg.NativeFingerprint, err = persist.CLIContentFingerprint(cfg.NativePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("retained configuration refused: %v", err)
	}
	wrongVersion := cfg
	wrongVersion.NativeVersion = "2.1.288"
	if err := validateConfig(wrongVersion); !errors.Is(err, ErrInvalid) {
		t.Fatal("retained configuration accepted legacy version")
	}
	// The older worker's first validation branch compares schema to 1, before
	// profile reads or native execution. Unknown optional fields cannot bypass it.
	if cfg.SchemaVersion == SchemaVersion {
		t.Fatal("old enrollment worker would accept retained config")
	}
	legacy := cfg
	legacy.SchemaVersion = SchemaVersion
	if err := validateConfig(legacy); !errors.Is(err, ErrInvalid) {
		t.Fatal("retained stamp downgraded to legacy configuration")
	}
	before, err := os.ReadDir(filepath.Join(cfg.StateRoot, "accounts", "jobs", cfg.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.NativePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.NativePath, []byte("tampered native bytes!"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.NativePath, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), "/must-not-execute", cfg); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("tamper did not refuse before Start writes: %v", err)
	}
	after, err := os.ReadDir(filepath.Join(cfg.StateRoot, "accounts", "jobs", cfg.JobID))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("tamper persisted worker intent")
	}
}
