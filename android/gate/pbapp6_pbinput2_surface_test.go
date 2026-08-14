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
// ---------------------------------------------------------------------------

// TestPBINPUT2_TheLeaseIsAFactAndNotALiteral is ADR-007 B83(3), verified from the source side.
//
// `PhoneSurface.renderReady` calls `bridge.sessionLease(session, leaseHeld = false)`. The comment
// beside it is honest -- "this surface never takes a lease on its own" -- and it is exactly the
// problem: the surface DOES have a Take control button, so the one screen that can acquire a lease
// renders every session as one it does not hold, always, and then enables Send from a different
// fact entirely (whether the roster yielded a row).
//
// BOTH LITERALS ARE REJECTED, and the second matters more than the first. `true` is the mutation a
// reader reaches for to make a lease assertion go green, and it is strictly worse than `false`: it
// tells every user they hold control of every session, so the keyboard opens over a machine that
// will drop the frames silently. What the parameter is FOR is stated on FacadeBridge.sessionLease
// itself -- "whether the machine has CONFIRMED a control lease ... reading it back from a snapshot
// would be guessing at a fact the reply already carries" -- and a literal is not a reply.
func TestPBINPUT2_TheLeaseIsAFactAndNotALiteral(t *testing.T) {
	args := leaseArgsOf(kotlinCodeOnly(appKotlinSource(t)))
	if len(args) < leaseCallSiteFloor {
		t.Fatalf("PB-INPUT-2: the scan found %d call(s) to FacadeBridge.sessionLease in "+
			"production Kotlin, want at least %d.\n"+
			"The surface does render the lease; a count this low means this file has stopped "+
			"reading the sources, and every assertion below would then pass over nothing -- which "+
			"is the defect class the whole android/gate package exists for, applied to itself.",
			len(args), leaseCallSiteFloor)
	}
	for _, arg := range args {
		if arg == "" {
			t.Errorf("PB-INPUT-2: a call to FacadeBridge.sessionLease was found whose `leaseHeld` " +
				"argument could not be read. The check reads the named form " +
				"(`leaseHeld = ...`) or the second positional argument; a call it cannot read is " +
				"a call it cannot fence, and it must not pass by default")
			continue
		}
		if isBooleanLiteral(arg) {
			t.Errorf("PB-INPUT-2: FacadeBridge.sessionLease is passed the LITERAL %s for "+
				"`leaseHeld`, so the lease is not a fact the screen holds -- it is a constant.\n"+
				"The requirement is that input is refused until the machine CONFIRMS a lease and "+
				"that the confirmation is visible. With a literal the surface renders the same "+
				"answer for a session the user has taken control of and one they have not, which "+
				"is `false` telling a user with control that they have none, or `true` opening a "+
				"keyboard over a machine that will silently drop every frame.\n"+
				"The lease is the outcome of this screen's own take_control operation "+
				"(PB-INPUT-3); pass what that reply said.", arg)
		}
	}
}

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

	if !readsModelProperty(kotlin, "showsTakeControl") && !readsModelProperty(kotlin, "showsRelease") {
		t.Errorf("PB-INPUT-2: no production Kotlin reads SessionLease.showsTakeControl or " +
			".showsRelease, so nothing on screen distinguishes a session the user HOLDS from one " +
			"they are merely watching.\n" +
			"\"Visibly confirmed\" is the requirement's own word. Today the surface shows the " +
			"same Take control button in both states and a keyboard that is live in both, which " +
			"is a user who cannot tell whether the next thing they type will reach their machine " +
			"until it does not.\n" +
			"Either property satisfies this -- they are two sides of one fact and rendering " +
			"either one distinguishes the states. Which control or wording carries it is not " +
			"fenced here.")
	}
}

// ---------------------------------------------------------------------------
// The decisions, as pure functions, so the assertions above can be driven against SYNTHETIC
// input. Against the production tree a check that understands nothing and a codebase that does
// nothing wrong are indistinguishable -- boundverbledger_test.go's own rule, and the reason this
// package survived five rounds of finding its checks measured nothing.
// ---------------------------------------------------------------------------

// leaseCallSiteFloor is the "cannot pass by measuring nothing" floor. One call site exists today.
const leaseCallSiteFloor = 1

// sessionLeaseCall matches a CALL to FacadeBridge.sessionLease. The dot is required, which is what
// keeps the DECLARATION in ui/FacadeBridge.kt (`fun sessionLease(sessionId: String, ...)`) from
// reading as a call -- the same strengthening boundVerbCall documents, for the same reason.
var sessionLeaseCall = regexp.MustCompile(`\.\s*sessionLease\s*\(`)

