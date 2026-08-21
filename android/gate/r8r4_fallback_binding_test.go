package gate

// WAVE R8 / CLOSING ROUND -- THE ROUTING FENCE THAT SURVIVED ITS OWN MUTATION.
//
// THE FINDING, verbatim from the closing review's probe. The reviewer appended to
// `SessionDetailPanel.kt` -- a STRUCTURED CHAT screen --
//
//	fun reviewEvadeWatch(bridge: FacadeBridge, id: String) { bridge.terminalFallbackBinding(id).watch() }
//
// and `go test ./android/gate/` stayed green. Every R8 gate test passed.
//
// THE MECHANISM, and it is this wave's own fix reopening this wave's own finding.
// `r8WatchShapedVerbs` bans the SHAPE of a FACADE CALL SITE -- `.terminalViewWatch(`,
// `.somethingTerminalSubscribe(`, `.peek(`. Round 3 moved those call sites behind
// `TerminalFallbackBinding`, whose verbs are named `watch()`, `unwatch()` and `renew()` --
// bare names that match no shape in that list -- and `FacadeBridge.terminalFallbackBinding`
// handed a live binding to ANY caller for ANY session id, with no capability read anywhere on
// the path. The structured-screen ban list named `terminalFallbackBody`,
// `TerminalFallbackView`, `TerminalFallbackScreen` and `terminalFallbackView`, and did not
// name the binding. Finding 8 of this wave was "renaming the verb is evasion"; the fix for it
// reopened it under a new name.
//
// THE ANSWER IS STRUCTURAL AND THE LIST IS THE SECOND HALF, NOT THE FIRST. Extending a ban
// list to name one more symbol is the losing move -- it is the same move that failed here,
// and the next indirection gets a name the list does not have either. So:
//
//  1. `TerminalFallbackBinding` has a PRIVATE CONSTRUCTOR and one factory, and the factory
//     performs the capability read: a session the MACHINE did not route to the fallback yields
//     NO BINDING AT ALL. `.watch()` on a structured session stops being something a gate
//     forbids and becomes something the type system cannot express -- there is no receiver.
//  2. Only the allowlisted fallback file may CONSTRUCT one, which is checkable and is checked.
//  3. AND the structured-screen ban list names the binding, because reachability and
//     authority are different rules and T2 rule 4 is the reachability one.
//
// THE REVIEWER'S EXACT EVASION IS A PERMANENT TEST, applied to a SYNTHETIC MUTANT of the real
// screen through the SAME predicate the real scan uses. A fence written as its own separate
// copy of the rule is a fence that can pass while the rule it copies does not -- which is
// finding 7's defect class -- so there is exactly one predicate here and both callers run it.

import (
	"regexp"
	"strings"
	"testing"
)

// r8FallbackSymbolsInStructuredScreens is THE predicate, and it is the only one. The real scan
// and the mutant scan below both call it, so a change that lets the evasion through cannot
// leave the mutant test green.
// `TerminalWatchLane` and `TerminalWatchHandle` joined with agents-tracker-jx1x: they are the
// watch machinery's dispatcher and its handle seam, and a structured screen that can name
// either is one handed-in binding away from opening a watch.
var r8FallbackSymbolsInStructuredScreens = regexp.MustCompile(
	`\b(terminalFallbackBody|TerminalFallbackView|TerminalFallbackScreen|terminalFallbackView|` +
		`terminalFallbackBinding|TerminalFallbackBinding|TerminalWatchLane|TerminalWatchHandle)\b`)

// r8StructuredScreens is the list T2 rule 4 is stated over.
var r8StructuredScreens = []string{
	"dev/swarm/phone/ui/screens/SessionDetailPanel.kt",
	"dev/swarm/phone/ui/screens/SessionDetailView.kt",
	"dev/swarm/phone/ui/screens/TranscriptPanel.kt",
	"dev/swarm/phone/ui/screens/TranscriptView.kt",
	"dev/swarm/phone/ui/screens/ApprovalSheetPanel.kt",
	"dev/swarm/phone/ui/screens/ApprovalSheetView.kt",
}

// r8ReviewEvasion is the closing review's probe, byte for byte. It is a string constant and
// not a description of one, because the whole value of this fence is that it is the thing that
// walked through, not a paraphrase of it.
const r8ReviewEvasion = "fun reviewEvadeWatch(bridge: FacadeBridge, id: String) " +
	"{ bridge.terminalFallbackBinding(id).watch() }\n"

