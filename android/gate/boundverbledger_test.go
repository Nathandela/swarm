package gate

// FAILING-FIRST (TDD RED, GG-5): the BIDIRECTIONAL control for this phase's standing defect
// class -- a bound facade verb that exists, is unit-tested, is traced in a coverage artifact,
// and that no production Kotlin ever calls.
//
// WHY EVERY EARLIER GATE WAS ONE-SIDED, and why that let six instances ship. A gomobile facade
// makes "exists" and "is used" independent BY CONSTRUCTION: the Go side compiles and tests
// green with no caller at all, because its callers are in a language the Go toolchain never
// sees. So `go test ./...` can be green over a verb nothing on Earth calls, `./gradlew test`
// can be green because Kotlin cannot miss what it never mentions, and the screen-coverage
// artifact (mobile/screen_coverage.tsv) traces verbs to SCREEN ELEMENTS, which is a statement
// about the design rather than about the shipped app. Six times now the answer has been "the
// requirement is implemented, and the app cannot reach it".
//
// The instances, so the size of the class is on the record rather than in a changelog:
//
//	1. an FCM sender fully tested with zero production callers;
//	2. an epoch-key grant opened only by the test simulator;
//	3. `App.Start` -- nothing connected the phone to the relay at all;
//	4. `LifecycleConvergence` -- the plan object, with no production caller;
//	5. `SetEventListener` / `SubscribeJournal` / `TerminalWatch` -- the whole observation
//	   plane, so PB-APP-3/4/5 were non-functional in the shipping app;
//	6. `CustodyPlanner.forDevice` -- PB-KEY-8's capability gate, never invoked.
//
// WHAT THIS FILE ASSERTS, in two dimensions.
//
//	App.<Verb>            every exported method on the bound `App` is EITHER called from
//	                      production Kotlin, OR named in android/unbound-verbs.tsv with a
//	                      stated reason.
//	FacadeBridge.<method> every public method on the ONE adapter between the screens and the
//	                      facade is EITHER reachable from production Kotlin outside it, OR
//	                      ledgered the same way.
//
// THE SECOND DIMENSION EXISTS BECAUSE THE FIRST HAS A HOLE, and the hole was occupied. A third
// of the App verbs reach Kotlin only through `FacadeBridge`, so a bridge method that nothing
// calls makes its verbs read as CALLED while the app can no more reach them than before. That
// was true of four bridge methods when this file was written -- `journal`, `clockBanner`,
// `sessionRow` and `streamView` -- carrying `App.ReadJournal`, `ClockVerdict`, `Session` and
// `ResyncPending` with them. Checking only the first dimension would have reported those four
// verbs as wired.
//
// The bridge dimension uses the S17 reachability walk (s17_pushclient_test.go) rather than a
// second weaker one, so a bridge method reached only from another bridge method -- `roster`,
// via `triageInbox` -- counts as live, which it is.
//
// WHAT IT CANNOT SEE, stated here rather than left for a reader to assume away, because a
// limit written down is worth more than a check that overclaims:
//
//   - It matches a CALL BY NAME, not by receiver type. `QrScanner.stop()` and `App.stop()` are
//     the same six characters to this file, so a verb whose name collides with an unrelated
//     Kotlin method reads as called. Narrowing it would need a type checker, and the direction
//     of the error is recorded rather than hidden: this over-counts CALLED, which is the
//     permissive direction.
//   - For App verbs NOT routed through the bridge it cannot see whether the call is
//     REACHABLE: a call inside a function nothing invokes satisfies it.
//     android/gate/wiring_test.go carries the reachability half for the verbs where the
//     lifecycle is the property, using the same walk.
//   - It cannot see through an INTERFACE with no production implementation. `CoreKeyCustody`
//     is declared over `App.InstallWakeKey`/`InstallContentKey`/`PurgeKeys` and its only
//     implementation is a test fixture (`RecordingCore`), so those three read as called here
//     and are not. The ledger's own rows say so; a check that silently claimed otherwise would
//     be worse than one that says what it cannot do.
//   - The S17 walk cannot cross a PROPERTY INITIALISER, which it documents. A bridge method
//     called only from `val x = bridge.journal(...)` inside another class would still be seen
//     (that is the external pass, a plain call match), but one called only from a property
//     initialiser INSIDE FacadeBridge would not.
//
// It reads checked-in source only: no Android SDK, no JDK, no emulator, no handset. Nothing
// here claims anything about PB-E2E-5's deferred set.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// boundVerbFloor is the "cannot pass by measuring nothing" floor on the SCAN, not on the
// codebase. The facade carries 45 exported App methods today; a run that found materially
// fewer has stopped parsing the facade rather than discovered a smaller one, and would report
// a clean ledger over an empty question. Defect class (i) applied to this file itself.
const boundVerbFloor = 40

// calledVerbFloor is the same idea from the other side. If the Kotlin matcher broke, every
// verb would read as uncalled and the failure would be obvious -- but if the KOTLIN READER
// broke (an empty source string), every verb would read as uncalled TOO, and the tempting
// repair is to widen the ledger. This floor makes the second case fail as itself.
const calledVerbFloor = 20

// minLedgerReason is the shortest thing that can count as a stated reason. A row reading
// "unused" is a blanket exemption wearing a reason's clothes.
const minLedgerReason = 40

// bridgeMethodFloor is the same floor for the adapter dimension.
const bridgeMethodFloor = 8

// ---------------------------------------------------------------------------
// The bound surface, read from the facade's SOURCE.
// ---------------------------------------------------------------------------

func facadeDir(t *testing.T) string { return filepath.Join(repoRoot(t), "mobile") }

