package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) for PB-APP-9: the EXHAUSTIVE error-taxonomy fence.
//
// v1's acceptance criterion for this requirement was "test/lint; reviewed", which is
// unenforceable -- nothing fails when a new error class ships unmapped. The requirement now
// reads "Every facade error class maps to a rendered state; a NEW error class without a
// mapping fails the test", and that word is what this file has to make mechanical.
//
// THE DESIGN, and why a new class cannot slip past it. Exhaustiveness is claimed in TWO
// directions, because each alone is escapable:
//
//	SYNTAX (here, TestPBAPP9_NoFacadeErrorIsConstructedWithoutNamingItsClass): every
//	error-CONSTRUCTION site in the facade's non-test source must lexically name an
//	ErrClass* constant. This covers every path, including ones no test reaches, which is
//	the half a runtime sweep can never have.
//
//	RUNTIME (mobile/conformance/s16_errorstates_test.go): every entry point ENUMERATED FROM
//	THE GOLDEN is invoked reflectively across an adversarial state matrix, and every non-nil
//	error must classify to a class in the table -- never to unknown. This covers errors the
//	facade does not construct at all, which is the half the syntax fence can never have:
//	crypto.ErrKeyInvalidated, relay.ErrRevoked and phonecore.ErrGrantLost are produced three
//	packages away and merely travel through.
//
// The CLOSED SET is the golden (mobile/testdata/exported_surface.golden), not a list in a
// test. A class exists only as an exported const, so it appears in the golden; the golden
// moves only as a REVIEWED change (PB-BIND-7); and the checks below are set EQUALITY in both
// directions against mobile/error_taxonomy.tsv. So:
//
//   - a class added without a table row       -> fails here (golden \ table non-empty)
//   - a table row for no real class           -> fails here (table \ golden non-empty)
//   - a class whose token drifts from its row  -> fails here (the const's value is read from
//     source, not trusted from the table)
//   - a class no screen renders               -> fails in android/gate (table -> Kotlin enum)
//   - an error constructed naming no class     -> fails here (syntax)
//   - an error identity that merely passes through -> fails in conformance (runtime sweep)
//
// WHY THE TABLE IS NOT MERELY "THE LIST SOMEBODY MAINTAINS". Nothing here trusts it: it is
// only ever the JOIN between two independently-derived sets (the golden's classes and the
// Kotlin enum's states), and every one of its rows is separately proved reachable by a real
// scenario in conformance. A row nobody can produce fails as loudly as a class nobody mapped.

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// errClassPrefix is the naming convention the closed set is derived from. It is a
// CONVENTION and it is load-bearing: the golden renders a const as its name alone, so the
// prefix is the only thing that distinguishes a taxonomy class from CallbackQueueSize.
const errClassPrefix = "ErrClass"

// taxonomyPath is the checked-in mapping, beside screen_coverage.tsv and for the same
// reason: a traceability table is reviewable and a hard-coded slice in a test is not.
const taxonomyFile = "error_taxonomy.tsv"

// The two remedies of PB-APP-10 and PB-KEY-3, as the vocabulary the table must use.
//
// THEY WERE THREE UNTIL ADR-007 B133 AND THE THIRD IS NOT COMING BACK. `authenticate` named
// an act the product performs nowhere -- every phone-side user authentication is removed --
// so a row carrying it would be advice nobody can follow, which is the defect this file
// exists to catch rather than an exception to it. crypto.ErrKeyAuthRequired outlived its
// remedy (internal/remote/crypto is FROZEN) and is classified with crypto.ErrKeyInvalidated:
// a key demanding an authentication that cannot happen is unusable, and pairing again
// re-provisions it without one.
//
// THE TWO THAT REMAIN MAY NEVER COLLAPSE, and that is the whole requirement. One is an act
// the user CAN carry out and the other is one they cannot: telling a grant-loss user to
// re-pair is a brick, because BeginPairing fail-fasts while a device is registered
// (PB-STATE-10), so the advice cannot be acted on and the only exit is physical access to
// the machine.
const (
	remedyRePair         = "re_pair"         // the crypto sentinels -- permanent; pair again
	remedyMachineReGrant = "machine_regrant" // phonecore.ErrGrantLost -- the MACHINE must act
	sentinelAuthRequired = "crypto.ErrKeyAuthRequired"
	sentinelKeyInvalid   = "crypto.ErrKeyInvalidated"
	sentinelGrantLost    = "phonecore.ErrGrantLost"
	sentinelRelayRevoked = "relay.ErrRevoked"
)