// TestR8R4Gate_TheReviewersExactEvasionIsCaught injects the probe into a real structured screen
// and asserts the REAL predicate faults on it.
//
// It is written over a mutant rather than over a comment because a gate that greps for its own
// declaration is satisfiable by its own declaration (this wave's finding 2 and finding 7 are
// both that shape). Nothing here asserts that the string appears; it asserts that the rule
// REJECTS it.
func TestR8R4Gate_TheReviewersExactEvasionIsCaught(t *testing.T) {
	sources := r8AllProductionKotlin(t)
	src, ok := sources[r8DetailPanel]
	if !ok {
		t.Fatalf("%s does not exist, so the structured screen the probe targeted is gone and this "+
			"fence measures nothing", r8DetailPanel)
	}
	mutant := kotlinCodeOnly(src + "\n" + r8ReviewEvasion)
	if !r8FallbackSymbolsInStructuredScreens.MatchString(mutant) {
		t.Errorf("ADR-017 T2 rule 4: the closing review's evasion -- %q appended to %s -- is NOT "+
			"caught by the structured-screen ban. That is the probe that walked through every R8 "+
			"gate: the binding hands a live watch to any caller for any session id, and its verbs "+
			"are named `watch`/`unwatch`/`renew`, which match no facade-call shape. Banning a shape "+
			"of call site does not ban a shape of HANDLE.", strings.TrimSpace(r8ReviewEvasion),
			r8DetailPanel)
	}
	// AND THE REAL SCREEN MUST STILL BE CLEAN. Without this the test above passes on a screen
	// that is already faulting, which would make the fence vacuous in the one direction that
	// matters.
	if r8FallbackSymbolsInStructuredScreens.MatchString(kotlinCodeOnly(src)) {
		t.Errorf("ADR-017 T2 rule 4: %s names the fallback render path in its own right", r8DetailPanel)
	}
}

// TestR8R4Gate_NoStructuredScreenNamesTheFallbackRenderPath is the real scan, through the same
// predicate.
func TestR8R4Gate_NoStructuredScreenNamesTheFallbackRenderPath(t *testing.T) {
	sources := r8AllProductionKotlin(t)
	for _, name := range r8StructuredScreens {
		src, ok := sources[name]
		if !ok {
			continue // a screen this app no longer has is not a route
		}
		if r8FallbackSymbolsInStructuredScreens.MatchString(kotlinCodeOnly(src)) {
			t.Errorf("ADR-017 T2 rule 4: %s names the terminal fallback render path or its facade "+
				"binding. A structured screen that can name it is one conditional away from opening a "+
				"watch, and RC-D5 is a routing rule rather than a default a future flag may override.",
				name)
		}
	}
}

// TestR8R4Gate_TheBindingIsConstructibleOnlyInTheAllowlistedFile is the STRUCTURAL half, and it
// is the half that makes the list above a second opinion rather than the whole defence.
func TestR8R4Gate_TheBindingIsConstructibleOnlyInTheAllowlistedFile(t *testing.T) {
	sources := r8AllProductionKotlin(t)
	screen, ok := sources[r8FallbackScreen]
	if !ok {
		t.Fatalf("%s does not exist", r8FallbackScreen)
	}
	decl := regexp.MustCompile(`class\s+TerminalFallbackBinding\s+private\s+constructor\s*\(`)
	if !decl.MatchString(kotlinCodeOnly(screen)) {
		t.Errorf("ADR-017 gate note: TerminalFallbackBinding in %s does not declare a PRIVATE "+
			"constructor. A public constructor means every file in the module can build a live "+
			"watch handle for any session id, which is the reopening of this wave's own finding 8 "+
			"under a name no ban list has.", r8FallbackScreen)
	}
	// Nobody outside the allowlisted file may CALL the constructor. `Type(` is the Kotlin
	// construction shape; the declaration itself is excluded by scanning only other files.
	ctorCall := regexp.MustCompile(`(^|[^.\w])TerminalFallbackBinding\s*\(`)
	for name, src := range sources {
		if name == r8FallbackScreen {
			continue
		}
		if ctorCall.MatchString(kotlinCodeOnly(src)) {
			t.Errorf("ADR-017 gate note: %s constructs a TerminalFallbackBinding. Exactly one file "+
				"may, because what is permitted is `one screen may watch`, not `screens may watch`.",
				name)
		}
	}
}

// TestR8R4Gate_ConstructingTheBindingRequiresTheMachinesRoutingDecision is the other structural
// half: a private constructor with an ungated factory beside it is a public constructor with an
// extra hop.
//
// The factory must perform the CAPABILITY READ -- `TerminalFallbackModel.from`, which answers
// null unless the MACHINE routed this session to the fallback -- and it must return a NULLABLE
// binding, so "this session was never routed here" has a representable answer that is not a
// live watch handle.
func TestR8R4Gate_ConstructingTheBindingRequiresTheMachinesRoutingDecision(t *testing.T) {
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[r8FallbackScreen])
	if code == "" {
		t.Fatalf("%s does not exist", r8FallbackScreen)
	}
	factory := regexp.MustCompile(`fun\s+forRoutedSession\s*\([^)]*\)\s*:\s*TerminalFallbackBinding\?`)
	if !factory.MatchString(code) {
		t.Fatalf("ADR-017 gate note: %s declares no `fun forRoutedSession(...): TerminalFallbackBinding?`. "+
			"The binding's only factory has to be able to answer NOTHING, or the capability read it "+
			"performs has nowhere to put its refusal.", r8FallbackScreen)
	}
	body := kotlinMember(t, code, "fun forRoutedSession(")
	if !strings.Contains(body, "TerminalFallbackModel.from(") {
		t.Errorf("ADR-017 gate note: forRoutedSession in %s performs no capability read. A private "+
			"constructor with an ungated factory beside it is a public constructor with one extra "+
			"hop; the point of the seam is that a session the machine did NOT route to the fallback "+
			"yields no receiver for `.watch()` at all.", r8FallbackScreen)
	}
	if !strings.Contains(body, "return null") {
		t.Errorf("ADR-017 gate note: forRoutedSession in %s never returns null, so the capability "+
			"read has no refusing arm and every session id gets a live handle.", r8FallbackScreen)
	}
}
