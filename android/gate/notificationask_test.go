package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-0dij: "POST_NOTIFICATIONS is never requested:
// Settings toggles dead by default with no guided flow."
//
// WHAT HAPPENED. It is agents-tracker-qx9m's closed loop, on the other permission and one screen
// over. Three facts met:
//
//   - `PermissionStateResolver` answers `!hasAskedBefore -> DENIED`, and `SettingsSurface.read`
//     passed `hasAskedBefore = true` as a LITERAL, with a comment claiming "the permission is
//     requested on the notification path". No such path existed: the app's only
//     `requestPermissions` call was the camera's. So on API 33+ an ungranted phone resolved
//     PERMANENTLY_DENIED five seconds after install.
//   - `SettingsScreen.togglesDisabled` was true on DENIED as well, so both switches drew disabled.
//   - Nothing else in the app could ask.
//
// So: no permission, so no live switch; no live switch, so no tap; no tap, so nothing asks; so no
// permission, for the life of the install. Push is the sole background wake path (ADR-007 B16), so
// the failure is silent -- the owner reported it as "the toggles do nothing".
//
// WHY THIS IS A GO GATE AND NOT ONLY A KOTLIN TEST, in the words qx9m's own header uses: the Kotlin
// suite asks the real model which states leave a switch tappable and which sentence each state
// says, and that is the right test for the DECISION. What it cannot assert is the fact UNDERNEATH
// the decision -- that the control the panel leaves live is still wired to the platform ask.
// `PhoneRuntime.phone()` answers Unavailable on every JVM run (the phone core is a gomobile AAR of
// .so files cross-compiled for Android ABIs), so `SettingsSurface` never draws a panel in a unit
// test and no switch there can be tapped at all. The wire between the two files is what is left,
// and this is the only thing in the repository that can see it.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. Every scan starts at the app module, so it cannot descend
// into `.claude/worktrees/`, which holds other agents' full checkouts.

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Reading the chain: the platform ask, and the control it hangs off.
// ---------------------------------------------------------------------------

// notifyAskArgument is the POST_NOTIFICATIONS permission named in an argument list. Both spellings
// are accepted because both are real: the app passes `AppPermission.POST_NOTIFICATIONS.manifestName`
// and a hand-written call would say `Manifest.permission.POST_NOTIFICATIONS`.
var notifyAskArgument = regexp.MustCompile(`\bPOST_NOTIFICATIONS\b`)

// notifyCalledName is any identifier applied to an argument list -- a call, as against a mention.
var notifyCalledName = regexp.MustCompile(`(\bfun\s+)?\b([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// notifyHardCodedAsk is the defect's own line: the persisted "have we asked" bit replaced by a
// literal. It is matched over code with comments stripped, so the fix's note about it is not the
// thing that fails.
var notifyHardCodedAsk = regexp.MustCompile(`hasAskedBefore\s*=\s*(true|false)\b`)

// notifySource reads one production Kotlin file as REFERENCES: comments out (a fence a comment can
// satisfy is one the next thorough comment turns off, and this fix's own notes quote the defect
// verbatim), and string literals out (a switch's label talks about notifications and is not a wire).
func notifySource(t *testing.T, path string) string {
	t.Helper()
	return kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-0dij")))
}

// notifyAskFunctions names every function in one source that asks the PLATFORM for
// POST_NOTIFICATIONS. It keeps only `requestPermissions` call sites whose arguments name the
// permission, so the camera's ask is not mistaken for this one.
func notifyAskFunctions(code string) []string {
	var out []string
	seen := map[string]bool{}
	for _, open := range kotlinCallSites(code, "requestPermissions") {
		args, ok := pairingEntryCallText(code, open)
		if !ok || !notifyAskArgument.MatchString(args) {
			continue
		}
		name, ok := kotlinEnclosingFunction(code, open)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// notifyBlockAfter returns the brace-balanced block that follows the first mention of name.
//
// IT EXISTS BECAUSE THE LISTENER HAS NO ARGUMENT LIST. `setOnCheckedChangeListener { _, checked ->
// ... }` is a trailing lambda, so `kotlinCallSites` -- which finds `name(` -- sees nothing at all,
// and a fence that used it would report every file clean.
func notifyBlockAfter(code, name string) (string, bool) {
	at := strings.Index(code, name)
	if at < 0 {
		return "", false
	}
	open := strings.IndexByte(code[at:], '{')
	if open < 0 {
		return "", false
	}
	start := at + open
	depth := 0
	for i := start; i < len(code); i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return code[start : i+1], true
			}
		}
	}
	return "", false
}