// taxonomyRow is one row of error_taxonomy.tsv.
type taxonomyRow struct {
	Class string // the exported const name, e.g. ErrClassGrantLost
	Token string // its VALUE: what App.ErrorClass returns and Kotlin branches on
	// Sentinels are the Go error identities the class classifies, empty for a facade-local
	// class ("-" in the table). A class may carry MORE than one: B133 left both crypto
	// sentinels on one class, and a column that could hold only one would have had to drop an
	// identity PB-APP-10 requires to be traced.
	Sentinels []string
	State     string // the rendered UI state (a Kotlin enum constant)
	Remedy    string // what the user (or the machine) must do
	Req       string
	Line      int
}

func TestPBAPP9_EveryErrorClassOnTheBoundSurfaceHasARenderedState(t *testing.T) {
	src := loadFacade(t)

	golden := goldenErrClasses(t, src.Dir)
	if len(golden) == 0 {
		t.Fatalf("PB-APP-9: the pinned surface declares no %s* constant, so there is no error "+
			"taxonomy at all.\nThe facade must export one constant per error class the UI has to "+
			"route differently -- at minimum the two remedies of PB-APP-10/PB-KEY-3 (%s for %s "+
			"and %s, %s for %s), which are two because they send the user somewhere different "+
			"and one of them is a dead end if misrouted.\nDeclaring them as EXPORTED consts is "+
			"what makes the set closed: the golden is regenerated only as a reviewed change, so "+
			"a class cannot be added without this test seeing it.",
			errClassPrefix, remedyRePair, sentinelKeyInvalid, sentinelAuthRequired,
			remedyMachineReGrant, sentinelGrantLost)
	}

	rows := loadTaxonomy(t, src.Dir)
	byClass := map[string]taxonomyRow{}
	for _, r := range rows {
		if prev, dup := byClass[r.Class]; dup {
			t.Errorf("%s:%d: class %q is mapped twice (first at line %d). One class, one rendered "+
				"state: two rows let the same error render two ways depending on which the UI reads",
				taxonomyFile, r.Line, r.Class, prev.Line)
		}
		byClass[r.Class] = r
	}

	// GOLDEN \ TABLE: a class that ships with no rendered state. This is the direction the
	// requirement names in as many words.
	for _, class := range golden {
		row, ok := byClass[class]
		if !ok {
			t.Errorf("PB-APP-9: the facade exports error class %s and %s maps it to NO rendered "+
				"state. Every class must name what the user is shown and what they can do about it; "+
				"a class with no mapping reaches the screen as an opaque exception message, which is "+
				"the failure this requirement exists to stop.", class, taxonomyFile)
			continue
		}
		if row.State == "" || row.Remedy == "" {
			t.Errorf("%s:%d: class %s has an empty rendered state (%q) or remedy (%q); a row that "+
				"maps nothing is worse than a missing row, because it reads as coverage",
				taxonomyFile, row.Line, class, row.State, row.Remedy)
		}
	}

	// TABLE \ GOLDEN: a row for a class the facade does not have. Left unchecked, the table
	// decays into a record of what someone intended rather than what ships.
	inGolden := map[string]bool{}
	for _, c := range golden {
		inGolden[c] = true
	}
	for _, r := range rows {
		if !inGolden[r.Class] {
			t.Errorf("%s:%d: maps class %q, which is not an exported constant on the pinned "+
				"surface. Either the class was removed and its row outlived it, or the name is "+
				"misspelled -- and a misspelled row silently leaves the REAL class unmapped",
				taxonomyFile, r.Line, r.Class)
		}
	}

	// The TOKEN is read from SOURCE, never trusted from the table. It is the value that
	// crosses JNI (gomobile flattens a Go error to its message, so the class has to ride the
	// message), so a table whose token drifts from the constant would route correctly in Go
	// and land on the wrong screen in Kotlin -- the one failure mode nothing else here sees.
	for _, class := range golden {
		row, ok := byClass[class]
		if !ok {
			continue
		}
		val, found := constStringValue(src, class)
		if !found {
			t.Errorf("PB-APP-9: %s is exported but is not a string constant this test can read. "+
				"The class TOKEN must be a plain string literal: it is what App.ErrorClass returns "+
				"and what the Android side branches on", class)
			continue
		}
		if val != row.Token {
			t.Errorf("%s:%d: class %s has token %q in the table and %q in the source. The token is "+
				"what crosses JNI, so a drifted row routes correctly in Go and renders the wrong "+
				"screen on the handset", taxonomyFile, row.Line, class, row.Token, val)
		}
	}
}