// boundAppVerbs is every exported method on `*App` in the facade's non-test Go files.
//
// gobind binds the exported methods of exported types, so this set IS the Java-visible verb
// list on `swarmmobile.App`. It is derived from source rather than from a checked-in list on
// purpose: a list would have to be edited by the same person adding the verb, which is exactly
// the step this control exists to stop depending on.
func boundAppVerbs(t *testing.T) []string {
	t.Helper()
	dir := facadeDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("PB-BIND-3: cannot read the bound facade at %s: %v", mustRel(t, dir), err)
	}
	fset := token.NewFileSet()
	var verbs []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("PB-BIND-3: parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || !fn.Name.IsExported() {
				continue
			}
			if receiverTypeName(fn.Recv.List[0].Type) == "App" {
				verbs = append(verbs, fn.Name.Name)
			}
		}
	}
	sort.Strings(verbs)
	return verbs
}

// receiverTypeName is the bare type name of a method receiver, dereferencing the pointer.
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// ---------------------------------------------------------------------------
// The OTHER bound types, which the ledger did not cover and which carried four instances.
// ---------------------------------------------------------------------------

// boundTypeFloor is the "cannot pass by measuring nothing" floor on the golden scan. The
// facade binds five method-carrying types today (App, JournalPage, Pairing, SessionList,
// UndeliveredList); a run that found fewer has stopped reading the golden.
const boundTypeFloor = 4

// nonAppMethodFloor is the same idea for the methods themselves.
const nonAppMethodFloor = 12

var goldenMethod = regexp.MustCompile(`(?m)^method\s+([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\(`)

// boundTypeMethods is every exported method on every bound type, keyed by receiver, read from
// the PINNED EXPORTED SURFACE (mobile/testdata/exported_surface.golden).
//
// WHY THE GOLDEN AND NOT A SECOND AST WALK. The golden is regenerated from the facade's own
// types by mobile/golden_test.go, which fails the build if it drifts from the source -- so it
// is source-derived and fenced, and it is the artifact PB-BIND-7 already treats as the
// authority on what crosses. Re-deriving the same set here would be a second parser to keep in
// step with the first.
//
// WHAT IS DELIBERATELY OUT OF SCOPE, stated rather than left to be assumed away:
//
//   - `field` rows. gobind binds a struct field as a getter/setter pair, so every field is
//     nominally a bound symbol; requiring a caller for each would demand rows for getters that
//     exist only so a struct can cross at all. This file is the bound-VERB ledger and the
//     four instances it was extended for are all methods.
//   - `ifacemethod` rows (EventListener.OnEvent, KeyCustody.*). Those are IMPLEMENTED by
//     Kotlin and called by Go, so "does production Kotlin call it" is the wrong question --
//     and android/unbound-verbs.tsv's header already records what the check cannot see about
//     the custody interface.
//   - package-level `func` rows (NewApp, DecodeQR). They have no receiver, so a call site
//     carries no dot and the matcher's one deliberate strengthening does not apply.
func boundTypeMethods(t *testing.T) map[string][]string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "mobile", "testdata", "exported_surface.golden")
	src := readFileOrFail(t, path, "PB-BIND-3")
	out := map[string][]string{}
	for _, m := range goldenMethod.FindAllStringSubmatch(src, -1) {
		out[m[1]] = append(out[m[1]], m[2])
	}
	for _, methods := range out {
		sort.Strings(methods)
	}
	return out
}

