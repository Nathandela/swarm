package gate

// WAVE R8 / SLICES S3, S4, S5, S9 -- THE PHONE HALF, AS SOURCE FENCES.
// Failing-first (TDD RED, GG-5).
//
// WHY SOURCE SCANS AND NOT ROBOLECTRIC, restating i1_sheetandwell_test.go's own reason: a
// rendered-tree assertion says what a screen CONTAINS. Every obligation below is about what
// the app does NOT contain anywhere, or about which single file may contain it -- and a
// deletion obligation cannot be observed from a tree.
//
// THE GATE THAT ALREADY EXISTS, AND WHY IT IS NOT ENOUGH. `i1WatchCalls` bans exactly three
// spellings: `.terminalWatch(`, `.terminalUnwatch(`, `.terminalPeek(`. R8 adds a fallback
// screen that legitimately issues a watch, so the naive move is to add a verb with a new name
// -- `app.terminalViewWatch(...)` -- which clears the ban with no argument and no ruling,
// and leaves the intent ("no phone surface issues a watch") routed around rather than
// amended. ADR-017's gate note answers it: the ban WIDENS to any watch-shaped verb, with a
// SINGLE-FILE ALLOWLIST naming exactly the one fallback screen, and the NET ASSERTION COUNT
// RISES. If it falls, the wave failed this rule.
//
// The allowlist is one file by name and not a directory, a prefix or a pattern, because the
// thing being permitted is "one screen may watch", not "screens may watch".

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// r8FallbackScreen is THE one file the widened bans allow. Naming it here is the allowlist:
// a second fallback screen is a second surface that watches, which is the state ADR-009 (3)
// describes as "a fallback surface that outlives its replacement stops being a fallback and
// becomes the design".
const (
	r8FallbackScreen = "dev/swarm/phone/ui/screens/TerminalFallbackScreen.kt"
	r8FallbackView   = "dev/swarm/phone/ui/screens/TerminalFallbackView.kt"
	r8DetailPanel    = "dev/swarm/phone/ui/screens/SessionDetailPanel.kt"
)

// r8WatchShapedVerbs is the widened ban, and round 2 widened it TWICE MORE after three
// mutations walked through the first version:
//
//  1. THE ANCHOR WAS WRONG. The patterns read `\.\s*terminal[A-Za-z]*[Ww]atch`, i.e. the
//     identifier had to START with `terminal` -- so `app.startTerminalWatch(s)` added to
//     SessionDetailPanel.kt, a STRUCTURED CHAT SCREEN, passed the gate. `terminal` may now
//     appear anywhere in the identifier.
//  2. THE BARE PEEK MATCHED NOTHING. `App.Peek` is the phone's snapshot-cache read and it
//     takes any session id; `FacadeBridge.terminalRows` called `app.peek(sessionId)` from a
//     file that is neither the fallback screen nor the fallback view, so the render path was
//     reachable without a capability read -- which is exactly what the ADR's gate note says
//     it must not be. A bare `.peek(` is banned on the same allowlist.
//
// The list is the SHAPE of a watch verb, never a spelling, so renaming one is not a way past
// the rule -- and the allowlist is one file, because what is permitted is "one screen may
// watch", not "screens may watch".
var r8WatchShapedVerbs = []string{
	`\.\s*[A-Za-z_]*[Tt]erminal[A-Za-z_]*[Ww]atch\s*\(`,
	`\.\s*[A-Za-z_]*[Tt]erminal[A-Za-z_]*[Ss]ubscribe\s*\(`,
	`\.\s*[A-Za-z_]*[Tt]erminal[A-Za-z_]*[Oo]bserve\s*\(`,
	`\.\s*[A-Za-z_]*[Tt]erminal[A-Za-z_]*[Ss]tream\s*\(`,
	`\.\s*[A-Za-z_]*[Tt]erminal[A-Za-z_]*[Pp]eek\s*\(`,
	`\.\s*peek\s*\(`,
}

