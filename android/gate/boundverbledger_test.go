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

// boundVerbCall matches a CALL to a bound verb in EITHER SPELLING.
//
// gobind LOWERCASES the first letter when it emits the Java binding -- `swarmmobile.App`
// declares terminalWatch, subscribeJournal, setEventListener -- so no correct Kotlin call site
// contains the Go-cased name. A single-spelling matcher has produced a false green here twice:
// once matching only the Go casing (five S17 assertions became unsatisfiable by any correct
// implementation, s17_pushclient_test.go:99-105), and the inverse would miss a Java-cased call
// through a generated stub. Both are accepted, which is s17NamesVerb's rule.
//
// IT REQUIRES THE DOT, which s17NamesVerb does not, and that is the one place this is
// deliberately STRONGER rather than a second weaker walk: without it `fun terminalWatch(` --
// the DECLARATION of a same-named Kotlin method, which is precisely how a facade verb gets
// shadowed by a local wrapper nobody calls -- satisfies the check that the verb is called.
// Every App verb reaches Kotlin through an instance, so a call site always carries the dot.
func boundVerbCall(goName string) *regexp.Regexp {
	lower := strings.ToLower(goName[:1]) + goName[1:]
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
// excuse. It is the set that must be empty.
func unledgeredVerbs(verbs []string, kotlin string, ledger map[string]ledgerRow) []string {
	var out []string
	for _, v := range verbs {
		if callsBoundVerb(kotlin, v) {
			continue
		}
		if _, excused := ledger["App."+v]; excused {
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

	for _, verb := range unledgeredVerbs(verbs, kotlin, ledger) {
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
	declared := append(qualify("App", boundAppVerbs(t)),
		qualify("FacadeBridge", facadeBridgeMethods(t))...)
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
		got := unledgeredVerbs(verbs, kotlin, map[string]ledgerRow{})
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
		if got := unledgeredVerbs(verbs, kotlin, ledger); len(got) != 0 {
			t.Fatalf("the check reported %v over a ledgered verb; the ledger is the escape "+
				"hatch and it must work, or the only way to add a verb is to wire it the same "+
				"day", got)
		}
	})

	t.Run("a row in the wrong namespace does not excuse it", func(t *testing.T) {
		ledger := map[string]ledgerRow{
			"FacadeBridge.AttachDebugger": {Symbol: "FacadeBridge.AttachDebugger", Reason: "x"},
		}
		if got := unledgeredVerbs(verbs, kotlin, ledger); len(got) != 1 {
			t.Fatalf("the check reported %v; a row about a bridge method must not excuse a "+
				"facade verb that happens to share its name, or one row silences two symbols",
				got)
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
