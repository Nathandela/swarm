package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-qx9m: "QR pairing is unreachable on a fresh
// install: the scan button requires the permission it exists to request."
//
// WHAT HAPPENED. The owner installed the internal-testing build (versionCode 2) on a real handset
// and reported no camera and no scan button -- only the paste field. Three facts closed a loop:
//
//   - `PermissionStateResolver` answers `!hasAskedBefore -> DENIED`, so a permission NOBODY HAS
//     ASKED FOR resolves to DENIED. That row is deliberate and correct: `showRationale` is false
//     before the first ask as well as after a permanent one, so a fresh install is otherwise
//     indistinguishable from a permanent refusal.
//   - `PairingPanel` offered `PairingControl.SCAN` on `scanner == ScannerState.SCANNING`, which is
//     the GRANTED state alone.
//   - `PairingSurface.beginScanning` -- the SCAN control's own click listener -- holds the ONLY
//     `requestPermissions(CAMERA)` call in the app, and it already branched correctly on both
//     denials.
//
// So: no permission, so no control; no control, so nothing could ask; nothing asked, so no
// permission. For the life of the install.
//
// WHY THIS IS A GO GATE AND NOT ONLY A KOTLIN TEST. The Kotlin suite now asks the real
// `PermissionStateResolver` what a phone that has never been asked gets, and asserts the panel
// offers it a control -- that is the right test for the DECISION and it is the stronger one. What
// it cannot assert is the fact UNDERNEATH the decision: that the control the panel offers is still
// the one wired to the platform ask. Those two live in different files, and the model tests would
// stay green if the wire between them were cut.
//
// WHAT THIS FILE DELIBERATELY DOES NOT FENCE is recorded at the bottom, with the reasons. A gate
// that pretends to check something it cannot see is worse than no gate (pairingentry_test.go's
// recorded standard, and this file holds to it).
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. Every scan starts at the app module, so it cannot
// descend into `.claude/worktrees/`, which holds other agents' full checkouts and has already made
// four gates in this repository report findings about somebody else's private copy.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Reading the chain: the platform ask, and the control it hangs off.
// ---------------------------------------------------------------------------

// qx9mCameraArgument is the CAMERA permission named in an argument list. Both spellings are
// accepted because both are real: the app passes `AppPermission.CAMERA.manifestName`, and
// `Manifest.permission.CAMERA` is what a hand-written call would say.
var qx9mCameraArgument = regexp.MustCompile(`\bCAMERA\b`)

// qx9mScanBinding is the view bound to the scan control in a `PairingControl` -> `View` map.
//
// The IDENTIFIER is captured rather than matched, because the fence's question is what that view
// DOES when pressed, and answering it means resolving the name.
var qx9mScanBinding = regexp.MustCompile(`PairingControl\.SCAN\s+to\s+([A-Za-z_][A-Za-z0-9_]*)`)

// qx9mScannerStateValue is any ScannerState CONSTANT named in code -- `ScannerState.SCANNING` and
// its two denials. The bare type is not matched: `scanner: ScannerState` is a parameter and is
// exactly what the panel is supposed to take.
var qx9mScannerStateValue = regexp.MustCompile(`\bScannerState\.[A-Z][A-Z_]*`)

// qx9mSource reads one production Kotlin file as REFERENCES: comments out (a fence a comment can
// satisfy is one the next thorough comment turns off -- and the comment this fix leaves behind
// quotes the defect verbatim, so a scan that read comments would fail on the fix's own note), and
// string literals out (a button's label talks about scanning and is not a wire).
func qx9mSource(t *testing.T, path string) string {
	t.Helper()
	return kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-qx9m")))
}

