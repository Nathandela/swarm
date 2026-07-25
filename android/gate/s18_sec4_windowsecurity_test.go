package gate

// FAILING-FIRST (TDD RED, GG-5) tests for PB-SEC-4, slice S18:
//
//	"FLAG_SECURE on pairing and terminal-peek screens; sensitive content excluded from recents."
//	Criterion: "Window-configuration assertion."
//
// ============================================================================
// READ THIS BEFORE READING THE ASSERTIONS: THERE IS NO WINDOW TO CONFIGURE.
// ============================================================================
//
// android/app/src/main/AndroidManifest.xml declares an <application>, one <receiver> and one
// <service>. It declares NO ACTIVITY. Every file under src/main/kotlin/.../ui/ is a pure
// Kotlin model -- data classes, enums and functions returning them -- with no Activity, no
// Compose setContent, no View and no Window anywhere in the module.
//
// So PB-SEC-4's criterion, "window-configuration assertion", HAS NO SUBJECT IN THIS TREE. That
// is stated here, at the top of the file that owns the requirement, because the alternative is
// the failure this slice was warned about twice over:
//
//   - a test that SKIPS when it finds no Activity reads as green, and an auditor concludes
//     FLAG_SECURE is verified;
//   - a test that iterates the (empty) set of Activities and asserts each one sets FLAG_SECURE
//     PASSES, and is standing defect class (i) -- a guard that cannot fail -- and class (iii),
//     a test passing because its subject does not exist.
//
// Both would be worse than a failure. So this file FAILS, loudly, and says what it is failing
// about: not that FLAG_SECURE is set wrongly, but that the screens the requirement names do
// not exist yet as windows, and nothing here can establish anything about their configuration.
//
// WHAT THE IMPLEMENTER CAN ACTUALLY DELIVER. Two coherent outcomes, and this file is written
// so that either clears it:
//
//	(a) the Activity lands, applies FLAG_SECURE and the recents exclusion at a single named
//	    sink, and the policy table below names every sensitive screen; or
//	(b) the project records that PB-SEC-4 is BLOCKED on an Activity no slice has delivered,
//	    in which case this test's failure is the record, and the slice owner decides whether
//	    S18 grows an Activity or the requirement moves.
//
// It is (b) that must not be reached by accident, which is what the failure message is for.
//
// WHAT IS NOT CLAIMED. FLAG_SECURE is a platform hint that the compositor honours; it stops
// screenshots, screen recording and the recents thumbnail. It does not stop a camera pointed
// at the screen, and it is not attested. PB-E2E-5 stays deferred and nothing here touches it.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The screen-protection policy artifact.
// ---------------------------------------------------------------------------

// windowRow is one line of android/window-security.tsv:
//
//	screen <TAB> flag_secure <TAB> exclude_from_recents <TAB> why
//
// The table exists because PB-SEC-4 names two screens by role ("pairing and terminal-peek")
// and the app has more than two; which of the others are sensitive is a decision, and a
// decision with no artifact is one the next screen silently opts out of.
type windowRow struct {
	Screen    string
	FlagSec   string
	NoRecents string
	Why       string
	Line      int
}

func windowPolicyPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "window-security.tsv")
}

func readWindowPolicy(t *testing.T) []windowRow {
	t.Helper()
	path := windowPolicyPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-SEC-4: %s does not exist, so there is no record of which screens are "+
			"sensitive. The requirement names two by role (pairing, terminal peek) and the app "+
			"has a dozen; the ones it does not name still show session content: %v",
			mustRel(t, path), err)
	}
	var rows []windowRow
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			t.Errorf("PB-SEC-4: %s:%d has %d column(s), want 4 (screen, flag_secure, "+
				"exclude_from_recents, why): %q", mustRel(t, path), i+1, len(parts), line)
			continue
		}
		rows = append(rows, windowRow{
			Screen:    strings.TrimSpace(parts[0]),
			FlagSec:   strings.TrimSpace(parts[1]),
			NoRecents: strings.TrimSpace(parts[2]),
			Why:       strings.TrimSpace(parts[3]),
			Line:      i + 1,
		})
	}
	if len(rows) == 0 {
		t.Fatalf("PB-SEC-4: %s lists no screens; every assertion below would pass vacuously",
			mustRel(t, path))
	}
	return rows
}

// ---------------------------------------------------------------------------
// PB-SEC-4: the honest precondition.
// ---------------------------------------------------------------------------

// TestPBSEC4_ThereIsAWindowToConfigure is the assertion this file exists to make first.
//
// It is a FAILURE and not a t.Skip, and that choice is the whole point. A skip here would
// leave a green suite in which "FLAG_SECURE on pairing and terminal-peek screens" appears
// covered, on a module that cannot put a flag on a window because it has no window.
func TestPBSEC4_ThereIsAWindowToConfigure(t *testing.T) {
	activities := 0
	for _, tag := range []string{"activity", "activity-alias"} {
		activities += len(applicationElement(t, "PB-SEC-4").findAll(tag))
	}
	if activities == 0 {
		t.Fatalf("PB-SEC-4 IS UNVERIFIABLE IN THIS TREE: %s declares no <activity>. There is no "+
			"window, so there is nothing to set FLAG_SECURE on and nothing to exclude from "+
			"recents, and the pairing and terminal-peek SCREENS the requirement names exist "+
			"only as pure Kotlin models under src/main/kotlin/.../ui/ (data classes and enums, "+
			"no Activity, no setContent, no View).\n\n"+
			"This is reported as a FAILURE rather than a skip on purpose. A skip would leave a "+
			"green run in which this requirement reads as covered; a loop over the empty set of "+
			"Activities would PASS, which is a guard that cannot fail. Neither is an honest "+
			"record of a requirement whose subject has not been built.\n\n"+
			"Two outcomes clear this test: an Activity that applies the protections at the sink "+
			"the tests below look for, or a recorded decision that PB-SEC-4 is blocked on an "+
			"Activity no slice has delivered. The second is a scope call for the slice owner, "+
			"not something to be reached by deleting this assertion",
			mustRel(t, manifestPath(t)))
	}
}