// nonAppBoundTypes are the bound receivers other than App, in a stable order.
func nonAppBoundTypes(methods map[string][]string) []string {
	var out []string
	for name := range methods {
		if name != "App" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// The adapter, which is the only production path to a third of those verbs.
// ---------------------------------------------------------------------------

const facadeBridgeFile = "dev/swarm/phone/ui/FacadeBridge.kt"

func facadeBridgePath(t *testing.T) string {
	return filepath.Join(kotlinMainRoot(t), filepath.FromSlash(facadeBridgeFile))
}

// publicFun matches a Kotlin function declared without a visibility modifier -- which in
// Kotlin means public. `private fun` and `internal fun` do not match, because the question is
// about the surface OTHER files can reach.
var publicFun = regexp.MustCompile(`(?m)^\s{4}fun\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func facadeBridgeMethods(t *testing.T) []string {
	t.Helper()
	src := stripKotlinComments(readFileOrFail(t, facadeBridgePath(t), "PB-BIND-3"))
	var out []string
	for _, m := range publicFun.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// kotlinSourceExcept is production Kotlin with one file left out, so "called from somewhere
// else" is a question that can be asked at all.
func kotlinSourceExcept(t *testing.T, skip string) string {
	t.Helper()
	var b strings.Builder
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		if f == skip {
			continue
		}
		b.WriteString(readFileOrFail(t, f, "PB-BIND-3"))
		b.WriteString("\n")
	}
	return b.String()
}

// namesKotlinCall matches a call to name with no receiver required, for reading a body where
// `roster()` and `this.roster()` are the same call.
func namesKotlinCall(src, name string) bool {
	return regexp.MustCompile(`(?:^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `\s*\(`).
		MatchString(src)
}

// liveBridgeMethods is the set a screen can actually reach.
//
// SEED: called from production Kotlin OUTSIDE the bridge, which is the only way in.
// CLOSURE: anything the S17 walk reaches from a seeded method's body, so `roster` -- reached
// only through `triageInbox` -- is live rather than reported as dead. Reusing that walk is
// deliberate: it already handles expression bodies, which is most of this file, and it was
// repaired twice this phase.
func liveBridgeMethods(declared []string, bridgeSrc, elsewhere string) map[string]bool {
	live := map[string]bool{}
	bodies := s17BodiesIn(bridgeSrc)
	for _, m := range declared {
		if !boundVerbCall(m).MatchString(elsewhere) {
			continue
		}
		live[m] = true
		reachable, ok := s17ReachableIn(bodies, m, 3)
		if !ok {
			continue
		}
		for _, other := range declared {
			if namesKotlinCall(reachable, other) {
				live[other] = true
			}
		}
	}
	return live
}

// ---------------------------------------------------------------------------
// The Kotlin side of the question.
// ---------------------------------------------------------------------------

// gobindJavaName is the Java spelling gobind emits for an exported Go method name. It is a
// transcription of golang.org/x/mobile/bind.lowerFirst (bind/gen.go), which is the function
// that actually names the binding, and it is NOT "lower-case the first letter".
//
// THE RULE IS: lower-case the WHOLE LEADING UPPER-CASE RUN, except that if the run is followed
// by a lower-case letter, its LAST character stays upper-case (that character begins the next
// word).
//
//	Roster        -> roster        (run of one)
//	TerminalWatch -> terminalWatch (run of one)
//	SAS           -> sas           (the whole name is the run)
//	SASCode       -> sasCode       (run of four, C begins the next word)
//
// WHY THIS MATTERS AND HOW IT WAS FOUND. This file previously modelled the spelling as
// `ToLower(name[:1]) + name[1:]`, which answers `sAS` for `SAS` -- a string no call site can
// contain. `swarmmobile.Pairing` exports `SAS()` and PairingSurface calls `live.sas()`, so the
// moment the ledger was extended past the `App` receiver the old model reported a verb the app
// calls on every SAS screen as UNCALLED, and the repair a reader reaches for is a ledger row
// excusing live code. No `App` verb has a leading acronym today, which is why the defect never
// fired -- but this file's stated defence is that its matcher is correct in BOTH spellings, and
// with the old model it was not.
func gobindJavaName(goName string) string {
	var conv []rune
	for i, r := range goName {
		if !unicode.IsUpper(r) {
			if len(conv) > 1 {
				conv[len(conv)-1] = unicode.ToUpper(conv[len(conv)-1])
			}
			return string(conv) + goName[i:]
		}
		conv = append(conv, unicode.ToLower(r))
	}
	return string(conv)
}

// boundVerbCall matches a CALL to a bound verb in EITHER SPELLING.
//
// gobind LOWERCASES the leading upper-case run when it emits the Java binding -- `swarmmobile.App`
// declares terminalWatch, subscribeJournal, setEventListener; `swarmmobile.Pairing` declares
// sas -- so no correct Kotlin call site contains the Go-cased name. A single-spelling matcher
// has produced a false green here twice: once matching only the Go casing (five S17 assertions
// became unsatisfiable by any correct implementation, s17_pushclient_test.go:99-105), and the
// inverse would miss a Java-cased call through a generated stub. Both are accepted, which is
// s17NamesVerb's rule. The Java spelling is [gobindJavaName]'s, not a first-letter fold.
//
// IT REQUIRES THE DOT, which s17NamesVerb does not, and that is the one place this is
// deliberately STRONGER rather than a second weaker walk: without it `fun terminalWatch(` --
// the DECLARATION of a same-named Kotlin method, which is precisely how a facade verb gets
// shadowed by a local wrapper nobody calls -- satisfies the check that the verb is called.
// Every App verb reaches Kotlin through an instance, so a call site always carries the dot.
func boundVerbCall(goName string) *regexp.Regexp {
	lower := gobindJavaName(goName)
	return regexp.MustCompile(`\.\s*(?:` +
		regexp.QuoteMeta(goName) + `|` + regexp.QuoteMeta(lower) + `)\s*\(`)
}

func callsBoundVerb(src, goName string) bool { return boundVerbCall(goName).MatchString(src) }

// ---------------------------------------------------------------------------
// The ledger.
// ---------------------------------------------------------------------------

// ledgerRow is one line of android/unbound-verbs.tsv:
//
//	symbol <TAB> reason
//
// `symbol` is namespaced -- `App.Presence`, `FacadeBridge.clockBanner` -- so one file covers
// both dimensions and the rot check can tell which declaration to go and look for.
type ledgerRow struct {
	Symbol string
	Reason string
	Line   int
}

func unboundLedgerPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "unbound-verbs.tsv")
}

func readUnboundLedger(t *testing.T) []ledgerRow {
	t.Helper()
	path := unboundLedgerPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s does not exist.\n"+
			"It is the half of this control that makes a NEW bound verb uncallable-by-default "+
			"LOUDLY rather than silently. Without it the only way a verb with no production "+
			"caller can be reported is by someone going and looking, which is how six of them "+
			"shipped. Columns: symbol <TAB> reason: %v", mustRel(t, path), err)
	}
	var rows []ledgerRow
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			t.Errorf("%s:%d has %d column(s), want 2 (symbol, reason): %q",
				mustRel(t, path), i+1, len(parts), line)
			continue
		}
		rows = append(rows, ledgerRow{
			Symbol: strings.TrimSpace(parts[0]),
			Reason: strings.TrimSpace(strings.Join(parts[1:], " ")),
			Line:   i + 1,
		})
	}
	return rows
}

func ledgerIndex(rows []ledgerRow) map[string]ledgerRow {
	out := make(map[string]ledgerRow, len(rows))
	for _, r := range rows {
		out[r.Symbol] = r
	}
	return out
}

// qualify namespaces a bare name for the ledger.
func qualify(owner string, names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, owner+"."+n)
	}
	return out
}

// ---------------------------------------------------------------------------
// The decision, as two pure functions, so the assertions below can be driven against
// SYNTHETIC input. Against the production tree a check that understands nothing and a codebase
// that does nothing wrong are indistinguishable -- which is exactly how this class survived
// five times.
// ---------------------------------------------------------------------------

