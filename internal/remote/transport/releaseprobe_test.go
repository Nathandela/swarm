package transport_test

// Three tests in this package prove a property by COMPILING A NON-TEST BINARY: that a
// release build cannot enable the loopback cleartext carve-out, cannot widen the machine
// policy past a loopback literal, and cannot select a trust-root source it is not on. Each
// needs a real `main` package inside this module, because the thing under test is what
// `go build` produces rather than what `go test` does.
//
// EACH OF THEM USED TO WRITE THAT PACKAGE INTO THE SOURCE TREE, at a fixed path, cleaned up
// with t.Cleanup. A killed run -- ^C, a timeout, a crashed test binary -- skips Cleanup and
// leaves the directory behind, and a reviewer hit the consequence:
//
//	pattern ./...: stat .../releasecheck: directory not found
//
// from a CONCURRENT build, because the stale directory was inside the package pattern while
// its .go file was half-written or already gone.
//
// The fix is the directory NAME, not more cleanup. Go's package patterns ignore any path
// element beginning with `_` or `.`, while an explicit relative path to one still builds. So
// the probe lives at `_releaseprobe-<random>`: invisible to `./...`, unique per run so two
// concurrent test binaries cannot collide, and harmless if a killed run leaves it. Cleanup is
// still registered -- this makes the abort case survivable rather than tidy.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildReleaseProbe compiles src as a plain (non-test) binary inside this module and returns
// the path to it. The package directory is excluded from every package pattern, so a run that
// dies before cleanup cannot break anyone else's build.
func buildReleaseProbe(t *testing.T, src string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}

	root := repoRoot(t)
	pkgDir, err := os.MkdirTemp(root, "_releaseprobe-")
	if err != nil {
		t.Fatalf("create the probe package: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(pkgDir) })
	if err := os.WriteFile(filepath.Join(pkgDir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write the probe main: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "probe")
	rel, err := filepath.Rel(root, pkgDir)
	if err != nil {
		t.Fatalf("locate the probe package: %v", err)
	}
	build := exec.Command("go", "build", "-o", bin, "./"+filepath.ToSlash(rel))
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the release binary failed: %v\n%s", err, out)
	}
	return bin
}

// runReleaseProbe runs a built probe and returns its trimmed output.
func runReleaseProbe(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin).CombinedOutput()
	if err != nil {
		t.Fatalf("running the release binary failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestReleaseProbe_IsInvisibleToPackagePatterns is the fence on the fix itself. If the probe
// directory ever stops being excluded, a killed run reintroduces exactly the broken concurrent
// build this arrangement exists to prevent -- and nothing else in the suite would notice,
// because the damage only shows up in someone ELSE's process.
func TestReleaseProbe_IsInvisibleToPackagePatterns(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}
	root := repoRoot(t)
	dir, err := os.MkdirTemp(root, "_releaseprobe-")
	if err != nil {
		t.Fatalf("create the probe package: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// A DELIBERATELY BROKEN file: a stale directory left by a killed run is exactly this --
	// present, and not compilable. `go list ./...` must not care.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("this is not go"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	list := exec.Command("go", "list", "./...")
	list.Dir = root
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("`go list ./...` failed with an uncompilable probe directory present: %v\n%s\n"+
			"That is the concurrent-build breakage this naming exists to prevent", err, out)
	}
	if name := filepath.Base(dir); strings.Contains(string(out), name) {
		t.Fatalf("the probe package %s is inside ./..., so a killed run leaves a directory that "+
			"breaks every concurrent build", name)
	}
}