// ---------------------------------------------------------------------------
// PB-SEC-4: the policy, which can be asserted today.
// ---------------------------------------------------------------------------

// TestPBSEC4_ThePolicyCoversTheTwoScreensTheRequirementNames.
//
// This one does NOT depend on an Activity existing: it asserts the decision has been made and
// written down, which is possible now and is the half that survives whichever way the scope
// call above goes. The two roles the requirement names are matched by substring, so the table
// may use whatever screen names the app actually has.
func TestPBSEC4_ThePolicyCoversTheTwoScreensTheRequirementNames(t *testing.T) {
	rows := readWindowPolicy(t)

	needed := map[string][]string{
		"pairing":       {"pair"},
		"terminal peek": {"peek", "terminal"},
	}
	for role, needles := range needed {
		var match *windowRow
		for i := range rows {
			lower := strings.ToLower(rows[i].Screen)
			for _, n := range needles {
				if strings.Contains(lower, n) {
					match = &rows[i]
					break
				}
			}
			if match != nil {
				break
			}
		}
		if match == nil {
			t.Errorf("PB-SEC-4: %s has no row for the %s screen, which the requirement names "+
				"explicitly", mustRel(t, windowPolicyPath(t)), role)
			continue
		}
		if match.FlagSec != "true" {
			t.Errorf("PB-SEC-4: %s:%d marks the %s screen flag_secure=%q, want \"true\". The "+
				"requirement names this screen: pairing shows the SAS and the destination "+
				"origin, and the peek shows the session's terminal grid",
				mustRel(t, windowPolicyPath(t)), match.Line, role, match.FlagSec)
		}
		if match.NoRecents != "true" {
			t.Errorf("PB-SEC-4: %s:%d marks the %s screen exclude_from_recents=%q, want "+
				"\"true\". The recents thumbnail is a screenshot the system takes when the app "+
				"backgrounds, and it survives on disk",
				mustRel(t, windowPolicyPath(t)), match.Line, role, match.NoRecents)
		}
	}
}

// TestPBSEC4_ProtectedScreensResolveToOneSecureWindowSink.
//
// The flags must be applied in ONE place. Per-screen application is how one screen gets missed,
// and the screen that gets missed is always the one added last -- with no test failing, because
// a per-screen test enumerates the screens that exist.
//
// The sink is identified by what it does, not by what it is called: any production Kotlin that
// names FLAG_SECURE (or setRecentsScreenshotEnabled, its modern companion). The assertion is
// that there is exactly one such site, and that the policy table's protected screens outnumber
// zero -- so a single sink cannot be a single sink that nothing routes to.
func TestPBSEC4_ProtectedScreensResolveToOneSecureWindowSink(t *testing.T) {
	rows := readWindowPolicy(t)
	protected := 0
	for _, r := range rows {
		if r.FlagSec == "true" || r.NoRecents == "true" {
			protected++
		}
	}
	if protected == 0 {
		t.Fatalf("PB-SEC-4: %s marks no screen as protected at all. A sink assertion over zero "+
			"protected screens cannot fail", mustRel(t, windowPolicyPath(t)))
	}

	// FLAG_SECURE reaches the window through addFlags/setFlags; setRecentsScreenshotEnabled is
	// the API-33+ way to drop the thumbnail without also blocking screenshots. Both count.
	markers := []string{"FLAG_SECURE", "setRecentsScreenshotEnabled"}
	var sites []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := stripKotlinComments(readFileOrFail(t, f, "PB-SEC-4"))
		for _, m := range markers {
			if strings.Contains(src, m) {
				sites = append(sites, mustRel(t, f)+" ("+m+")")
			}
		}
	}
	sort.Strings(sites)

	if len(sites) == 0 {
		t.Errorf("PB-SEC-4: no file under %s names FLAG_SECURE or setRecentsScreenshotEnabled, "+
			"so %d screen(s) the policy marks protected are protected by nothing. The screen "+
			"content at stake is the SAS emoji and the destination origin on the pairing screen, "+
			"and the rendered terminal grid on the peek -- both of which the recents thumbnail "+
			"writes to disk when the app backgrounds",
			mustRel(t, kotlinMainRoot(t)), protected)
		return
	}
	if len(sites) > 2 {
		// >2 because one file may legitimately name both markers.
		t.Errorf("PB-SEC-4: the window protections are applied at %d sites:\n\t%s\nOne sink, "+
			"applied where screens are created, is what makes the NEXT screen protected by "+
			"default. Per-screen application is how the last screen added gets missed with "+
			"nothing failing", len(sites), strings.Join(sites, "\n\t"))
	}
}