// notifyCalls names the functions one block applies to an argument list.
func notifyCalls(block string) []string {
	var out []string
	for _, m := range notifyCalledName.FindAllStringSubmatch(block, -1) {
		if m[1] != "" {
			continue // a declaration, not a call
		}
		out = append(out, m[2])
	}
	return out
}

// notifyReaches reports whether anything called from block is one of targets, following calls
// declared IN THIS FILE for at most hops more levels.
//
// THE BOUND IS STATED AND IS DELIBERATE, like `pairingEntryFaults`'s one hop. The shipped chain is
// two levels -- the switch's listener calls `onToggled`, which calls the function that asks -- and a
// walk with no bound would need a call graph this file has no type checker to build. A chain longer
// than the bound fails LOUDLY here rather than silently: re-point the gate rather than deleting it.
func notifyReaches(code, block string, targets map[string]bool, hops int, seen map[string]bool) bool {
	for _, name := range notifyCalls(block) {
		if targets[name] {
			return true
		}
		if hops == 0 || seen[name] {
			continue
		}
		seen[name] = true
		if body, ok := kotlinFunBody(code, name); ok {
			if notifyReaches(code, body, targets, hops-1, seen) {
				return true
			}
		}
	}
	return false
}

// notifyUnreachableAsk reports the ways one file's notification ask fails to hang off the control a
// user can actually press.
//
// @param code the whole source, strings and comments already gone.
// @param asks the functions in it that call requestPermissions(POST_NOTIFICATIONS).
func notifyUnreachableAsk(where, code string, asks []string) []string {
	if len(asks) == 0 {
		return nil
	}
	targets := map[string]bool{}
	for _, name := range asks {
		targets[name] = true
	}
	listener, ok := notifyBlockAfter(code, "setOnCheckedChangeListener")
	if !ok {
		return []string{where + " asks the platform for POST_NOTIFICATIONS in " +
			strings.Join(asks, ", ") + ", and no switch in it carries a listener -- so the ask " +
			"hangs off no control this screen offers"}
	}
	if !notifyReaches(code, listener, targets, 2, map[string]bool{}) {
		return []string{where + " has a switch whose press reaches none of the functions that ask " +
			"for POST_NOTIFICATIONS (" + strings.Join(asks, ", ") + ")"}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The notification ask is reachable from the switch the settings screen draws.
// ---------------------------------------------------------------------------

// TestOdij_TheNotificationAskHangsOffTheSwitchThatNeedsIt fences the wire the defect ran along.
func TestOdij_TheNotificationAskHangsOffTheSwitchThatNeedsIt(t *testing.T) {
	var faults []string
	askingFiles := 0

	for _, path := range kotlinFiles(t, kotlinMainRoot(t)) {
		code := notifySource(t, path)
		asks := notifyAskFunctions(code)
		if len(asks) == 0 {
			continue
		}
		askingFiles++
		faults = append(faults, notifyUnreachableAsk(mustRel(t, path), code, asks)...)
	}

	if askingFiles == 0 {
		t.Fatalf("agents-tracker-0dij: no production Kotlin calls `requestPermissions` with " +
			"POST_NOTIFICATIONS, so nothing in this app can ever obtain it. On API 33+ that means " +
			"every notification this product sends is dropped by the platform, silently, and push " +
			"is the sole background wake path (ADR-007 B16). docs/ops/play-closed-testing-" +
			"application.md tells Play that this permission raises a runtime prompt. If the ask " +
			"moved behind a helper this scan cannot see, re-point this gate at it rather than " +
			"deleting it.")
	}

	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("agents-tracker-0dij: the notification ask hangs off no control the settings "+
			"screen offers:\n  %s\n\nA fresh API 33+ install resolves to DENIED "+
			"(`!hasAskedBefore -> DENIED`), and the switch's own tap is the only code in this app "+
			"that can request the permission: sever that and it can never be granted, for the life "+
			"of the install.", strings.Join(faults, "\n  "))
	}
}

// TestOdij_NoScreenHardCodesTheAskedBit fences the defect's own line.
//
// The bit `PermissionStateResolver` cannot read for itself is a PERSISTED fact -- the platform
// offers no API for it, which its header says in as many words. A literal at the call site is not a
// simplification of that fact, it is the opposite of it: `true` claims an ask nobody made and
// resolves a fresh install to PERMANENTLY_DENIED, and `false` claims a first run forever and sends
// a user who has permanently refused back to a prompt the platform silently drops.
func TestOdij_NoScreenHardCodesTheAskedBit(t *testing.T) {
	var faults []string
	for _, path := range kotlinFiles(t, kotlinMainRoot(t)) {
		code := notifySource(t, path)
		if hits := notifyHardCodedAsk.FindAllString(code, -1); len(hits) > 0 {
			faults = append(faults, mustRel(t, path)+": "+strings.Join(hits, ", "))
		}
	}
	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("agents-tracker-0dij: a permission resolve is handed a literal `hasAskedBefore`:"+
			"\n  %s\n\nThat argument exists because `shouldShowRequestPermissionRationale` is false "+
			"BEFORE the first ask as well as after a permanent denial, so the two are otherwise "+
			"indistinguishable. It must come from the persisted bit (`PermissionAsks`), which is "+
			"the only thing in this app that knows.", strings.Join(faults, "\n  "))
	}
}

