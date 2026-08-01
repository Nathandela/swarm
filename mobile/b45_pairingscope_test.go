package swarmmobile_test

// ADR-007 B45 (FAILING FIRST): the unverified-TLS policy is reachable from the PAIRING dial
// and from nothing else.
//
// B45 rules that the handset's pairing rendezvous accepts an unverified certificate, because
// it is the dial that fetches the pin and a pinning-only platform refuses an unpinned dial
// outright. The ruling is sound for that ONE exchange, whose peer the operator authenticates
// by comparing a SAS. It would be the whole hole coming back if it reached App.dial, where
// nothing compares anything and the pin the phone already holds is the only thing standing
// between it and an impostor relay.
//
// So the scope is a property of the code rather than of this comment: exactly one production
// file may name relay.PairingSecurity, and it is the file that performs that dial. A
// behavioural fence cannot say this -- it can show that a pinned dial refuses a bad peer, but
// not that no OTHER dial quietly opted out -- so this one is a source fact, parsed rather than
// grepped for the reason ADR-007 B42 records: a text search matches the comments that describe
// the rule.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// pairingScopeOwner is the ONE production file permitted to construct the unverified policy.
const pairingScopeOwner = "mobile/pairing.go"

// relayPkgPath is the package whose unverified-TLS constructor is scoped here.
const relayPkgPath = "github.com/Nathandela/swarm/internal/remote/relay"

// TestB45_OnlyThePairingDialMayUseTheUnverifiedPolicy walks every production file in the
// repository and fails if any file other than the pairing dial constructs it.
func TestB45_OnlyThePairingDialMayUseTheUnverifiedPolicy(t *testing.T) {
	root := repoRootB45(t)
	fset := token.NewFileSet()
	var callers []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			// `.claude` holds per-agent git worktrees -- full checkouts of this repository that
			// `git worktree add` leaves behind. A walk from the repo root treats them as source
			// and reports findings about an agent's private copy as findings about this tree.
			// Adding the directory to .gitignore does NOT prevent this: gitignore governs what
			// git tracks and has no effect on filepath.WalkDir. Two gates were red for this
			// reason before it was understood.
			case ".git", ".claude", "vendor", "testdata", "build", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		local, ok := relayImportName(f)
		if !ok {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "PairingSecurity" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != local {
				return true
			}
			rel, _ := filepath.Rel(root, path)
			callers = append(callers, filepath.ToSlash(rel)+":"+
				strconv.Itoa(fset.Position(sel.Pos()).Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	if len(callers) == 0 {
		t.Fatalf("nothing in production names relay.PairingSecurity. Either the pairing dial "+
			"stopped using it -- in which case a handset can no longer pair over wss:// at all "+
			"(ADR-007 B45) -- or this fence is now guarding a path production does not take, "+
			"which is the defect B34 recorded. Expected exactly %s", pairingScopeOwner)
	}
	for _, c := range callers {
		if !strings.HasPrefix(c, pairingScopeOwner+":") {
			t.Errorf("%s constructs relay.PairingSecurity, which accepts ANY certificate.\n"+
				"    Only %s may: its peer is authenticated by the Noise handshake and the SAS "+
				"the operator compares. Every other dial has a pin to check and must check it.",
				c, pairingScopeOwner)
		}
	}
}

// relayImportName resolves the local name of the relay import, so an aliased import is still
// checked and a file that does not import it is skipped.
func relayImportName(f *ast.File) (string, bool) {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != relayPkgPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return "", false
			}
			return imp.Name.Name, true
		}
		return "relay", true
	}
	return "", false
}

func repoRootB45(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
