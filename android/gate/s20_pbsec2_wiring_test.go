package gate

// PB-SEC-2's WIRING -- the layer every other fence in this package assumes and none of them
// checks: that the screen actually asks the gate, and that the observers which end custody are
// actually installed.
//
// WHY THIS FILE EXISTS. PB-SEC-2's evidence column says "tests per clause", and the clauses were
// adjudicated one at a time by mutating the PRODUCTION CONNECTION rather than the value a test
// transcribes. Two mutations survived everything:
//
//	(1) `PhoneSurface.timedButton` stops calling the timed gate -- `setOnClickListener {
//	    invoke(verb) }` -- and the keyboard reaches `App.sendInput` with no freshness decision and
//	    no prompt. The TimedTierGate FIELD is still there, so every check that asks "is the gate
//	    named in this file" passes. `TimedTierGateTest` passes too: it drives the gate class,
//	    which is still perfectly correct and is simply no longer asked.
//	    THE SAME MUTATION SURVIVES ON THE PER-USE TIER: gutting `perUseButton` the same way leaves
//	    `TestPBSEC2_EveryPerUseFacadeVerbIsReachedThroughThePerUseButton` green, because that check
//	    proves the verb is DECLARED through the factory and never asks what the factory does.
//	    Revoke and kill then run on no authorization at all.
//
//	(2) `SwarmApplication.onCreate` stops calling `ContentLockTriggers.install`. Nothing then
//	    registers the screen-off receiver or the lifecycle callbacks, so NO INVALIDATION EVENT IS
//	    EVER RAISED -- and `BiometricGateTest.every_invalidation_event_drops_every_authorization`,
//	    `ContentLockTest` and `LockPurgeTest` all stay green, because each of them raises the
//	    event itself and asks what the ledger did about it.
//
// Both are the same defect one layer up from where the fences look, and it is the defect ADR-007
// B51 named: a correct mechanism nothing reaches. B51 was about a policy object with no caller;
// this is about a GATE with no caller, which is worse, because the gate's own tests go on passing
// and read as coverage.
//
// WHAT THESE CHECKS HOLD, and it is deliberately a reachability property rather than a control
// flow one: from the control that names a gated verb, the gate is REACHABLE by same-file calls;
// and the observer that raises invalidation events is CONSTRUCTED somewhere other than its own
// file. Stated as limits:
//
//   - REACHABLE IS NOT ON-PATH. A gate call sitting in a branch that never runs, or beside the
//     ungated call rather than around it, satisfies check (1). Verified by mutation: a factory
//     that calls `invoke(verb)` directly AND forwards to the gate from a dead helper passes. What
//     it cannot do is remove the gate from the control's reach entirely, which is what every
//     bypass found this round actually did.
//   - IT FOLLOWS CALLS WITHIN ONE FILE. A factory that delegates into another file is not
//     followed, and reads as ungated. The direction of that error is toward FAILING, which is the
//     safe direction, and the remedy is to name the gate where the control is built.
//   - IT MATCHES TEXT, not types, for the reason every check in this package states.
//
// It reads checked-in source only: no Android SDK, no JDK, no Gradle, no emulator, no handset.
// This file never skips.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// (1) The control reaches its gate.
// ---------------------------------------------------------------------------

// gatedTier is one of requirements 6.0's two tiers: the facade verbs it binds, and the gate entry
// point a control naming one of those verbs must be able to reach.
//
// THE GATE IS NAMED BY ITS DECLARED METHOD, and `declaredAs` is the vacuity control that keeps
// that honest: the method must exist as a production declaration. Renaming the gate's entry point
// without updating this file fails as "the gate this fence names does not exist" rather than as a
// silent pass over an ungated app -- which is the failure mode a fence must never have.
type gatedTier struct {
	name       string
	verbs      []string
	gateCall   string
	declaredAs string
	floor      int
}