// TestPBAPP9_TheTwoRemediesAreNeverCollapsed is the requirement's real subject.
//
// Other slices spent considerable effort making these failures DISTINGUISHABLE rather than
// collapsing them -- phonecore.ErrGrantLost exists as a separate identity for exactly this
// reason, and its own doc says so. This is where that pays off or is wasted.
//
// IT WAS THREE REMEDIES AND IS NOW TWO (ADR-007 B133), and the narrowing is the one thing this
// file may not treat as a collapse: `authenticate` did not merge into another remedy, its
// SUBJECT was removed, and a remedy naming an act the product cannot perform is exactly the
// unfollowable advice the fence exists to catch. What survives untouched is the property: the
// remedy the user CAN carry out and the one they cannot must never share a row, a remedy or a
// screen.
func TestPBAPP9_TheTwoRemediesAreNeverCollapsed(t *testing.T) {
	src := loadFacade(t)
	rows := loadTaxonomy(t, src.Dir)

	bySentinel := map[string]taxonomyRow{}
	for _, r := range rows {
		for _, s := range r.Sentinels {
			if prev, dup := bySentinel[s]; dup {
				t.Errorf("%s:%d: %s is classified twice (also line %d). One identity, one class: "+
					"two rows means the rendered state depends on lookup order",
					taxonomyFile, r.Line, s, prev.Line)
			}
			bySentinel[s] = r
		}
	}

	// The user-performable side and the machine side, named by the identities that reach them.
	// Both crypto sentinels are on the re_pair side after B133 -- see the const block.
	want := map[string]string{
		sentinelAuthRequired: remedyRePair,
		sentinelKeyInvalid:   remedyRePair,
		sentinelGrantLost:    remedyMachineReGrant,
	}
	for _, sentinel := range []string{sentinelAuthRequired, sentinelKeyInvalid, sentinelGrantLost} {
		row, ok := bySentinel[sentinel]
		if !ok {
			t.Errorf("PB-APP-9/PB-APP-10: %s has no row in %s. Every identity the facade routes "+
				"must say which of the two remedies it reaches, and the collapse that matters is "+
				"between them: %s and %s say pair again, %s says the MACHINE must re-grant, and "+
				"BeginPairing fail-fasts while a device is registered -- so routing a grant-loss "+
				"user to re-pair is a brick whose only exit is physical access to the machine "+
				"(PB-STATE-10)",
				sentinel, taxonomyFile, sentinelKeyInvalid, sentinelAuthRequired, sentinelGrantLost)
			continue
		}
		if row.Remedy != want[sentinel] {
			t.Errorf("%s:%d: %s carries remedy %q, want %q", taxonomyFile, row.Line, sentinel,
				row.Remedy, want[sentinel])
		}
	}

	// THE TWO CRYPTO SENTINELS SHARE ONE ROW, asserted rather than left to the remedy check: a
	// later edit that gave crypto.ErrKeyAuthRequired its own class again would have to come
	// through here, where B133's reasoning is, instead of quietly re-introducing a screen that
	// asks for an authentication this product cannot perform.
	a, haveAuth := bySentinel[sentinelAuthRequired]
	b, haveInvalid := bySentinel[sentinelKeyInvalid]
	if haveAuth && haveInvalid && a.Line != b.Line {
		t.Errorf("%s:%d and :%d: %s and %s are classified separately (%s and %s). ADR-007 B133 "+
			"removed every phone-side user authentication, so a key gated on one is as unusable "+
			"as a destroyed key and has the same fix; a class of its own would need a remedy, and "+
			"the only one it could name is an act nobody can perform",
			taxonomyFile, a.Line, b.Line, sentinelAuthRequired, sentinelKeyInvalid, a.Class, b.Class)
	}

	// Distinctness ACROSS the two remedies, asserted rather than implied by the literals above:
	// the defect this fence exists for is the user-performable remedy and the machine-only one
	// sharing a destination, and a future edit that renamed both consistently would satisfy the
	// check above and collapse them anyway.
	lost, haveLost := bySentinel[sentinelGrantLost]
	for _, sentinel := range []string{sentinelAuthRequired, sentinelKeyInvalid} {
		row, ok := bySentinel[sentinel]
		if !ok || !haveLost {
			continue
		}
		if row.Remedy == lost.Remedy {
			t.Errorf("PB-APP-10: %s and %s both send the user to remedy %q. One of them is an act "+
				"the user can carry out and the other is one only the MACHINE can; collapsed, the "+
				"grant-loss user is given advice they cannot act on",
				sentinel, sentinelGrantLost, row.Remedy)
		}
		if row.State == lost.State {
			t.Errorf("PB-APP-10: %s and %s render the SAME state %q, so the screen cannot tell the "+
				"user which of the two happened even if the taxonomy can", sentinel, sentinelGrantLost,
				row.State)
		}
	}

	// A revoked device is the fourth identity PB-APP-10 names, and it is not a custody
	// refusal: relay.ErrRevoked comes back from the relay handshake, so today it does not
	// match either crypto sentinel and falls through the transport loop's switch into an
	// unbounded "reconnecting" -- the failure LOOP the requirement forbids in as many words.
	if _, ok := bySentinel[sentinelRelayRevoked]; !ok {
		t.Errorf("PB-APP-10: %s has no row in %s. A revoked device must show an explicit re-pair "+
			"prompt, and %s is the only signal it ever gets: mobile/relay.go's dial switch handles "+
			"the two crypto sentinels and `continue`s on everything else, so a revoked phone "+
			"reconnects every %s forever behind a spinner", sentinelRelayRevoked, taxonomyFile,
			sentinelRelayRevoked, "250ms")
	}
}

