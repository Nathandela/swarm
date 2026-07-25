package gate

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Paths. Every path in this package is derived from the repository root, never
// from the working directory, so the tests survive being moved.
// ---------------------------------------------------------------------------

// repoRoot walks up from the test's working directory to the directory holding
// go.mod. It fails the test rather than returning an error: a gate that cannot
// find the repository has nothing meaningful to say.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found walking up from the test working directory")
		}
		dir = parent
	}
}

// androidRoot is the Gradle root. S13 places the Android project under
// android/ rather than at the repository root so that gradlew, settings, the
// wrapper directory and Gradle's build outputs do not land in the top level of
// a Go repository, and so that a second module (for example a separate
// core-aar module) has somewhere to go.
func androidRoot(t *testing.T) string { return filepath.Join(repoRoot(t), "android") }

func appModule(t *testing.T) string { return filepath.Join(androidRoot(t), "app") }

// pinPath is PB-TOOL-1's checked-in toolchain pin.
func pinPath(t *testing.T) string { return filepath.Join(androidRoot(t), "toolchain.env") }

// readFileOrFail reports a missing file as the specific requirement failure it
// is, rather than as an opaque os.ErrNotExist.
func readFileOrFail(t *testing.T, path, requirement string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: cannot read %s: %v", requirement, mustRel(t, path), err)
	}
	return string(b)
}

func mustRel(t *testing.T, path string) string {
	t.Helper()
	rel, err := filepath.Rel(repoRoot(t), path)
	if err != nil {
		return path
	}
	return rel
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

// ---------------------------------------------------------------------------
// The toolchain pin, read the way a build reads it: by sourcing it.
// ---------------------------------------------------------------------------

// scrubbedEnv is the environment a "fresh shell" gets. It deliberately excludes
// /usr/local/bin, which on this host holds BOTH the system Gradle (9.6.1) and
// the Go toolchain -- so a pin that silently relies on either is caught. HOME is
// kept because the Go build cache and the Gradle user home live under it and
// removing them would test the caches, not the pin.
func scrubbedEnv() []string {
	return []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"TMPDIR=" + os.TempDir(),
	}
}

// sourcePin sources the pin file in a scrubbed /bin/sh and returns the resulting
// environment. It is the mechanical form of PB-TOOL-1's criterion "a fresh shell
// sourcing it can build": if sourcing fails, or the pin needs something the
// scrubbed shell does not have, this fails.
func sourcePin(t *testing.T, root string) map[string]string {
	t.Helper()
	pin := filepath.Join(root, "android", "toolchain.env")
	// `env -0` so a value containing whitespace cannot be mis-split.
	cmd := exec.Command("/bin/sh", "-c", ". "+shellQuote(pin)+" && env -0")
	cmd.Env = scrubbedEnv()
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("PB-TOOL-1: sourcing %s in a fresh shell failed: %v\nstderr:\n%s",
			mustRel(t, pin), err, stderr.String())
	}
	env := map[string]string{}
	for _, kv := range strings.Split(string(out), "\x00") {
		if kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[k] = v
	}
	return env
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// pinInt reads a pinned value that must be an integer (API levels).
func pinInt(t *testing.T, env map[string]string, key string) int {
	t.Helper()
	raw, ok := env[key]
	if !ok || strings.TrimSpace(raw) == "" {
		t.Fatalf("PB-TOOL-1: the toolchain pin does not export %s", key)
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		t.Fatalf("PB-TOOL-1: %s=%q is not an integer API level: %v", key, raw, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// git, for the "never in the repo" assertions.
// ---------------------------------------------------------------------------

// trackedFiles lists the files git actually tracks. PB-TOOL-3's "no keystore in
// git" must be asserted against the index, not against the working tree: an
// ignored keystore sitting on disk is fine, a committed one is not.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		// An empty set would make every "nothing forbidden is tracked"
		// assertion below pass vacuously.
		t.Fatalf("git ls-files returned no files; the hygiene assertions would pass vacuously")
	}
	return files
}

// goListDeps returns the full dependency closure of a package.
func goListDeps(t *testing.T, root, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, stderr.String())
	}
	var deps []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			deps = append(deps, line)
		}
	}
	if len(deps) == 0 {
		t.Fatalf("go list -deps %s returned nothing; the closure assertion would pass vacuously", pkg)
	}
	return deps
}