// unledgeredVerbs are the verbs that production Kotlin does not call and the ledger does not
// excuse. It is the set that must be empty. owner namespaces the ledger lookup, so a row about
// one receiver cannot silence a same-named method on another.
func unledgeredVerbs(owner string, verbs []string, kotlin string, ledger map[string]ledgerRow) []string {
	var out []string
	for _, v := range verbs {
		if callsBoundVerb(kotlin, v) {
			continue
		}
		if _, excused := ledger[owner+"."+v]; excused {
			continue
		}
		out = append(out, v)
	}
	return out
}

// staleLedgerRows are ledger entries naming a symbol the sources no longer declare.
//
// WITHOUT THIS THE LEDGER ROTS INTO A BLANKET EXEMPTION. A verb renamed in Go leaves its old
// row behind; the row excuses nothing, nobody notices, and the file slowly becomes a list of
// names with no referents that a reader mistakes for considered decisions.
func staleLedgerRows(declared []string, rows []ledgerRow) []ledgerRow {
	exists := make(map[string]bool, len(declared))
	for _, s := range declared {
		exists[s] = true
	}
	var out []ledgerRow
	for _, r := range rows {
		if !exists[r.Symbol] {
			out = append(out, r)
		}
	}
	return out
}

// calledVerbs counts how many of verbs the Kotlin actually calls, for the floor.
func calledVerbs(verbs []string, kotlin string) int {
	n := 0
	for _, v := range verbs {
		if callsBoundVerb(kotlin, v) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// The control itself.
// ---------------------------------------------------------------------------

// TestBoundVerbs_EveryBoundVerbIsCalledFromProductionKotlinOrLedgered.
//
// AT THE COMMIT THAT INTRODUCED THIS FILE it failed on three load-bearing verbs --
// setEventListener, subscribeJournal, terminalWatch -- each of which appeared ZERO times in
// all Kotlin, main, test and androidTest alike. That was PB-APP-3/4/5 non-functional in the
// shipping app: no listener, no journal delivery, and a terminal peek reading a local cache
// nothing populated, failing in the shape of a quiet machine.
func TestBoundVerbs_EveryBoundVerbIsCalledFromProductionKotlinOrLedgered(t *testing.T) {
	verbs := boundAppVerbs(t)
	kotlin := stripKotlinComments(appKotlinSource(t))
	ledger := ledgerIndex(readUnboundLedger(t))

	for _, verb := range unledgeredVerbs("App", verbs, kotlin, ledger) {
		t.Errorf("swarmmobile.App.%s has NO production-Kotlin caller and no row in %s.\n"+
			"A bound verb with no caller is this phase's standing defect class: it compiles, its "+
			"Go tests pass, and the app a user installs cannot reach it -- the Go toolchain never "+
			"sees the language its callers are written in. Either call it from "+
			"android/app/src/main, or add an `App.%s` row saying why it is deliberately unbound.\n"+
			"The check looks for `.%s(` or `.%s(` in production Kotlin with comments stripped.",
			verb, mustRel(t, unboundLedgerPath(t)), verb,
			verb, strings.ToLower(verb[:1])+verb[1:])
	}
}

// TestBoundVerbs_EveryMethodOnEveryOtherBoundTypeIsCalledOrLedgered is the SAME question over
// the receivers the ledger never asked it about.
//
// THE LEDGER WAS HARDCODED TO `App`, and four bound methods lived in the blind spot it left:
//
//	SessionList.Stale       PB-APP-8 at PB-APP-2's screen. TriageInboxTest carries the comment
//	                        "this is the assertion that makes swarmmobile.SessionList.Stale
//	                        reach a user" -- and it did not: the assertion is driven by
//	                        TriageInbox.from's `journalStale` PARAMETER, which FacadeBridge
//	                        filled from App.StreamState. The handle's own verdict, which the Go
//	                        doc says rides on the handle precisely so a caller cannot forget to
//	                        ask, was never read.
//	JournalPage.Stale       the same fact for PB-APP-3's chronology, also never read.
//	JournalPage.NextCursor  so journal paging could not advance: FacadeBridge.journal took an
//	                        `afterCursor` and returned rows with no way to compute the next one.
//	UndeliveredList.Dropped PB-INPUT-1's overflow count.
//
// Every one of them is the standing defect class on a receiver the control did not look at.
func TestBoundVerbs_EveryMethodOnEveryOtherBoundTypeIsCalledOrLedgered(t *testing.T) {
	byType := boundTypeMethods(t)
	if len(byType) < boundTypeFloor {
		t.Fatalf("the golden scan found %d bound types with methods, want at least %d.\n"+
			"The facade has not shrunk; this file has stopped reading "+
			"mobile/testdata/exported_surface.golden, and a ledger checked against a surface of "+
			"nothing passes vacuously.\nfound: %v", len(byType), boundTypeFloor, byType)
	}
	kotlin := stripKotlinComments(appKotlinSource(t))
	ledger := ledgerIndex(readUnboundLedger(t))

	total := 0
	for _, owner := range nonAppBoundTypes(byType) {
		methods := byType[owner]
		total += len(methods)
		for _, m := range unledgeredVerbs(owner, methods, kotlin, ledger) {
			t.Errorf("swarmmobile.%s.%s has NO production-Kotlin caller and no row in %s.\n"+
				"A bound method with no caller is this phase's standing defect class, and the "+
				"handle types are where it hid longest: the ledger only ever asked about the "+
				"`App` receiver, so a fact the facade goes to the trouble of carrying -- a stale "+
				"roster, a page cursor, a dropped-input count -- could be dropped on the floor by "+
				"the adapter with every check green.\n"+
				"Either read it from production Kotlin, or add a `%s.%s` row saying why it is "+
				"deliberately unbound.\n"+
				"The check looks for `.%s(` or `.%s(` in production Kotlin with comments stripped.",
				owner, m, mustRel(t, unboundLedgerPath(t)), owner, m, m, gobindJavaName(m))
		}
	}
	if total < nonAppMethodFloor {
		t.Fatalf("the golden scan found %d methods on non-App bound types, want at least %d",
			total, nonAppMethodFloor)
	}
}

// TestBoundVerbs_EveryBridgeMethodIsReachableOrLedgered is the same question one layer up, and
// it is not redundant: a third of the App verbs reach Kotlin ONLY through this adapter, so a
// bridge method nothing calls makes its verbs read as wired while the app cannot reach them.
//
// AT THE COMMIT THAT INTRODUCED THIS FILE four bridge methods had no caller outside
// FacadeBridge.kt -- journal, clockBanner, sessionRow, streamView -- and between them they
// carried App.ReadJournal, App.ClockVerdict, App.Session and App.ResyncPending. The first
// dimension alone reported all four verbs as called.
func TestBoundVerbs_EveryBridgeMethodIsReachableOrLedgered(t *testing.T) {
	declared := facadeBridgeMethods(t)
	if len(declared) < bridgeMethodFloor {
		t.Fatalf("the scan found %d public methods on FacadeBridge, want at least %d. It has "+
			"stopped reading %s, and a reachability question over nothing passes vacuously",
			len(declared), bridgeMethodFloor, facadeBridgeFile)
	}
	bridge := stripKotlinComments(readFileOrFail(t, facadeBridgePath(t), "PB-BIND-3"))
	elsewhere := stripKotlinComments(kotlinSourceExcept(t, facadeBridgePath(t)))
	live := liveBridgeMethods(declared, bridge, elsewhere)
	ledger := ledgerIndex(readUnboundLedger(t))

	for _, m := range declared {
		if live[m] {
			continue
		}
		if _, excused := ledger["FacadeBridge."+m]; excused {
			continue
		}
		t.Errorf("FacadeBridge.%s is reachable from no production Kotlin outside %s, and has no "+
			"`FacadeBridge.%s` row in %s.\n"+
			"This adapter is the ONE place the screen models meet the bound facade, so a method "+
			"nothing reaches is a screen model with no screen AND a set of facade verbs the app "+
			"cannot get to -- while a check on the verbs alone reports them as called.",
			m, facadeBridgeFile, m, mustRel(t, unboundLedgerPath(t)))
	}
}

// TestBoundVerbs_TheLedgerCannotOutliveTheSymbolsItExcuses is the rot check. A ledger nobody
// can invalidate is a file that only ever grows.
func TestBoundVerbs_TheLedgerCannotOutliveTheSymbolsItExcuses(t *testing.T) {
	byType := boundTypeMethods(t)
	declared := append(qualify("App", boundAppVerbs(t)),
		qualify("FacadeBridge", facadeBridgeMethods(t))...)
	for _, owner := range nonAppBoundTypes(byType) {
		declared = append(declared, qualify(owner, byType[owner])...)
	}
	rows := readUnboundLedger(t)

	for _, r := range staleLedgerRows(declared, rows) {
		t.Errorf("%s:%d excuses %s, which the sources no longer declare.\n"+
			"Either it was renamed and the row must follow it, or it is gone and the row is a "+
			"name with no referent -- which is how a considered exemption list decays into a "+
			"blanket one.\nThe symbols that exist: %s",
			mustRel(t, unboundLedgerPath(t)), r.Line, r.Symbol, strings.Join(declared, ", "))
	}
	for _, r := range rows {
		if len(r.Reason) < minLedgerReason {
			t.Errorf("%s:%d excuses %s with %q (%d chars, want >= %d).\n"+
				"A row must say what makes the symbol deliberately unbound -- which screen it is "+
				"waiting on, or which decision retired it. A one-word reason is an exemption "+
				"with no argument in it.",
				mustRel(t, unboundLedgerPath(t)), r.Line, r.Symbol, r.Reason,
				len(r.Reason), minLedgerReason)
		}
	}
	seen := map[string]int{}
	for _, r := range rows {
		if first, dup := seen[r.Symbol]; dup {
			t.Errorf("%s:%d repeats %s, first excused at line %d",
				mustRel(t, unboundLedgerPath(t)), r.Line, r.Symbol, first)
			continue
		}
		seen[r.Symbol] = r.Line
	}
}

// wiredLedgerRows are ledger entries excusing a symbol the app NOW REACHES.
//
// THE ROT CHECK ABOVE ONLY ASKS WHETHER THE SYMBOL STILL EXISTS, and that is the smaller half of
// the question. A row survives a rename badly; it survives being WIRED silently. Every assertion
// in this file treats the ledger as an escape hatch -- `unledgeredVerbs` skips a symbol the
// moment a row exists for it -- so once a verb acquires a caller, its row goes on excusing
// something that no longer needs excusing, and nothing anywhere fails.
//
// That is not a tidiness point. Two things depend on the file shrinking. PB-DS-9's exit criterion
// is literally "android/unbound-verbs.tsv shrinks by the verbs the screens now reach", enforced
// until now by somebody remembering. And a row's REASON is prose that stops being true when the
// wiring lands: `App.Resync`'s said "the stale/repairing screen ... does not exist" for as long
// as that was so, and a stale reason in an exemption file is worse than a missing one, because
// the next reader takes it for a considered decision rather than a leftover.
func wiredLedgerRows(rows []ledgerRow, reached map[string]bool) []ledgerRow {
	var out []ledgerRow
	for _, r := range rows {
		if reached[r.Symbol] {
			out = append(out, r)
		}
	}
	return out
}

// reachedSymbols is every ledgerable symbol production Kotlin can get to, namespaced the way the
// ledger names them.
//
// EACH DIMENSION IS ASKED THE SAME WAY ITS OWN FORWARD ASSERTION ASKS IT, deliberately: a second
// weaker notion of "reached" here could report a verb wired that
// TestBoundVerbs_EveryBoundVerbIsCalledFromProductionKotlinOrLedgered still reports unwired, and
// the two would demand opposite edits to the same row.
func reachedSymbols(t *testing.T) map[string]bool {
	t.Helper()
	kotlin := stripKotlinComments(appKotlinSource(t))
	out := map[string]bool{}

	for _, v := range boundAppVerbs(t) {
		if callsBoundVerb(kotlin, v) {
			out["App."+v] = true
		}
	}
	byType := boundTypeMethods(t)
	for _, owner := range nonAppBoundTypes(byType) {
		for _, m := range byType[owner] {
			if callsBoundVerb(kotlin, m) {
				out[owner+"."+m] = true
			}
		}
	}
	declared := facadeBridgeMethods(t)
	bridge := stripKotlinComments(readFileOrFail(t, facadeBridgePath(t), "PB-BIND-3"))
	elsewhere := stripKotlinComments(kotlinSourceExcept(t, facadeBridgePath(t)))
	for m, live := range liveBridgeMethods(declared, bridge, elsewhere) {
		if live {
			out["FacadeBridge."+m] = true
		}
	}
	return out
}

// TestBoundVerbs_TheLedgerCannotExcuseASymbolTheAppNowReaches is the rot check's other half.
func TestBoundVerbs_TheLedgerCannotExcuseASymbolTheAppNowReaches(t *testing.T) {
	for _, r := range wiredLedgerRows(readUnboundLedger(t), reachedSymbols(t)) {
		t.Errorf("%s:%d excuses %s as deliberately unbound, and production Kotlin now calls it.\n"+
			"Delete the row. While it stands, every check in this file treats the symbol as "+
			"exempt -- so it is no longer covered by the control that would notice if the caller "+
			"went away again -- and its stated reason is prose about a screen or a decision that "+
			"has since changed, which the next reader will take for a live one.\nThe reason on "+
			"the row today: %q",
			mustRel(t, unboundLedgerPath(t)), r.Line, r.Symbol, r.Reason)
	}
}

// TestBoundVerbs_TheWiredRowCheckSeesARowThatShouldHaveGone is that check's negative control,
// driven synthetically for this file's stated reason: against the production tree a check that
// understands nothing and a ledger with nothing wrong in it are indistinguishable.
func TestBoundVerbs_TheWiredRowCheckSeesARowThatShouldHaveGone(t *testing.T) {
	rows := []ledgerRow{
		{Symbol: "App.Presence", Reason: "a blocking relay round trip", Line: 7},
		{Symbol: "FacadeBridge.sessionRow", Reason: "no screen opens on one session", Line: 9},
	}

	if got := wiredLedgerRows(rows, map[string]bool{}); len(got) != 0 {
		t.Errorf("the check reported %v over a ledger whose symbols nothing reaches; every row "+
			"in a correct ledger would have to be deleted", got)
	}

	got := wiredLedgerRows(rows, map[string]bool{"FacadeBridge.sessionRow": true})
	if len(got) != 1 || got[0].Symbol != "FacadeBridge.sessionRow" {
		t.Fatalf("a row excusing a symbol the app now reaches was not reported: %v", got)
	}

	// AND THE NAMESPACE IS LOAD-BEARING. `sessionRow` is a FacadeBridge method and `SessionRow` is
	// a model; a check comparing bare names would let a row about one be cleared by the other.
	if got := wiredLedgerRows(rows, map[string]bool{"App.sessionRow": true}); len(got) != 0 {
		t.Errorf("a row was reported wired by a same-named symbol on a different receiver: %v", got)
	}
}

// TestBoundVerbs_TheScanHasEnoughToMeasure is defect class (i) turned on this file.
//
// Every assertion above is of the form "nothing was found wrong". A parser that stopped
// reading the facade, or a Kotlin reader that returned an empty string, produces exactly that
// answer -- and the second one produces it while making the FIRST test fail, whose tempting
// repair is to widen the ledger until it is green. Both floors below fail as themselves first.
func TestBoundVerbs_TheScanHasEnoughToMeasure(t *testing.T) {
	verbs := boundAppVerbs(t)
	if len(verbs) < boundVerbFloor {
		t.Fatalf("the scan found %d exported methods on swarmmobile.App, want at least %d.\n"+
			"The facade has not shrunk by half; this file has stopped reading it, and a ledger "+
			"checked against a surface of nothing passes vacuously.\nfound: %s",
			len(verbs), boundVerbFloor, strings.Join(verbs, ", "))
	}
	kotlin := stripKotlinComments(appKotlinSource(t))
	if n := calledVerbs(verbs, kotlin); n < calledVerbFloor {
		t.Fatalf("only %d of %d bound verbs have a production-Kotlin call site, want at least "+
			"%d.\nThe app does reach the facade; a number this low means the Kotlin reader "+
			"returned nothing, and the repair is to fix the reader rather than to grow %s.",
			n, len(verbs), calledVerbFloor, mustRel(t, unboundLedgerPath(t)))
	}
}

// TestBoundVerbs_AnAddedVerbIsUncallableByDefaultAndSaysSo drives the decision against
// SYNTHETIC input, because that is the only way to measure that it can FAIL.
//
// The production tree is (by the time this lands) clean, so the first test passing proves
// nothing about whether the check works. This is the mutation: a verb added to the facade and
// never called, which is the exact shape of all six instances.
func TestBoundVerbs_AnAddedVerbIsUncallableByDefaultAndSaysSo(t *testing.T) {
	const kotlin = `
class PhoneSurface(private val app: App) {
    fun render() {
        app.roster()
        app.subscribeJournal()
    }
}
`
	verbs := []string{"Roster", "SubscribeJournal", "AttachDebugger"}

	t.Run("an added verb with no caller and no row fails", func(t *testing.T) {
		got := unledgeredVerbs("App", verbs, kotlin, map[string]ledgerRow{})
		if len(got) != 1 || got[0] != "AttachDebugger" {
			t.Fatalf("the check reported %v, want exactly [AttachDebugger].\n"+
				"A verb added to the facade and called by nothing must be reported. If this "+
				"passes, the control is measuring nothing and the six instances it exists to "+
				"catch would ship again.", got)
		}
	})

	t.Run("a ledger row excuses it", func(t *testing.T) {
		ledger := map[string]ledgerRow{
			"App.AttachDebugger": {Symbol: "App.AttachDebugger", Reason: "x"},
		}
		if got := unledgeredVerbs("App", verbs, kotlin, ledger); len(got) != 0 {
			t.Fatalf("the check reported %v over a ledgered verb; the ledger is the escape "+
				"hatch and it must work, or the only way to add a verb is to wire it the same "+
				"day", got)
		}
	})

	t.Run("a row in the wrong namespace does not excuse it", func(t *testing.T) {
		ledger := map[string]ledgerRow{
			"FacadeBridge.AttachDebugger": {Symbol: "FacadeBridge.AttachDebugger", Reason: "x"},
		}
		if got := unledgeredVerbs("App", verbs, kotlin, ledger); len(got) != 1 {
			t.Fatalf("the check reported %v; a row about a bridge method must not excuse a "+
				"facade verb that happens to share its name, or one row silences two symbols",
				got)
		}
	})

	// A row about the SAME method name on ANOTHER bound type must not excuse it either. The
	// handle types collide with App by construction -- Count, At and Stale all appear on more
	// than one -- so an unnamespaced lookup would let one row silence three receivers.
	t.Run("a row on another bound type does not excuse it", func(t *testing.T) {
		ledger := map[string]ledgerRow{
			"SessionList.Stale": {Symbol: "SessionList.Stale", Reason: "x"},
		}
		got := unledgeredVerbs("JournalPage", []string{"Stale"}, "", ledger)
		if len(got) != 1 {
			t.Fatalf("the check reported %v; a row about SessionList.Stale must not excuse "+
				"JournalPage.Stale, which is a different fact about a different stream", got)
		}
	})

	t.Run("a ledger row for a symbol that no longer exists fails", func(t *testing.T) {
		rows := []ledgerRow{{Symbol: "App.AttachDebugger", Reason: "x", Line: 7}}
		if got := staleLedgerRows([]string{"App.Roster", "App.SubscribeJournal"}, rows); len(got) != 1 {
			t.Fatalf("the rot check reported %v over a row naming a verb the facade does not "+
				"export, want exactly one. A ledger that cannot go stale is one that only grows",
				got)
		}
	})
}

// TestBoundVerbs_TheBridgeWalkFollowsAMethodReachedOnlyThroughAnother is the bridge dimension's
// own self-fence, and it is the case that decides whether that dimension is a fence or a
// nuisance: `roster` is called from NOWHERE outside FacadeBridge, and it is live, because
// `triageInbox` -- which is called from a screen -- calls it. A seed-only check would report it
// as dead, and the repair a reader would reach for is a ledger row excusing a method the app
// uses on every draw.
func TestBoundVerbs_TheBridgeWalkFollowsAMethodReachedOnlyThroughAnother(t *testing.T) {
	const bridge = `
class FacadeBridge(private val app: App) {

    fun roster(): List<SessionRow> = app.roster()

    fun triageInbox(): TriageInbox =
        TriageInbox.from(roster(), journalStale = app.streamState(JOURNAL) == STALE)

    fun clockBanner(): ClockBanner = ClockBanner.of(app.clockVerdict())
}
`
	const screen = `
class PhoneSurface {
    fun render() {
        bridge.triageInbox()
    }
}
`
	live := liveBridgeMethods([]string{"roster", "triageInbox", "clockBanner"}, bridge, screen)

	if !live["triageInbox"] {
		t.Errorf("the walk does not see the method a screen calls directly")
	}
	if !live["roster"] {
		t.Errorf("the walk does not follow triageInbox -> roster, so a method the app reaches " +
			"on every draw reads as dead. A check that reports live code as dead gets a ledger " +
			"row written to silence it, and the row is then indistinguishable from a real one")
	}
	if live["clockBanner"] {
		t.Errorf("the walk reports clockBanner as live when nothing calls it. PB-TIME-1's " +
			"banner is the exact instance this dimension exists to catch, and a check that " +
			"cannot fail on it is reported as coverage")
	}
}

// TestBoundVerbs_TheMatcherReadsBothSpellingsAndNothingElse fences the reader against itself.
//
// Each case below is a shape that HAS produced a false answer in this package: the Go casing
// (which no correct call site contains), a declaration of a same-named Kotlin method, and a
// doc comment naming the verb it is supposed to require a call to.
func TestBoundVerbs_TheMatcherReadsBothSpellingsAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"the gobind spelling, which is what a call site contains", "app.terminalWatch(id)", true},
		{"the Go spelling, accepted so a generated stub is not a false red", "App.TerminalWatch(id)", true},
		{"a safe call", "app?.terminalWatch(id)", true},
		{"the dot on the previous line", "app\n    .terminalWatch(id)", true},
		{"a DECLARATION of a same-named wrapper is not a call", "fun terminalWatch(id: String) {}", false},
		{"a mention with no call", "terminalWatch is what asks the machine to send frames", false},
		{"a longer name that merely contains it", "app.terminalWatchAll(id)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callsBoundVerb(tc.src, "TerminalWatch"); got != tc.want {
				t.Errorf("callsBoundVerb(%q, TerminalWatch) = %v, want %v.\n"+
					"A matcher that answers wrong here decides whether the whole control is a "+
					"fence or a formality", tc.src, got, tc.want)
			}
		})
	}

	// The comment case is the reader one layer out: this file's own prose names every verb it
	// guards, so a check run over unstripped source would pass on its own doc comments.
	commented := stripKotlinComments("// app.terminalWatch(id) is what PB-APP-4 needs\nfun x() {}")
	if callsBoundVerb(commented, "TerminalWatch") {
		t.Errorf("a commented-out call satisfies the check.\n"+
			"stripKotlinComments left: %q", commented)
	}
}

