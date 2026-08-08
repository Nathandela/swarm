package gate

// FAILING-FIRST (TDD RED, GG-5) for the obsidian migration plan's phase O6.2 -- THE HAPTICS GATE.
//
// O6.2 asks for a haptics VOCABULARY: six signals a person learns by their rhythm, not six calls
// to `vibrate(50)` scattered across the surface files. The plan states the fence in one sentence --
// "a gate asserts no `VibrationEffect` constructed outside Haptics.kt" -- and this file is it,
// widened by one word for the reason the sweep fence was widened: `VibrationEffect` is only one of
// the platform's names for this hardware, and a fence that knows one name is a fence somebody
// walks past by using another.
//
// WHY A FENCE AT ALL, when six constants in a file would look like enough. A vibration is the one
// output in this app with NO visual trace: it does not appear in a screenshot, it does not appear
// in a view hierarchy dump, and a Robolectric assertion about a screen cannot see it. So the
// properties that make a vocabulary a vocabulary -- that the six rhythms are the only six, that
// each one means the same thing everywhere, and that every one of them respects the user's own
// haptics setting -- are enforceable ONLY by there being exactly one implementation. That is the
// same argument D8.2 makes for the sweep, and it is stronger here: a stray sweep is at least
// visible to anyone who looks at the screen.
//
// THE THREE THINGS THIS FILE ASSERTS:
//
//	(a) ONE CONSTRUCTION SITE. No production Kotlin outside `ui/kit/Haptics.kt` names the
//	    vibration hardware -- `Vibrator`, `VibratorManager`, `VibrationEffect`,
//	    `VibrationAttributes`, `vibrate` -- and none of them reaches the platform's OTHER
//	    vibration path either (`performHapticFeedback` / `HapticFeedbackConstants`), which the
//	    vocabulary rule cannot see because it is not spelled like the hardware.
//	(b) THE VOCABULARY IS SIX AND THE SIX ARE THE PLAN'S. The signal names are read out of the
//	    enum and compared against the plan's own list, so a seventh signal is a decision somebody
//	    has to make in the open and a renamed one cannot drift away from the document that
//	    commissioned it.
//	(c) THE USER'S SETTING IS CONSULTED. The one implementation reads the platform's haptic
//	    setting and attributes its vibrations to TOUCH, which is what makes the system's own
//	    suppression apply. Source-level, because `HapticsTest` runs the behaviour and this
//	    catches the case where a later edit keeps the test green by consulting the setting
//	    somewhere the test happens to look.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. Every scan starts at the app module, so it cannot
// descend into `.claude/worktrees/`, which holds other agents' full checkouts and has already made
// four gates in this repository report findings about somebody else's private copy as findings
// about this tree.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// o6HapticsFile is the ONE file allowed to name the vibration hardware, relative to the scanned
// production-Kotlin root. Whole-path equality, for isExemptFile's reason: a `Haptics.kt` in some
// other package is an entirely different file from the one whose six rhythms this gate trusts.
const o6HapticsFile = "dev/swarm/phone/ui/kit/Haptics.kt"

func o6IsHapticsFile(relPath string) bool {
	return filepath.ToSlash(relPath) == o6HapticsFile
}

// ---------------------------------------------------------------------------
// (a) The construction site.
// ---------------------------------------------------------------------------

// o6HapticVocabulary is the fence's subject, in the VOCABULARY form s23_motion_test.go argued for:
// a list of NAMES fails open on every name nobody thought of, so the rule is a word.
//
// ONE WORD, AND IT IS THE HARDWARE'S. `vibrat` catches `Vibrator`, `VibratorManager`,
// `VibrationEffect`, `VibrationAttributes` and a bare `vibrate(` in one rule. It has no innocent
// use in this app's production Kotlin: nothing else in the product vibrates, and the model's word
// for the state that EARNS a signal is the state's own name (`promoted`, `needs_input`), never the
// effect it collects -- the same naming discipline the sweep fence depends on.
var o6HapticVocabulary = regexp.MustCompile(`(?i)vibrat`)

