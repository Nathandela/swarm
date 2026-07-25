package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) guards for PB-BIND-4 (no unnecessary secret crosses
// JNI), PB-BIND-5 (no Go panic crosses the boundary), PB-BIND-6 (the documented
// threading/lifecycle contract) and PB-SAS-1 (the SAS comes from the shared Go core and
// the emoji table is never re-implemented in Kotlin) -- plus the S7 residuals that land
// directly on this surface.
//
// These are the SOURCE-level halves. The runtime halves -- a panic injected per entry
// point, a -race hammer, a slow callback, the SAS KAT -- are in ./conformance.

import (
	"go/ast"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---- PB-BIND-4: no unnecessary secret crosses the boundary ---------------------

// facadeLocalTypes are the only type expressions permitted anywhere in the exported
// surface: the Go primitives gomobile maps, []byte, error, and types the facade itself
// declares. Anything else is BOTH a bind hazard (4.1: crypto.KeyStore, protocol.Control,
// status.Group, time.Time are all unbindable) and a custody hazard (a cross-package type
// is how key material reaches the boundary by accident).
var facadeAllowedBasics = map[string]bool{
	"string": true, "bool": true, "error": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"float32": true, "float64": true, "byte": true,
}

func TestPBBIND4_ExportedSurfaceCarriesOnlyFacadeTypes(t *testing.T) {
	src := loadFacade(t)

	declared := map[string]bool{}
	for _, s := range exportedSurface(src) {
		if s.Kind == "type" {
			declared[s.Name] = true
		}
	}

	var bad []string
	check := func(where string, expr ast.Expr) {
		if !typeIsFacadeLocal(expr, declared) {
			bad = append(bad, where+": "+types.ExprString(expr))
		}
	}
	for _, f := range src.Files {
		for _, d := range f.Decls {
			switch decl := d.(type) {
			case *ast.FuncDecl:
				if !decl.Name.IsExported() {
					continue
				}
				owner := receiverTypeName(decl.Recv)
				if decl.Recv != nil && (owner == "" || !ast.IsExported(owner)) {
					continue
				}
				name := decl.Name.Name
				if owner != "" {
					name = owner + "." + name
				}
				for _, fl := range []*ast.FieldList{decl.Type.Params, decl.Type.Results} {
					if fl == nil {
						continue
					}
					for _, fld := range fl.List {
						check(name, fld.Type)
					}
				}
			case *ast.GenDecl:
				if decl.Tok != token.TYPE {
					continue
				}
				for _, spec := range decl.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ts.Name.IsExported() {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, fld := range st.Fields.List {
						for _, fn := range fld.Names {
							if fn.IsExported() {
								check(ts.Name.Name+"."+fn.Name, fld.Type)
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("PB-BIND-4/PB-BIND-2: %d exported element(s) carry a type that is neither a "+
			"gomobile primitive nor declared by the facade. Every one of these is a cross-package "+
			"type crossing JNI:\n\t%s", len(bad), strings.Join(bad, "\n\t"))
	}
}

// TestPBBIND4_TheOnlySecretCrossingIsNamedAndInbound pins ADR-007 B8. The one deliberate
// exception is a transient per-tier data key, unwrapped by an authenticated-Keystore AES
// KEK on the Java side and passed Java -> Go, zeroized after use. It is DIRECTIONAL: no
// exported method may RETURN raw bytes, because that is the only shape in which long-term
// private material could leave the Go core.
func TestPBBIND4_TheOnlySecretCrossingIsNamedAndInbound(t *testing.T) {
	src := loadFacade(t)

	// The permitted []byte parameters. SendInput carries keystrokes, which are sensitive
	// but are not KEY material and are inbound-only; the two Install* methods are B8's
	// single crossing.
	allowedByteParams := map[string]bool{
		"App.InstallWakeKey":    true,
		"App.InstallContentKey": true,
		"App.SendInput":         true,
	}
	custodyCrossings := []string{"App.InstallWakeKey", "App.InstallContentKey"}

	seen := map[string]bool{}
	for _, s := range entryPoints(src) {
		name := s.Name
		if s.Kind == "method" {
			name = s.Owner + "." + s.Name
		}
		if s.Decl == nil {
			continue
		}
		if s.Decl.Type.Results != nil {
			for _, r := range s.Decl.Type.Results.List {
				if isByteSlice(r.Type) {
					t.Errorf("PB-BIND-4: %s RETURNS []byte. The custody contract is inbound only "+
						"(ADR-007 B8): Go returns sealed blobs, public keys and signatures, never raw "+
						"key material", name)
				}
			}
		}
		if s.Decl.Type.Params != nil {
			for _, p := range s.Decl.Type.Params.List {
				if !isByteSlice(p.Type) {
					continue
				}
				seen[name] = true
				if !allowedByteParams[name] {
					t.Errorf("PB-BIND-4: %s takes raw bytes across JNI but is not one of the "+
						"documented crossings %v. An undocumented byte channel is how a secret "+
						"crosses by accident", name, sortedKeys(allowedByteParams))
				}
			}
		}
	}
	for _, want := range custodyCrossings {
		if !seen[want] {
			t.Errorf("PB-KEY-1/PB-BIND-4: %s does not exist. The custody contract requires the "+
				"per-tier data key to cross Java -> Go explicitly; a facade with no named crossing "+
				"has either no key custody at all or an undocumented one", want)
		}
	}

	// STRUCT FIELDS ARE THE SAME CHANNEL WITH NO SIGNATURE TO INSPECT. gomobile binds an
	// exported struct field as a getter/setter pair, so a []byte field becomes a
	// `byte[] getX()` on the Java side -- an OUTBOUND crossing, the direction B8 forbids,
	// reached without declaring a single method. The walk above cannot see it: there is no
	// *ast.FuncDecl to look at. Until this existed, a []byte field landing was caught only by
	// the exported-surface golden needing regeneration, which is a review step someone can
	// approve, not a rule that refuses.
	//
	// The rule is absolute for fields, with no allow-list beside allowedByteParams: the two
	// permitted crossings are METHODS by construction (they take the transient per-tier data
	// key and zeroize it after use, which a field cannot do -- a field keeps the bytes alive
	// for as long as the object), and SendInput's keystrokes are a parameter for the same
	// reason. A field that genuinely needs to carry bytes has string and the facade's own
	// declared types available.
	for _, f := range src.Files {
		for _, d := range f.Decls {
			decl, ok := d.(*ast.GenDecl)
			if !ok || decl.Tok != token.TYPE {
				continue
			}
			for _, spec := range decl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, fld := range st.Fields.List {
					if !isByteSlice(fld.Type) {
						continue
					}
					for _, fn := range fld.Names {
						if !fn.IsExported() {
							continue
						}
						t.Errorf("PB-BIND-4: the exported field %s.%s is []byte. gomobile binds it as a "+
							"`byte[] get%s()`, which is raw bytes leaving the Go core -- the OUTBOUND "+
							"direction ADR-007 B8 forbids and B17 declined to widen. The custody "+
							"crossings are methods on purpose: they zeroize after use, and a field "+
							"cannot", ts.Name.Name, fn.Name, fn.Name)
					}
				}
			}
		}
	}

	// The crossing must be JUSTIFIED in the package doc, not merely present.
	for _, phrase := range []string{"PB-KEY-1", "Keystore", "zeroize"} {
		if !strings.Contains(src.Doc, phrase) {
			t.Errorf("PB-BIND-4/PB-KEY-1: the package doc does not mention %q. The one deliberate "+
				"crossing must be NAMED, DIRECTIONAL and JUSTIFIED in the facade package doc "+
				"(ADR-007 B8)", phrase)
		}
	}
}

// ---- PB-BIND-5: no Go panic crosses the boundary ------------------------------

// TestPBBIND5_EveryEntryPointHasAPanicBarrier. A panic through JNI kills the app
// process -- there is no Java frame to catch it. Every exported entry point must
// therefore open with a deferred recover, and must be able to REPORT: a method with no
// error result can recover but cannot tell the app anything, so the contract is that
// every exported function and method returns an error as its last result.
func TestPBBIND5_EveryEntryPointHasAPanicBarrier(t *testing.T) {
	src := loadFacade(t)

	recovering := recoveringHelpers(src)
	if len(recovering) == 0 {
		t.Errorf("PB-BIND-5: the facade declares no function whose body calls recover(); there is " +
			"no panic barrier at all")
	}

	eps := entryPoints(src)
	if len(eps) == 0 {
		t.Fatalf("PB-BIND-5: the facade exports no entry points; the guard would be vacuous")
	}

	for _, s := range eps {
		name := s.Name
		if s.Kind == "method" {
			name = s.Owner + "." + s.Name
		}
		if s.Decl == nil || s.Decl.Body == nil {
			continue
		}

		if !returnsErrorLast(s.Decl.Type) {
			t.Errorf("PB-BIND-5: %s%s does not return an error as its last result, so a recovered "+
				"panic has nowhere to go. Every entry point recovers INTO AN ERROR", name, s.Sig)
		}

		if len(s.Decl.Body.List) == 0 {
			t.Errorf("PB-BIND-5: %s has an empty body and therefore no panic barrier", name)
			continue
		}
		def, ok := s.Decl.Body.List[0].(*ast.DeferStmt)
		if !ok {
			t.Errorf("PB-BIND-5: the first statement of %s is not a deferred recover; a barrier "+
				"installed later does not cover the code before it", name)
			continue
		}
		if callee, _ := deferredCallee(def); !recovering[callee] {
			t.Errorf("PB-BIND-5: %s defers %q, which is not a facade function whose body calls "+
				"recover()", name, callee)
		}
	}
}

// ---- PB-BIND-6: the documented threading/lifecycle contract -------------------

func TestPBBIND6_ThreadingAndLifecycleContractIsDocumented(t *testing.T) {
	src := loadFacade(t)

	// 6.0 binds the numbers; the doc must state them, not merely gesture at "a bound".
	required := []struct{ phrase, why string }{
		{"any thread", "PB-BIND-6: the contract must say the surface is safe to call from any thread"},
		{"idempotent", "PB-BIND-6: Start and Stop must be documented idempotent"},
		{"goroutine", "PB-BIND-6: callbacks arrive on a Go goroutine"},
		{"marshal", "PB-BIND-6: the UI must marshal callbacks onto its own thread"},
		{"256", "PB-BIND-6/6.0: the callback queue bound is 256 items and must be STATED"},
		{"drop-oldest", "PB-BIND-6/6.0: the overflow behaviour is drop-oldest with a surfaced signal"},
		{"overflow", "PB-BIND-6: the overflow signal must be named so the app can observe it"},
	}
	low := strings.ToLower(src.Doc)
	for _, r := range required {
		if !strings.Contains(low, strings.ToLower(r.phrase)) {
			t.Errorf("%s -- the facade package doc does not contain %q", r.why, r.phrase)
		}
	}

	if v, ok := constIntValue(src, "CallbackQueueSize"); !ok {
		t.Errorf("PB-BIND-6: the facade exports no CallbackQueueSize constant, so the stated bound " +
			"is not machine-checkable")
	} else if v != 256 {
		t.Errorf("PB-BIND-6/6.0: CallbackQueueSize is %d, want 256 (changing a 6.0 budget requires "+
			"committee agreement, not implementer discretion)", v)
	}

	// The overflow must be OBSERVABLE, not just documented: a dropped-count on the event.
	have := map[string]bool{}
	for _, s := range exportedSurface(src) {
		if s.Kind == "field" {
			have[s.Owner+"."+s.Name] = true
		}
	}
	if !have["Event.Dropped"] {
		t.Errorf("PB-BIND-6: the facade exposes no Event.Dropped, so a queue overflow is invisible " +
			"to the app; the requirement asks for an OBSERVABLE overflow")
	}
}

// ---- PB-SAS-1: the SAS comes from the shared Go core --------------------------

// TestPBSAS1_SASIsADisplayStringFromTheGoCoreAndNeverATableInKotlin covers both halves
// of PB-SAS-1 in one test, deliberately: the scan half alone would pass vacuously today
// (there is no Kotlin in the tree), and a guard that cannot fail is not a guard. The
// negative control is generated at test time from the real wordlist, so no emoji is
// checked in.
func TestPBSAS1_SASIsADisplayStringFromTheGoCoreAndNeverATableInKotlin(t *testing.T) {
	src := loadFacade(t)

	found := false
	for _, s := range entryPoints(src) {
		if s.Kind == "method" && s.Owner == "Pairing" && s.Name == "SAS" {
			found = true
			if s.Sig != "() (string, error)" {
				t.Errorf("PB-SAS-1: Pairing.SAS has signature %s, want () (string, error). The SAS is "+
					"computed by the shared Go core and returned as ONE DISPLAY STRING; a [6]string "+
					"is bind-illegal and a per-index accessor invites a Kotlin-side table", s.Sig)
			}
		}
	}
	if !found {
		t.Errorf("PB-SAS-1: the facade exports no Pairing.SAS, so the phone has no way to obtain the " +
			"SAS from the Go core and the Kotlin side would have to compute it")
	}

	words := sasWordlist(t)
	if len(words) != 64 {
		t.Fatalf("expected a 64-entry SAS wordlist in internal/remote/crypto/sas.go, got %d", len(words))
	}

	// Negative control first: prove the scanner trips on a synthetic Kotlin table.
	ctrl := filepath.Join(t.TempDir(), "SasTable.kt")
	var b strings.Builder
	b.WriteString("package swarm.ui\n\nval SAS_WORDS = listOf(\n")
	for _, w := range words[:16] {
		b.WriteString("    \"" + w + "\",\n")
	}
	b.WriteString(")\n")
	if err := os.WriteFile(ctrl, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write negative control: %v", err)
	}
	if hits := scanForSASTable(t, filepath.Dir(ctrl), words); len(hits) == 0 {
		t.Fatalf("PB-SAS-1 negative control FAILED: the scanner did not detect a synthetic Kotlin " +
			"emoji table, so a real one would pass unnoticed")
	}

	if hits := scanForSASTable(t, repoRoot(t), words); len(hits) > 0 {
		t.Errorf("PB-SAS-1: the SAS emoji table is re-implemented outside the Go core:\n\t%s\n"+
			"That is the cross-language failure mode PB-SAS-1 exists to REMOVE, not test around",
			strings.Join(hits, "\n\t"))
	}
}

// ---- S7 residuals that land on this surface -----------------------------------

// TestS7Residual_FacadeNeverIssuesANonDurableSeq. phonecore.Sequencer.Next() is still
// exported and "returns the next seq WITHOUT a durable reservation"; the S7 evidence
// records that "the facade slice binds this surface". A facade that calls Next()
// reintroduces seq reuse across an Android process death -- the 4.3 brick, restored.
func TestS7Residual_FacadeNeverIssuesANonDurableSeq(t *testing.T) {
	src := loadFacade(t)
	body := facadeSourceText(t, src)

	if strings.Contains(body, ".Next()") {
		t.Errorf("S7 residual / PB-STATE-3: the facade calls Sequencer.Next(), which issues a seq " +
			"with NO durable reservation. After one Android process death the phone restarts its " +
			"send-seq under the same epoch and the gateway stale-drops every keystroke, take_control, " +
			"launch and kill -- permanently. Use NextCommand/NextInput, which can report a failed " +
			"reservation and honour PB-STATE-8's gap rule")
	}
	if !strings.Contains(body, "NextCommand(") {
		t.Errorf("S7 residual / PB-STATE-8: the facade never calls Sequencer.NextCommand. The burned " +
			"reservation block must be absorbed by a COMMAND frame; an input frame carrying the Gap " +
			"bit is dropped silently by the gateway, so the first post-restart keystroke vanishes")
	}
	if !strings.Contains(body, "NextInput(") {
		t.Errorf("S7 residual / PB-STATE-8: the facade never calls Sequencer.NextInput, which is the " +
			"allocator that REFUSES to emit a keystroke while a gap is outstanding")
	}
}

// TestS7Residual_FacadeClaimsOutcomesByOperationID. phonecore.ReplyCache.Take() is
// unkeyed and the cache is rebuilt from an unpruned OpOutcomes map, so it can hand the
// app a STALE outcome for an operation it never asked about. TakeFor is the safe one.
func TestS7Residual_FacadeClaimsOutcomesByOperationID(t *testing.T) {
	src := loadFacade(t)
	body := facadeSourceText(t, src)

	if strings.Contains(body, ".Take()") {
		t.Errorf("S7 residual / PB-SYNC-2: the facade calls the UNKEYED ReplyCache.Take(). It is " +
			"rebuilt from an unpruned OpOutcomes map, so it can return a stale outcome attributed to " +
			"the wrong operation. Use TakeFor(operationID)")
	}
	if !strings.Contains(body, "TakeFor(") {
		t.Errorf("S7 residual / PB-SYNC-2: the facade never calls ReplyCache.TakeFor, so App.Outcome " +
			"cannot be attributing replies to operation ids")
	}
}

// TestS8Trap_FacadeDoesNotReimplementLaunchContentHash. Recorded in
// remote-phaseB-s1-evidence.md (review R2) and carried forward in the progress note:
// LaunchContentHash stayed in internal/protocol, which is NOT in the bound closure.
// Option (c) -- reimplementing its canonical length-prefixed encoding in the facade -- is
// FORBIDDEN, because a one-byte divergence produces silent signature verification
// failures with no compile error. The runtime KAT in ./conformance is the test that links
// the two implementations; this is the structural half.
func TestS8Trap_FacadeDoesNotReimplementLaunchContentHash(t *testing.T) {
	src := loadFacade(t)
	body := facadeSourceText(t, src)

	if !strings.Contains(body, "LaunchContentHash") {
		t.Errorf("S8 trap (S1 review R2): the facade never references LaunchContentHash, yet " +
			"PB-BIND-3 requires launch. Either the facade is not binding the launch spec into the " +
			"signature at all, or it has open-coded the canonical encoding under another name -- " +
			"the second is explicitly NOT PERMITTED")
	}
	for _, decl := range []string{"func LaunchContentHash", "func launchContentHash"} {
		if strings.Contains(body, decl) {
			t.Errorf("S8 trap (S1 review R2): the facade DECLARES %s. Reimplementing the canonical "+
				"length-prefixed encoding is forbidden: a one-byte divergence yields silent signature "+
				"verification failures with no compile error. Move it to internal/protocol/schema or "+
				"re-export it -- do not rewrite it", decl)
		}
	}
}

// ---- helpers ------------------------------------------------------------------

func typeIsFacadeLocal(expr ast.Expr, declared map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return facadeAllowedBasics[e.Name] || declared[e.Name]
	case *ast.StarExpr:
		return typeIsFacadeLocal(e.X, declared)
	case *ast.ArrayType:
		return isByteSlice(e)
	default:
		return false
	}
}

func isByteSlice(expr ast.Expr) bool {
	at, ok := expr.(*ast.ArrayType)
	if !ok || at.Len != nil {
		return false
	}
	id, ok := at.Elt.(*ast.Ident)
	return ok && id.Name == "byte"
}

func returnsErrorLast(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return false
	}
	last := ft.Results.List[len(ft.Results.List)-1]
	id, ok := last.Type.(*ast.Ident)
	return ok && id.Name == "error"
}

// recoveringHelpers names every facade function whose body calls recover().
func recoveringHelpers(src *facadeSource) map[string]bool {
	out := map[string]bool{}
	for _, f := range src.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "recover" {
					out[fd.Name.Name] = true
				}
				return true
			})
		}
	}
	return out
}

// deferredCallee returns the name of the function a defer statement invokes. Both
// `defer barrier(&err)` and `defer barrier(&err)()` shapes resolve to "barrier".
func deferredCallee(def *ast.DeferStmt) (string, bool) {
	fun := def.Call.Fun
	if inner, ok := fun.(*ast.CallExpr); ok {
		fun = inner.Fun
	}
	if id, ok := fun.(*ast.Ident); ok {
		return id.Name, true
	}
	if sel, ok := fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name, true
	}
	return types.ExprString(def.Call.Fun), false
}

// sasWordlist extracts the 64-entry table from the FROZEN crypto package, so the Kotlin
// scan hunts for the real words rather than a copy that could drift.
func sasWordlist(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "internal", "remote", "crypto", "sas.go")
	fset := token.NewFileSet()
	f, err := parseGoFile(fset, path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var words []string
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if name.Name != "sasWords" || i >= len(vs.Values) {
				continue
			}
			cl, ok := vs.Values[i].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, el := range cl.Elts {
				lit, ok := el.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err == nil {
					words = append(words, s)
				}
			}
		}
		return true
	})
	return words
}

// scanForSASTable reports every .kt/.java file under root that contains at least eight
// distinct SAS words -- a threshold no incidental emoji use reaches and no partial copy
// of a 64-entry table falls under.
func scanForSASTable(t *testing.T, root string, words []string) []string {
	t.Helper()
	var hits []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "build", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".kt" && ext != ".kts" && ext != ".java" {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		body := string(raw)
		n := 0
		for _, w := range words {
			if strings.Contains(body, w) {
				n++
			}
		}
		if n >= 8 {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, rel+" ("+strconv.Itoa(n)+" SAS words)")
		}
		return nil
	})
	sort.Strings(hits)
	return hits
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
