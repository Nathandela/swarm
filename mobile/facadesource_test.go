package swarmmobile_test

// Shared source-level machinery for the PB-BIND-* standing guards (slice S8).
//
// These guards deliberately do NOT import the facade. They read it as SOURCE -- via
// `go list` plus go/parser -- for two reasons:
//
//  1. A guard that imports the package it guards cannot report "the package does not
//     exist" as an assertion; it fails to compile, and a compile failure is one lump
//     error for the whole directory instead of one message per requirement.
//  2. PB-BIND-2's real subject is the EXPORTED SURFACE, not runtime behaviour. gobind
//     itself works from source; so must the guard that wraps it.
//
// The behavioural half of S8 (PB-BIND-3's exercise, PB-BIND-5's injection, PB-BIND-6's
// -race hammer, PB-SAS-2's KAT) lives in ./conformance and DOES import the facade.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	// facadePkgPath is the ONE bound surface (PB-BIND-1). It is deliberately NOT under
	// internal/: gobind's generated wrapper lives outside the module's internal boundary,
	// so an internal package cannot be bound directly (requirements 4.2).
	facadePkgPath = "github.com/Nathandela/swarm/mobile"

	// facadePkgName is the Go package name, which is what gobind turns into the Java
	// package. The directory is `mobile`; the package is `swarmmobile` so the Kotlin
	// import reads `swarmmobile.App`.
	facadePkgName = "swarmmobile"

	// phonecorePkgPath is the internal core the facade wraps. It must never be bound.
	phonecorePkgPath = "github.com/Nathandela/swarm/internal/phonecore"
)

// facadeSource is the parsed facade package.
type facadeSource struct {
	Dir     string
	Name    string
	GoFiles []string
	Fset    *token.FileSet
	Files   []*ast.File
	Doc     string // concatenated package doc comments
}

// loadFacade locates and parses the facade. It FATALS with the PB-BIND-1 message when
// the package does not exist, which is the correct RED for this slice: every guard in
// this directory is a guard ON that package.
func loadFacade(t *testing.T) *facadeSource {
	t.Helper()

	cmd := exec.Command("go", "list", "-json", facadePkgPath)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("PB-BIND-1: the bound facade package %s does not exist.\n"+
			"S8 must create it as a NON-INTERNAL package (package %s) that wraps %s.\n"+
			"go list said: %v",
			facadePkgPath, facadePkgName, phonecorePkgPath, err)
	}

	var meta struct {
		Dir     string
		Name    string
		GoFiles []string
	}
	if err := json.Unmarshal(out, &meta); err != nil {
		t.Fatalf("decode go list -json for %s: %v", facadePkgPath, err)
	}
	if len(meta.GoFiles) == 0 {
		t.Fatalf("PB-BIND-1: %s has no non-test Go files, so there is nothing to bind", facadePkgPath)
	}
	if meta.Name != facadePkgName {
		t.Fatalf("PB-BIND-1: facade package name is %q, want %q (gobind turns the PACKAGE name "+
			"into the Java package, so this is part of the Kotlin-visible contract)", meta.Name, facadePkgName)
	}

	src := &facadeSource{Dir: meta.Dir, Name: meta.Name, GoFiles: meta.GoFiles, Fset: token.NewFileSet()}
	var docs []string
	for _, base := range meta.GoFiles {
		f, err := parser.ParseFile(src.Fset, filepath.Join(meta.Dir, base), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", base, err)
		}
		src.Files = append(src.Files, f)
		if f.Doc != nil {
			docs = append(docs, f.Doc.Text())
		}
	}
	src.Doc = strings.Join(docs, "\n")
	return src
}

// parseGoFile parses one Go file with comments, for the guards that read source outside
// the facade (the frozen crypto package's SAS wordlist).
func parseGoFile(fset *token.FileSet, path string) (*ast.File, error) {
	return parser.ParseFile(fset, path, nil, parser.ParseComments)
}

// repoRoot walks up from the test's working directory to the module root.
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
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// exportedSymbol is one element of the bound contract.
type exportedSymbol struct {
	Kind  string // const | var | type | field | func | method | ifacemethod
	Owner string // declaring type for field/method/ifacemethod, "" otherwise
	Name  string
	Sig   string // rendered signature or type, "" for a bare type/const/var
	Decl  *ast.FuncDecl
}

// Line is the golden-file rendering (PB-BIND-7). Parameter NAMES are deliberately
// excluded: renaming a parameter is not a breaking change for a positional JNI binding,
// while a type or arity change is.
func (s exportedSymbol) Line() string {
	switch s.Kind {
	case "const", "var":
		return s.Kind + " " + s.Name
	case "type":
		return "type " + s.Name + " " + s.Sig
	case "field":
		return "field " + s.Owner + "." + s.Name + " " + s.Sig
	case "method", "ifacemethod":
		return s.Kind + " " + s.Owner + "." + s.Name + s.Sig
	default:
		return "func " + s.Name + s.Sig
	}
}

