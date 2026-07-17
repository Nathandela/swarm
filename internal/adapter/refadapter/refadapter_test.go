package refadapter

// E9.5 / T-5 — the FIXTURE-ONLY REFERENCE ADAPTER. It proves the anti-corruption
// boundary: an adapter can be built purely from a recorded fixture, pass the
// frozen conformance suite, and depend on NOTHING but the adapter contract +
// internal/vt — so adding a real adapter later (Epic 10/11) touches only its own
// package, never daemon/protocol/TUI (T-5).
//
// PINNED CONSTRUCTOR:  func New(fx adapter.Fixture) adapter.Adapter
//
// This test package IMPORTS the contract (github.com/Nathandela/swarm/internal/
// adapter), so — by design — it cannot build until the contract package has
// production files. It is therefore OUTSIDE the pinned RED command
//
//	go test ./internal/adapter/ ./cmd/swarm-char/
//
// (that command scopes to the contract + harness packages, which fail with
// undefined symbols only). The reference adapter's RED reason is "the contract
// must exist first", which is inherent to any consumer of the boundary.
//
// PIN (per orchestrator brief): E9.5's "no package outside adapters/<name>/
// changes" is proven HERE by an import-list assertion (go list -deps), NOT by
// commit archaeology.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

const refPkg = "github.com/Nathandela/swarm/internal/adapter/refadapter"

// loadRef loads the reference fixture and builds the reference adapter from it.
func loadRef(t *testing.T) (adapter.Adapter, adapter.Fixture) {
	t.Helper()
	fx, err := adapter.LoadFixture("testdata/reference.json")
	if err != nil {
		t.Fatalf("load reference fixture: %v", err)
	}
	return New(fx), fx
}

// TestReferenceAdapter_PassesConformance — the T-1 freeze applied to the real
// reference adapter: zero violations through the interface.
func TestReferenceAdapter_PassesConformance(t *testing.T) {
	a, _ := loadRef(t)
	if errs := adapter.CheckConformance(a); len(errs) != 0 {
		t.Fatalf("reference adapter is not conformant: %v", errs)
	}
	// Also run the *testing.T wrapper so the reference adapter exercises the
	// exact entrypoint real adapters (Epic 10/11) will call.
	adapter.Conformance(t, a)
}

// TestReferenceAdapter_ExtractsFixtureConversationID — an adapter BUILT from a
// fixture recognizes the conversation id that fixture recorded, and returns
// ("", false) on unrelated input.
func TestReferenceAdapter_ExtractsFixtureConversationID(t *testing.T) {
	a, fx := loadRef(t)

	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	emu.Feed(fx.PTYCapture)
	b, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	id, ok := a.ExtractConversationID(snap, fx.PTYCapture)
	if !ok || id == "" {
		t.Fatalf("reference adapter did not extract an id from its own fixture (got %q, %v)", id, ok)
	}
	if !strings.Contains(string(fx.PTYCapture), id) {
		t.Errorf("extracted id %q is not present in the recorded capture", id)
	}

	// A grid/tail with no recorded id yields nothing. (Using a nil grid here,
	// not the fixture's snap, since that snap legitimately still shows the id.)
	if gid, gok := a.ExtractConversationID(nil, []byte("nothing here")); gok || gid != "" {
		t.Errorf("expected no id from unrelated input, got (%q, %v)", gid, gok)
	}
}

// TestReferenceAdapter_Capability — the reference adapter demonstrates the full
// capability surface (hooks + resume + conversation-id), so its matrix entry is
// a worked example of E9.6 output.
func TestReferenceAdapter_Capability(t *testing.T) {
	a, fx := loadRef(t)
	entry := adapter.Capability(a, fx)
	if !entry.Hooks || !entry.Resume || !entry.ConversationID {
		t.Errorf("reference capability incomplete: %+v", entry)
	}
	if entry.CLI != fx.CLI || entry.Version != fx.Version {
		t.Errorf("capability identity %q/%q != fixture %q/%q", entry.CLI, entry.Version, fx.CLI, fx.Version)
	}
}

// TestReferenceAdapter_ImportBoundary — T-5 PROOF. The reference adapter's
// package may transitively import, within this module, ONLY the adapter contract
// and internal/vt. Any dependency on internal/daemon, internal/shim,
// internal/wire, internal/persist, internal/status, internal/transcript, the
// TUI, or cmd/* is a boundary violation that would force core edits to add an
// adapter.
func TestReferenceAdapter_ImportBoundary(t *testing.T) {
	allowed := map[string]bool{
		refPkg: true, // the package itself
		"github.com/Nathandela/swarm/internal/adapter": true,
		"github.com/Nathandela/swarm/internal/vt":      true,
	}
	for _, dep := range moduleInternalDeps(t, refPkg) {
		if !allowed[dep] {
			t.Errorf("reference adapter imports forbidden package %q (T-5: adapters depend on the contract + vt only)", dep)
		}
	}
}

// TestReferenceAdapter_Stateless_NoIOInSource — E9.2 statelessness, automated.
// The review checklist ("grep adapter packages for os.Open/os.Create/
// os.MkdirAll/net.Listen/net.Dial outside tests — zero hits") is enforced here
// as a source scan over the reference adapter's non-test files. Core owns all
// lifecycle; adapters own no fds/disk/sockets.
func TestReferenceAdapter_Stateless_NoIOInSource(t *testing.T) {
	banned := []string{"os.Open", "os.Create", "os.MkdirAll", "os.OpenFile", "net.Listen", "net.Dial"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, tok := range banned {
			if strings.Contains(string(src), tok) {
				t.Errorf("%s uses %q — adapters own no fds/disk/sockets (E9.2)", f, tok)
			}
		}
	}
	if scanned == 0 {
		t.Skip("no non-test source files to scan yet (reference adapter not implemented)")
	}
}

// moduleInternalDeps returns pkg's transitive dependencies that belong to this
// module, via `go list -deps`. It shells out to the toolchain (already a build
// dependency of the test suite); if go is unavailable the boundary check is
// skipped rather than failed.
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
