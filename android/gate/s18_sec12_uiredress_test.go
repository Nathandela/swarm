package gate

// FAILING-FIRST (TDD RED, GG-5) tests for PB-SEC-12, slice S18:
//
//	"UI-redress and input-path defenses: overlay/tapjacking protection on gated actions
//	 (filterTouchesWhenObscured or equivalent), NO SENSITIVE CLIPBOARD USE, and documented
//	 limits regarding third-party IMEs and accessibility services."
//	Criterion: "Tests where testable; documented where not."
//
// The criterion splits this row into three clauses with three different honest treatments, and
// the split is not a convenience -- it is the requirement's own wording.
//
// ---------------------------------------------------------------------------
// CLAUSE 1, TAPJACKING: testable only once a View exists, and none does.
// ---------------------------------------------------------------------------
//
// filterTouchesWhenObscured is a View attribute. The module declares no Activity and contains
// no View, no Compose setContent and no layout XML -- src/main/kotlin/.../ui/ is pure Kotlin
// models. So there is no touch to filter, exactly as PB-SEC-4 has no window to flag. This file
// treats that the same way s18_sec4_windowsecurity_test.go does: a loud FAILURE naming the
// missing subject, never a skip and never a vacuous loop over an empty set.
//
// ---------------------------------------------------------------------------
// CLAUSE 2, CLIPBOARD: testable now, and the answer is more interesting than it looks.
// ---------------------------------------------------------------------------
//
// A reviewer recorded during S11 that App.Paste takes a Kotlin String -- a password out of a
// manager, say -- and that a String CANNOT BE ZEROIZED, nor is the byte-slice input path
// zeroized either. That is true and it is recorded again below. It is also, on inspection, NOT
// what this clause is about, and conflating the two would leave the actual requirement untested.
//
// "No sensitive clipboard use" is about what the APP does with the SYSTEM CLIPBOARD:
//
//   - it must not PUT session content on the clipboard (a Copy affordance on the terminal grid,
//     a "share this session id" button), because the clipboard is readable by other apps on
//     older behaviour, is synced to other devices by some OEM builds, and outlives the app;
//   - it must not READ the clipboard on its own initiative, which since Android 12 raises a
//     system toast naming the app and is a surveillance surface in its own right.
//
// App.Paste is neither. It is an INPUT path: the user performs a paste, Android hands the app
// the text, and the app types it at the remote shell. The app never touches ClipboardManager.
// So MY POSITION IS THAT THE CLAUSE IS SATISFIABLE AS WRITTEN, and the tests below assert it
// the only way that means anything: no ClipboardManager surface in production code at all.
//
// What would NOT satisfy it, and what the residual actually is: the pasted String's bytes stay
// in the JVM heap until GC, unreachable by any zeroization the Go core could perform, because
// the crossing is a Java String by PB-BIND-4's own design (gomobile keeps []byte crossings to
// the enumerated few). Making that zeroizable would mean changing the crossing to a byte array
// and zeroizing on both sides -- a PB-BIND-4 and ADR-007 B8 decision, not an S18 one. It is
// recorded here rather than fixed here, and the test below requires it to be recorded in the
// artifact too, so a later reader cannot conclude the paste path is hygienic when it is merely
// not a clipboard read.
//
// ---------------------------------------------------------------------------
// CLAUSE 3, IME AND ACCESSIBILITY: "documented where not", so an artifact.
// ---------------------------------------------------------------------------
//
// A third-party IME sees every keystroke before the app does; an accessibility service can read
// the screen and inject events. Neither is defensible from inside the app -- there is no API
// that refuses a keyboard -- which is precisely why the criterion says "documented where not".
// This project has already ruled that an unwritten consideration is unenforceable, so the
// document is asserted as an artifact.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func inputPathLimitsPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "input-path-limits.md")
}

// ---------------------------------------------------------------------------
// Clause 1: tapjacking.
// ---------------------------------------------------------------------------