// TestPBAPP9_NoFacadeErrorIsConstructedWithoutNamingItsClass is the SYNTAX half of
// exhaustiveness: it covers paths no test reaches.
//
// The rule: an error-construction site (errors.New / fmt.Errorf) must lexically name an
// ErrClass* constant in the statement that constructs it. Naming it AT the site rather than
// inside a helper is the point -- a helper that decided the class from the message would be
// prose matching, and a helper that defaulted would put every new error in one bucket, which
// is precisely "a new class shipped unmapped" wearing a different hat.
//
// The taxonomy's own declaring file is exempt: it is where the constructors live.
func TestPBAPP9_NoFacadeErrorIsConstructedWithoutNamingItsClass(t *testing.T) {
	src := loadFacade(t)

	// The exempt file is identified by CONTENT, not by name: whichever file declares the
	// ErrClass* constants is the taxonomy's home, so this does not pin a filename an
	// implementer is free to choose.
	exempt := map[string]bool{}
	for i, f := range src.Files {
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
				for _, n := range vs.Names {
					if strings.HasPrefix(n.Name, errClassPrefix) {
						exempt[src.GoFiles[i]] = true
					}
				}
			}
		}
	}

	var unclassified []string
	for i, f := range src.Files {
		base := src.GoFiles[i]
		if exempt[base] {
			continue
		}
		// Every construction site in the file...
		sites := map[token.Pos]string{}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if name := errorConstructorName(call); name != "" {
					sites[call.Pos()] = name
				}
			}
			return true
		})
		// ...must sit inside a small statement that also names a class.
		ast.Inspect(f, func(n ast.Node) bool {
			switch n.(type) {
			case *ast.ReturnStmt, *ast.AssignStmt, *ast.ValueSpec:
			default:
				return true
			}
			if !subtreeNamesAnErrClass(n) {
				return true
			}
			ast.Inspect(n, func(inner ast.Node) bool {
				if call, ok := inner.(*ast.CallExpr); ok {
					delete(sites, call.Pos())
				}
				return true
			})
			return true
		})
		for pos, name := range sites {
			unclassified = append(unclassified, base+":"+
				strconv.Itoa(src.Fset.Position(pos).Line)+" "+name)
		}
	}
	sort.Strings(unclassified)

	if len(unclassified) > 0 {
		t.Errorf("PB-APP-9: %d error(s) are constructed in the facade without naming an %s* "+
			"constant:\n\t%s\n\nEvery error the facade authors must declare, AT THE SITE, which "+
			"rendered state it reaches the user as. The alternative shapes were considered and are "+
			"both worse: a classifier that matched on the MESSAGE is prose matching (gomobile "+
			"flattens an error to its message and every reword becomes a silent misroute), and one "+
			"that DEFAULTED puts every future error in one bucket, which is exactly the unmapped "+
			"class this requirement forbids -- wearing a default's clothes.",
			len(unclassified), errClassPrefix, strings.Join(unclassified, "\n\t"))
	}
}