// TestOdij_TheBlockedNotificationRedirectOpensNotificationSettings fences WHERE the one state with
// no prompt left sends the user.
//
// `ACTION_APPLICATION_DETAILS_SETTINGS` is the app info page -- correct for the camera, whose
// permission has no screen of its own -- and it leaves the user two taps and a scroll from the
// notification switch they came to fix. `ACTION_APP_NOTIFICATION_SETTINGS` is the platform's own
// screen for exactly this, and it needs `EXTRA_APP_PACKAGE`: without it the intent names no app.
func TestOdij_TheBlockedNotificationRedirectOpensNotificationSettings(t *testing.T) {
	found := false
	var faults []string
	for _, path := range kotlinFiles(t, kotlinMainRoot(t)) {
		code := notifySource(t, path)
		if !strings.Contains(code, "ACTION_APP_NOTIFICATION_SETTINGS") {
			continue
		}
		found = true
		if !strings.Contains(code, "EXTRA_APP_PACKAGE") {
			faults = append(faults, mustRel(t, path)+
				": opens the notification settings screen without `EXTRA_APP_PACKAGE`, so the "+
				"intent names no app and the system has nothing to open")
		}
	}
	if !found {
		t.Fatalf("agents-tracker-0dij: no production Kotlin opens " +
			"`Settings.ACTION_APP_NOTIFICATION_SETTINGS`, so a phone whose owner has refused the " +
			"permission twice is shown two dead switches and no way to reach the one screen that " +
			"can un-block them.")
	}
	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("agents-tracker-0dij: %s", strings.Join(faults, "\n  "))
	}
}

// ---------------------------------------------------------------------------
// Negative controls. Each feeds a perturbed source to the SAME functions the assertions call.
// ---------------------------------------------------------------------------

// notifyWiredSurface is the shape the fix produces, in miniature: two switches built by a factory
// that installs one listener, the listener calling the toggle handler, and the handler asking.
const notifyWiredSurface = `class SettingsSurface {
    private val needsInput = touchFilteredSwitch(PushToggle.FIRST)
    private val finished = touchFilteredSwitch(PushToggle.SECOND)

    private fun onToggled(toggle: PushToggle, value: Boolean) {
        if (screen.tapAsksForPermission(value)) {
            askForNotifications()
            return
        }
        persist(value)
    }

    private fun askForNotifications() {
        PermissionAsks.remember(activity, AppPermission.POST_NOTIFICATIONS)
        activity.requestPermissions(arrayOf(AppPermission.POST_NOTIFICATIONS.manifestName), ASK)
    }

    private fun touchFilteredSwitch(toggle: PushToggle): SwitchCompat = SecureWindow.gate(
        SwitchCompat(activity).apply {
            setOnCheckedChangeListener { _: CompoundButton, checked: Boolean ->
                onToggled(toggle, checked)
            }
        },
    )
}`