// o6HapticAlwaysForbidden is the platform's OTHER way to make the phone buzz, which the vocabulary
// rule above cannot reach because it is not spelled like the hardware.
//
// EVERY ENTRY IS AN ADMISSION THAT THE WORD DOES NOT REACH IT, exactly as animatorAlwaysForbidden
// records for the frame-driving APIs, and it is held to constructs there is evidence for rather
// than grown by imagining APIs. `View.performHapticFeedback` is a real second path: it takes a
// `HapticFeedbackConstants` value, it produces a vibration the user feels, it is available on
// every View in the app, and it would give a control a seventh signal nobody wrote down.
var o6HapticAlwaysForbidden = []string{"performHapticFeedback", "HapticFeedbackConstants"}

// o6HapticFault returns the identifier on one COMMENT-STRIPPED line that O6.2 forbids, or "".
//
// THE SCAN AND THE JUDGEMENT ARE ONE FUNCTION so that every control below feeds its probe to the
// same one the repository assertion calls. A control that rebuilds the comparison inline proves
// something about the copy and nothing about the assertion; this package has shipped that mistake
// before.
//
// The permitted-receiver test is animatorPermitted's SHAPE and not its call: that helper hardcodes
// `Motion.` as the receiver, and the routing this fence asks for is `Haptics.`. The whole-word rule
// is what matters and it is reimplemented here for one receiver rather than generalised, because a
// generalised version would be a second parameter nobody reads at either call site.
func o6HapticFault(line string) string {
	for _, loc := range animatorIdentifier.FindAllStringIndex(line, -1) {
		id := line[loc[0]:loc[1]]
		if o6Contains(o6HapticAlwaysForbidden, id) {
			return id
		}
		if !o6HapticVocabulary.MatchString(id) {
			continue
		}
		return id
	}
	return ""
}

func o6Contains(haystack []string, want string) bool {
	for _, v := range haystack {
		if v == want {
			return true
		}
	}
	return false
}