// TestPBAPP9_TheClassifierIsReachableFromKotlin pins the one verb the Android side needs.
//
// The class has to ride the MESSAGE and be read back out of it, because that is the only
// thing that survives the boundary: gomobile turns a Go error into a Java exception carrying
// its message and nothing else. keycustody.go already established the shape for the two
// custody verdicts (a stable token, stamped centrally in barrier); this generalises it, and
// the verb is what stops the Android side keeping a second copy of the token strings -- a
// copy that drifted would degrade a permanent invalidation into a prompt the user can never
// satisfy, which is the failure PB-KEY-6 already recorded once.
func TestPBAPP9_TheClassifierIsReachableFromKotlin(t *testing.T) {
	src := loadFacade(t)

	const want = "method App.ErrorClass(string) (string, error)"
	for _, s := range exportedSurface(src) {
		if s.Line() == want {
			return
		}
	}
	t.Errorf("PB-APP-9: the facade exports no error classifier. Required:\n\t%s\n"+
		"It takes the MESSAGE because that is all gomobile leaves of a Go error at the JNI "+
		"boundary, and it returns the token of the %s* constant the error was stamped with -- "+
		"never the empty string and never an unknown class for an error this facade produced.\n"+
		"It returns no []byte, so ADR-007 B8 is untouched: the key crossing stays single and "+
		"inbound and this widens the surface by one string verb.", want, errClassPrefix)
}

// ---- helpers -------------------------------------------------------------------

// goldenErrClasses reads the CLOSED SET from the pinned surface. Reading the golden rather
// than the parsed source is deliberate: the golden is the artifact a reviewer signs off, so
// a class that reached the source without reaching the golden is caught by PB-BIND-7 and a
// class in both is caught here.
func goldenErrClasses(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "testdata", "exported_surface.golden"))
	if err != nil {
		t.Fatalf("PB-APP-9 reads the closed class set from the pinned surface: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		name, ok := strings.CutPrefix(strings.TrimSpace(line), "const ")
		if ok && strings.HasPrefix(name, errClassPrefix) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func loadTaxonomy(t *testing.T, dir string) []taxonomyRow {
	t.Helper()
	path := filepath.Join(dir, taxonomyFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-APP-9 requires a checked-in error taxonomy at %s: %v\n"+
			"Columns (tab separated): class_const<TAB>token<TAB>sentinel<TAB>rendered_state"+
			"<TAB>remedy<TAB>requirement<TAB>note.\n"+
			"`sentinel` is the Go error identity the class classifies (%s, %s, %s, %s, ...), "+
			"comma-separated where one class classifies several, or \"-\" for a class the facade "+
			"authors itself. `rendered_state` must be a constant of the Kotlin error-state enum "+
			"-- android/gate checks that direction.",
			path, err, sentinelAuthRequired, sentinelKeyInvalid, sentinelGrantLost, sentinelRelayRevoked)
	}
	var rows []taxonomyRow
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		row := taxonomyRow{Line: i + 1, Class: strings.TrimSpace(cols[0])}
		get := func(n int) string {
			if len(cols) > n {
				return strings.TrimSpace(cols[n])
			}
			return ""
		}
		row.Token = get(1)
		// One class may classify several identities, comma-separated. "-" is the facade's own
		// classes, which have no Go sentinel at all.
		for _, s := range strings.Split(get(2), ",") {
			if s = strings.TrimSpace(s); s != "" && s != "-" {
				row.Sentinels = append(row.Sentinels, s)
			}
		}
		row.State, row.Remedy, row.Req = get(3), get(4), get(5)
		if row.Req == "" {
			t.Errorf("%s:%d: class %q names no requirement", taxonomyFile, row.Line, row.Class)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no rows; the fence would be vacuous", path)
	}
	return rows
}

// errorConstructorName reports which error constructor a call is, or "".
func errorConstructorName(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	switch {
	case pkg.Name == "errors" && sel.Sel.Name == "New":
		return "errors.New"
	case pkg.Name == "fmt" && sel.Sel.Name == "Errorf":
		return "fmt.Errorf"
	}
	return ""
}

func subtreeNamesAnErrClass(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(inner ast.Node) bool {
		if id, ok := inner.(*ast.Ident); ok && strings.HasPrefix(id.Name, errClassPrefix) {
			found = true
		}
		return !found
	})
	return found
}

// constStringValue returns the value of an exported string constant declared in the facade.
func constStringValue(src *facadeSource, name string) (string, bool) {
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
					if !ok || lit.Kind != token.STRING {
						return "", false
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						return "", false
					}
					return v, true
				}
			}
		}
	}
	return "", false
}
