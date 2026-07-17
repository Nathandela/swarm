package codex

// Shared test helpers for the codex adapter suite: pure argv inspection, the vt
// grid renderer, the T-5 import-boundary probe, and the E9.2 no-IO source scan.
// Mirrors the claude/reference adapter harnesses so the three stay consistent.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/vt"
)

// first returns argv[0] or "" for an empty argv.
func first(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

// containsArg reports whether argv has an element exactly equal to s.
func containsArg(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}

// renderGrid feeds capture through the vt emulator and returns the decoded
// snapshot — the projection the engine hands an adapter at runtime.
func renderGrid(t *testing.T, capture []byte) *vt.Snap {
	t.Helper()
	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	emu.Feed(capture)
	b, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snap
}

// moduleInternalDeps returns pkg's transitive in-module dependencies via
// `go list -deps` (non-test deps only).
func moduleInternalDeps(t *testing.T, pkg string) []string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not found (%v); import-boundary check unavailable", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, goBin, "list", "-deps", "-f", "{{.ImportPath}}", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	const prefix = "github.com/Nathandela/swarm/"
	var deps []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			deps = append(deps, line)
		}
	}
	return deps
}

// bannedIOTokens mirrors internal/adapter's E9.2 banned-token list.
var bannedIOTokens = []string{
	"os.Open", "os.OpenFile", "os.Create", "os.CreateTemp",
	"os.ReadFile", "os.WriteFile", "os.ReadDir", "os.MkdirAll",
	"io/ioutil",
	"net.Listen", "net.Dial", "net.Dialer", "net.ListenConfig",
	"exec.Command", "exec.LookPath",
	"syscall.Open", "syscall.Socket",
}

// scanBannedIO fails t for every banned token in a non-test .go file in dir,
// skipping when no production source exists yet.
func scanBannedIO(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		src, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, tok := range bannedIOTokens {
			if strings.Contains(string(src), tok) {
				t.Errorf("%s names %q — adapters own no fds/disk/sockets (E9.2)", name, tok)
			}
		}
	}
	if scanned == 0 {
		t.Skip("no non-test source files to scan yet (adapter not implemented)")
	}
}
