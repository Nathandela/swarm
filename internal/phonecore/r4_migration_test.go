package phonecore

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// r4Phone and hashDir remain shared v2 isolation-test fixtures. They do not model or
// exercise legacy migration.
type r4Phone struct {
	dir     string
	machine string
	wake    *s14aSealer
	content *s14aSealer
}

func (p *r4Phone) resume(t *testing.T) *Core {
	t.Helper()
	core, err := Resume(Config{Dir: p.dir, Machine: p.machine, WakeSealer: p.wake, ContentSealer: p.content})
	if err != nil {
		t.Fatalf("Resume(%s): %v", p.machine, err)
	}
	return core
}

func hashDir(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%s\n%x\n", rel, sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatalf("hashing %s: %v", root, err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// TestRegistryOnly_OldRegistryRequiresReset records the intentional v2 boundary:
// registries written by the retired migration layout are not imported, re-resumed, or
// upgraded in place.
func TestRegistryOnly_OldRegistryRequiresReset(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(registryPath(root)), 0o700); err != nil {
		t.Fatalf("create old registry directory: %v", err)
	}
	if err := os.WriteFile(registryPath(root), []byte(`{"schema_version":1,"machines":[]}`), 0o600); err != nil {
		t.Fatalf("write old registry: %v", err)
	}
	if _, err := OpenMachineRegistry(root); !errors.Is(err, ErrLegacyRegistryResetRequired) {
		t.Fatalf("OpenMachineRegistry error = %v, want ErrLegacyRegistryResetRequired", err)
	}
}