// r8AllProductionKotlin is EVERY production Kotlin file, with NO exempt directory.
//
// s24ProductionKotlin exempts `dev/swarm/phone/ui/kit` (PB-DS-6 gives the kit its own
// stricter typography fence, so re-scanning it there would be a second opinion rather than a
// second fence). That exemption is correct for a typography rule and CATASTROPHIC for this
// one: a kit file rendering `monoWell(terminal = true)` -- the raw grid -- or calling
// `app.terminalViewWatch(` passed every R8 gate, defeating the one-file allowlist and
// ADR-009 (1) as re-scoped together. Round 2 measured all three mutations passing.
//
// Every ban in this file that is stated over "no production Kotlin file" is scanned with
// THIS function. A ban with an exempt directory is a ban with a documented bypass.
func r8AllProductionKotlin(t *testing.T) map[string]string {
	t.Helper()
	root := s24KotlinRoot(t)
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".kt") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("ADR-017 gate note: walking %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("ADR-017 gate note: no production Kotlin found under %s; every ban in this file "+
			"would pass vacuously", root)
	}
	// The scan must be STRICTLY WIDER than the exempting one, or the widening is nominal.
	if narrow := len(s24ProductionKotlin(t)); len(out) <= narrow {
		t.Fatalf("ADR-017 gate note: the unexempted scan sees %d files and the exempting one sees %d. "+
			"The kit and theme directories are where a raw grid or a watch verb would hide from every "+
			"other fence in this file.", len(out), narrow)
	}
	return out
}

// TestR8Gate_OnlyTheFallbackScreenIssuesAWatch is the widened D-GATE ban.
func TestR8Gate_OnlyTheFallbackScreenIssuesAWatch(t *testing.T) {
	var faults []string
	var allowed int
	for name, src := range r8AllProductionKotlin(t) {
		code := kotlinCodeOnly(src)
		for _, pat := range r8WatchShapedVerbs {
			if !regexp.MustCompile(pat).MatchString(code) {
				continue
			}
			if name == r8FallbackScreen {
				allowed++
				continue
			}
			faults = append(faults, name+": matches "+pat)
		}
	}
	sort.Strings(faults)
	for _, f := range faults {
		t.Errorf("ADR-017 gate note: %s. The ban on a phone surface issuing a watch is stated over the "+
			"SHAPE of the verb, not over three legacy spellings, precisely so that renaming it is not a "+
			"way past the rule. Exactly one file -- %s -- may issue one.", f, r8FallbackScreen)
	}
	if allowed == 0 {
		t.Errorf("ADR-017 T4: NO production Kotlin file issues a terminal watch, so the fallback screen "+
			"either does not exist or never asks the machine for a snapshot. %s is the one file that may.",
			r8FallbackScreen)
	}
}

