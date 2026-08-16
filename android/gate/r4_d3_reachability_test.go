package gate

// FAILING-FIRST (TDD RED, GG-5) for wave R4's D3 follow-on slice (bead agents-tracker-0ox9):
// the machine switcher and the global inbox COMPOSED AND REACHABLE, not merely modelled.
//
// THE STATE OF THE WORLD THIS FILE PINS, verified rather than assumed (and disclosed by
// docs/verification/r4-multimachine.md D3 in its own words):
//
//	MachinesScreen.kt        exists -- a pure model, 6/6 JVM tests green
//	MachinesScreen           referenced by NOTHING in android/app/src/main
//	App.Machines / AddMachine / SelectMachine / ForgetMachine / GlobalInbox / MachineList.Cap
//	                         bound, tested, traced -- and ledgered UNCALLED in
//	                         android/unbound-verbs.tsv (6 rows)
//
// which is EXACTLY this phase's standing defect class (android/unbound-verbs.tsv's header: six
// separate defects shipped that way in Phase B), plus the three recorded UX defect shapes:
//
//	FINDABLE   a control that exists must be named by the composition -- the pairing panel was
//	           built, wired, covered, and an owner could not find it under `triageInboxView`'s
//	           anonymous `below:` column (agents-tracker-64rf); the composer repeated the burial
//	           (agents-tracker-nx44.6).
//	REACHABLE  a screen that exists must have a navigation caller -- `machinesPanelView` (the
//	           deleted predecessor) had "zero production call sites" while composed and covered
//	           (PhoneSurfaceNavigationTest's own record).
//	NEVER HAND-FED  the destination is the RESOLVER's answer from first-run state
//	           (MachinesScreen.destinationFor), not a branch a surface invents; a resolver no
//	           production code consults is the model-nothing-renders defect again.
//
// WHY A GO GATE AND NOT ONLY THE ROBOLECTRIC SUITE. The JVM suite (MachinesPanelViewTest)
// asserts the composition BEHAVES over models it can build; what it cannot durably assert is
// that the app's own surface composes it at all, because `PhoneRuntime.phone()` answers
// Unavailable on every JVM run and the paired destinations are out of reach there -- the same
// line android/gate/pairingentry_test.go draws over the same surface for the same reason.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. Every scan starts at the app module or the mobile
// facade, so it cannot descend into other agents' worktrees.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// r4d3ViewFile is the switcher's composition, in the module's established shape (screen model
// + <Name>View.kt beside it: TriageInboxView, ActivityPanelView, PairOnlyView).
const r4d3ViewFile = "dev/swarm/phone/ui/screens/MachinesPanelView.kt"

// r4d3SurfaceFile is the one surface that decides what the window holds.
const r4d3SurfaceFile = "dev/swarm/phone/PhoneSurface.kt"

// r4d3Code reads one production Kotlin source as REFERENCES and nothing else: comments and
// string literals out, for pairingentry_test.go's recorded reasons -- a fence a comment can
// satisfy is one the next thorough comment turns off, and screen copy talks about the product,
// so a word in a user-visible string must be able neither to fail a containment fence nor to
// fake a call site.
func r4d3Code(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(rel))
	return kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-0ox9")))
}

// r4d3ProductionCode is every main-source-set Kotlin file, code-only, keyed by path.
func r4d3ProductionCode(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		out[f] = kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, f, "agents-tracker-0ox9")))
	}
	if len(out) == 0 {
		t.Fatalf("agents-tracker-0ox9: no production Kotlin under %s", mustRel(t, kotlinMainRoot(t)))
	}
	return out
}

// r4d3CallersOf are the production files whose CODE references name, excluding the declaring
// file(s): the files that contain `fun name(` or `val NAME =`-style declarations of it.
func r4d3CallersOf(code map[string]string, name string, declPattern *regexp.Regexp) []string {
	ref := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `[^A-Za-z0-9_]`)
	var out []string
	for path, src := range code {
		if declPattern != nil && declPattern.MatchString(src) {
			continue
		}
		if ref.MatchString(src) {
			out = append(out, path)
		}
	}
	return out
}

// r4d3BlockAfter returns the brace-balanced block opened by the first `{` at or after the
// first occurrence of marker, or "" when the marker is absent. It is the same containment
// question pairingentry_test.go asks of the same file: which view is a child of which
// container is a fact about the source's view graph.
func r4d3BlockAfter(src, marker string) string {
	at := strings.Index(src, marker)
	if at < 0 {
		return ""
	}
	open := strings.Index(src[at:], "{")
	if open < 0 {
		return ""
	}
	depth := 0
	start := at + open
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	return src[start:]
}

// ---------------------------------------------------------------------------
// REACHABLE: the switcher is composed by the app, from a caller outside its own file.
// ---------------------------------------------------------------------------