// TestBoundVerbs_TheJavaSpellingLowersTheWholeLeadingAcronym is the mutation for the matcher's
// OTHER half, and it is the one the old model failed.
//
// This file modelled gobind's Java spelling as `ToLower(name[:1]) + name[1:]` -- a first-letter
// fold. gobind (golang.org/x/mobile/bind.lowerFirst) lowers the WHOLE leading upper-case run,
// keeping its last character upper-cased only when a lower-case letter follows. The two models
// disagree on exactly one shape: a name that begins with an acronym.
//
// It was not a latent hazard. `swarmmobile.Pairing` exports `SAS()`, which binds as `sas()`,
// and PairingSurface calls `live.sas()` on the comparison screen -- so the first extension of
// this ledger past the `App` receiver would have reported a verb the app calls on every pairing
// as UNCALLED, and the repair a reader reaches for is a ledger row excusing live code.
//
// The synthetic verb below is the general case, so the fence does not depend on the facade
// keeping a leading-acronym method.
func TestBoundVerbs_TheJavaSpellingLowersTheWholeLeadingAcronym(t *testing.T) {
	for _, tc := range []struct{ goName, java string }{
		{"Roster", "roster"},                     // a run of one: unchanged by either model
		{"TerminalWatch", "terminalWatch"},       // ditto, and the shape the file already knew
		{"SAS", "sas"},                           // the live instance: Pairing.SAS
		{"URLPolicy", "urlPolicy"},               // the synthetic acronym-leading verb
		{"HTTPRelayDial", "httpRelayDial"},       // a longer run, still one word boundary
		{"ID", "id"},                             // the whole name is the run
		{"SetEventListener", "setEventListener"}, // regression: no acronym at all
	} {
		t.Run(tc.goName, func(t *testing.T) {
			if got := gobindJavaName(tc.goName); got != tc.java {
				t.Errorf("gobindJavaName(%q) = %q, want %q (golang.org/x/mobile/bind.lowerFirst)",
					tc.goName, got, tc.java)
			}
			// The point of the model is the matcher, so assert the matcher too: the call site a
			// correct implementation contains is the JAVA one, and it must be recognised.
			if !callsBoundVerb("app."+tc.java+"(x)", tc.goName) {
				t.Errorf("callsBoundVerb(%q, %q) = false.\n"+
					"That is a verb the app calls reported as uncalled, whose tempting repair is "+
					"a ledger row excusing live code", "app."+tc.java+"(x)", tc.goName)
			}
			// And the first-letter fold this file used to carry must NOT be what is matched,
			// or the mutation above passes for the wrong reason.
			if fold := strings.ToLower(tc.goName[:1]) + tc.goName[1:]; fold != tc.java {
				if callsBoundVerb("app."+fold+"(x)", tc.goName) {
					t.Errorf("callsBoundVerb accepts %q, which gobind never emits. A matcher that "+
						"accepts a spelling no binding contains reports a verb as called on the "+
						"strength of a call site that cannot compile", "app."+fold+"(")
				}
			}
		})
	}
}