// qx9mCameraAskFunctions names every function in one source that asks the PLATFORM for the camera.
//
// It reads call sites of `requestPermissions` and keeps the ones whose arguments name CAMERA, so a
// screen asking for POST_NOTIFICATIONS is not mistaken for this one.
func qx9mCameraAskFunctions(code string) []string {
	var out []string
	seen := map[string]bool{}
	for _, open := range kotlinCallSites(code, "requestPermissions") {
		args, ok := pairingEntryCallText(code, open)
		if !ok || !qx9mCameraArgument.MatchString(args) {
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

// qx9mUnreachableAsk reports the ways one file's camera ask fails to hang off the scan control.
//
// THE HOP IS THE SAME ONE `pairingEntryFaults` FOLLOWS, and for the same reason: the control is not
// the function, it is a `val` whose initialiser installs the function as a listener
// (`val startScan = touchFilteredButton(...) { beginScanning() }`). A check that only read the map
// entry would see an identifier and learn nothing.
//
// @param code the whole source, strings and comments already gone.
// @param asks the functions in it that call requestPermissions(CAMERA).
func qx9mUnreachableAsk(where, code string, asks []string) []string {
	if len(asks) == 0 {
		return nil
	}
	m := qx9mScanBinding.FindStringSubmatch(code)
	if m == nil {
		return []string{where + " asks the platform for CAMERA in " + strings.Join(asks, ", ") +
			", and binds no view to " + "`PairingControl.SCAN` -- so the ask hangs off no control " +
			"this screen can offer"}
	}
	control := m[1]
	body, ok := kotlinInitialiser(code, control)
	if !ok {
		return []string{where + " binds `" + control + "` to the scan control, and `" + control +
			"` has no declaration in this file, so the gate cannot tell what pressing it does"}
	}
	for _, fn := range asks {
		if len(kotlinCallSites(body, fn)) > 0 {
			return nil
		}
	}
	return []string{where + " binds `" + control + "` to the scan control, and pressing it reaches " +
		"none of the functions that ask for CAMERA (" + strings.Join(asks, ", ") + ")"}
}

// ---------------------------------------------------------------------------
// The camera ask is reachable from the control the pairing screen offers.
// ---------------------------------------------------------------------------

// TestQX9M_TheOnlyCameraAskHangsOffTheScanControl fences the wire the defect ran along.
//
// The defect itself was upstream of this wire -- the panel never OFFERED the control -- and that
// half is asserted in Kotlin, against the real `PermissionStateResolver`, because which state
// offers which control is a predicate and not a string. What is fenced here is the half no Kotlin
// test can hold: that the app's only platform ask is still behind the control the screen draws. Cut
// that wire and every model test stays green while the handset goes back to a paste field and no
// camera.
func TestQX9M_TheOnlyCameraAskHangsOffTheScanControl(t *testing.T) {
	var faults []string
	askingFiles := 0

	for _, path := range kotlinFiles(t, kotlinMainRoot(t)) {
		code := qx9mSource(t, path)
		asks := qx9mCameraAskFunctions(code)
		if len(asks) == 0 {
			continue
		}
		askingFiles++
		faults = append(faults, qx9mUnreachableAsk(mustRel(t, path), code, asks)...)
	}

	if askingFiles == 0 {
		t.Fatalf("agents-tracker-qx9m: no production Kotlin calls `requestPermissions` with CAMERA, " +
			"so nothing in this app can ever obtain the camera permission and QR pairing is " +
			"unreachable on every install. PB-PAIR-2's first clause is that the permission is " +
			"REQUESTED; if the ask moved behind a helper this scan cannot see, re-point this gate at " +
			"it rather than deleting it.")
	}

	sort.Strings(faults)
	if len(faults) > 0 {
		t.Errorf("agents-tracker-qx9m: the camera ask hangs off no control the pairing screen "+
			"offers:\n  %s\n\nThe owner installed the internal-testing build on a real handset and "+
			"found no camera and no scan button -- only the paste field. A fresh install resolves to "+
			"PERMISSION_DENIED (`!hasAskedBefore -> DENIED`), and the only code in this app that can "+
			"request CAMERA is the scan control's click listener: sever that and the permission can "+
			"never be granted, for the life of the install.", strings.Join(faults, "\n  "))
	}
}

// ---------------------------------------------------------------------------
// The panel asks PairingFlow what the camera's answer means. It never decides.
// ---------------------------------------------------------------------------

const qx9mPanelFile = "dev/swarm/phone/ui/screens/PairingPanel.kt"

// TestQX9M_ThePairingPanelDecidesNoScannerStateOfItsOwn fences the SHAPE of the defect.
//
// The line was `if (scanner == ScannerState.SCANNING) controls += PairingControl.SCAN` -- one
// comparison, in the screen, beside two questions (`offersManualEntry`, `routesToSystemSettings`)
// that were already asked of `PairingFlow`. That asymmetry is the whole defect: two of the three
// answers about the camera lived where they could be read as a set and reviewed against each other,
// and the third was inlined where nothing related it to them.
//
// So the fence is that the panel names NO `ScannerState` value at all. The three answers are
// `PairingFlow`'s, and a reader who opens that object sees all three or none.
//
// ITS LIMIT IS STATED AND IS REAL: this cannot check that `PairingFlow`'s answers are RIGHT. A
// wrong predicate written in the right place passes here and fails in `PairingFlowTest` and
// `PairingPanelScreenTest`, which drive the real resolver. What it stops is the answer moving back
// out of the set.
func TestQX9M_ThePairingPanelDecidesNoScannerStateOfItsOwn(t *testing.T) {
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(qx9mPanelFile))
	code := qx9mSource(t, path)

	if !strings.Contains(code, "PairingControl.SCAN") {
		t.Fatalf("agents-tracker-qx9m: %s no longer names `PairingControl.SCAN`, so the screen this "+
			"gate is about does not offer the scanner and the fence has no subject. If the decision "+
			"moved, re-point this check at its new home.", qx9mPanelFile)
	}

	if hits := qx9mScannerStateValue.FindAllString(code, -1); len(hits) > 0 {
		t.Errorf("agents-tracker-qx9m: %s decides a camera state of its own (%s).\n\nThe three "+
			"answers about the scanner -- is it offered, is manual entry offered, does this route to "+
			"Settings -- belong together in `PairingFlow`, where a reader sees them as a set. The "+
			"defect was exactly one of them inlined here: `scanner == ScannerState.SCANNING` offered "+
			"the scan control only where the permission was ALREADY GRANTED, and that control's "+
			"listener is the only code in the app that can request it.",
			qx9mPanelFile, strings.Join(hits, ", "))
	}
}

// ---------------------------------------------------------------------------
// Negative controls. Each feeds a perturbed source to the SAME function the assertions call.
// ---------------------------------------------------------------------------

// TestQX9M_TheReachabilityScanDiscriminates is the control on the chain reader, in both directions.
//
// The direction that fails silently is a scan that can see nothing: it reports every file clean and
// a green run then says nothing at all. Each row below is a real way the wire can be cut.
func TestQX9M_TheReachabilityScanDiscriminates(t *testing.T) {
	const wired = `class PairingSurface {
    private val startScan = touchFilteredButton("") { beginScanning() }
    private val useTypedPayload = touchFilteredButton("") { accept() }
    private val slots = PairingSlots(
        controls = mapOf(
            PairingControl.SCAN to startScan,
            PairingControl.USE_TYPED_PAYLOAD to useTypedPayload,
        ),
    )
    private fun beginScanning() {
        activity.requestPermissions(arrayOf(AppPermission.CAMERA.manifestName), CAMERA_ASK)
    }
}`

	if got := qx9mCameraAskFunctions(wired); len(got) != 1 || got[0] != "beginScanning" {
		t.Fatalf("the ask reader does not find the camera request in a file that plainly makes one, "+
			"so every clean run of the assertion is about nothing: %v", got)
	}
	if got := qx9mUnreachableAsk("wired.kt", wired, qx9mCameraAskFunctions(wired)); len(got) > 0 {
		t.Errorf("the reachability scan rejects a correctly wired screen, which is a fence nobody "+
			"could satisfy:\n%s", strings.Join(got, "\n"))
	}

	// The cuts. Each leaves a file that compiles, renders, and passes every model test.
	cuts := []struct{ what, src string }{
		{
			"the ask rewired to a control the scanner does not own",
			strings.Replace(wired, "PairingControl.SCAN to startScan", "PairingControl.STOP to startScan", 1),
		},
		{
			"the scan control bound to a view that does not ask",
			strings.Replace(wired, "PairingControl.SCAN to startScan",
				"PairingControl.SCAN to useTypedPayload", 1),
		},
		{
			"the listener no longer calling the asking function",
			strings.Replace(wired, `val startScan = touchFilteredButton("") { beginScanning() }`,
				`val startScan = touchFilteredButton("") { openPreview() }`, 1),
		},
	}
	for _, c := range cuts {
		asks := qx9mCameraAskFunctions(c.src)
		if got := qx9mUnreachableAsk("cut.kt", c.src, asks); len(got) == 0 {
			t.Errorf("the reachability scan is blind to %s, so the ask can be orphaned from the "+
				"control with this gate green", c.what)
		}
	}

	// A screen asking for a DIFFERENT permission is not this screen and must not be dragged in.
	const notifications = `class SettingsSurface {
    private fun askAlerts() {
        activity.requestPermissions(arrayOf(AppPermission.POST_NOTIFICATIONS.manifestName), ASK)
    }
}`
	if got := qx9mCameraAskFunctions(notifications); len(got) > 0 {
		t.Errorf("a POST_NOTIFICATIONS ask reads as the camera's, so this gate would demand a scan "+
			"control on the settings screen: %v", got)
	}
}

// TestQX9M_ThePanelScanSeesAnInlinedCameraState is the second fence's negative control.
//
// It is the direction that fails silently: a regexp that matches nothing reports a clean panel
// whether or not the decision came back. The perturbed sources are fed to the same expression the
// assertion uses, including the exact line the defect shipped as.
func TestQX9M_ThePanelScanSeesAnInlinedCameraState(t *testing.T) {
	inlined := []struct{ what, src string }{
		{"the defect's own line", `if (scanner == ScannerState.SCANNING) controls += PairingControl.SCAN`},
		{"the same test negated", `if (scanner != ScannerState.PERMISSION_PERMANENTLY_DENIED) controls += x`},
		{"a when over the states", `when (scanner) { ScannerState.SCANNING -> offer() else -> {} }`},
	}
	for _, c := range inlined {
		if !qx9mScannerStateValue.MatchString(c.src) {
			t.Errorf("the panel scan is blind to %s (`%s`), so a clean run says nothing about where "+
				"the camera decision lives", c.what, c.src)
		}
	}

	// What the panel is SUPPOSED to look like: the type as a parameter, the questions asked of the
	// flow. A fence that rejected this would be a fence nobody could satisfy.
	const allowed = `fun of(attempt: PairingAttempt, scanner: ScannerState): PairingPanel {
    if (PairingFlow.offersScanner(scanner)) controls += PairingControl.SCAN
    if (PairingFlow.offersManualEntry(scanner)) controls += PairingControl.TYPED_PAYLOAD
    if (PairingFlow.routesToSystemSettings(scanner)) controls += PairingControl.OPEN_SYSTEM_SETTINGS
}`
	if hits := qx9mScannerStateValue.FindAllString(allowed, -1); len(hits) > 0 {
		t.Errorf("the panel scan rejects a panel that asks PairingFlow for every answer, which is "+
			"the shape the fix exists to produce: %v", hits)
	}

	// And the comment case, which this fix creates: the fix's own note quotes the defective line
	// verbatim. A scan that read comments would fail on the explanation of the thing it checks.
	const commented = `// This read ` + "`scanner == ScannerState.SCANNING`" + `, so the control was
// offered only where the permission was already granted.
if (PairingFlow.offersScanner(scanner)) controls += PairingControl.SCAN`
	if hits := qx9mScannerStateValue.FindAllString(kotlinCodeOnly(commented), -1); len(hits) > 0 {
		t.Errorf("a comment explaining the defect reads as the defect, so the fence fails on its own "+
			"documentation: %v", hits)
	}
}

// ---------------------------------------------------------------------------
// What is NOT fenced here, and why. Recorded so the absence reads as a decision, and so the next
// reader does not take a green run for more than it is.
//
//   - "The panel offers SCAN in the states where the camera is askable." That is a PREDICATE over
//     an enum, evaluated by Kotlin, and no amount of reading source text evaluates it. A regexp
//     asserting the shape of the condition would be this gate writing the product's logic and then
//     checking that the product used its spelling -- and it would pass over any wrong answer
//     written in the approved shape. It is asserted where it can be executed: `PairingFlowTest` and
//     `PairingPanelScreenTest` drive the real `PermissionStateResolver` with
//     `hasAskedBefore = false`, which is the state that shipped broken and the state no test in
//     this repository had ever constructed.
//
//   - "Pressing the scan control on a granted phone opens a camera." That needs a camera, and
//     whether a real one yields a frame is PB-E2E-5, which is deferred. `PairingSurface` is also
//     the one file in this module with no unit test at all -- it takes an Activity -- so the claim
//     has no cheap home yet.
//
//   - The hop is followed ONE level and within ONE file, like `pairingEntryFaults`. A listener that
//     reaches the ask through a second helper, or an ask declared in another file from the control
//     that triggers it, is not followed: that needs a type checker, and a heuristic that guessed
//     would fail in both directions. Both would fail this gate LOUDLY rather than silently, which
//     is the direction to fail in -- re-point it rather than deleting it.
//
// ---------------------------------------------------------------------------