// leaseArgsOf is the expression passed for `leaseHeld` at every sessionLease call site.
//
// It reads the NAMED form first and falls back to the second positional argument, because both
// compile and a check that understood only one would be silenced by rewriting the call.
func leaseArgsOf(src string) []string {
	var out []string
	for offset := 0; ; {
		loc := sessionLeaseCall.FindStringIndex(src[offset:])
		if loc == nil {
			return out
		}
		open := offset + loc[1] - 1
		args, end, ok := balancedArgList(src, open)
		if !ok {
			return out
		}
		out = append(out, leaseArgumentOf(args))
		offset = end
	}
}

// balancedArgList returns the text between the parenthesis at open and its match, plus the offset
// just past the closer. Quoted strings are skipped so a parenthesis inside a literal cannot
// unbalance the walk.
func balancedArgList(src string, open int) (string, int, bool) {
	depth := 0
	inString := false
	for i := open; i < len(src); i++ {
		switch c := src[i]; {
		case inString:
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return src[open+1 : i], i + 1, true
			}
		}
	}
	return "", 0, false
}

var namedLeaseArg = regexp.MustCompile(`(?s)^\s*leaseHeld\s*=\s*(.*)$`)

// leaseArgumentOf picks the `leaseHeld` expression out of an argument list, or "" when it cannot
// be identified -- which the caller reports rather than passing over.
func leaseArgumentOf(args string) string {
	parts := splitTopLevel(args)
	for _, p := range parts {
		if m := namedLeaseArg.FindStringSubmatch(p); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	if len(parts) >= 2 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

// splitTopLevel splits an argument list on commas that are not nested inside brackets or strings.
func splitTopLevel(args string) []string {
	var out []string
	depth := 0
	inString := false
	start := 0
	for i := 0; i < len(args); i++ {
		switch c := args[i]; {
		case inString:
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			out = append(out, args[start:i])
			start = i + 1
		}
	}
	out = append(out, args[start:])
	return out
}

// isBooleanLiteral is true for the two expressions that make the lease a constant. Trailing
// commas and whitespace are trimmed; anything else -- a property, a call, a variable -- is a fact
// the screen holds, and this file takes no view on which.
func isBooleanLiteral(expr string) bool {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), ","))
	return trimmed == "true" || trimmed == "false"
}

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
	t.Run("the lease argument", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			src    string
			want   string
			reject bool
		}{
			{
				name:   "the shipped defect: a named false",
				src:    `val view = bridge.sessionLease(session, leaseHeld = false)`,
				want:   "false",
				reject: true,
			},
			{
				name:   "the tempting repair, which is worse: a named true",
				src:    `val view = bridge.sessionLease(session, leaseHeld = true)`,
				want:   "true",
				reject: true,
			},
			{
				name:   "the same literal moved to the positional form",
				src:    `val view = bridge.sessionLease(session, false)`,
				want:   "false",
				reject: true,
			},
			{
				name:   "a fact the screen holds is accepted, whatever it is called",
				src:    `val view = bridge.sessionLease(session, leaseHeld = lease.heldFor(session))`,
				want:   "lease.heldFor(session)",
				reject: false,
			},
			{
				name:   "and a plain property read",
				src:    `val view = bridge.sessionLease(session, leaseHeld = confirmedLease)`,
				want:   "confirmedLease",
				reject: false,
			},
			{
				name:   "arguments on separate lines, which is how this call is actually written",
				src:    "val view = bridge.sessionLease(\n    session,\n    leaseHeld = false,\n)",
				want:   "false",
				reject: true,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				args := leaseArgsOf(tc.src)
				if len(args) != 1 {
					t.Fatalf("the scan found %d lease argument(s) in %q, want exactly 1. A call "+
						"site it cannot see is a call site it cannot fence", len(args), tc.src)
				}
				if args[0] != tc.want {
					t.Fatalf("read %q as the lease argument of %q, want %q", args[0], tc.src, tc.want)
				}
				if got := isBooleanLiteral(args[0]); got != tc.reject {
					t.Errorf("isBooleanLiteral(%q) = %v, want %v.\nThis one answer decides "+
						"whether PB-INPUT-2's fence is a fence or a formality", args[0], got, tc.reject)
				}
			})
		}
	})

	// The declaration of the parameter is not a call, and neither is the doc comment that
	// explains it. Both live in ui/FacadeBridge.kt, so both are read by the production scan.
	t.Run("a declaration is not a call site", func(t *testing.T) {
		const decl = `
    fun sessionLease(sessionId: String, leaseHeld: Boolean): SessionLease =
        SessionLease(sessionId, leaseHeld = leaseHeld, online = isOnline())
`
		if got := leaseArgsOf(decl); len(got) != 0 {
			t.Errorf("the scan read %v out of FacadeBridge's own declaration. A check that "+
				"treats a declaration as a call site fences the wrong file, and would report the "+
				"adapter's `leaseHeld = leaseHeld` forwarding as the defect", got)
		}
	})

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