// TestPBSEC12_ThereIsATouchSurfaceToProtect mirrors PB-SEC-4's precondition, for the same
// reason and with the same refusal to skip.
func TestPBSEC12_ThereIsATouchSurfaceToProtect(t *testing.T) {
	app := applicationElement(t, "PB-SEC-12")
	if len(app.findAll("activity"))+len(app.findAll("activity-alias")) == 0 {
		t.Fatalf("PB-SEC-12 CLAUSE 1 IS UNVERIFIABLE IN THIS TREE: %s declares no <activity>, "+
			"so there is no View and no touch to filter. filterTouchesWhenObscured is a View "+
			"attribute; the gated actions the clause names (take control, kill, revoke, the "+
			"kill switch) exist today only as Kotlin enums and data classes with no UI "+
			"attached.\n\n"+
			"Reported as a FAILURE, not a skip: a skip reads as green, and a loop over zero "+
			"Views asserting each filters obscured touches would PASS. Tapjacking is the "+
			"attack where an overlay covers a confirm button so the user's tap lands on "+
			"something they cannot see, and the buttons at stake here revoke a device and "+
			"take control of a shell",
			mustRel(t, manifestPath(t)))
	}
}

// TestPBSEC12_GatedActionsFilterObscuredTouches is the assertion that becomes live the moment a
// View exists. It is written now so the UI cannot land without it.
func TestPBSEC12_GatedActionsFilterObscuredTouches(t *testing.T) {
	markers := []string{"filterTouchesWhenObscured", "FLAG_WINDOW_IS_OBSCURED", "FLAG_WINDOW_IS_PARTIALLY_OBSCURED"}
	var sites []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := stripKotlinComments(readFileOrFail(t, f, "PB-SEC-12"))
		for _, m := range markers {
			if strings.Contains(src, m) {
				sites = append(sites, mustRel(t, f)+" ("+m+")")
			}
		}
	}
	// Layout XML is the other place the attribute can be set.
	resRoot := filepath.Join(appModule(t), "src", "main", "res")
	_ = filepath.Walk(resRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".xml") {
			return nil
		}
		if strings.Contains(readFileOrFail(t, path, "PB-SEC-12"), "filterTouchesWhenObscured") {
			sites = append(sites, mustRel(t, path))
		}
		return nil
	})
	sort.Strings(sites)

	if len(sites) == 0 {
		t.Errorf("PB-SEC-12: nothing under %s or %s applies overlay protection. The gated "+
			"actions are the ones an overlay attack is worth mounting against: take control of "+
			"a live shell, kill a session, revoke a device. \"or equivalent\" in the "+
			"requirement admits a Compose-side check of MotionEvent flags; what it does not "+
			"admit is nothing", mustRel(t, kotlinMainRoot(t)), mustRel(t, resRoot))
	}
}

// ---------------------------------------------------------------------------
// Clause 2: the clipboard.
// ---------------------------------------------------------------------------

// TestPBSEC12_TheAppNeverTouchesTheSystemClipboard is the clause asserted as behaviour.
//
// LEGITIMATE PASSER TODAY: no production Kotlin references ClipboardManager. It is here
// because the affordance this forbids is one a UI author adds without thinking -- a Copy button
// on a terminal grid is an obvious convenience, and it writes the session's rendered output to
// a buffer other apps can read and some OEM builds sync to another device.
//
// It is asserted over src/main only. A test fixture that exercises a paste is not the app
// putting session content on the clipboard.
func TestPBSEC12_TheAppNeverTouchesTheSystemClipboard(t *testing.T) {
	forbidden := map[string]string{
		"ClipboardManager":  "the system clipboard service",
		"setPrimaryClip":    "writes to the system clipboard",
		"getPrimaryClip":    "reads the system clipboard, which since Android 12 raises a system toast naming this app",
		"ClipData":          "constructs a clipboard payload",
		"CLIPBOARD_SERVICE": "resolves the system clipboard service",
	}
	var findings []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := stripKotlinComments(readFileOrFail(t, f, "PB-SEC-12"))
		for needle, why := range forbidden {
			if strings.Contains(src, needle) {
				findings = append(findings, mustRel(t, f)+": "+needle+" -- "+why)
			}
		}
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Errorf("PB-SEC-12: %d clipboard call site(s) in production code. The clipboard is "+
			"readable outside this app, outlives it, and on some OEM builds syncs to another "+
			"device -- so session content on it defeats the whole at-rest design:\n\t%s",
			len(findings), strings.Join(findings, "\n\t"))
	}
}