// TestOdij_TheReachabilityScanDiscriminates is the control on the chain reader, in both directions.
//
// The direction that fails silently is a scan that can see nothing: it reports every file clean and
// a green run then says nothing at all. Each cut below leaves a file that compiles, renders, and
// passes every model test in the Kotlin suite.
func TestOdij_TheReachabilityScanDiscriminates(t *testing.T) {
	if got := notifyAskFunctions(notifyWiredSurface); len(got) != 1 || got[0] != "askForNotifications" {
		t.Fatalf("the ask reader does not find the notification request in a file that plainly "+
			"makes one, so every clean run of the assertion is about nothing: %v", got)
	}
	if got := notifyUnreachableAsk("wired.kt", notifyWiredSurface, notifyAskFunctions(notifyWiredSurface)); len(got) > 0 {
		t.Errorf("the reachability scan rejects a correctly wired surface, which is a fence nobody "+
			"could satisfy:\n%s", strings.Join(got, "\n"))
	}

	cuts := []struct{ what, src string }{
		{
			"the listener no longer calling the handler that asks",
			strings.Replace(notifyWiredSurface, "onToggled(toggle, checked)", "record(toggle, checked)", 1),
		},
		{
			"the handler no longer reaching the ask",
			strings.Replace(notifyWiredSurface, "askForNotifications()\n            return",
				"return", 1),
		},
		{
			"the switch losing its listener altogether",
			strings.Replace(notifyWiredSurface, "setOnCheckedChangeListener", "setOnLongClickListener", 1),
		},
	}
	for _, c := range cuts {
		if got := notifyUnreachableAsk("cut.kt", c.src, notifyAskFunctions(c.src)); len(got) == 0 {
			t.Errorf("the reachability scan is blind to %s, so the ask can be orphaned from the "+
				"control with this gate green", c.what)
		}
	}

	// A screen asking for a DIFFERENT permission is not this screen and must not be dragged in.
	const camera = `class PairingSurface {
    private fun beginScanning() {
        activity.requestPermissions(arrayOf(AppPermission.CAMERA.manifestName), CAMERA_ASK)
    }
}`
	if got := notifyAskFunctions(camera); len(got) > 0 {
		t.Errorf("a CAMERA ask reads as the notification one, so this gate would demand a switch "+
			"on the pairing screen: %v", got)
	}
}

// TestOdij_TheHardCodedBitScanSeesTheDefectAndNotItsFix is the second fence's negative control.
func TestOdij_TheHardCodedBitScanSeesTheDefectAndNotItsFix(t *testing.T) {
	// The line the defect shipped as, verbatim.
	const shipped = `hasAskedBefore = true,`
	if !notifyHardCodedAsk.MatchString(shipped) {
		t.Errorf("the literal scan is blind to the defect's own line (`%s`), so a clean run says "+
			"nothing about where the asked bit comes from", shipped)
	}
	if notifyHardCodedAsk.MatchString(`hasAskedBefore = PermissionAsks.hasAsked(activity, AppPermission.POST_NOTIFICATIONS),`) {
		t.Error("the literal scan rejects the persisted bit, which is the shape the fix produces")
	}
	// A comment quoting the defect must not fail the fence that explains it.
	const commented = `// This read ` + "`hasAskedBefore = true`" + `, so a fresh install resolved
// to PERMANENTLY_DENIED.
hasAskedBefore = PermissionAsks.hasAsked(activity, permission),`
	if notifyHardCodedAsk.MatchString(kotlinCodeOnly(commented)) {
		t.Error("a comment explaining the defect reads as the defect, so the fence fails on its " +
			"own documentation")
	}
}

// ---------------------------------------------------------------------------
// What is NOT fenced here, and why -- recorded so the absence reads as a decision.
//
//   - "The panel leaves the switch live in the states where the permission is askable." That is a
//     predicate over an enum and no amount of reading source text evaluates it. It is asserted where
//     it can be executed: `SettingsScreenTest` and `SettingsPanelScreenTest` drive the real model.
//
//   - "Tapping the switch on a real handset raises the system dialog." That needs a handset. The
//     Robolectric half asserts what it can reach without one -- the redirect's intent, the touch
//     filter, and the absence of any `onRequestPermissionsResult` the redraw could have ridden
//     instead of `onResume` (`SettingsSurfaceNotificationsTest`).
//
//   - The hop is followed at most two levels and within ONE file. A listener that reached the ask
//     through a third helper, or an ask declared in another file, is not followed: that needs a type
//     checker, and a heuristic that guessed would fail in both directions. Both fail this gate
//     LOUDLY rather than silently, which is the direction to fail in.
//
// ---------------------------------------------------------------------------