// o6ScanHapticsIn walks root and reports every forbidden match in every .kt file except Haptics.kt.
// root is a PARAMETER, exactly as scanAnimatorConstructsIn's is, so the discrimination (one file is
// exempt, everything else is not) can be exercised on a tree this file controls.
func o6ScanHapticsIn(t *testing.T, root string) []animatorConstruct {
	t.Helper()
	var out []animatorConstruct
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".kt") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if o6IsHapticsFile(rel) {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("O6.2: cannot read %s: %v", path, rerr)
		}
		for i, line := range strings.Split(kotlinCodeOnly(string(raw)), "\n") {
			if id := o6HapticFault(line); id != "" {
				out = append(out, animatorConstruct{
					File:       rel,
					Line:       i + 1,
					Identifier: id,
					Text:       strings.TrimSpace(line),
				})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("O6.2: walking %s: %v", root, err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// TestO62_TheHardwareIsNamedOnlyInsideHaptics is constraint (a).
func TestO62_TheHardwareIsNamedOnlyInsideHaptics(t *testing.T) {
	root := productionKotlinRoot(t)
	if n := countScannedKotlinFiles(t, root); n == 0 {
		t.Fatalf("O6.2: scanned zero .kt file(s) under %s; a zero-findings result below would be "+
			"vacuous", mustRel(t, root))
	}
	if !exists(filepath.Join(root, filepath.FromSlash(o6HapticsFile))) {
		t.Fatalf("O6.2: %s does not exist, so this fence has no subject: every scan below would "+
			"report a clean tree because nothing in the app vibrates at all. The plan's O6.2 asks "+
			"for a six-signal vocabulary owning ALL vibration, and a fence around an empty room "+
			"is the failure mode this package calls vacuous.", o6HapticsFile)
	}
	found := o6ScanHapticsIn(t, root)
	if len(found) == 0 {
		return
	}
	var lines []string
	for _, f := range found {
		lines = append(lines, f.File+":"+itoa(f.Line)+": `"+f.Identifier+"` in: "+f.Text)
	}
	t.Errorf("O6.2: %d vibration construct(s) outside %s. A vibration leaves no visual trace -- "+
		"not in a screenshot, not in a view dump, not in a Robolectric hierarchy -- so the only "+
		"thing that makes the six rhythms a VOCABULARY rather than six unrelated buzzes is that "+
		"there is exactly one implementation. A control asks for one as "+
		"`Haptics.play(context, Haptics.Signal.SENT)`:\n\t%s",
		len(found), o6HapticsFile, strings.Join(lines, "\n\t"))
}

// TestO62_AVibrationRaisedAnywhereElseIsCaught is the positive control, on synthetic sources.
//
// The repository cannot demonstrate rejection -- there is one implementation and it is in the
// exempt file -- which is the same limitation s23_motion_test.go documents for the animator fence
// and o4_sweep_test.go for the sweep. Each row is a plausible way to reintroduce a buzz outside
// the one place the vocabulary is enforced.
func TestO62_AVibrationRaisedAnywhereElseIsCaught(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{"the service, reached directly", `val v = context.getSystemService(Vibrator::class.java)`},
		{"the modern service", `val vm = context.getSystemService(VibratorManager::class.java)`},
		{"an effect built at a call site", `v.vibrate(VibrationEffect.createOneShot(50L, 255))`},
		{"a bare legacy call", `vibrate(40L)`},
		{"attributes chosen at a call site", `VibrationAttributes.createForUsage(USAGE_TOUCH)`},
		{"the platform's other path", `control.performHapticFeedback(HapticFeedbackConstants.CONFIRM)`},
		{"an import of somebody else's", `import dev.swarm.phone.fx.VibrationKit`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "package dev.swarm.phone.ui\n\nfun press() {\n    " + tc.line + "\n}\n"
			root := pbds8SyntheticTree(t, "dev/swarm/phone/ui/Surface.kt", source)
			if found := o6ScanHapticsIn(t, root); len(found) == 0 {
				t.Fatalf("O6.2: pattern %q passed the fence. A vibration raised outside %s has "+
					"nothing holding it to the six rhythms, and nothing holding it to the user's "+
					"own haptics setting either.", tc.name, o6HapticsFile)
			}
		})
	}
}

// TestO62_TheExemptFileIsExemptByPathAndNotByName is the discrimination this fence rests on, and
// it is a NEGATIVE CONTROL in the strict sense this package means: the same scan function, fed a
// tree that differs from the real one in exactly the property under test.
//
// Two probes, because the two failures are opposite. A fence that exempted by BASENAME would let
// `ui/fx/Haptics.kt` -- a second implementation in another package, with its own rhythms and its
// own opinion about the user's setting -- through untouched. A fence that exempted nothing would
// flag the real implementation and get widened until it flagged nothing at all.
func TestO62_TheExemptFileIsExemptByPathAndNotByName(t *testing.T) {
	const body = "package p\n\nfun buzz() {\n    val v = context.getSystemService(Vibrator::class.java)\n}\n"

	real := pbds8SyntheticTree(t, o6HapticsFile, body)
	if found := o6ScanHapticsIn(t, real); len(found) != 0 {
		t.Errorf("O6.2: the fence flags %s, which is the one file that owns the hardware. A guard "+
			"that refuses the documented right answer gets widened until it refuses nothing: %v",
			o6HapticsFile, found)
	}

	impostor := pbds8SyntheticTree(t, "dev/swarm/phone/fx/Haptics.kt", body)
	if found := o6ScanHapticsIn(t, impostor); len(found) == 0 {
		t.Errorf("O6.2: a second `Haptics.kt` in another package passed the fence. The exemption "+
			"is a PATH and not a name -- a file that merely shares the basename is a second "+
			"vocabulary, with its own six rhythms and its own answer about whether the user "+
			"turned haptics off.")
	}
}

// TestO62_TheOneWayToAskForASignalIsNotFlagged is the other half of the discrimination, and
// without it the tests above are satisfied by a fence that refuses every line in the app.
func TestO62_TheOneWayToAskForASignalIsNotFlagged(t *testing.T) {
	for _, line := range []string{
		`Haptics.play(control.context, Haptics.Signal.SENT)`,
		`Haptics.play(activity, Haptics.Signal.NEEDS_YOU)`,
		`import dev.swarm.phone.ui.kit.Haptics`,
		// The STATE that earns a signal is not the signal. The model's words carry none of the
		// hardware's vocabulary, which is what keeps this fence usable at the call sites that
		// matter.
		`val promoted = TriageInboxScreen.promotions(inboxDrawn, it)`,
		`if (planned != null) return dispatchPress(control, app, planned)`,
	} {
		if id := o6HapticFault(line); id != "" {
			t.Errorf("O6.2: the fence flags %q on the identifier %q. That is what a correct call "+
				"site writes.", line, id)
		}
	}
}

// ---------------------------------------------------------------------------
// (b) The vocabulary is six, and the six are the plan's.
// ---------------------------------------------------------------------------

// o6PlanFile is the document that commissions the vocabulary, and o6PlannedSignals is its list --
// "needs-you two-pulse, sent single sharp, completed soft thud, failed double low, sheet-settle
// thud, scroll ratchet tick" -- as the enum names those six become.
//
// THE JOIN IS TO THE PLAN AND NOT TO A COPY OF IT. TestO62_TheSignalListIsThePlansOwn reads the
// phrase out of the document, so this table cannot quietly grow a seventh entry that the plan
// never authorised, and the plan cannot drop one without the build saying so.
const o6PlanFile = "docs/specifications/obsidian-migration-plan.md"

var o6PlannedSignals = map[string]string{
	"NEEDS_YOU":    "needs-you two-pulse",
	"SENT":         "sent single sharp",
	"COMPLETED":    "completed soft thud",
	"FAILED":       "failed double low",
	"SHEET_SETTLE": "sheet-settle thud",
	"SCROLL_TICK":  "scroll ratchet tick",
}

// o6SignalValue matches one `Signal` enum constant: a bare SCREAMING_SNAKE name on its own line
// inside the enum body, with the trailing comma Kotlin requires between constants.
//
// ANCHORED ON THE LINE, exactly as guidedControlValue is, and for a reason measured rather than
// anticipated: an unanchored `[A-Z][A-Z0-9_]*` reads the `S` out of `enum class Signal {` as a
// seventh constant. A declaration is a line; a capital letter is not.
var o6SignalValue = regexp.MustCompile(`(?m)^\s*([A-Z][A-Z0-9_]*)\s*,\s*$`)

// o6Signals reads the Signal enum body out of a Haptics SOURCE.
//
// It takes the source rather than reading the file, so the control below can feed a perturbed one
// to the same function the real assertion calls.
func o6Signals(t *testing.T, src string) []string {
	t.Helper()
	code := kotlinCodeOnly(src)
	start := strings.Index(code, "enum class Signal {")
	if start < 0 {
		t.Fatalf("O6.2: %s declares no `enum class Signal {`. The vocabulary is what makes the "+
			"six rhythms learnable -- one name per meaning, spent at every call site that means "+
			"it -- and without the enum there is nothing for the join below to be true of.",
			o6HapticsFile)
	}
	body := code[start:]
	if end := strings.Index(body, "}"); end >= 0 {
		body = body[:end]
	}
	var out []string
	for _, m := range o6SignalValue.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func o6HapticsSource(t *testing.T) string {
	t.Helper()
	return readFileOrFail(t, filepath.Join(kotlinMainRoot(t), filepath.FromSlash(o6HapticsFile)),
		"obsidian-migration-plan O6.2")
}

// TestO62_TheVocabularyIsTheSixSignalsThePlanNames is constraint (b).
func TestO62_TheVocabularyIsTheSixSignalsThePlanNames(t *testing.T) {
	got := o6Signals(t, o6HapticsSource(t))
	var want []string
	for name := range o6PlannedSignals {
		want = append(want, name)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("O6.2: the vocabulary is\n\t%v\nand the plan commissions\n\t%v\nSix signals is a "+
			"decision about how much a person can learn by feel; a seventh is a decision somebody "+
			"has to make in the open, and a renamed one is a meaning that has drifted from the "+
			"document that asked for it.", got, want)
	}
}

// TestO62_TheSignalListIsThePlansOwn keeps o6PlannedSignals honest. Without it the table above is
// six names this file invented and then checked against itself.
func TestO62_TheSignalListIsThePlansOwn(t *testing.T) {
	// WHITESPACE IS COLLAPSED BEFORE THE SEARCH, and that is not tidying. The plan is prose wrapped
	// at 96 columns, so `completed soft thud` is split across two lines with an indent between the
	// words; a raw substring test would report the phrase missing from a document that says it, and
	// a reader that lies in that direction is one somebody deletes rather than fixes.
	plan := strings.Join(strings.Fields(readFileOrFail(t,
		filepath.Join(repoRoot(t), filepath.FromSlash(o6PlanFile)),
		"obsidian-migration-plan O6.2")), " ")
	for name, phrase := range o6PlannedSignals {
		if !strings.Contains(plan, phrase) {
			t.Errorf("O6.2: %s no longer says %q, and the enum constant %s is joined to it. "+
				"Either the plan changed the vocabulary and this table has to follow, or this "+
				"table names a signal the plan never asked for.", o6PlanFile, phrase, name)
		}
	}
}

// TestO62_TheSignalReaderRefusesPerturbedInput is the negative control for the reader.
//
// IN MEMORY, ON A COPY OF THE REAL SOURCE, and never on disk: the assertion above passes over
// whatever the reader returns, so a reader that returned the same list for a source missing a
// signal would make it pass while saying nothing. Each perturbation is a way the enum could be
// wrong that the join must notice.
func TestO62_TheSignalReaderRefusesPerturbedInput(t *testing.T) {
	src := o6HapticsSource(t)
	base := o6Signals(t, src)
	if len(base) == 0 {
		t.Fatalf("O6.2: the reader found no signal in the real source, so every perturbation " +
			"below would differ from an empty list for the wrong reason")
	}

	dropped := strings.Replace(src, "SHEET_SETTLE,", "", 1)
	if got := o6Signals(t, dropped); len(got) == len(base) {
		t.Errorf("O6.2: the reader returned %d signal(s) from a source with SHEET_SETTLE removed, "+
			"the same count as the real one. It is not reading the enum.", len(got))
	}

	renamed := strings.Replace(src, "SCROLL_TICK", "SCROLL_RATCHET", 1)
	if got := o6Signals(t, renamed); strings.Join(got, ",") == strings.Join(base, ",") {
		t.Errorf("O6.2: the reader returned the same list from a source where SCROLL_TICK was "+
			"renamed. A rename is exactly the drift the join to the plan exists to catch.")
	}
}

// ---------------------------------------------------------------------------
// (c) The user's setting is consulted, and the vibration is attributed to touch.
// ---------------------------------------------------------------------------

// o6SettingWitnesses are the two things the one implementation must do so that a person who has
// turned haptics off feels nothing.
//
// BOTH, NOT EITHER, and they cover different failures. `HAPTIC_FEEDBACK_ENABLED` is the setting a
// user actually toggles and is the half this app can check for itself. `USAGE_TOUCH` is what makes
// the PLATFORM's own suppression apply -- `Vibrator.vibrate` with no attributes is an
// unclassified vibration, and an unclassified vibration is not what the touch-feedback switch
// governs. An implementation with only the first is one OS release away from being wrong; one with
// only the second is trusting a suppression it never verified.
var o6SettingWitnesses = map[string]string{
	"HAPTIC_FEEDBACK_ENABLED": "the setting the user toggles, read by the app for itself",
	"USAGE_TOUCH":             "the attribution that makes the platform's own suppression apply",
}

// TestO62_TheUsersHapticSettingIsConsulted is constraint (c).
//
// SOURCE-LEVEL, ALONGSIDE THE BEHAVIOURAL TEST rather than instead of it. `HapticsTest` turns the
// setting off and asserts nothing vibrates, which is the real assertion; this one catches the edit
// that keeps that test green by consulting the setting on a path the test happens to take while a
// second path skips it, and it catches the removal of the attribution entirely, which no
// Robolectric assertion in this repository can see.
func TestO62_TheUsersHapticSettingIsConsulted(t *testing.T) {
	code := kotlinCodeOnly(o6HapticsSource(t))
	for token, why := range o6SettingWitnesses {
		if !strings.Contains(code, token) {
			t.Errorf("O6.2: %s never names %s (%s). The plan's own clause is that the system "+
				"haptic-disable is honoured; a vocabulary that buzzes a phone whose owner turned "+
				"vibration off is not a quieter product, it is a louder one.",
				o6HapticsFile, token, why)
		}
	}
}