// TestPBSEC12_ThePasteResidualIsRecorded is the honest half, and it is the one this row was
// flagged for.
//
// App.Paste(session, text String) is NOT a clipboard use -- see the file header for why -- but
// the reviewer's S11 observation stands on its own terms: a Kotlin String cannot be zeroized,
// so a password pasted out of a manager stays in the JVM heap until GC, and the []byte the
// facade derives from it is not zeroized either. Neither fact is fixable inside PB-SEC-12: the
// crossing is a String by PB-BIND-4's design, and changing it is an ADR-007 B8 decision.
//
// So the requirement here is that it be WRITTEN DOWN, in the same artifact clause 3 needs. The
// alternative is a reader who finds "no clipboard use: verified" and concludes the paste path
// leaves nothing behind.
func TestPBSEC12_ThePasteResidualIsRecorded(t *testing.T) {
	path := inputPathLimitsPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-SEC-12: %s does not exist: %v", mustRel(t, path), err)
	}
	doc := strings.ToLower(string(raw))

	if !strings.Contains(doc, "paste") {
		t.Errorf("PB-SEC-12: %s does not mention the paste path. App.Paste takes a Kotlin "+
			"String, which cannot be zeroized; a password pasted from a manager stays in the "+
			"JVM heap until GC and the derived byte slice is not wiped either. That is not a "+
			"clipboard USE and does not fail this requirement, but a document about input-path "+
			"limits that omits it lets a reader conclude the path is hygienic",
			mustRel(t, path))
	}
	if !strings.Contains(doc, "zeroi") && !strings.Contains(doc, "wipe") {
		t.Errorf("PB-SEC-12: %s does not state the zeroization limit. The specific claim the "+
			"artifact owes: pasted text crosses as a String (PB-BIND-4 keeps []byte crossings "+
			"to the enumerated few), Strings are immutable, and neither the String nor the "+
			"byte slice derived from it is wiped after the frame is sealed",
			mustRel(t, path))
	}
}

// ---------------------------------------------------------------------------
// Clause 3: documented limits.
// ---------------------------------------------------------------------------

// TestPBSEC12_TheIMEAndAccessibilityLimitsAreDocumented.
//
// These are the two input-path adversaries the app genuinely cannot defend against, and the
// criterion's "documented where not" is aimed squarely at them. The artifact must name both and
// say what the limit IS -- not that they exist, which the requirement already said.
//
// There is real content owed. A third-party IME receives every keystroke before this app does,
// which means the take-control lease and the biometric gate protect the CHANNEL and not the
// KEYBOARD; an accessibility service can read the rendered screen and synthesise taps, and no
// window flag ever shut it out -- that it did not was one of the reasons PB-SEC-4's FLAG_SECURE
// was withdrawn (ADR-007 B65), and the limit is unchanged by the withdrawal. Both are
// user-installed and user-enabled, so the app's honest posture is to say so, not to pretend
// otherwise.
func TestPBSEC12_TheIMEAndAccessibilityLimitsAreDocumented(t *testing.T) {
	path := inputPathLimitsPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-SEC-12: %s does not exist. The criterion for this clause is \"documented "+
			"where not [testable]\", and this project has already ruled that an unwritten "+
			"consideration is unenforceable -- PB-SEC-3 and PB-SEC-8 both carry an "+
			"evidence-artifact criterion for that reason: %v", mustRel(t, path), err)
	}
	doc := strings.ToLower(string(raw))

	required := map[string][]string{
		"third-party IMEs":       {"ime", "input method", "keyboard"},
		"accessibility services": {"accessibility"},
	}
	var missing []string
	for topic, needles := range required {
		found := false
		for _, n := range needles {
			if strings.Contains(doc, n) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, topic)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("PB-SEC-12: %s documents no limit for: %s. The requirement names both "+
			"explicitly. What is owed is the limit itself: a third-party IME sees every "+
			"keystroke before the app does, so the lease and the biometric gate protect the "+
			"channel and not the keyboard; an accessibility service can read the rendered "+
			"screen and synthesise taps, which no window flag ever excluded -- and PB-SEC-4's "+
			"FLAG_SECURE is gone entirely since ADR-007 B65",
			mustRel(t, path), strings.Join(missing, ", "))
	}
}
