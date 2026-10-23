package loginmigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestFrozenV020CheckLocksSurviveOfflineMigration(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.state, "accounts", "checks", ".lock-"+strings.Repeat("d", 64))
	put(t, path, []byte{})
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = Preflight(f.state); err != nil {
		t.Fatalf("frozen empty check lock blocked preflight: %v", err)
	}
	if _, err = os.Stat(filepath.Join(f.state, "logins")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight mutated login state")
	}
	if err = Run(f.state); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || after.Size() != 0 || after.Mode().Perm() != 0600 || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("retained shared lock was replaced or modified")
	}
}

func TestCheckLockInventoryRefusesMalformedOrHeldFiles(t *testing.T) {
	for _, kind := range []string{"held", "uppercase", "short", "nonhex", "unknown", "nonempty", "public", "readonly", "directory", "symlink", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			name := ".lock-" + strings.Repeat("d", 64)
			switch kind {
			case "uppercase":
				name = ".lock-" + strings.Repeat("D", 64)
			case "short":
				name = ".lock-" + strings.Repeat("d", 63)
			case "nonhex":
				name = ".lock-" + strings.Repeat("z", 64)
			case "unknown":
				name = "unrecognized-empty-file"
			}
			path := filepath.Join(f.state, "accounts", "checks", name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink", "hardlink":
				target := filepath.Join(filepath.Dir(f.state), "empty-lock-target")
				put(t, target, []byte{})
				var err error
				if kind == "symlink" {
					err = os.Symlink(target, path)
				} else {
					err = os.Link(target, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			default:
				put(t, path, []byte{})
				if kind == "nonempty" {
					put(t, path, "unexpected data")
				}
				if kind == "public" {
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "readonly" {
					if err := os.Chmod(path, 0400); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "held" {
				lock, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Close() }()
				if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			err := Preflight(f.state)
			if kind == "held" && !errors.Is(err, ErrLiveWriter) {
				t.Fatalf("held legacy lock not classified live: %v", err)
			}
			if kind != "held" && !errors.Is(err, ErrUnsupported) {
				t.Fatalf("malformed legacy lock accepted: %v", err)
			}
			if _, err = os.Stat(filepath.Join(f.state, "logins")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refusal mutated login state")
			}
		})
	}
}

func TestRetainedCheckLockDoesNotReplaceWorkerCustodyProof(t *testing.T) {
	f := newFixture(t)
	base := filepath.Join(f.state, "accounts", "checks")
	put(t, filepath.Join(base, ".lock-"+strings.Repeat("d", 64)), []byte{})
	generation := strings.Repeat("e", 32)
	ref := map[string]any{"schema_version": 2, "generation": generation, "worker": oldProcess{}}
	put(t, filepath.Join(base, generation, "worker.json"), ref)
	if err := inventoryCheckWorkers(f.state); !errors.Is(err, ErrLiveWriter) {
		t.Fatalf("missing worker stop proof admitted: %v", err)
	}
	put(t, filepath.Join(base, generation, "stopped.json"), map[string]any{"schema_version": 2, "ref": ref, "writers_stopped": true, "native": oldProcess{}})
	if err := inventoryCheckWorkers(f.state); err != nil {
		t.Fatal(err)
	}
}