// TestBoundVerbs_TheLedgerIsReportedInFull prints the current state, so the evidence for this
// control is the ledger itself rather than a claim about it. It asserts nothing beyond the
// floors above; its output is the artifact.
func TestBoundVerbs_TheLedgerIsReportedInFull(t *testing.T) {
	verbs := boundAppVerbs(t)
	kotlin := stripKotlinComments(appKotlinSource(t))
	ledger := ledgerIndex(readUnboundLedger(t))

	var called, excused []string
	for _, v := range verbs {
		switch {
		case callsBoundVerb(kotlin, v):
			called = append(called, v)
		case ledger["App."+v].Symbol != "":
			excused = append(excused, fmt.Sprintf("App.%s -- %s", v, ledger["App."+v].Reason))
		}
	}
	bridge := stripKotlinComments(readFileOrFail(t, facadeBridgePath(t), "PB-BIND-3"))
	elsewhere := stripKotlinComments(kotlinSourceExcept(t, facadeBridgePath(t)))
	methods := facadeBridgeMethods(t)
	live := liveBridgeMethods(methods, bridge, elsewhere)
	for _, m := range methods {
		if !live[m] && ledger["FacadeBridge."+m].Symbol != "" {
			excused = append(excused,
				fmt.Sprintf("FacadeBridge.%s -- %s", m, ledger["FacadeBridge."+m].Reason))
		}
	}
	t.Logf("bound App verbs: %d, called from production Kotlin: %d\n"+
		"FacadeBridge methods: %d, reachable: %d\ndeliberately unbound (%d):\n%s",
		len(verbs), len(called), len(methods), len(live), len(excused),
		strings.Join(excused, "\n"))
}
