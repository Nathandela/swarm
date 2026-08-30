package phonecore

// PB-SYNC-8's FIXTURE RULE, made mechanical: "no test may use a nonzero roster cursor,
// since production never emits one -- that is precisely why the existing fixtures hide
// this."
//
// The daemon leaves a roster record's Cursor DELIBERATELY UNSET (internal/daemon/journal.go:
// "a roster record is a set member keyed by SessionID, NOT a point in the cursor-ordered
// event stream"), while SessionCache.Apply drops any record whose Cursor is below the
// highest applied one. A fixture that stamps a roster record with a plausible-looking
// nonzero cursor therefore tests a wire the machine never puts on it, and every assertion
// built on that fixture passes over a repair channel that is a no-op in production. That is
// standing defect class (ii) -- a plausible-but-wrong value hiding a brick -- and this is
// the guard that stops it coming back.
//
// It is a SOURCE guard rather than a runtime one because the subject is the fixtures
// themselves: nothing at runtime can tell a roster record whose cursor was invented from
// one whose cursor was read off the wire.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rosterTypeNames are the composite-literal type names that carry a journal record on any
// of the three hops. A literal of one of these, tagged as a roster record, is a roster
// fixture wherever it lives.
var rosterTypeNames = map[string]bool{
	"JournalRecord": true, // schema.JournalRecord / protocol.JournalRecord
	"Record":        true, // journal.Record, the daemon-internal shape
}

// repoRoot locates the module root so the guard covers EVERY package, not just this one.
// A rule enforced only where it is already obeyed is not a rule.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("locate the module root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestS10_NoTestFixtureStampsANonzeroRosterCursor walks every _test.go in the module and
// fails on any composite literal that is BOTH tagged as a roster record and given a nonzero
// Cursor.
func TestS10_NoTestFixtureStampsANonzeroRosterCursor(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", ".codex", ".gradle", "build", "dist", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // an unparseable file is some other guard's problem
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isRosterRecordLit(lit) {
				return true
			}
			if seq, bad := nonzeroCursor(lit); bad {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+":"+strconv.Itoa(fset.Position(lit.Pos()).Line)+
					" (cursor "+seq+")")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d roster fixture(s) carry a NONZERO cursor:\n  %s\n\n"+
			"Production never emits one: internal/daemon/journal.go leaves a roster record's "+
			"Cursor deliberately unset because a roster record is a set member keyed by "+
			"SessionID, not a point in the cursor-ordered event stream. A fixture that invents "+
			"one makes SessionCache.Apply accept a record it would DROP on the real wire, so "+
			"every assertion resting on it passes over a journal repair channel that is a "+
			"silent no-op in production -- which is exactly how PB-SYNC-8's defect survived to "+
			"be found by inspection rather than by a test",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// isRosterRecordLit reports whether lit is a journal-record composite literal tagged as a
// roster record -- either Type: "roster" or Type: <pkg>.TypeRoster.
func isRosterRecordLit(lit *ast.CompositeLit) bool {
	name := ""
	switch tn := lit.Type.(type) {
	case *ast.Ident:
		name = tn.Name
	case *ast.SelectorExpr:
		name = tn.Sel.Name
	default:
		return false
	}
	if !rosterTypeNames[name] {
		return false
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Type" {
			continue
		}
		switch v := kv.Value.(type) {
		case *ast.BasicLit:
			if s, err := strconv.Unquote(v.Value); err == nil && s == "roster" {
				return true
			}
		case *ast.SelectorExpr:
			if v.Sel.Name == "TypeRoster" {
				return true
			}
		case *ast.Ident:
			if v.Name == "TypeRoster" {
				return true
			}
		}
	}
	return false
}

// nonzeroCursor reports the literal's Cursor value when it is a nonzero constant.
func nonzeroCursor(lit *ast.CompositeLit) (string, bool) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Cursor" {
			continue
		}
		bl, ok := kv.Value.(*ast.BasicLit)
		if !ok || bl.Kind != token.INT {
			continue // a non-constant cursor is not a stamped fixture value
		}
		if bl.Value != "0" {
			return bl.Value, true
		}
	}
	return "", false
}