func TestR4D3_TheMachinesSwitcherIsComposedAndHasANavigationCaller(t *testing.T) {
	// The view exists in the module's established shape. readFileOrFail is the RED report for
	// a missing file: today ui/screens/ has no machines view at all.
	viewSrc := r4d3Code(t, r4d3ViewFile)
	if !regexp.MustCompile(`fun\s+machinesPanelView\s*\(`).MatchString(viewSrc) {
		t.Errorf("agents-tracker-0ox9: %s declares no `fun machinesPanelView(`; the switcher "+
			"screen model (MachinesScreen.kt) is referenced by nothing in android/app/src/main "+
			"-- no companion View, no Surface, no navigation caller -- which is "+
			"docs/verification/r4-multimachine.md D3's own disclosure", r4d3ViewFile)
	}

	// And it is CALLED from production Kotlin outside that file. "Composed and covered with
	// zero production call sites" is the exact state the deleted predecessor shipped in
	// (PhoneSurfaceNavigationTest's record) and the exists-but-unreachable shape by name.
	code := r4d3ProductionCode(t)
	viewPath := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(r4d3ViewFile))
	callers := []string{}
	call := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])machinesPanelView\s*\(`)
	for path, src := range code {
		if path == viewPath {
			continue
		}
		if call.MatchString(src) {
			callers = append(callers, path)
		}
	}
	if len(callers) == 0 {
		t.Errorf("agents-tracker-0ox9: no production Kotlin outside %s calls machinesPanelView("+
			"...); a screen nothing navigates to is worth exactly as much as a component "+
			"library nothing renders (PB-DS-6's recorded NOT MET)", r4d3ViewFile)
	}
}

func TestR4D3_TheSwitcherIsNotBuriedInTheAnonymousBelowSlot(t *testing.T) {
	// The two burials on record, fenced by name. A machines view hung under the inbox as the
	// `below:` column -- or added to `unrecomposedControls` -- type-checks, renders, and is the
	// agents-tracker-64rf defect verbatim: built, wired, unfindable.
	surface := r4d3Code(t, r4d3SurfaceFile)
	block := r4d3BlockAfter(surface, "val unrecomposedControls")
	if block == "" {
		t.Fatalf("agents-tracker-0ox9: %s no longer declares unrecomposedControls; this fence's "+
			"subject moved and the fence must move with it rather than report clean over "+
			"nothing", r4d3SurfaceFile)
	}
	for _, view := range []string{"machinesPanelView", "globalInboxView"} {
		if strings.Contains(block, view) {
			t.Errorf("agents-tracker-0ox9: %s composes %s inside unrecomposedControls -- the "+
				"anonymous column hosted below the inbox list. That is the burial that made "+
				"the pairing panel exist and be unfindable on a real handset "+
				"(agents-tracker-64rf), recorded as UX defect shape 1", r4d3SurfaceFile, view)
		}
	}
	for path, src := range r4d3ProductionCode(t) {
		for _, view := range []string{"machinesPanelView", "globalInboxView"} {
			if regexp.MustCompile(`below\s*=\s*` + view).MatchString(src) {
				t.Errorf("agents-tracker-0ox9: %s passes %s as a `below =` argument -- the "+
					"anonymous slot by its literal name", mustRel(t, path), view)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// FINDABLE: the way in is NAMED, by the model's recorded copy, at a call site.
// ---------------------------------------------------------------------------

func TestR4D3_TheEntryIsNamedByTheRecordedCopyAtACallSite(t *testing.T) {
	// MachinesPanelScreen.ENTRY_LABEL is the recorded name of the control that leads to the
	// switcher (frozen by MachinesPanelScreenTest). This asserts a production file that does
	// NOT declare the constant spends it -- i.e. the composition names the entry from the
	// model rather than typing its own words or, worse, offering no named entry at all.
	code := r4d3ProductionCode(t)
	decl := regexp.MustCompile(`(?:const\s+)?val\s+ENTRY_LABEL`)
	callers := r4d3CallersOf(code, "ENTRY_LABEL", decl)
	if len(callers) == 0 {
		t.Errorf("agents-tracker-0ox9: no production Kotlin outside its declaring file " +
			"references ENTRY_LABEL, so the machine switcher has no NAMED way in. A control " +
			"that exists must be findable -- named by the composition, not discovered by " +
			"scrolling past an anonymous column (agents-tracker-64rf, agents-tracker-nx44.6)")
	}
}

// ---------------------------------------------------------------------------
// NEVER HAND-FED: the destination is the first-run resolver's answer.
// ---------------------------------------------------------------------------

func TestR4D3_TheFirstRunResolverIsConsultedByProduction(t *testing.T) {
	// MachinesScreen.destinationFor is "a function rather than a branch in PhoneSurface's
	// redraw, so the JVM suite drives it from the empty state" -- its own KDoc. A resolver no
	// production code consults leaves the first-run decision to whatever branch a surface
	// invents, which is precisely the hand-fed-state defect shape: the tests would drive a
	// resolver the app does not use.
	//
	// THE DOT IS REQUIRED, for boundVerbCall's recorded reason: without it the DECLARATION
	// `fun destinationFor(` in MachinesScreen.kt satisfies the check that the resolver is
	// consulted -- which is exactly what a first run of this test observed. Every call site
	// carries the receiver (`MachinesScreen.destinationFor(...)`), so the dot is always there.
	call := regexp.MustCompile(`\.\s*destinationFor\s*\(`)
	found := false
	for _, src := range r4d3ProductionCode(t) {
		if call.MatchString(src) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("agents-tracker-0ox9: no production Kotlin calls MachinesScreen." +
			"destinationFor(...). The first-run resolver (0 machines -> PAIR_ONLY, >=1 -> " +
			"MACHINES) exists and decides nothing: the destination a real user lands on is " +
			"not the resolver's answer, so every JVM test that drives destinationFor is " +
			"testing a function the app ignores")
	}
}

// ---------------------------------------------------------------------------
// The wire itself: the six R4 symbols leave the ledger by being CALLED.
// ---------------------------------------------------------------------------

func TestR4D3_TheSixMachineVerbsAreCalledAndLeaveTheLedger(t *testing.T) {
	// The bidirectional control (boundverbledger_test.go) accepts EITHER a production caller
	// OR a ledger row, so it is green today over six verbs no user can reach. This slice's
	// exit is the other corner: CALLED, and the excusing row DELETED -- the ledger's own
	// uncallable-by-default rule run to completion. (The existing
	// TestBoundVerbs_TheLedgerCannotExcuseASymbolTheAppNowReaches forces the deletion half
	// once the call half lands; this test is what forces the call half.)
	kotlin := stripKotlinComments(appKotlinSource(t))
	ledger := ledgerIndex(readUnboundLedger(t))

	symbols := []struct {
		owner string
		verb  string
		why   string
	}{
		{"App", "Machines", "the switcher's row snapshot (machines.list)"},
		{"App", "AddMachine", "Add computer -- BESIDE the existing pairings, MM6 migration on first use (machines.add)"},
		{"App", "SelectMachine", "Switch computer -- feeds the least-recently-viewed policy (machines.select)"},
		{"App", "ForgetMachine", "Forget this computer -- phone-side, distinct from revoke (machines.forget)"},
		{"App", "GlobalInbox", "the aggregate inbox keyed (machine_id, session_id) (inbox.global)"},
		{"MachineList", "Cap", "the documented foreground connection cap, rendered honestly (ADR-018)"},
	}
	for _, s := range symbols {
		qualified := s.owner + "." + s.verb
		if !callsBoundVerb(kotlin, s.verb) {
			t.Errorf("agents-tracker-0ox9: swarmmobile.%s -- %s -- has NO production-Kotlin "+
				"caller. This is the phase's standing defect class ('the requirement is "+
				"implemented, and the app cannot reach it'), and wave R4's D3 disclosure says "+
				"so; composing the switcher and inbox over FacadeBridge is what retires it",
				qualified, s.why)
		}
		if row, excused := ledger[qualified]; excused {
			t.Errorf("agents-tracker-0ox9: android/unbound-verbs.tsv:%d still excuses %s. The "+
				"row's own text names this slice as its deletion condition; a wired verb's "+
				"row must go with the wiring, or the ledger rots into a list of considered-"+
				"sounding rows about symbols the app reaches", row.Line, qualified)
		}
	}
}

// ---------------------------------------------------------------------------
// The global inbox destination is composed, like the switcher.
// ---------------------------------------------------------------------------

func TestR4D3_TheGlobalInboxIsComposedAndHasANavigationCaller(t *testing.T) {
	code := r4d3ProductionCode(t)
	decl := regexp.MustCompile(`fun\s+globalInboxView\s*\(`)
	declared := ""
	for path, src := range code {
		if decl.MatchString(src) {
			declared = path
			break
		}
	}
	if declared == "" {
		t.Fatalf("agents-tracker-0ox9: no production Kotlin declares `fun globalInboxView(`; " +
			"the GLOBAL_INBOX destination (inbox.global) exists as a bound verb and a ledger " +
			"row, and no screen composes it -- the triage inbox is still the only inbox, " +
			"single-machine by construction")
	}
	call := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])globalInboxView\s*\(`)
	callers := 0
	for path, src := range code {
		if path == declared {
			continue
		}
		if call.MatchString(src) {
			callers++
		}
	}
	if callers == 0 {
		t.Errorf("agents-tracker-0ox9: globalInboxView(...) has no caller outside %s; a "+
			"destination nothing navigates to is the exists-but-unreachable shape again",
			mustRel(t, declared))
	}
}
