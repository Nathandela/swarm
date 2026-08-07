package gate

// FAILING-FIRST (TDD RED, GG-5) for the obsidian migration plan's phase O6.3 -- PREDICTIVE BACK.
//
// O6.3: "predictive back honoured on drill-downs (scale to 90%, 8dp margin, 35% crossfade), within
// the existing programmatic-Views nav."
//
// WHY A GO GATE AND NOT ONLY A KOTLIN TEST. This feature has a manifest half and a code half, and
// EITHER ONE ALONE IS SILENT. `android:enableOnBackInvokedCallback` is what makes the platform
// dispatch the gesture's progress at all: without it the callbacks below are declared, compiled,
// unit-tested green, and never invoked by anything -- which is the exact failure mode this
// manifest already records twice in its own comments, for the boot receiver ("declared and never
// invoked, which is silent rather than broken") and for the messaging service ("dead code with a
// full unit-test suite: it compiles, its tests pass, and the OS never calls a line of it"). A
// Robolectric assertion cannot see the attribute, and the attribute cannot see whether anything
// implements the callbacks. This file reads both and joins them.
//
// WHAT IT ASSERTS:
//
//	(a) THE MANIFEST OPTS IN, on <application>, with the literal `true`.
//	(b) THE ACTIVITY IMPLEMENTS THE PROGRESS HALF. `handleOnBackPressed` alone is the
//	    pre-predictive contract: it fires once, at the end, and a gesture that has been dragged
//	    halfway across the screen has already been shown nothing. The three progress members are
//	    what turn a commit into a preview.
//	(c) THE THREE NUMBERS ARE THE PLAN'S. 90%, 8dp and 35% are read out of the plan and joined to
//	    the constants, so neither can drift from the other in silence.
//
// PB-SEC-11 IS NOT RE-ASSERTED HERE AND THAT IS DELIBERATE. The new callbacks live in the same
// exported, LAUNCHER-filtered Activity as the old one, and `s18_sec11_exported_test.go` already
// scans that file BY NAME for every session verb -- so the rule that the back gesture may touch
// local screen state and nothing else is enforced over this change by the gate that was written
// for it. A second copy of that scan here would be a second chance to get it wrong.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	o6ActivityFile = "dev/swarm/phone/PhoneActivity.kt"
	o6MotionFile   = "dev/swarm/phone/ui/kit/Motion.kt"
)

func o6KotlinSource(t *testing.T, rel string) string {
	t.Helper()
	return readFileOrFail(t, filepath.Join(kotlinMainRoot(t), filepath.FromSlash(rel)),
		"obsidian-migration-plan O6.3")
}

// ---------------------------------------------------------------------------
// (a) The manifest opts in.
// ---------------------------------------------------------------------------

// o6BackInvokedAttr reads the opt-in off the <application> tag, with its value.
//
// THE VALUE IS CAPTURED RATHER THAN THE ATTRIBUTE'S PRESENCE, because
// `enableOnBackInvokedCallback="false"` is a real thing somebody writes while debugging and it is
// the whole feature switched off in a way that reads, at a glance, like the feature switched on.
var o6BackInvokedAttr = regexp.MustCompile(`android:enableOnBackInvokedCallback\s*=\s*"([^"]*)"`)

// o6ManifestOptIn reports the attribute's value in the given manifest source, or "".
//
// It takes the SOURCE so the control below can feed a perturbed one to the same function the real
// assertion calls.
func o6ManifestOptIn(src string) string {
	m := o6BackInvokedAttr.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	return m[1]
}

func o6ManifestSource(t *testing.T) string {
	t.Helper()
	return readFileOrFail(t,
		filepath.Join(appModule(t), "src", "main", "AndroidManifest.xml"),
		"obsidian-migration-plan O6.3")
}

