package adapter

// E9.2 — the SOURCE-GREP boundary check, applied to the pure contract package
// itself. Core owns all lifecycle; the contract package (and every adapter
// package) must name NO fd/disk/socket/exec primitive in production source. The
// disk read that used to live here (LoadFixture) moved to internal/adapter/
// fixtureio and the version exec lives in internal/adapter/detect — both
// harness-side, outside this scan — so this package must now be entirely clean.
//
// The same scan runs over the reference adapter in refadapter/refadapter_test.go
// with the SAME banned list (bannedIOTokens); both are kept in sync with the
// enumerated list in docs/specifications/implementation-goals.md E9.2.

import (
	"os"
	"strings"
	"testing"
)

// bannedIOTokens is the E9.2 banned-token list: fd/disk/socket/exec primitives
// that must not appear in an adapter's (or the contract's) production source.
// Kept in sync with implementation-goals.md E9.2 and refadapter's copy.
var bannedIOTokens = []string{
	"os.Open", "os.OpenFile", "os.Create", "os.CreateTemp",
	"os.ReadFile", "os.WriteFile", "os.ReadDir", "os.MkdirAll",
	"io/ioutil",
	"net.Listen", "net.Dial", "net.Dialer", "net.ListenConfig",
	"exec.Command", "exec.LookPath",
	"syscall.Open", "syscall.Socket",
}

// scanBannedIO fails t for every banned token found in a non-test .go file in
// dir. It is the shared engine behind the contract-package and adapter-package
// E9.2 scans.
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
				t.Errorf("%s names %q — the adapter contract owns no fds/disk/sockets (E9.2)", name, tok)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no production source files; the E9.2 grep is vacuous")
	}
}

// TestContractPackage_NoIOInSource — the pure adapter contract package opens
// nothing: no production source file may name any banned fd/disk/socket/exec
// primitive. The loader's disk read now lives in fixtureio, and detection's exec
// in detect — both outside this package.
func TestContractPackage_NoIOInSource(t *testing.T) {
	scanBannedIO(t, ".")
}