var gatedTiers = []gatedTier{
	{
		name: "TIMED (requirements 6.0: 60 s for input/take_control)",
		// Matched with the `app.` receiver so an unrelated Kotlin method of the same name does
		// not read as one of these. A rename of the receiver makes them vanish, which the floor
		// below turns into a failure rather than a silent pass.
		verbs:      []string{"app.sendInput(", "app.takeControl("},
		gateCall:   ".withFreshAuthorization(",
		declaredAs: "fun withFreshAuthorization(",
		floor:      2,
	},
	{
		name:       "PER-USE (requirements 6.0: CryptoObject for revoke/kill switch/launch/kill)",
		verbs:      []string{"app.revokeThisDevice(", "app.kill(", "app.launch("},
		gateCall:   ".authorize(",
		declaredAs: "fun authorize(",
		floor:      2,
	},
}

// factoryOfDeclaration extracts the function a control is built by, from its own declaration
// line: `private val send = timedButton("Send line", ...)` yields `timedButton`.
//
// IT IS DERIVED AND NOT LISTED. A fence that hardcoded "timedButton" and "perUseButton" would be
// a fence that a rename turns off, and would say nothing about a third factory somebody adds.
var factoryOfDeclaration = regexp.MustCompile(`=\s*([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// callIdentifier is any identifier used as a call. Used to walk one file's call graph.
var callIdentifier = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// kotlinOwningMemberOf returns the CLASS MEMBER declaration a source offset sits under, chosen by
// indentation rather than by proximity.
//
// IT IS NOT `kotlinDeclarationOf`, AND THE DIFFERENCE IS A FALSE POSITIVE THIS CHECK ACTUALLY
// PRODUCED. That helper takes the nearest preceding `val`/`var`/`fun`, and the control this fence
// most needs to read is
//
//	private val send = timedButton("Send line", GatedOperation.INPUT) { app, session ->
//	    val line = typed.text?.toString().orEmpty()
//	    app.sendInput(session, ...)
//
// where the nearest preceding declaration is the LOCAL `val line` inside the lambda. The keyboard
// -- the single most important control this fence covers -- reported as built by no factory at
// all. A fence that fails on correct code gets relaxed, and the relaxation is what ships.
//
// Class members share one indentation; locals are deeper. So the owning member is the nearest
// preceding declaration at the file's SHALLOWEST member indentation, which skips locals whatever
// they are called and however many of them there are.
func kotlinOwningMemberOf(src string, offset int) string {
	locs := kotlinMemberDecl.FindAllStringIndex(src, -1)
	if len(locs) == 0 {
		return ""
	}
	indentAt := func(start int) int {
		n := 0
		for start+n < len(src) && (src[start+n] == ' ' || src[start+n] == '\t') {
			n++
		}
		return n
	}
	shallowest := -1
	for _, l := range locs {
		if in := indentAt(l[0]); shallowest < 0 || in < shallowest {
			shallowest = in
		}
	}
	best := -1
	for _, l := range locs {
		if l[0] >= offset {
			break
		}
		if indentAt(l[0]) == shallowest {
			best = l[0]
		}
	}
	if best < 0 {
		return ""
	}
	if end := strings.Index(src[best:], "\n"); end >= 0 {
		return src[best : best+end]
	}
	return src[best:]
}

// memberSpanNamed returns the source of the function `name`, from its declaration to the start of
// the next member declaration.
//
// THE END BOUND IS THE NEXT MEMBER AND NOT A BRACE MATCH, because Kotlin's expression bodies have
// no braces to match: `private fun timedButton(...): Button = SecureWindow.gate(...)` is the
// actual shape of the factory this check exists to read. A function whose body contains a
// line-start `val` would be truncated early; the direction of that error is toward reporting the
// gate unreachable, which fails rather than passes.
func memberSpanNamed(src, name string) string {
	re := regexp.MustCompile(`(?m)^[ \t]*(?:private[ \t]+|internal[ \t]+)?fun[ \t]+` +
		regexp.QuoteMeta(name) + `\b`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	next := kotlinMemberDecl.FindStringIndex(src[loc[1]:])
	if next == nil {
		return src[loc[0]:]
	}
	return src[loc[0] : loc[1]+next[0]]
}

// sameFileCallClosure is every function reachable from entry by calls within one file, as text.
func sameFileCallClosure(src, entry string) string {
	seen := map[string]bool{}
	var out strings.Builder
	var walk func(string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		span := memberSpanNamed(src, name)
		if span == "" {
			return
		}
		out.WriteString(span)
		for _, m := range callIdentifier.FindAllStringSubmatch(span, -1) {
			walk(m[1])
		}
	}
	walk(entry)
	return out.String()
}

// TestPBSEC2_EveryGatedControlCanActuallyReachItsGate.
//
// THE MUTATIONS THAT MUST FAIL IT, both verified:
//
//	`setOnClickListener { withFreshTimedAuthorization(operation) { invoke(verb) } }`
//	   -> `setOnClickListener { invoke(verb) }`                       (timed tier)
//	the equivalent edit to `perUseButton`'s click listener            (per-use tier)
//
// Each is a one-line edit, each compiles, and before this check each left every Go and Kotlin
// test in the repository green while returning its tier to no authorization at all.
func TestPBSEC2_EveryGatedControlCanActuallyReachItsGate(t *testing.T) {
	code := productionKotlinCode(t)

	for _, tier := range gatedTiers {
		// VACUITY CONTROL ONE: the gate this fence names must exist. Without it a renamed entry
		// point would make `gateCall` unfindable everywhere, and every control would read as
		// ungated -- which fails loudly -- or, had the check been written the other way round,
		// would pass over nothing at all.
		declared := false
		for _, src := range code {
			if strings.Contains(src, tier.declaredAs) {
				declared = true
				break
			}
		}
		if !declared {
			t.Fatalf("PB-SEC-2: the %s gate entry point %q is declared by no production Kotlin. "+
				"This fence names it, so it cannot check anything until the name is corrected "+
				"here -- and a fence that cannot find its subject must fail rather than pass",
				tier.name, tier.declaredAs)
		}

		type site struct{ where, factory string }
		var sites []site
		for f, src := range code {
			for _, verb := range tier.verbs {
				for at := 0; ; {
					i := strings.Index(src[at:], verb)
					if i < 0 {
						break
					}
					off := at + i
					at = off + len(verb)
					decl := kotlinOwningMemberOf(src, off)
					factory := ""
					if m := factoryOfDeclaration.FindStringSubmatch(decl); m != nil {
						factory = m[1]
					}
					sites = append(sites, site{
						where:   mustRel(t, f) + ": " + verb,
						factory: factory,
					})
				}
			}
		}

		// VACUITY CONTROL TWO: the floor. A receiver rename, or a control the app lost, makes
		// every verb read as absent -- and a check that measures nothing reports a clean gate
		// over an ungated app, which is the whole defect class this package exists for.
		if len(sites) < tier.floor {
			t.Fatalf("PB-SEC-2: found %d production call site(s) for the %s verbs %v, want at "+
				"least %d. Either the app lost those controls or this check can no longer see "+
				"them; both need this fence updated rather than passing quietly",
				len(sites), tier.name, tier.verbs, tier.floor)
		}

		var unreachable []string
		for _, s := range sites {
			if s.factory == "" {
				unreachable = append(unreachable, s.where+" is not built by any factory")
				continue
			}
			reached := false
			for _, src := range code {
				if strings.Contains(sameFileCallClosure(src, s.factory), tier.gateCall) {
					reached = true
					break
				}
			}
			if !reached {
				unreachable = append(unreachable,
					s.where+" is built by "+s.factory+"(), which cannot reach "+tier.gateCall)
			}
		}
		sort.Strings(unreachable)
		if len(unreachable) > 0 {
			t.Errorf("PB-SEC-2: %d %s control(s) reach a gated facade verb through a factory that "+
				"never reaches the gate:\n\t%s\n"+
				"The gate class can be perfectly correct and perfectly tested and still not be "+
				"ASKED. That is ADR-007 B51's shape applied to the gate itself: `TimedTierGateTest` "+
				"and `PerUseGateTest` drive the gate over seams and go on passing, because what "+
				"broke is the one edge neither of them can see -- the screen calling it",
				len(unreachable), tier.name, strings.Join(unreachable, "\n\t"))
		}
	}
}

// ---------------------------------------------------------------------------
// (2) The observers that end custody are installed.
// ---------------------------------------------------------------------------

// invalidationRaise is production code telling the lock that an invalidating event happened. It is
// the ledger's own method name plus the event type, so both ends of the call have to change
// together for this to stop matching.
const invalidationRaise = ".invalidate(InvalidationEvent."

// enclosingClass finds the class a source offset sits inside.
var enclosingClass = regexp.MustCompile(
	`(?m)^[ \t]*(?:private[ \t]+|internal[ \t]+)?(?:open[ \t]+|abstract[ \t]+)?class[ \t]+([A-Za-z_][A-Za-z0-9_]*)`)

// TestPBSEC2_TheObserversThatEndCustodyAreInstalled.
//
// PB-SEC-2 clause 2 -- "invalidation on background, screen lock, process death, and
// biometric-enrollment change". Every existing fence for that clause raises the event ITSELF and
// then asks what the ledger did, which is the right question about the ledger and no question at
// all about whether anything raises it in a running app.
//
// THE MUTATION THAT MUST FAIL IT: deleting `ContentLockTriggers(contentLock).install(this)` from
// `SwarmApplication.onCreate`. One line. It compiles, and before this check it left
// `BiometricGateTest`, `ContentLockTest`, `LockPurgeTest` and every Go gate green over an app in
// which a screen lock ends nothing at all.
//
// WHAT IT CANNOT SEE: that `install` is reached at RUNTIME. `Application.onCreate` is the
// platform's entry point and nothing in this repository executes it -- ADR-007 B56 puts the
// androidTest tier out of reach. This asks only that the observer is constructed by production
// code other than its own file, which is the edge the mutation cut.
func TestPBSEC2_TheObserversThatEndCustodyAreInstalled(t *testing.T) {
	code := productionKotlinCode(t)

	raisers := map[string]string{} // class name -> file that declares it
	for f, src := range code {
		for at := 0; ; {
			i := strings.Index(src[at:], invalidationRaise)
			if i < 0 {
				break
			}
			off := at + i
			at = off + len(invalidationRaise)
			locs := enclosingClass.FindAllStringSubmatchIndex(src[:off], -1)
			if len(locs) == 0 {
				continue
			}
			last := locs[len(locs)-1]
			raisers[src[last[2]:last[3]]] = f
		}
	}

	if len(raisers) == 0 {
		t.Fatalf("PB-SEC-2: no production Kotlin raises %q, so NOTHING ends content custody on "+
			"any platform signal. Clause 2 names four events; a ledger that handles all of them "+
			"correctly and is never told about any of them satisfies the requirement nowhere",
			invalidationRaise)
	}

	var orphaned []string
	for class, declaredIn := range raisers {
		constructed := false
		for f, src := range code {
			if f == declaredIn {
				continue
			}
			if strings.Contains(src, class+"(") {
				constructed = true
				break
			}
		}
		if !constructed {
			orphaned = append(orphaned, class+" (declared in "+filepath.Base(declaredIn)+")")
		}
	}
	sort.Strings(orphaned)
	if len(orphaned) > 0 {
		t.Errorf("PB-SEC-2: %d class(es) raise invalidation events and are constructed by no "+
			"other production file:\n\t%s\n"+
			"An observer nobody installs raises nothing. The screen-off receiver and the "+
			"lifecycle callbacks are registered by this class's own `install`, and `install` runs "+
			"only if something builds it -- so deleting the one construction site silently "+
			"removes screen lock and backgrounding as invalidating events, while every test that "+
			"raises those events by hand keeps passing",
			len(orphaned), strings.Join(orphaned, "\n\t"))
	}
}