// TestO63_TheManifestOptsIntoPredictiveBack is constraint (a).
func TestO63_TheManifestOptsIntoPredictiveBack(t *testing.T) {
	src := o6ManifestSource(t)
	switch got := o6ManifestOptIn(src); got {
	case "true":
		// The opt-in has to be on <application> and not on the <activity>: the app has ONE
		// Activity, so the two would be equivalent today and would stop being equivalent the day
		// a second one is declared, silently, for whichever one nobody edited.
		app := strings.Index(src, "<application")
		attr := strings.Index(src, "android:enableOnBackInvokedCallback")
		end := strings.Index(src[app:], ">")
		if app < 0 || attr < app || (end >= 0 && attr > app+end) {
			t.Errorf("O6.3: android:enableOnBackInvokedCallback is declared outside the "+
				"<application> tag. The app has one Activity today, so the two spellings behave "+
				"identically and stop doing so the day a second one is declared -- for whichever "+
				"of them nobody edited.")
		}
	case "":
		t.Errorf("O6.3: AndroidManifest.xml declares no android:enableOnBackInvokedCallback. " +
			"Without it the platform never dispatches the gesture's progress, so every " +
			"OnBackAnimationCallback member the Activity implements is declared, compiled, " +
			"unit-tested green and invoked by nothing -- which is the failure this same manifest " +
			"records for the boot receiver (\"declared and never invoked, which is silent rather " +
			"than broken\") and for the messaging service (\"dead code with a full unit-test " +
			"suite\").")
	default:
		t.Errorf("O6.3: android:enableOnBackInvokedCallback is %q. Anything but \"true\" is the "+
			"whole feature switched off in a way that reads, at a glance, like the feature "+
			"switched on.", got)
	}
}