// TestR8Gate_TheRetiredPeekSymbolsStayBannedByName is the half that must NOT widen.
// `i1WellSymbols` bans `peekHost`, `peekPanelView`, `PeekPanelScreen`, `PeekPanel`,
// `TerminalPeek` by name, forever. The fallback is a NEW screen under a capability record,
// not the restoration of the retired one -- ADR-017's own closing note is written against
// exactly that misreading ("nobody restores a grid to the phone believing they are fixing a
// regression").
func TestR8Gate_TheRetiredPeekSymbolsStayBannedByName(t *testing.T) {
	for name, src := range r8AllProductionKotlin(t) {
		code := kotlinCodeOnly(src)
		for _, symbol := range i1WellSymbols {
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`).MatchString(code) {
				t.Errorf("ADR-017 Notes: %s names the retired peek symbol `%s`. The fallback returns BY "+
					"RULING for one routed class of session; restoring the peek's own hosting path is drift, "+
					"not this decision being implemented.", name, symbol)
			}
		}
		if strings.HasPrefix(filepath.Base(name), "PeekPanel") {
			t.Errorf("ADR-009 (3): %s is back in the app", name)
		}
	}
}

// TestR8Gate_OnlyTheFallbackBodyPrintsTheTerminalWell is the same allowlist for the ink.
// `monoWell(terminal = true)` is the escape-filtered VT snapshot's rendering; ADR-009 (1)
// remains the rule everywhere except the one fallback body.
func TestR8Gate_OnlyTheFallbackBodyPrintsTheTerminalWell(t *testing.T) {
	terminalWell := regexp.MustCompile(`monoWell\s*\([^)]*terminal\s*=\s*true`)
	var allowed int
	for name, src := range r8AllProductionKotlin(t) {
		if !terminalWell.MatchString(kotlinCodeOnly(src)) {
			continue
		}
		if name == r8FallbackView || name == r8FallbackScreen {
			allowed++
			continue
		}
		t.Errorf("ADR-009 (1) as re-scoped by ADR-017 T1: %s prints the daemon-rendered grid. The no-grid "+
			"rule is re-scoped to structured_chat sessions, NOT repealed; only the one fallback body may "+
			"render the sanitized snapshot.", name)
	}
	if allowed == 0 {
		t.Errorf("ADR-017 T1: no production Kotlin file renders the sanitized terminal view. %s is where "+
			"the fallback body lives.", r8FallbackView)
	}
}

// TestR8Gate_NoStructuredScreenNamesTheFallbackRenderPath MOVED, and it moved because it was
// EVADABLE. The closing review appended one line to SessionDetailPanel.kt --
// `bridge.terminalFallbackBinding(id).watch()` -- and this test, and every other R8 gate, stayed
// green: the ban list named the fallback BODY and VIEW and not the facade BINDING that round 3
// introduced. The rule now lives in `r8r4_fallback_binding_test.go` as ONE shared predicate that
// the real scan and a synthetic mutant of the reviewer's exact probe both run, beside the
// structural fence (a private constructor and a capability-reading factory) that makes the list
// a second opinion rather than the whole defence. Keeping a second copy of the rule here is the
// defect class this wave's finding 7 describes -- two fences for one property, either of which
// can pass while the property does not.

// TestR8Gate_TheFallbackBodyNeverRoutesASnapshotLineThroughMarkdown is amendment T4-c's third
// addition, and it is the one no machine-side sanitizer can cover.
//
// A terminal line is LITERAL MONOSPACE TEXT. Re-interpreting it -- markdown, annotated
// strings, autolink/linkify -- lets the session's own output author a tappable link, a
// heading, or hidden text in the phone's chrome, out of characters that are individually
// innocent and that `SnapText` has no reason to strip. `ui/kit/Markdown.kt` exists and is one
// import away.
func TestR8Gate_TheFallbackBodyNeverRoutesASnapshotLineThroughMarkdown(t *testing.T) {
	sources := s24ProductionKotlin(t)
	banned := []string{
		"Markdown", "markdown", "buildAnnotatedString", "AnnotatedString",
		"LinkAnnotation", "Linkify", "autoLink", "ClickableText", "toSpanned", "HtmlCompat",
	}
	for _, name := range []string{r8FallbackScreen, r8FallbackView} {
		src, ok := sources[name]
		if !ok {
			t.Errorf("ADR-017 T1/T4: %s does not exist, so R8's read half has no surface", name)
			continue
		}
		code := kotlinCodeOnly(src)
		for _, b := range banned {
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(b) + `\b`).MatchString(code) {
				t.Errorf("ADR-017 T4-c: %s names `%s`. A snapshot line must never be re-interpreted: the "+
					"session's own output would then author links, emphasis and hidden text in the phone's "+
					"chrome, out of characters the machine-side sanitizer has no reason to strip.", name, b)
			}
		}
	}
}

// TestR8Gate_TheFallbackBodyForcesLTRParagraphDirection is amendment T4-c's second addition,
// and it is the half the machine CANNOT supply.
//
// Implicit bidi reorders a line whenever it contains strongly-RTL characters -- no control
// character is involved, so no strip catches it, and ADR-017:44's stated A7 property ("no
// Unicode bidi rune can visually spoof what is displayed") is FALSE without a layout
// attribute on the phone. A forced LTR paragraph direction is a layout attribute, not
// terminal emulation, and crosses no boundary this ADR draws.
func TestR8Gate_TheFallbackBodyForcesLTRParagraphDirection(t *testing.T) {
	sources := s24ProductionKotlin(t)
	src, ok := sources[r8FallbackView]
	if !ok {
		t.Fatalf("ADR-017 T4-c: %s does not exist", r8FallbackView)
	}
	code := kotlinCodeOnly(src)
	ltr := regexp.MustCompile(`LayoutDirection\.Ltr|TextDirection\.Ltr|LocalLayoutDirection\s+provides\s+LayoutDirection\.Ltr`)
	if !ltr.MatchString(code) {
		t.Errorf("ADR-017 T4-c: %s does not force an LTR paragraph direction on the snapshot rows. "+
			"Strongly-RTL characters reorder a line implicitly, with no control character present, so "+
			"the machine-side sanitizer cannot prevent it and the ADR's stated A7 property is false "+
			"without this one layout attribute.", r8FallbackView)
	}
}

// TestR8Gate_TheFallbackHeaderIsHonestAboutWhyThereIsNoChat is playbook:280 -- "an honest
// header naming provider, detected version, and the missing capability that cost it
// structured chat" -- and ADR-017's own consequence: "the routing table has to be legible in
// the UI or the honesty is lost".
func TestR8Gate_TheFallbackHeaderIsHonestAboutWhyThereIsNoChat(t *testing.T) {
	sources := s24ProductionKotlin(t)
	src, ok := sources[r8FallbackScreen]
	if !ok {
		t.Fatalf("ADR-017 T1: %s does not exist", r8FallbackScreen)
	}
	code := kotlinCodeOnly(src)
	for _, field := range []string{"provider", "providerVersion", "missingCapability"} {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(field) + `\b`).MatchString(code) {
			t.Errorf("playbook:280 / ADR-017 T1: %s never reads `%s`. Without all three the screen says "+
				"\"this is a terminal\" and not \"this is a terminal BECAUSE this build of this provider "+
				"does not do X\" -- and the second sentence is the whole of what makes three destinations "+
				"honest rather than arbitrary.", r8FallbackScreen, field)
		}
	}
	if !regexp.MustCompile(`\b(staleness|snapshotAge|lastRenderedAt|renderedAt)\b`).MatchString(code) {
		t.Errorf("ADR-017 T4-b: %s renders no staleness indicator derived from the snapshot's own age. "+
			"Without it a machine that went quiet is drawn identically to a terminal that is idle, which "+
			"is the state a user is most likely to type into.", r8FallbackScreen)
	}
	if !regexp.MustCompile(`(?i)interleav|同时|simultaneous`).MatchString(src) {
		t.Errorf("ADR-017 T8 / playbook:286-287: %s carries no interleaving warning. Decision G keeps the "+
			"owner typing throughout, ADR-013's co-presence finding proves both streams stay live, and "+
			"the UX must warn that simultaneous typing can interleave -- it must NOT 'fix' this by "+
			"evicting the terminal.", r8FallbackScreen)
	}
}

// TestR8Gate_TheControlBannerIsPersistentAndInView is T6's middle property, which the ADR
// names as "the easiest to lose".
//
// "A sheet that grants control and then disappears is explicit and not persistent, and it
// leaves a user typing into a live generation they have to REMEMBER they opened." So for the
// whole life of a generation the screen continuously displays that control is live, its
// REMAINING HORIZON, and a RELEASE CONTROL in the same view -- no drawer, no menu, no second
// navigation step.
func TestR8Gate_TheControlBannerIsPersistentAndInView(t *testing.T) {
	sources := s24ProductionKotlin(t)
	src, ok := sources[r8FallbackScreen]
	if !ok {
		t.Fatalf("ADR-017 T6: %s does not exist", r8FallbackScreen)
	}
	code := kotlinCodeOnly(src)
	for _, want := range []struct{ sym, why string }{
		{"controlBanner", "the persistent banner itself: a confirmation dialog alone does not satisfy T6"},
		{"remainingHorizon", "the live remaining horizon, so the user never has to remember when it ends"},
		{"releaseControl", "an in-view Release, not a drawer entry and not a second navigation step"},
	} {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(want.sym) + `\b`).MatchString(code) {
			t.Errorf("ADR-017 T6: %s declares no `%s` (%s). All three properties bind INDEPENDENTLY; the "+
				"one that is easiest to lose is persistence, and losing it leaves a user typing into a "+
				"live generation with nothing on screen saying so.", r8FallbackScreen, want.sym, want.why)
		}
	}
	if regexp.MustCompile(`ModalBottomSheet|BottomSheetScaffold|AlertDialog`).MatchString(code) &&
		!regexp.MustCompile(`\bcontrolBanner\b`).MatchString(code) {
		t.Errorf("ADR-017 T6: %s grants control from a sheet or dialog with no persistent banner. The "+
			"ADR rules that shape out by name: a sheet that grants control and then disappears is "+
			"explicit and not persistent.", r8FallbackScreen)
	}
}

// TestR8Gate_OnlyTheLiveForegroundScreenEmitsInputOrKeepalive is amendment T6-c.
//
// T6 already makes "only the active fallback screen may send raw input" a routing rule in its
// own right. T6-c binds the KEEPALIVE identically, and the reason is that the rule is
// otherwise unenforceable: a background coroutine, a scheduled job or a service-hosted timer
// can hold a generation open for the full horizon with NO screen displaying it, which defeats
// the persistent banner and the leave-screen trigger together.
func TestR8Gate_OnlyTheLiveForegroundScreenEmitsInputOrKeepalive(t *testing.T) {
	emitters := []string{
		`\.\s*terminalInput\s*\(`,
		`\.\s*terminalControlKeepalive\s*\(`,
	}
	var allowed int
	for name, src := range r8AllProductionKotlin(t) {
		code := kotlinCodeOnly(src)
		for _, pat := range emitters {
			if !regexp.MustCompile(pat).MatchString(code) {
				continue
			}
			if name == r8FallbackScreen {
				allowed++
				continue
			}
			t.Errorf("ADR-017 T6/T6-c: %s emits %s. The app has no place to hold a byte on the way to "+
				"terminal_input -- not a refused one, none -- and a keepalive emitted from anywhere but "+
				"the live foreground composition keeps a generation alive with no screen displaying it.",
				name, pat)
		}
	}
	if allowed == 0 {
		t.Errorf("ADR-017 T6: no production Kotlin file emits terminal_input or the keepalive, so R8b's " +
			"control half is not wired. This is the correct state at R8a's exit and the failing state at " +
			"R8b's.")
	}
	// The keepalive must not be scheduled off a background primitive anywhere in the app.
	for name, src := range r8AllProductionKotlin(t) {
		code := kotlinCodeOnly(src)
		if !regexp.MustCompile(`\bterminalControlKeepalive\b`).MatchString(code) {
			continue
		}
		for _, bg := range []string{"WorkManager", "PeriodicWorkRequest", "ForegroundService", "AlarmManager", "JobScheduler"} {
			if strings.Contains(code, bg) {
				t.Errorf("ADR-017 T6-c: %s schedules the control keepalive through %s. The daemon's idle "+
					"expiry is what holds when the app does not; the app's own contract is that the "+
					"keepalive comes from the live foreground composition and from nowhere else.", name, bg)
			}
		}
	}
}

// TestR8Gate_ComposerAvailabilityIsReadFromTheCapabilityRecord is T2 rule 3 on the phone.
//
// `SessionDetailPanel.kt:770-773` derives `structuredChat = !transcript.structureTorn`, and
// its own comment discloses why: "there is no capability read on this facade (Wave R6's
// disclosed residual -- the daemon authors session capability records that nothing
// publishes)". S2 publishes them, so the disclosure's blocker is removed and the inference
// must go with it -- a torn transcript and a provider that never had structured chat are
// different states with different explanations, and only the record can tell them apart.
func TestR8Gate_ComposerAvailabilityIsReadFromTheCapabilityRecord(t *testing.T) {
	sources := s24ProductionKotlin(t)
	src, ok := sources[r8DetailPanel]
	if !ok {
		t.Fatalf("%s does not exist", r8DetailPanel)
	}
	code := kotlinCodeOnly(src)
	if regexp.MustCompile(`structuredChat\s*=\s*!\s*transcript\.structureTorn`).MatchString(code) {
		t.Errorf("ADR-017 T2 rule 3: %s still derives composer availability from `transcript."+
			"structureTorn`. The phone renders from the capability record and infers nothing; deriving "+
			"support from the shape of the transcript is the exact inference the rule names.", r8DetailPanel)
	}
	if !regexp.MustCompile(`\b(capabilities|structuredChatCapability|capabilityRecord)\b`).MatchString(code) {
		t.Errorf("ADR-017 T2 rule 3: %s reads no capability record at all", r8DetailPanel)
	}
}

// TestR8Gate_TheNetAssertionCountRose is the gate note's own arithmetic, made checkable.
//
// "The net assertion count must go up. If it goes down, the wave failed this rule." The
// counted quantity is the number of banned patterns the two watch/well fences enforce: R8
// widens three legacy spellings into five shapes AND keeps the five retired peek symbols, so
// the count rises rather than being traded.
func TestR8Gate_TheNetAssertionCountRose(t *testing.T) {
	const preR8 = 3 // len(i1WatchCalls) as ADR-009 left it
	if got := len(r8WatchShapedVerbs); got <= preR8 {
		t.Errorf("ADR-017 gate note: the watch ban now enforces %d patterns, down from or equal to the %d "+
			"ADR-009 left. Widening a ban must add assertions, never trade them: a rename must be caught "+
			"by the same rule that caught the original spelling.", got, preR8)
	}
	if len(i1WellSymbols) < 5 {
		t.Errorf("ADR-017 Notes: `i1WellSymbols` shrank to %d entries. The retired peek symbols stay "+
			"banned by name forever; the fallback is a new screen under a capability record, not a "+
			"restoration.", len(i1WellSymbols))
	}
	if len(r8AllProductionKotlin(t)) <= len(s24ProductionKotlin(t)) {
		t.Errorf("ADR-017 gate note: the R8 bans no longer scan more files than the exempting walker. " +
			"A ban with an exempt directory is a ban with a documented bypass, and the kit is where a " +
			"raw grid would hide.")
	}
	if len(i1WatchCalls) < preR8 {
		t.Errorf("`i1WatchCalls` shrank to %d entries; the legacy spellings stay banned alongside the "+
			"widened shapes", len(i1WatchCalls))
	}
}

// ---------------------------------------------------------------------------
// ROUND 2 -- the three phone-side findings the first pass left open.
// ---------------------------------------------------------------------------

// TestR8Gate_TheWatchIsOpenedOnceClosedAndRenewed is round-2 MAJOR 5.
//
// `watch()` was the ONLY binding verb the app ever called, and it sat directly inside the
// fallback drawer -- which `drawContent` re-enters on every state change with no redraw
// guard -- so every journal event, every gated action and every resume emitted ANOTHER sealed
// unsigned mailbox append asking for a watch. `unwatch()` and `renew()` had no call site at
// all. The net was a watch that was opened repeatedly, never closed when the user left the
// screen, and never renewed, which is the exact cost T4-b's horizon exists to bound.
func TestR8Gate_TheWatchIsOpenedOnceClosedAndRenewed(t *testing.T) {
	const surfaceFile = "dev/swarm/phone/PhoneSurface.kt"
	const laneFile = "dev/swarm/phone/TerminalWatchLane.kt"
	src, ok := r8AllProductionKotlin(t)[surfaceFile]
	if !ok {
		t.Fatalf("%s does not exist", surfaceFile)
	}
	code := kotlinCodeOnly(src)
	// The verbs moved onto VerbDispatch's command lane with agents-tracker-jx1x: the surface
	// still owns the reconciliation (and the renew), and TerminalWatchLane carries the open
	// and the close for it -- inline they were five-second awaitConn stalls on the main
	// thread. The LEASE is therefore asserted over the pair; the drawer rules below stay on
	// the surface alone, where the drawer lives.
	laneSrc, ok := r8AllProductionKotlin(t)[laneFile]
	if !ok {
		t.Fatalf("%s does not exist, so the watch verbs have no lane to ride "+
			"(agents-tracker-jx1x)", laneFile)
	}
	lease := code + "\n" + kotlinCodeOnly(laneSrc)
	// The DOTTED shape, not the bare name: the lane declares `TerminalWatchHandle`, and its
	// `fun unwatch()` would satisfy a bare-name scan with a declaration -- measured when this
	// test stayed green over a lane whose unwatch call had been mutated away.
	for _, want := range []struct{ verb, why string }{
		{".unwatch()", "without it the machine renders, seals and appends for a screen the user has left"},
		{".renew()", "the horizon's only evidence that anyone is still looking"},
		{".watch()", "the open half, which must still exist"},
	} {
		if !strings.Contains(lease, want.verb) {
			t.Errorf("ADR-017 T4-b: neither %s nor %s calls `%s` -- %s. A watch is a LEASE: open, "+
				"renew while the screen is up, close when it is not. Round 1 wired only the open.",
				surfaceFile, laneFile, want.verb, want.why)
		}
	}
	// THE OPEN MUST NOT SIT IN THE DRAWER. A redraw-rate `watch()` is one sealed append per
	// state change, against a budget shared with every session's transcript.
	drawer := kotlinMember(t, code, "private fun drawTerminalFallback(")
	if strings.Contains(drawer, ".watch()") {
		t.Errorf("ADR-017 T4-b: drawTerminalFallback in %s issues the watch itself. That drawer runs "+
			"on every journal event and every resume, so the watch is re-asked at output rate. The "+
			"watch belongs to a reconciliation between what is on the glass and what is held, not "+
			"to a draw.", surfaceFile)
	}
	// AND THE DRAWER MUST HAVE A REDRAW GUARD, for drawDetail's reason one screen over: without
	// one the whole view hierarchy is rebuilt at output rate under whoever is reading it.
	if !regexp.MustCompile(`if\s*\(.*fallbackDrawn.*\)\s*return`).MatchString(drawer) {
		t.Errorf("PB-DS-9 / agents-tracker-ksvb.3: drawTerminalFallback in %s has no redraw guard. "+
			"`drawDetail` states the argument: a rebuild at output rate re-creates every view the "+
			"user is reading and throws the grid back to the top.", surfaceFile)
	}
}

// TestR8Gate_TheGridReadIsGatedOnACapabilityRead is round-2 MODERATE 11.
//
// ADR-017's gate note requires that "the fallback render path is unreachable without a
// capability read". `FacadeBridge.terminalRows` called `app.peek(sessionId)` directly, and
// `App.Peek` reads the phone's snapshot cache for ANY session id with no record check of any
// kind -- so the render path was reachable from a file that is neither the fallback screen
// nor the fallback view and that read no record. The daemon gate keeps that cache empty for a
// structured session, so it was one-layer defence rather than a live leak; the property the
// ADR states is that the read is gated HERE.
func TestR8Gate_TheGridReadIsGatedOnACapabilityRead(t *testing.T) {
	peek := regexp.MustCompile(`\.\s*peek\s*\(`)
	var readers []string
	for name, src := range r8AllProductionKotlin(t) {
		if peek.MatchString(kotlinCodeOnly(src)) {
			readers = append(readers, name)
		}
	}
	sort.Strings(readers)
	if len(readers) == 0 {
		t.Fatalf("no production Kotlin reads the machine-sanitized grid at all, so the fallback " +
			"screen renders nothing")
	}
	for _, name := range readers {
		if name != r8FallbackScreen {
			t.Errorf("ADR-017 gate note: %s reads the terminal snapshot cache. `App.Peek` answers for "+
				"ANY session id with no record check, so a reader outside the one allowlisted file is "+
				"a render path reachable without a capability read.", name)
		}
	}
	// AND THE ALLOWLISTED READER MUST ACTUALLY READ THE RECORD, in the same member: naming the
	// file is not the property, gating the read is.
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[r8FallbackScreen])
	grid := kotlinMember(t, code, "fun grid()")
	if !strings.Contains(grid, "TerminalFallbackModel.from(") {
		t.Errorf("ADR-017 gate note: the grid read in %s does not consult the capability record "+
			"before it peeks. `TerminalFallbackModel.from` is the routing rule -- it answers null "+
			"for every session the MACHINE did not route here -- and a peek that runs first has "+
			"already read a screen it may not show.", r8FallbackScreen)
	}
	if !strings.Contains(grid, ".peek(") {
		t.Errorf("the extracted grid member does not peek at all; the assertion above is vacuous")
	}
}

// TestR8Gate_TheStalenessSignalIsCarriedRatherThanDiscarded is round-2 MODERATE 9.
//
// `App.Peek` returns `Stale: a.streamStale("terminal")` and the adapter discarded it while
// hardcoding `ageMs = 0L` -- and `staleness(0)` returns the empty string, which the view
// treats as FRESH. So the screen asserted freshness about an arbitrarily old terminal, which
// is T4-b's named lie: "the machine went quiet" must never be rendered as "the terminal is
// idle".
func TestR8Gate_TheStalenessSignalIsCarriedRatherThanDiscarded(t *testing.T) {
	sources := r8AllProductionKotlin(t)
	screen := kotlinCodeOnly(sources[r8FallbackScreen])
	grid := kotlinMember(t, screen, "fun grid()")
	if !regexp.MustCompile(`\bsnap\.stale\b`).MatchString(grid) {
		t.Errorf("ADR-017 T4-b: the grid read in %s discards `Snapshot.stale`. The phone core's own "+
			"verdict on the machine's terminal stream is the live half of the staleness signal, and "+
			"throwing it away leaves the screen asserting freshness about a terminal it has not "+
			"heard from.", r8FallbackScreen)
	}
	view := kotlinCodeOnly(sources[r8FallbackView])
	if !strings.Contains(view, "streamStale") {
		t.Errorf("ADR-017 T4-b: %s never renders the stream-staleness verdict", r8FallbackView)
	}
	if !strings.Contains(screen, "STREAM_STALE") {
		t.Errorf("ADR-017 T4-b: %s declares no sentence for a machine that went quiet", r8FallbackScreen)
	}
}

// kotlinMember returns one Kotlin member's source, from its declaration to the first line
// that closes it at member indentation. It exists so a fence can be stated over a FUNCTION
// rather than over a file: a file-wide grep for a symbol is satisfied by that symbol's own
// declaration, which is the defect class this wave's rule 4 names.
func kotlinMember(t *testing.T, code, decl string) string {
	t.Helper()
	i := strings.Index(code, decl)
	if i < 0 {
		t.Fatalf("the Kotlin source declares no %s", decl)
	}
	rest := code[i:]
	j := strings.Index(rest, "\n    }")
	if j < 0 {
		t.Fatalf("could not find the end of %s", decl)
	}
	return rest[:j]
}
