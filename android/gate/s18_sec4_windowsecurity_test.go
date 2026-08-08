package gate

// PB-SEC-4, slice S18 -- INVERTED. The requirement as written was:
//
//	"FLAG_SECURE on pairing and terminal-peek screens; sensitive content excluded from recents."
//
// ADR-007 B65, ruled by the owner on 2026-07-26, WITHDREW it: the shipped app allows
// screenshots and screen recording. This file no longer asserts the protection is applied. It
// asserts the protection is ABSENT, and it is the same file rather than a deleted one.
//
// ============================================================================
// WHY THIS FILE STILL EXISTS, WHICH IS THE WHOLE OF ITS VALUE.
// ============================================================================
//
// A requirement DELETED leaves nothing behind. A requirement INVERTED keeps the one property
// the original actually bought -- that the app's screenshot posture is a DECISION and not
// drift -- and points at the entry where the decision was made.
//
// So the polarity is reversed and nothing else is. Reinstating FLAG_SECURE now FAILS, by name,
// and that failure is how the next person to add it back -- for what will feel at the time like
// an obvious security improvement -- is made to read ADR-007 B65 first. The argument they will
// find there is that the flag is a compositor hint: not attested, no defence against a camera
// pointed at the screen, and no defence against an accessibility service, which reads the
// rendered screen regardless (android/input-path-limits.md, and PB-SEC-12's own gate says so
// twice in prose). It stopped an app that could already screenshot and stopped nothing else,
// while blocking users of a developer tool from sharing terminal output.
//
// WHAT THIS FILE DOES NOT TOUCH. PB-SEC-12 clause 1 -- filterTouchesWhenObscured on the
// destructive and authorising controls -- is a different requirement against a different
// attack, it has no screenshot clause, and B65 leaves it exactly as it was. Its gate is
// android/gate/s18_sec12_uiredress_test.go.
//
// WHAT IS NOT CLAIMED. Nothing here is evidence about a physical handset; PB-E2E-5 stays
// deferred. A source scan cannot prove the compositor's behaviour, which is why the RUNTIME
// half lives in android/app/src/test/kotlin/dev/swarm/phone/PhoneActivityWindowTest.kt: that
// one drives a real Activity and asserts the window does not carry the flag after onCreate,
// which catches a flag set by some path this scan cannot see (a raw 0x2000, a theme attribute).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// screenshotBlockMarkers are the two APIs B65 removed. FLAG_SECURE reaches the window through
// addFlags/setFlags; setRecentsScreenshotEnabled is the API-33+ way to drop the recents
// thumbnail without also blocking screenshots. Both are named, because B65 withdrew both and a
// gate that caught only one would let the thumbnail decision be re-made in silence.
var screenshotBlockMarkers = []string{"FLAG_SECURE", "setRecentsScreenshotEnabled"}

// ---------------------------------------------------------------------------
// The screen-policy artifact.
// ---------------------------------------------------------------------------