// exportedSurface renders every exported element of the facade, sorted.
func exportedSurface(src *facadeSource) []exportedSymbol {
	var out []exportedSymbol
	for _, f := range src.Files {
		for _, d := range f.Decls {
			switch decl := d.(type) {
			case *ast.GenDecl:
				out = append(out, genDeclSymbols(decl)...)
			case *ast.FuncDecl:
				if !decl.Name.IsExported() {
					continue
				}
				sig := renderFuncType(decl.Type)
				if decl.Recv == nil {
					out = append(out, exportedSymbol{Kind: "func", Name: decl.Name.Name, Sig: sig, Decl: decl})
					continue
				}
				owner := receiverTypeName(decl.Recv)
				if owner == "" || !ast.IsExported(owner) {
					continue
				}
				out = append(out, exportedSymbol{Kind: "method", Owner: owner, Name: decl.Name.Name, Sig: sig, Decl: decl})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Line() < out[j].Line() })
	return out
}

func genDeclSymbols(decl *ast.GenDecl) []exportedSymbol {
	var out []exportedSymbol
	for _, spec := range decl.Specs {
		switch sp := spec.(type) {
		case *ast.ValueSpec:
			kind := "var"
			if decl.Tok == token.CONST {
				kind = "const"
			}
			for _, n := range sp.Names {
				if n.IsExported() {
					out = append(out, exportedSymbol{Kind: kind, Name: n.Name})
				}
			}
		case *ast.TypeSpec:
			if !sp.Name.IsExported() {
				continue
			}
			name := sp.Name.Name
			switch under := sp.Type.(type) {
			case *ast.StructType:
				out = append(out, exportedSymbol{Kind: "type", Name: name, Sig: "struct"})
				for _, fld := range under.Fields.List {
					ft := types.ExprString(fld.Type)
					for _, fn := range fld.Names {
						if fn.IsExported() {
							out = append(out, exportedSymbol{Kind: "field", Owner: name, Name: fn.Name, Sig: ft})
						}
					}
				}
			case *ast.InterfaceType:
				out = append(out, exportedSymbol{Kind: "type", Name: name, Sig: "interface"})
				for _, m := range under.Methods.List {
					ft, ok := m.Type.(*ast.FuncType)
					if !ok {
						continue
					}
					for _, mn := range m.Names {
						if mn.IsExported() {
							out = append(out, exportedSymbol{Kind: "ifacemethod", Owner: name, Name: mn.Name, Sig: renderFuncType(ft)})
						}
					}
				}
			default:
				out = append(out, exportedSymbol{Kind: "type", Name: name, Sig: types.ExprString(sp.Type)})
			}
		}
	}
	return out
}

func receiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	switch t := recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// renderFuncType renders "(paramType, ...) (resultType, ...)" -- types only.
func renderFuncType(ft *ast.FuncType) string {
	params := fieldTypes(ft.Params)
	results := fieldTypes(ft.Results)
	sig := "(" + strings.Join(params, ", ") + ")"
	switch len(results) {
	case 0:
	case 1:
		sig += " " + results[0]
	default:
		sig += " (" + strings.Join(results, ", ") + ")"
	}
	return sig
}

func fieldTypes(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var out []string
	for _, f := range fl.List {
		t := types.ExprString(f.Type)
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, t)
		}
	}
	return out
}

// entryPoints are the exported funcs and methods -- the JNI entry points PB-BIND-5
// requires a panic barrier on.
func entryPoints(src *facadeSource) []exportedSymbol {
	var out []exportedSymbol
	for _, s := range exportedSurface(src) {
		if s.Kind == "func" || s.Kind == "method" {
			out = append(out, s)
		}
	}
	return out
}

// constIntValue returns the integer value of an exported untyped/int constant.
func constIntValue(src *facadeSource, name string) (int, bool) {
	for _, f := range src.Files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if n.Name != name || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.INT {
						return 0, false
					}
					v, err := strconv.Atoi(lit.Value)
					if err != nil {
						return 0, false
					}
					return v, true
				}
			}
		}
	}
	return 0, false
}

// facadeSourceText is every non-test source byte of the facade, for the call-site bans
// (the S7 residuals: Sequencer.Next, ReplyCache.Take).
func facadeSourceText(t *testing.T, src *facadeSource) string {
	t.Helper()
	var b strings.Builder
	for _, base := range src.GoFiles {
		raw, err := os.ReadFile(filepath.Join(src.Dir, base))
		if err != nil {
			t.Fatalf("read %s: %v", base, err)
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	return b.String()
}

// goListDeps returns the non-standard transitive closure of pkg for a GOOS/GOARCH,
// mirroring internal/phonecore/deps_allowlist_test.go so the PB-BIND-0 guard reads the
// same way after it moves to the facade.
func goListDeps(t *testing.T, pkg, goos, goarch string) ([]string, error) {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", pkg)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var deps []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		deps = append(deps, line)
	}
	sort.Strings(deps)
	return deps, nil
}
