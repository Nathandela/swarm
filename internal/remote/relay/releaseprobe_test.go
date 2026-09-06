package relay_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildReleaseProbe(t *testing.T, src string) string {
	t.Helper()
	root := repoRoot(t)
	pkgDir, err := os.MkdirTemp(root, "_releaseprobe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(pkgDir) })
	if err := os.WriteFile(filepath.Join(pkgDir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "probe")
	rel, err := filepath.Rel(root, pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, "./"+filepath.ToSlash(rel))
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build release probe: %v\n%s", err, out)
	}
	return bin
}

func runReleaseProbe(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin).CombinedOutput()
	if err != nil {
		t.Fatalf("run release probe: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