// windowRow is one line of android/window-security.tsv:
//
//	screen <TAB> flag_secure <TAB> exclude_from_recents <TAB> why
//
// The table pre-dates B65 and survives it inverted. It exists because PB-SEC-4 named two
// screens by role ("pairing and terminal-peek") and the app has more than two: which of the
// others the decision covers is itself a decision, and a decision with no artifact is one the
// next screen silently opts out of. Post-B65 it records what allowing screenshots EXPOSES,
// screen by screen, which is where an argument to re-add the flag has to start.
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
		t.Fatalf("PB-SEC-4: %s does not exist, so there is no record of what allowing "+
			"screenshots exposes. ADR-007 B65 withdrew the requirement and INVERTED it rather "+
			"than deleting it, and this table is half of that: deleting it would leave the "+
			"decision with no artifact, which is the state the table was written to end: %v",
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
// The honest precondition, inverted with the rest.
// ---------------------------------------------------------------------------

// TestPBSEC4_ThereIsStillAWindowThisDecisionIsAbout.
//
// Before S18 this test failed because the module declared no <activity>: there was no window to
// put a flag on, and it said so as a loud failure rather than a skip, because a skip would have
// left a green run in which "FLAG_SECURE on pairing and terminal-peek screens" read as covered.
//
// It is kept, inverted, for the mirror-image reason. Every assertion below is a NEGATIVE one --
// no source names the markers, no row asks for the protection -- and negative assertions are
// exactly the kind that pass loudest when their subject has been deleted. If the Activity goes,
// "nothing sets FLAG_SECURE" becomes true because nothing sets anything, and this file would
// certify a decision about a window that does not exist.
func TestPBSEC4_ThereIsStillAWindowThisDecisionIsAbout(t *testing.T) {
	activities := 0
	for _, tag := range []string{"activity", "activity-alias"} {
		activities += len(applicationElement(t, "PB-SEC-4").findAll(tag))
	}
	if activities == 0 {
		t.Fatalf("PB-SEC-4/B65: %s declares no <activity>, so there is no window and the "+
			"screenshot decision has no subject. Every other assertion in this file is a "+
			"NEGATIVE one and all of them would now pass vacuously: nothing names FLAG_SECURE "+
			"because nothing names anything. ADR-007 B65 is a decision about what the shipped "+
			"app's window does; with no window there is nothing for it to be about",
			mustRel(t, manifestPath(t)))
	}
}

// ---------------------------------------------------------------------------
// The inversion itself.
// ---------------------------------------------------------------------------

// TestPBSEC4_NoProductionSourceReinstatesTheScreenshotBlock is the load-bearing assertion.
//
// It is the exact inverse of the assertion this file used to make. The old one required at
// least one production site naming FLAG_SECURE or setRecentsScreenshotEnabled, and at most one
// sink; this one requires none. Adding either name back to production Kotlin fails here, and
// the failure names the file and the marker so the person who did it knows what tripped and
// where to read why.
//
// IT SCANS THE RAW SOURCE, COMMENTS INCLUDED, AND THAT IS THE POINT.
//
// The obvious shape is to strip comments first, so the files carrying this decision can name
// what was removed while explaining it. That shape was tried and REJECTED, because the
// inversion changes which way the scanner's mistakes fall. stripKotlinComments (s16_wiring)
// is not string-literal-aware: a `//` inside a string literal blanks the rest of that LINE.
// Under the old POSITIVE assertion that bug was fail-safe -- hiding a real sink made the test
// demand one and fail. Under this NEGATIVE one it is fail-open, and it is demonstrable:
//
//	val u = "http://x"; val f = WindowManager.LayoutParams.FLAG_SECURE
//
// is live code that a stripping scan does not see. So the strip is gone, and with it the whole
// class of "is this text code or prose" mistakes; what is left is a scan that cannot pass by
// being fooled. The price is that production Kotlin may not spell the two identifiers even in
// prose, which SecureWindow.kt says in the place a reader will look and which costs nothing:
// B65, android/window-security.tsv and this file all name them, so a grep still lands on the
// explanation. A gate weakened to tolerate its own documentation would tolerate a real
// reinstatement sitting next to some.
//
// TEST SOURCES ARE OUT OF SCOPE, narrowly and necessarily. PhoneActivityWindowTest asserts the
// window does NOT carry FLAG_SECURE, which it cannot do without naming the constant, and
// nothing under src/test ships.
func TestPBSEC4_NoProductionSourceReinstatesTheScreenshotBlock(t *testing.T) {
	files := kotlinFiles(t, kotlinMainRoot(t))
	if len(files) == 0 {
		t.Fatalf("PB-SEC-4/B65: no .kt file under %s, so a scan for FLAG_SECURE has nothing to "+
			"scan and this assertion cannot fail", mustRel(t, kotlinMainRoot(t)))
	}

	var sites []string
	for _, f := range files {
		src := readFileOrFail(t, f, "PB-SEC-4")
		for _, m := range screenshotBlockMarkers {
			if strings.Contains(src, m) {
				sites = append(sites, mustRel(t, f)+" names "+m)
			}
		}
	}
	sort.Strings(sites)

	if len(sites) > 0 {
		t.Errorf("PB-SEC-4/B65: production Kotlin reinstates the screenshot block at %d "+
			"site(s):\n\t%s\n\nTHE SHIPPED APP ALLOWS SCREENSHOTS. This is a product decision "+
			"the owner made on 2026-07-26 and it is recorded in docs/adr/ADR-007-remote-access.md "+
			"under B65 -- READ IT BEFORE REMOVING THIS ASSERTION. Its short form: the flag is a "+
			"compositor hint, it is not attested, it stops no camera pointed at the screen, and "+
			"an accessibility service reads the rendered screen regardless, so it stopped an app "+
			"that could already screenshot and stopped nothing else -- while blocking users of a "+
			"developer tool from sharing terminal output. If the decision is being reversed, the "+
			"ADR entry and %s are reversed with it and this test is inverted again; if it is not "+
			"being reversed, this is the drift the inversion exists to catch.\n\nIF THIS IS A "+
			"COMMENT rather than a call: production Kotlin may not spell these two identifiers "+
			"at all, prose included. The scan deliberately does not strip comments -- a negative "+
			"assertion that has to tell code from prose is one that passes when it guesses wrong "+
			"-- so describe the API instead and let B65 carry the name",
			len(sites), strings.Join(sites, "\n\t"), mustRel(t, windowPolicyPath(t)))
	}
}

// TestPBSEC4_ThePolicyRecordsScreenshotsAreAllowedOnEveryScreen.
//
// The table's data must agree with the decision its prose argues for. An appendix that
// contradicts the body is a recorded defect class in this project (ADR-007 B62(3)), and a table
// reading `true` beside a sink that sets nothing is that defect in its purest form: it would
// describe protection the app does not apply.
func TestPBSEC4_ThePolicyRecordsScreenshotsAreAllowedOnEveryScreen(t *testing.T) {
	rows := readWindowPolicy(t)
	for _, r := range rows {
		for _, col := range []struct{ name, value string }{
			{"flag_secure", r.FlagSec},
			{"exclude_from_recents", r.NoRecents},
		} {
			if col.value == "false" {
				continue
			}
			t.Errorf("PB-SEC-4/B65: %s:%d marks the %s screen %s=%q, want \"false\". Nothing in "+
				"the app sets either protection any more (ADR-007 B65), so a row asking for one "+
				"describes a control that is not there. If this row is arguing for the flag to "+
				"come back, that argument belongs in the ADR: reversing the decision means "+
				"reversing B65, this column, and the source assertion above, together",
				mustRel(t, windowPolicyPath(t)), r.Line, r.Screen, col.name, col.value)
		}
	}
}

// ---------------------------------------------------------------------------
// The join, which keeps the table from rotting in either direction.
// ---------------------------------------------------------------------------

// TestPBSEC4_ThePolicyStillCoversTheTwoScreensTheRequirementNamed is the first direction: a
// screen the requirement named, with no row, fails.
//
// It survives the withdrawal because those two screens are the two the decision COSTS
// something on, and B65 answers them individually rather than overwriting them: the pairing
// screen shows the SAS, and the peek shows content S15 seals at rest. A table that quietly lost
// either row would have dropped the two arguments the decision had to answer.
//
// The roles are matched by substring so the table may use whatever screen names the app has.
func TestPBSEC4_ThePolicyStillCoversTheTwoScreensTheRequirementNamed(t *testing.T) {
	rows := readWindowPolicy(t)

	// THE SECOND ROLE IS AMENDED AND NOT DROPPED, which is the whole of what this change is.
	// The requirement named the terminal peek because the peek was where session content was
	// shown; `ADR-009-structured-chat-interaction.md` (3) deletes that screen at slice I1's exit
	// and (1) puts the same content -- what the agent said, ran and changed -- on the session
	// detail. So the ARGUMENT survives its screen and moves with it, and this table asks the
	// table of record to keep answering it. Dropping the role because the screen was renamed out
	// of existence would drop B65's answer along with the question it answered, which is exactly
	// what the test below this one exists to prevent from the other direction.
	needed := map[string][]string{
		"pairing":        {"pair"},
		"session detail": {"detail", "session"},
	}
	for role, needles := range needed {
		found := false
		for i := range rows {
			lower := strings.ToLower(rows[i].Screen)
			for _, n := range needles {
				if strings.Contains(lower, n) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			t.Errorf("PB-SEC-4/B65: %s has no row for the %s screen. The withdrawn requirement "+
				"named it explicitly, and it is one of the two rows that carried a SPECIFIC "+
				"argument against allowing screenshots -- the SAS on the pairing screen, the "+
				"terminal grid on the peek. B65 answers both rather than overwriting them, and "+
				"dropping the row would drop the answer with the question",
				mustRel(t, windowPolicyPath(t)), role)
		}
	}
}

// kotlinTypeDecl matches a top-level Kotlin type declaration's name.
var kotlinTypeDecl = regexp.MustCompile(`(?m)^\s*(?:@\w+\s+)*(?:public\s+|internal\s+|private\s+|abstract\s+|open\s+|sealed\s+|data\s+|value\s+|annotation\s+)*(?:class|object|interface)\s+([A-Z]\w*)`)

// TestPBSEC4_EveryPolicyRowNamesAScreenTheAppStillHas is the second direction: a row for a
// screen that no longer exists fails.
//
// WITHOUT IT THE TABLE ROTS SILENTLY. Post-inversion every other assertion here is negative --
// no source names the markers, no row reads true -- and a row naming a screen that was deleted
// satisfies all of them. The table would go on reciting the cost of a decision on screens the
// app no longer has, which is the same defect as an appendix contradicting the body, arrived at
// from the other side.
//
// The join is by NAME: a row's snake_case screen resolves to the PascalCase Kotlin declaration
// it is named after (terminal_peek -> TerminalPeek, machine_pane -> MachinePane), matched as a
// prefix so a screen may be declared as SettingsScreen or PairingFlow rather than as the bare
// noun. Deleting or renaming the model fails the row.
//
// WHAT THIS DIRECTION DOES NOT CATCH, stated so the pair is not read as more than it is: the
// converse general form -- a NEW screen that has no row -- is enforced above only for the two
// screens the requirement named. Enforcing it for every screen would need a registry of the
// app's screens, and this module has none: `ui/` holds screen models beside row models, error
// routing and the facade bridge, with no mechanical line between them. Inventing one inside
// this gate would produce a rule a new screen escapes by choosing a different name, which is
// worse than the stated limit. The header of window-security.tsv carries the obligation in
// prose: a later surface that wants different treatment has to argue for it in that file.
func TestPBSEC4_EveryPolicyRowNamesAScreenTheAppStillHas(t *testing.T) {
	rows := readWindowPolicy(t)

	declared := map[string]string{} // type name -> file it is declared in
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := stripKotlinComments(readFileOrFail(t, f, "PB-SEC-4"))
		for _, m := range kotlinTypeDecl.FindAllStringSubmatch(src, -1) {
			if _, seen := declared[m[1]]; !seen {
				declared[m[1]] = mustRel(t, f)
			}
		}
	}
	if len(declared) == 0 {
		t.Fatalf("PB-SEC-4/B65: no Kotlin type declaration found under %s, so every row would "+
			"resolve to nothing and this join could only fail for the wrong reason",
			mustRel(t, kotlinMainRoot(t)))
	}

	for _, r := range rows {
		want := pascalCase(r.Screen)
		if want == "" {
			t.Errorf("PB-SEC-4/B65: %s:%d has an empty screen name",
				mustRel(t, windowPolicyPath(t)), r.Line)
			continue
		}
		matched := false
		for name := range declared {
			if strings.HasPrefix(name, want) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("PB-SEC-4/B65: %s:%d names the screen %q, which resolves to no Kotlin "+
				"declaration under %s (looked for a type whose name starts with %q). Either the "+
				"screen was removed and the row is stale, or it was renamed and the row was not. "+
				"A stale row satisfies every other assertion in this file -- they are all "+
				"negative -- so this is the only thing standing between the table and a list of "+
				"screens that do not exist",
				mustRel(t, windowPolicyPath(t)), r.Line, r.Screen, mustRel(t, kotlinMainRoot(t)), want)
		}
	}
}

// pascalCase turns a snake_case screen name into the Kotlin type name it is named after.
func pascalCase(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}
