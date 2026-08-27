// FAILING-FIRST (TDD RED, GG-5) contract for the TUI's hello capability set.
//
// The hands-off handoff (ADR-010 Amendment 4) is capability-negotiated, and the
// daemon guard is already committed: handleLaunch refuses any launch carrying
// handoff_from with CodeCapabilityRefused unless CapHandsOffHandoff was negotiated
// at hello. The TUI offered only {"attach","subscribe"}, so every hands-off launch
// it submitted would have come back refused and the feature could not work at all.
//
// The TUI dials TWICE, and the second is the dangerous one: runTUI dials the
// long-lived roster client, and daemonRestarter dials the REPLACEMENT client after
// a daemon auto-upgrade swaps the first one out. A capability set that is right in
// one place and wrong in the other produces a feature that works until the daemon
// upgrades itself and then silently starts refusing -- an intermittent failure that
// is miserable to diagnose. Both sites therefore share one definition, and this
// test fails if either stops using it.
//
// attachDialer is deliberately NOT covered: it dials {"attach"} per attach and
// never submits a launch, so it has no business negotiating a launch capability.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestTUICapsNegotiateHandsOffHandoff(t *testing.T) {
	caps := tuiCaps()
	for _, want := range []string{"attach", "subscribe", protocol.CapHandsOffHandoff} {
		if !slices.Contains(caps, want) {
			t.Errorf("tuiCaps() = %q, missing %q", caps, want)
		}
	}
}

// TestTUIDialSitesUseTheSharedCaps pins the two call sites BY NAME. Sharing one
// definition is what makes the two impossible to diverge; this proves they actually
// share it, so a future edit that re-inlines a list at either site fails here rather
// than in production after an auto-upgrade.
func TestTUIDialSitesUseTheSharedCaps(t *testing.T) {
	fset := token.NewFileSet()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	file, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	for _, name := range []string{"runTUI", "daemonRestarter"} {
		decl := funcDecl(file, name)
		if decl == nil {
			t.Fatalf("main.go no longer declares %s", name)
		}
		if !callsTUICaps(decl) {
			t.Errorf("%s dials without tuiCaps(): a divergent capability list here means "+
				"hands-off handoff is refused by the daemon at that site", name)
		}
	}

	// Belt and braces: no capability list anywhere in main.go may offer BOTH "attach"
	// and "subscribe" -- the TUI roster client's signature, and only the TUI's; `swarm
	// watch` offers subscribe alone and attachDialer attach alone, and neither ever
	// launches -- without also offering the hands-off capability. This catches a NEW
	// dial site, which the two named checks above cannot see.
	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		elems := make([]string, 0, len(lit.Elts))
		for _, e := range lit.Elts {
			elems = append(elems, string(src[fset.Position(e.Pos()).Offset:fset.Position(e.End()).Offset]))
		}
		if !slices.Contains(elems, `"attach"`) || !slices.Contains(elems, `"subscribe"`) {
			return true
		}
		found++
		if !slices.Contains(elems, "protocol."+capHandsOffHandoffIdent) &&
			!slices.Contains(elems, `"`+protocol.CapHandsOffHandoff+`"`) {
			t.Errorf("capability list at %s is a TUI roster dial without the hands-off capability: {%s}",
				fset.Position(lit.Pos()), strings.Join(elems, ", "))
		}
		return true
	})
	if found == 0 {
		t.Fatal("no attach+subscribe capability list found in main.go; this test would pass vacuously")
	}
}

// capHandsOffHandoffIdent is the Go identifier the source is expected to name, kept
// next to the constant it mirrors so a rename is caught by the compiler here.
const capHandsOffHandoffIdent = "CapHandsOffHandoff"

func funcDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func callsTUICaps(decl *ast.FuncDecl) bool {
	found := false
	ast.Inspect(decl, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "tuiCaps" {
			found = true
		}
		return true
	})
	return found
}
