package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-APP-6 (the launch screen) and PB-INPUT-2 (the lease is
// visibly confirmed before anything can be typed).
//
// WHY THESE TWO ARE IN ONE FILE. They are the same defect wearing two faces, and ADR-007 B80 and
// B83(3) found them in the same round: a screen MODEL that decides the right thing, tested green,
// with the shipped surface reaching a different answer. PB-APP-6 has `ui/MachineAndLaunch.kt`'s
// LaunchScreen and no launch form. PB-INPUT-2 has `ui/SessionScreens.kt`'s lease model --
// `keyboardEnabled = leaseHeld && online`, `showsTakeControl`, `showsRelease` -- and
// PhoneSurface.kt passes `leaseHeld = false` as a LITERAL, so the verdict is computed from a
// constant and then not read at all.
//
// WHY THE ASSERTIONS ARE HERE AND NOT IN KOTLIN, which is a limit rather than a preference and is
// the single most important thing to know before extending this file. **No JVM test can reach the
// surface's `PhoneStartup.Ready` path.** The phone core is a gomobile AAR carrying .so files
// cross-compiled for arm64-v8a and x86_64 ANDROID, so `Swarmmobile.newApp` cannot load under
// Robolectric; `PhoneRuntime.phone()` answers Unavailable on every JVM run, and `PhoneRuntime` is
// a final Kotlin class whose `phone()` is not open, so no fake can stand in for it. Everything
// PB-INPUT-2 is about -- a session on screen, a lease taken, a keyboard that must stay shut until
// the machine confirms one -- lives past that branch. A Robolectric assertion about it would pass
// VACUOUSLY, because with no phone the surface disables every control anyway, and a vacuous pass
// over the exact requirement B83(3) found unmet is worse than no test.
//
// So the hosted half of PB-APP-6 is asserted where it can be
// (android/app/src/test/.../PhoneLaunchSurfaceTest.kt walks the real window), and the facts that
// live past the native boundary are asserted HERE, against checked-in source, the way
// s19_pbe2e2_test.go already asserts that the app can perform the actions the smoke drives.
//
// IT READS CHECKED-IN SOURCE ONLY: no Android SDK, no JDK, no Gradle, no AAR, no emulator, no
// handset. Nothing here claims anything about PB-E2E-5's deferred set -- no camera, no biometric,
// no FCM delivery, no Doze, no hardware attestation.

import (
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// PB-APP-6 -- the launch screen, and the two artifacts that disagreed about it.
// ---------------------------------------------------------------------------

// TestPBAPP6_TheAppCanStartASessionAndTheLedgerAgrees is ADR-007 B80's finding as a check.
//
// B80: `App.Launch` has zero Kotlin callers; android/unbound-verbs.tsv records that honestly
// ("MachineAndLaunch is the model, and the surface has no machine pane, no launch form and no
// session picker"); and the traceability table said PB-APP-6 was SHIPPED. Its acceptance criterion
// is "UI + facade test", so a requirement whose acceptance names a UI cannot be met while the
// ledger records that no UI exists. **The real defect is that two artifacts can both be honest and
// still contradict each other, and B80's residual 4.20 is that nothing in this project checks
// artifact against artifact.** This is that check, for the one row where the disagreement was
// load-bearing.
//
// IT BEARS ON THE BINDING EXIT CRITERION, which is why it is a gate and not a note: section 1
// requires a demonstration that a phone "pairs, observes, LAUNCHES, and types into a real
// session". Note that PB-E2E-2's own verb list (s19_pbe2e2_test.go's pbe2e2Verbs) does not contain
// launch at all -- so the smoke could pass in full while section 1's fourth clause has no subject.
func TestPBAPP6_TheAppCanStartASessionAndTheLedgerAgrees(t *testing.T) {
	kotlin := kotlinCodeOnly(appKotlinSource(t))
	ledger := ledgerIndex(readUnboundLedger(t))

	if !callsBoundVerb(kotlin, "Launch") {
		t.Errorf("PB-APP-6: no production Kotlin calls swarmmobile.App.Launch, so there is no " +
			"path from a phone screen to a launch.\n" +
			"`ui/MachineAndLaunch.kt` ships LaunchScreen -- the model that decides what a launch " +
			"screen shows -- with unit tests over its policy, and no launch screen. Testing the " +
			"model harder cannot meet an acceptance criterion that reads \"UI + facade test\", " +
			"and section 1's binding exit criterion is a phone that \"pairs, observes, LAUNCHES, " +
			"and types into a real session\".\n" +
			"The check looks for `.Launch(` or `.launch(` in production Kotlin with comments " +
			"stripped; the hosted half is PhoneLaunchSurfaceTest.")
	}

	if row, excused := ledger[launchLedgerSymbol]; excused {
		t.Errorf("%s:%d still excuses %s, and PB-APP-6 cannot be met while it does.\n"+
			"The row says: %q\n"+
			"That row is an ADMISSION that the launch screen does not exist. ADR-007 B80 is the "+
			"record of the ledger being right and the traceability table saying SHIPPED anyway, "+
			"with nothing anywhere joining the two. Wiring the verb without deleting this row "+
			"leaves the same contradiction pointing the other way -- a screen that exists and a "+
			"ledger that says it does not -- and the rot check cannot see it, because a row for a "+
			"symbol the sources still DECLARE is not stale.",
			mustRel(t, unboundLedgerPath(t)), row.Line, row.Symbol, row.Reason)
	}
}

// launchLedgerSymbol is the row whose presence and PB-APP-6's satisfaction are mutually
// exclusive. Named once so the two directions above cannot drift apart.
const launchLedgerSymbol = "App.Launch"

// TestPBAPP6_TheLedgerCannotExcuseAVerbTheAppAlreadyCalls is the general form, and it is the
// direction of ledger rot nobody was checking.
//
// staleLedgerRows catches a row naming a symbol the sources no longer DECLARE. It cannot catch the
// opposite: a row that goes on excusing a verb after somebody wired it. That row then reads as a
// considered decision about a gap that has been closed -- which is precisely how a file whose
// header says "THIS IS NOT A LIST OF THINGS THAT DO NOT MATTER" decays into one.
//
// MEASURED, not assumed. Wiring `app.launch(` into PhoneSurface with the `App.Launch` row left in
// place leaves `go test ./android/gate/...` entirely green today.
//
// SCOPE: `App.` rows only. The FacadeBridge dimension asks a reachability question rather than a
// call question (liveBridgeMethods), and the handle receivers collide with App by name, so both
// would need their own walk here; the requirement-bearing verbs are all on App.
func TestPBAPP6_TheLedgerCannotExcuseAVerbTheAppAlreadyCalls(t *testing.T) {
	kotlin := kotlinCodeOnly(appKotlinSource(t))
	rows := readUnboundLedger(t)

	for _, r := range redundantLedgerRows(kotlin, rows) {
		t.Errorf("%s:%d excuses %s as deliberately unbound, and production Kotlin calls it.\n"+
			"The row says: %q\n"+
			"Every App row in that file is traced to a screen element in "+
			"mobile/screen_coverage.tsv and records the screen that does not exist YET. Once the "+
			"screen lands the row is no longer a decision, it is a false statement about the "+
			"shipped app -- and a reader cannot tell it from the rows that are still true. Delete "+
			"it.", mustRel(t, unboundLedgerPath(t)), r.Line, r.Symbol, r.Reason)
	}
}

// redundantLedgerRows are `App.` rows whose verb production Kotlin already calls.
func redundantLedgerRows(kotlin string, rows []ledgerRow) []ledgerRow {
	var out []ledgerRow
	for _, r := range rows {
		verb, ok := strings.CutPrefix(r.Symbol, "App.")
		if !ok {
			continue
		}
		if callsBoundVerb(kotlin, verb) {
			out = append(out, r)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// PB-INPUT-2 -- "input is refused until the machine confirms a lease, and the confirmation is
// VISIBLE".
// TestPBINPUT2_TheLeaseIsAFactAndNotALiteral IS DELETED (owner ruling R1, 2026-08-26).
//
// It fenced ADR-007 B83(3): `FacadeBridge.sessionLease` must be passed the OUTCOME of this
// screen's own take_control, never a literal `false` or `true`. Both literals were rejected,
// and the second mattered more -- `true` tells every user they hold control of every session,
// so the keyboard opens over a machine that will drop the frames silently.
//
// THE PARAMETER IT FENCED NO LONGER EXISTS. `SessionLease` carries the link and nothing else;
// there is no lease to state as a fact or fake as a literal, because composer_send takes none.
// A fence over a deleted parameter is not a weaker fence, it is a fence with no subject -- and
// leaving it green over an empty scan is exactly how this module has grown checks that pass
// without being right.
//
// What replaced its purpose is one file over: android/gate/r1_takecontrolgone_test.go asserts
// the control, the model properties and the copy are all gone, and the amended
// TestPBINPUT2_TheKeyboardVerdictAndTheLeaseStateAreReadFromTheModel below asserts that the
// requirement's own intent -- can I tell whether what I type will arrive -- is still read from
// a model by a real screen.

// TestPBINPUT2_TheKeyboardVerdictAndTheLeaseStateAreReadFromTheModel is the standing "called by
// nothing" class, on a screen model rather than on a facade verb -- which is why no existing
// control sees it.
//
// android/unbound-verbs.tsv covers bound facade verbs and FacadeBridge methods. `SessionLease` is
// neither: it is a Kotlin data class, so the ledger has no row for it and the boundverbledger walk
// never asks. And its three lease properties -- `keyboardEnabled`, `showsTakeControl`,
// `showsRelease` -- are read by NO production Kotlin at all, only by SessionScreensTest. That is
// the seventh instance of the class this phase has now found, and it is PB-INPUT-2 exactly: the
// requirement's whole content is decided, unit-tested, and consulted by nothing.
//
// WHY THE MODEL AND NOT ANY LEASE-SHAPED CODE, since this is the most opinionated line in the
// file. `keyboardEnabled` is `leaseHeld && online`, and the `&& online` half is a separate clause
// -- "a lease cannot be live while the link is down either, so both conditions are required". An
// implementation that enables the keyboard from its own lease flag satisfies PB-INPUT-2's first
// clause and drops the second, silently, while the model that states it stays green and unread.
// This module already refuses second copies of a decided policy everywhere it makes one
// (ConnectivityPolicy, ContentUnlockPolicy, GateFreshness); this is the same rule.
//
// THE PAIR IS AN `OR` ON PURPOSE. "Visibly confirmed" needs the screen to say WHICH state the user
// is in, and `showsTakeControl`/`showsRelease` are the two sides of one fact -- rendering either
// one distinguishes them. Requiring both would pin a presentation.
func TestPBINPUT2_TheKeyboardVerdictAndTheLeaseStateAreReadFromTheModel(t *testing.T) {
	kotlin := kotlinCodeOnly(appKotlinSource(t))

	if !readsModelProperty(kotlin, "keyboardEnabled") {
		t.Errorf("PB-INPUT-2: no production Kotlin reads SessionLease.keyboardEnabled.\n" +
			"Its own doc says why it exists: \"Ungated, the user types happily at a machine that " +
			"granted them nothing and the gateway drops every frame silently: a live keyboard " +
			"over a dead terminal.\" The surface instead enables Send and the text field from " +
			"whether the triage inbox yielded a row -- so the keyboard opens for ANY session, " +
			"held or not, and the verdict that says otherwise is computed on every draw and read " +
			"by nobody.\n" +
			"The check looks for `.keyboardEnabled` in production Kotlin with comments stripped. " +
			"The DECLARATION in ui/SessionScreens.kt does not satisfy it, and must not: a model " +
			"reachable only from a test is one the real screen may quietly disagree with.")
	}

	// AMENDED 2026-08-26 (owner ruling R1). This asked for `showsTakeControl` or
	// `showsRelease` -- the two sides of "which control to draw" -- and both are deleted with
	// the control they chose between. The requirement's INTENT is unchanged and is what is
	// fenced here instead: "visibly confirmed" exists so a reader can tell whether what they
	// type will reach their machine, and the composer's own shut state answers that from the
	// facts that actually decide it, naming which of four reasons it cannot send.
	if !readsModelProperty(kotlin, "composerShut") && !readsModelProperty(kotlin, "composerAvailability") {
		t.Errorf("PB-INPUT-2: no production Kotlin reads the composer's shut state, so nothing " +
			"on screen distinguishes a session this phone can send to from one it cannot.\n" +
			"\"Visibly confirmed\" is the requirement's own word. It used to be carried by the " +
			"lease controls; R1 deleted those because composer_send takes no lease at any " +
			"layer, and the composer's availability took the job over -- a stronger answer, " +
			"because a held lease never implied a session could receive a message and the four " +
			"shut reasons do.\n" +
			"Either property satisfies this. Which sentence carries it is not fenced here.")
	}
}

// ---------------------------------------------------------------------------
// The decisions, as pure functions, so the assertions above can be driven against SYNTHETIC
// input. Against the production tree a check that understands nothing and a codebase that does
// nothing wrong are indistinguishable -- boundverbledger_test.go's own rule, and the reason this
// package survived five rounds of finding its checks measured nothing.
// ---------------------------------------------------------------------------

// THE LEASE SCANNER IS DELETED (owner ruling R1, 2026-08-26).
//
// Eight symbols lived here -- leaseCallSiteFloor, sessionLeaseCall, leaseArgsOf,
// balancedArgList, namedLeaseArg, leaseArgumentOf, splitTopLevel and isBooleanLiteral -- and
// between them they read the `leaseHeld` argument out of every `FacadeBridge.sessionLease`
// call site in production Kotlin, across line breaks and named or positional, so a literal
// could be told from a fact. That was ADR-007 B83(3)'s fence and it was a good one.
//
// It has no subject now: `SessionLease` carries the link and nothing else, because
// composer_send takes no lease at any layer. Kept, the scanner would find zero call sites and
// report green -- a check satisfied by an empty scan, which is the shape this module has been
// burned by before.

// modelPropertyRead matches a READ of a property on some receiver. The dot is what separates a
// read from the declaration, and \b is what stops `keyboardEnabled` matching a longer name.
func modelPropertyRead(name string) *regexp.Regexp {
	return regexp.MustCompile(`\.\s*` + regexp.QuoteMeta(name) + `\b`)
}

func readsModelProperty(src, name string) bool { return modelPropertyRead(name).MatchString(src) }

// TestPBAPP6PBINPUT2_TheDecisionsRejectTheWrongImplementations is the mutation battery.
//
// Every assertion above is of the form "nothing was found wrong", and the production tree is (by
// the time this lands) meant to be clean -- so those tests passing proves nothing about whether
// the checks work. Each case below is an implementation that COMPILES and is wrong.
func TestPBAPP6PBINPUT2_TheDecisionsRejectTheWrongImplementations(t *testing.T) {
	// THE LEASE-ARGUMENT NEGATIVE CONTROLS ARE DELETED WITH THE FENCE THEY PROVED (R1).
	// They exercised leaseArgsOf and isBooleanLiteral against a table of call sites -- a
	// literal `false`, a literal `true`, a real property read, arguments split across lines,
	// and FacadeBridge's own declaration, which must not read as a call. Every one of them
	// described a parameter that no longer exists.

	t.Run("a model property read", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			src  string
			want bool
		}{
			{"a read on the lease model", "send.isEnabled = view.keyboardEnabled", true},
			{"a safe call", "send.isEnabled = view?.keyboardEnabled == true", true},
			{"the dot on the previous line", "send.isEnabled = view\n    .keyboardEnabled", true},
			{
				"the DECLARATION does not satisfy it",
				"val keyboardEnabled: Boolean get() = leaseHeld && online",
				false,
			},
			{
				"nor a local of the same name, which is how a model gets shadowed",
				"val keyboardEnabled = session.isNotEmpty()",
				false,
			},
			{"a longer name that merely contains it", "view.keyboardEnabledAt(now)", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := readsModelProperty(tc.src, "keyboardEnabled"); got != tc.want {
					t.Errorf("readsModelProperty(%q) = %v, want %v", tc.src, got, tc.want)
				}
			})
		}

		// The reader one layer out: this file's own prose names the property it requires a read
		// of, so a check run over unstripped source would pass on its own documentation. That has
		// defeated a first draft in this package before (kotlinCodeOnly's own doc records it).
		commented := kotlinCodeOnly("// view.keyboardEnabled is PB-INPUT-2's verdict\nfun x() {}")
		if readsModelProperty(commented, "keyboardEnabled") {
			t.Errorf("a commented-out read satisfies the check.\nkotlinCodeOnly left: %q", commented)
		}
	})

	t.Run("a ledger row that outlived the gap it recorded", func(t *testing.T) {
		rows := []ledgerRow{
			{Symbol: "App.Launch", Reason: "the surface has no launch form", Line: 30},
			{Symbol: "App.Interrupt", Reason: "the surface has no Stop control", Line: 31},
			{Symbol: "FacadeBridge.journal", Reason: "the activity list has no screen", Line: 32},
		}
		const kotlin = `
class PhoneSurface {
    fun start() {
        app.launch(spec)
    }
}
`
		got := redundantLedgerRows(kotlin, rows)
		if len(got) != 1 || got[0].Symbol != "App.Launch" {
			t.Fatalf("the check reported %v, want exactly [App.Launch].\n"+
				"A row excusing a verb the app now calls is a false statement a reader cannot "+
				"tell from a true one, and staleLedgerRows cannot see it: the symbol is still "+
				"DECLARED, so the rot check is satisfied. Measured on the production tree -- "+
				"wiring app.launch( with the row left in place leaves every gate green", got)
		}

		if got := redundantLedgerRows("class PhoneSurface { fun render() {} }", rows); len(got) != 0 {
			t.Errorf("the check reported %v over a ledger whose verbs nothing calls; every row "+
				"there is then a true statement and the file is doing its job", got)
		}
	})
}
