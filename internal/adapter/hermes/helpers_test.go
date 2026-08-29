package hermes

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

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

var bannedIOTokens = []string{
	"os.Open", "os.OpenFile", "os.Create", "os.CreateTemp",
	"os.ReadFile", "os.WriteFile", "os.ReadDir", "os.MkdirAll",
	"io/ioutil",
	"net.Listen", "net.Dial", "net.Dialer", "net.ListenConfig",
	"exec.Command", "exec.LookPath",
	"syscall.Open", "syscall.Socket",
}

func scanBannedIO(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		src, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, token := range bannedIOTokens {
			if strings.Contains(string(src), token) {
				t.Errorf("%s names %q — adapters own no fds/disk/sockets", name, token)
			}
		}
	}
	if scanned == 0 {
		t.Skip("no non-test source files to scan yet")
	}
}

func TestImportBoundary(t *testing.T) {
	const pkg = "github.com/Nathandela/swarm/internal/adapter/hermes"
	allowed := map[string]bool{
		pkg: true,
		"github.com/Nathandela/swarm/internal/adapter": true,
		"github.com/Nathandela/swarm/internal/vt":      true,
	}
	for _, dep := range moduleInternalDeps(t, pkg) {
		if !allowed[dep] {
			t.Errorf("Hermes adapter imports forbidden package %q", dep)
		}
	}
}

func TestStatelessNoIOInSource(t *testing.T) {
	scanBannedIO(t, ".")
}