// TestO63_TheManifestReaderRefusesPerturbedInput is the negative control for the reader above.
// IN MEMORY, on a copy of the real manifest; nothing on disk is touched.
func TestO63_TheManifestReaderRefusesPerturbedInput(t *testing.T) {
	src := o6ManifestSource(t)
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"the attribute removed", strings.Replace(src,
			`android:enableOnBackInvokedCallback="true"`, "", 1), ""},
		{"the attribute switched off", strings.Replace(src,
			`android:enableOnBackInvokedCallback="true"`,
			`android:enableOnBackInvokedCallback="false"`, 1), "false"},
	} {
		if got := o6ManifestOptIn(tc.src); got != tc.want {
			t.Errorf("O6.3: with %s the reader answered %q, want %q. A reader that reports the "+
				"same thing whatever it is fed makes the assertion above pass while saying "+
				"nothing.", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// (b) The Activity implements the progress half.
// ---------------------------------------------------------------------------

// o6ProgressMembers are the three members that turn a back COMMIT into a back PREVIEW.
//
// `handleOnBackPressed` is deliberately not in the list: it is the pre-predictive contract and it
// is already implemented, so requiring it would be an assertion that passes today and says nothing
// about this phase. What is new is that the gesture is watched while it happens and that an
// abandoned one is put back.
var o6ProgressMembers = map[string]string{
	"handleOnBackStarted":    "the gesture began: the preview is set up here, once",
	"handleOnBackProgressed": "the finger moved: this is the frame the user actually sees",
	"handleOnBackCancelled":  "the finger let go short of the threshold, and the screen the user " +
		"decided to stay on has to be put back exactly as it was",
}

// o6ProgressMembersIn reports which of the three a source declares. Source in, so the control can
// feed a perturbed one to the same function.
func o6ProgressMembersIn(src string) map[string]bool {
	code := kotlinCodeOnly(src)
	out := map[string]bool{}
	for member := range o6ProgressMembers {
		if regexp.MustCompile(`override\s+fun\s+` + member + `\s*\(`).MatchString(code) {
			out[member] = true
		}
	}
	return out
}

// TestO63_TheDrillDownWatchesTheGestureRatherThanOnlyItsEnd is constraint (b).
func TestO63_TheDrillDownWatchesTheGestureRatherThanOnlyItsEnd(t *testing.T) {
	got := o6ProgressMembersIn(o6KotlinSource(t, o6ActivityFile))
	for member, why := range o6ProgressMembers {
		if !got[member] {
			t.Errorf("O6.3: %s overrides no `%s` (%s). `handleOnBackPressed` alone fires once, at "+
				"the end -- so a gesture dragged halfway across the screen has been shown nothing, "+
				"and the user has no way to tell whether letting go will leave the session they "+
				"are reading.", o6ActivityFile, member, why)
		}
	}
}

// TestO63_TheProgressReaderRefusesPerturbedInput is the negative control for that reader.
func TestO63_TheProgressReaderRefusesPerturbedInput(t *testing.T) {
	src := o6KotlinSource(t, o6ActivityFile)
	base := o6ProgressMembersIn(src)
	if len(base) == 0 {
		t.Fatalf("O6.3: the reader found no progress member in the real source, so every " +
			"perturbation below would differ from an empty answer for the wrong reason")
	}

	dropped := strings.Replace(src, "override fun handleOnBackProgressed", "private fun ignored", 1)
	if o6ProgressMembersIn(dropped)["handleOnBackProgressed"] {
		t.Errorf("O6.3: the reader still reports handleOnBackProgressed in a source where the " +
			"override was renamed away. It is matching prose rather than a declaration.")
	}

	// A KDoc naming the member is not an implementation of it, and prose is where these names
	// appear most often.
	commented := strings.Replace(src, "override fun handleOnBackProgressed",
		"// override fun handleOnBackProgressed", 1)
	if o6ProgressMembersIn(commented)["handleOnBackProgressed"] {
		t.Errorf("O6.3: the reader counts a COMMENTED-OUT override as an implementation. A " +
			"constraint a comment can satisfy is one the next thorough KDoc turns off.")
	}
}

// ---------------------------------------------------------------------------
// (c) The three numbers are the plan's.
// ---------------------------------------------------------------------------

// o6BackGeometry joins each constant in Motion.kt to the phrase the plan states it with.
//
// THE JOIN IS TO THE DOCUMENT AND NOT TO A COPY OF IT, which is the arrangement type.xml's
// `origin:` comments established: the value is read out of the plan at test time and compared, so
// a number edited in Kotlin without the plan agreeing fails, and a plan that changes the
// choreography takes the build with it.
var o6BackGeometry = []struct {
	Constant string
	Phrase   string // as the plan writes it
	Want     string // the literal the constant must carry
}{
	{"PREDICTIVE_BACK_SCALE", "scale to 90%", "0.90f"},
	{"PREDICTIVE_BACK_MARGIN_DP", "8dp margin", "8f"},
	{"PREDICTIVE_BACK_CROSSFADE_AT", "35% crossfade", "0.35f"},
}

// o6ConstantValue reads `const val NAME = <literal>` out of a source.
func o6ConstantValue(src, name string) string {
	m := regexp.MustCompile(`const\s+val\s+` + name + `\s*(?::\s*\w+\s*)?=\s*(\S+)`).
		FindStringSubmatch(kotlinCodeOnly(src))
	if m == nil {
		return ""
	}
	return m[1]
}

// TestO63_TheChoreographyIsThePlansOwnNumbers is constraint (c).
func TestO63_TheChoreographyIsThePlansOwnNumbers(t *testing.T) {
	motion := o6KotlinSource(t, o6MotionFile)
	plan := strings.Join(strings.Fields(readFileOrFail(t,
		filepath.Join(repoRoot(t), filepath.FromSlash(o6PlanFile)),
		"obsidian-migration-plan O6.3")), " ")

	for _, row := range o6BackGeometry {
		if !strings.Contains(plan, row.Phrase) {
			t.Errorf("O6.3: %s no longer says %q, and %s is joined to it. Either the plan changed "+
				"the choreography and the constant has to follow, or the constant names a number "+
				"the plan never asked for.", o6PlanFile, row.Phrase, row.Constant)
		}
		if got := o6ConstantValue(motion, row.Constant); got != row.Want {
			t.Errorf("O6.3: %s declares `const val %s = %s`, and the plan's %q wants %s. A "+
				"predictive-back preview whose numbers are not the recorded ones is a gesture "+
				"that behaves differently from every other app on the handset, for a reason "+
				"nobody wrote down.", o6MotionFile, row.Constant, got, row.Phrase, row.Want)
		}
	}
}
