package gate

// FAILING-FIRST (TDD RED, GG-5) is demonstrated on SYNTHETIC sources for this file, not on the
// repository: PB-DS-8's fence is preventative, and the repository has never contained an
// animator construct outside dev/swarm/phone/ui/kit/Motion.kt (verified by hand, re-verified
// when the exemption was narrowed from the kit package to that one file, and re-verified again
// when the fence was inverted from a name denylist to a vocabulary allowlist: `grep -rniE
// 'animat|transition' android/app/src/main/kotlin/` finds three hits and all three are prose in
// comments, which kotlinCodeOnly strips). A real-repo run can therefore only ever demonstrate
// ACCEPTANCE of a clean tree, the same limitation pbsec3_logdiscrimination_test.go documents for
// the log scan -- so the guard's ability to REJECT is proved here on sources built to contain the
// violation, in the style established there and by scanLogSinksIn's roots parameter.
//
// "PB-DS-8: Motion: Substrate is static, and the exceptions are named." ADR-007 B134 decision 3:
// no decorative animation anywhere. Only the bottom sheet, the push banner and the streaming
// caret move, and dev/swarm/phone/ui/kit/Motion.kt is where they are built -- reduced motion
// (Settings.Global.ANIMATOR_DURATION_SCALE == 0) checked once, at construction, for every one
// of them. A fence with no mechanical form would let a screen build its own ObjectAnimator
// beside a call into Motion and skip that check silently; this is the mechanical form.
//
// SCOPED TO PRODUCTION KOTLIN under android/app/src/main/kotlin, and EXEMPTING EXACTLY ONE FILE
// BY NAME: dev/swarm/phone/ui/kit/Motion.kt. That is where every animator this app runs is built,
// and where the KDoc names the forbidden identifiers in prose.
//
// THE EXEMPTION WAS PACKAGE-SCOPED ("any path segment pair ui/kit") AND THAT PERMITTED WHAT THE
// FENCE EXISTS TO FORBID. Under it, any component beside Motion.kt could construct a raw
// ObjectAnimator, bypass Motion.duration entirely and stay green -- while Motion.kt's own KDoc
// claimed "there is no second path that constructs an animator without it". A sibling kit file is
// the LIKELIEST place for that to happen, not the least likely: it is where the components that
// need animation are written. The test asserting the package-wide exemption is now inverted
// (TestPBDS8_ASiblingKitFileIsNotExempt), and TestPBDS8_MotionKtIsTheOneExemption is its control.
//
// WHAT A KIT COMPONENT DOES INSTEAD: call Motion's primitives (translateY, translateX,
// colorTransition), which return the animators already carrying the reduced-motion duration, or
// route a duration of its own through Motion.duration. A component that needs to name the returned
// type spells it android.animation.Animator; naming ObjectAnimator or ValueAnimator is flagged,
// which is a cost of matching bare identifiers and is accepted -- the alternative is a pattern that
// tries to tell a construction from a type reference and gets it wrong in the permissive direction.
//
// THE SIX-NAME DENYLIST THIS FILE USED TO CARRY IS GONE; see animatorVocabulary for what replaced
// it and why a longer list of names was not the repair.
//
// KNOWN, NOT FIXED HERE: if a surface file (PhoneSurface.kt, SessionScreens.kt, TriageInbox.kt,
// ...) is ever found constructing an animator directly, this gate must NOT grow a per-file
// allowlist for it -- PB-DS-11 in S24 owns cleaning surface code, and an allowlist here is the
// exact defect PB-DS-11 was reassigned away from S23 to avoid (docs/specifications/
// remote-phaseB-requirements.md §6.20, S23/S24 row). At the time of writing no such violation
// exists; see TestPBDS8_NoAnimatorIsConstructedOutsideMotion's own comment for the measurement.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The fence: an allowlist over the platform's animation vocabulary.
// ---------------------------------------------------------------------------

// WHY THIS IS NOT A LIST OF FORBIDDEN NAMES, WHICH IS WHAT IT WAS TWICE.
//
// It held six regexps -- ObjectAnimator, ValueAnimator, ViewPropertyAnimator, `.animate(`,
// `startAnimation(`, TransitionManager -- described here as "the team's exact list". Two APIs that
// do precisely what PB-DS-8 forbids walked straight through it:
//
//	AnimatorInflater.loadAnimator(context, R.animator.slide).start()
//	SpringAnimation(view, DynamicAnimation.ALPHA).start()
//
// Both construct an animation outside Motion.kt, and neither consults ANIMATOR_DURATION_SCALE --
// which is the single thing the requirement asks this fence to guarantee. The first repair was to
// add those two names and seven of their neighbours. THAT REPAIR IS THE DEFECT REPEATING: it fixes
// the two spellings a reviewer happened to name and leaves the class exactly where it was, because
// a denylist of names fails open on every name nobody thought of, and "nobody thought of it" is
// the normal case for a platform carrying three animation frameworks at once (android.animation,
// android.view.animation, androidx.dynamicanimation) with a fourth arriving on some future
// release. TimeAnimator, ViewAnimationUtils.createCircularReveal, setStateListAnimator,
// AnimatedVectorDrawable and AnimationDrawable were all still missing from the widened list.
//
// SO THE RULE IS INVERTED, and this is the choice the review asked to see argued. The forbidden
// side becomes a VOCABULARY rather than a list -- any identifier spelled with `animat` or
// `transition` in it, in any case -- and the PERMITTED side is enumerated instead, because unlike
// the forbidden side it is small, stable and knowable in advance:
//
//   - `Animator`: the one type this file's own text says a component may name, for the value
//     Motion hands back (`android.animation.Animator`).
//   - `animation`: the package segment that type lives in. Permitting the segment costs nothing,
//     because every forbidden member of `android.animation` carries its own forbidden type name on
//     the same line -- `import android.animation.ObjectAnimator` is caught by `ObjectAnimator`.
//   - anything reached THROUGH `Motion.`: `Motion.duration`, `Motion.translateY`,
//     `Motion.colorTransition`. Writing the exemption as "immediately preceded by `Motion.`"
//     rather than as a list of primitive names is deliberate: Motion.kt belongs to another slice,
//     and a primitive renamed or added there must neither unfence this file nor break it.
//
// WHAT THE INVERSION TRADES. The failure mode moves from a silent pass on an unlisted API to a
// false positive on some future identifier that merely READS as animation. That cost is one
// reviewed line in the permitted set, paid by whoever hits it, in the open. The old cost was a
// component animating outside the reduced-motion check with a green build, discoverable only by
// someone re-deriving the list from the platform. Those are not comparable, which is what makes
// the noise worth buying.
//
// Matched against COMMENT-STRIPPED source (kotlinCodeOnly). Prose is what carries these words most
// often -- Motion.kt's own KDoc names every one of them, WorkingBar.kt's explains why the mock's
// pulse is not implemented -- so stripping is what makes a vocabulary rule usable at all.
var animatorVocabulary = regexp.MustCompile(`(?i)animat|transition`)

// animatorIdentifier splits a line into the identifiers the rule above judges.
//
// SPLITTING FIRST IS WHAT MAKES THE EXEMPTION EXPRESSIBLE. `ObjectAnimator` and `Animator` both
// contain `Animator`, so a vocabulary matched against the whole line could never permit the second
// without permitting the first; only a rule that sees them as separate tokens can.
var animatorIdentifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// animatorAlwaysForbidden are the frame-driving APIs that animate without being spelled like it.
//
// `Choreographer.postFrameCallback` is a hand-rolled animation loop: it moves a view over time, it
// is not built by Motion, and nothing in it consults ANIMATOR_DURATION_SCALE. It is listed BY NAME
// because no vocabulary rule reaches it -- so this list inherits the very weakness the inversion
// above exists to remove, and it is therefore held to the one entry there is evidence for rather
// than grown speculatively. Anything added here is an admission, not a feature.
var animatorAlwaysForbidden = []string{"Choreographer"}

// animatorFault returns the identifier on one COMMENT-STRIPPED line that PB-DS-8 forbids, or "".
//
// The SCAN AND THE JUDGEMENT ARE ONE FUNCTION so that every control in this file feeds its probe
// to the same one the repository assertion calls, rather than to a copy of it.
func animatorFault(line string) string {
	for _, loc := range animatorIdentifier.FindAllStringIndex(line, -1) {
		id := line[loc[0]:loc[1]]
		if animatorContains(animatorAlwaysForbidden, id) {
			return id
		}
		if !animatorVocabulary.MatchString(id) {
			continue
		}
		if animatorPermitted(line, loc[0], id) {
			continue
		}
		return id
	}
	return ""
}

// animatorPermitted is the allowlist, applied to one identifier in the context it appears in.
func animatorPermitted(line string, at int, id string) bool {
	// The one type a component may name, and the package segment it lives in.
	if id == "Animator" || id == "animation" {
		return true
	}
	// Reached through Motion, which is the routing PB-DS-8 asks for. Checked on the text BEFORE
	// the identifier rather than by a regexp over the line, so `Motion.colorTransition` is
	// permitted and a bare `colorTransition` -- a local copy of the primitive, which is the thing
	// that would bypass the reduced-motion check -- is not.
	//
	// THE RECEIVER MUST BE THE WHOLE WORD `Motion`. A plain suffix test also permits
	// `MyMotion.colorTransition`, which is a different object with the same method name and no
	// reduced-motion check in it -- one character of difference in review, and the exemption
	// handed to whoever writes it. A `.` before it is fine: that is the fully-qualified spelling.
	prefix := line[:at]
	if !strings.HasSuffix(prefix, "Motion.") {
		return false
	}
	before := strings.TrimSuffix(prefix, "Motion.")
	if before == "" {
		return true
	}
	c := before[len(before)-1]
	return !(c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9'))
}

func animatorContains(haystack []string, want string) bool {
	for _, v := range haystack {
		if v == want {
			return true
		}
	}
	return false
}

// animatorConstruct is one forbidden match.
type animatorConstruct struct {
	File       string // repo-relative
	Line       int
	Identifier string // the forbidden identifier, which is what the reader has to act on
	Text       string // the matched line, trimmed
}

// motionFile is the ONE file the fence exempts, as a path relative to the scanned root.
const motionFile = "dev/swarm/phone/ui/kit/Motion.kt"

// isExemptFile reports whether a root-relative Kotlin path is that one file. WHOLE-PATH equality,
// not a suffix or a substring: a "Motion.kt" in some other package, a "MotionExtras.kt" beside the
// real one, or a "ui/kitchen/Motion.kt" would each be exempted by a looser test while being an
// entirely different file from the one whose animators this gate trusts.
func isExemptFile(relPath string) bool {
	return filepath.ToSlash(relPath) == motionFile
}

// scanAnimatorConstructsIn walks root and reports every forbidden match in every .kt file except
// Motion.kt. root is a parameter, exactly as scanLogSinksIn's roots are, so the discrimination
// (one file is exempt, everything else is not) can be exercised on a SYNTHETIC tree this file
// controls rather than only on whatever the repository happens to contain today.
func scanAnimatorConstructsIn(t *testing.T, root string) []animatorConstruct {
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
		if isExemptFile(rel) {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("PB-DS-8: cannot read %s: %v", path, rerr)
		}
		stripped := kotlinCodeOnly(string(raw))
		lines := strings.Split(stripped, "\n")
		for i, line := range lines {
			if id := animatorFault(line); id != "" {
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
		t.Fatalf("PB-DS-8: walking %s: %v", root, err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// countScannedKotlinFiles is the sanity check behind TestPBDS8_NoAnimatorIsConstructedOutsideMotion's
// zero-findings branch: a scan that walked no files reports the same "0 findings" as a scan that
// walked hundreds and found nothing wrong, and only one of those is the requirement satisfied.
func countScannedKotlinFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
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
		if !isExemptFile(rel) {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PB-DS-8: walking %s: %v", root, err)
	}
	return n
}

// productionKotlinRoot is where PB-DS-8 applies -- production sources only. Test sources
// (src/test, src/androidTest) are excluded because a test that constructs an animator to assert
// something about Android's animation APIs -- exactly what MotionTest.kt itself now does, to
// probe Robolectric's ValueAnimator -- is not the app animating anything.
func productionKotlinRoot(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "kotlin")
}

// ---------------------------------------------------------------------------
// PB-DS-8: the real-repo assertion.
// ---------------------------------------------------------------------------

// TestPBDS8_NoAnimatorIsConstructedOutsideMotion is the requirement's mechanical form.
//
// GREEN AT THE TIME OF WRITING, and that is the correct state to record rather than paper over:
// `grep -rn 'ObjectAnimator\|ValueAnimator\|ViewPropertyAnimator\|\.animate(\|startAnimation\|
// TransitionManager' android/app/src/main/kotlin/` finds zero matches outside Motion.kt -- the
// kit's other components included, re-measured when the exemption was narrowed from the package
// to that one file. So this test has never had a violation to catch in the repository itself; its
// rejection power is proved on synthetic sources below, exactly as PB-SEC-3's argument check is.
func TestPBDS8_NoAnimatorIsConstructedOutsideMotion(t *testing.T) {
	root := productionKotlinRoot(t)
	if n := countScannedKotlinFiles(t, root); n == 0 {
		t.Fatalf("PB-DS-8: scanned zero .kt file(s) under %s; a zero-findings result below would "+
			"be vacuous -- either the production tree moved or isExemptFile is exempting more "+
			"than %s", mustRel(t, root), motionFile)
	}

	found := scanAnimatorConstructsIn(t, root)
	if len(found) == 0 {
		return
	}
	var lines []string
	for _, f := range found {
		lines = append(lines, f.File+":"+itoa(f.Line)+": `"+f.Identifier+"` in: "+f.Text)
	}
	t.Errorf("PB-DS-8: %d animator construct(s) outside %s. ADR-007 B134 decision 3 requires "+
		"reduced motion to be checked at animator construction for every animation the app runs; "+
		"an animator built anywhere else has no mechanism enforcing that check. A KIT COMPONENT "+
		"calls Motion's primitives (translateY, translateX, colorTransition) or routes its own "+
		"duration through Motion.duration, and names the result android.animation.Animator rather "+
		"than ObjectAnimator or ValueAnimator. If this fires against a SURFACE file, do not "+
		"allowlist it here -- PB-DS-11 in S24 owns cleaning surface code, and the requirement was "+
		"reassigned away from S23 specifically because a per-violation allowlist is the defect it "+
		"forbids. If the identifier merely READS as animation and animates nothing, that is the "+
		"false positive animatorVocabulary trades for, and the fix is one reviewed line in "+
		"animatorPermitted -- not a hole in the vocabulary:\n\t%s",
		len(found), motionFile, strings.Join(lines, "\n\t"))
}

// ---------------------------------------------------------------------------
// PB-DS-8: proof the scan can actually fail, and is scoped correctly. (Negative controls.)
// ---------------------------------------------------------------------------

// pbds8SyntheticTree writes one Kotlin file at relPath (repo-relative to the tree root) holding
// source, and returns the tree's root for scanAnimatorConstructsIn.
func pbds8SyntheticTree(t *testing.T, relPath, source string) string {
	t.Helper()
	root := t.TempDir()
	full := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(source), 0o644); err != nil {
		t.Fatalf("writing synthetic source: %v", err)
	}
	return root
}

// TestPBDS8_EveryForbiddenConstructIsCaught is the POSITIVE half: each named way to build or drive
// a platform animation, planted in a file that is not the exempt one, must be found. Table-driven
// so a rule that stops matching shows up as a named failure rather than as a general "the count
// changed" assertion that does not say which construct went blind.
//
// THE TABLE IS EVIDENCE, NOT THE RULE. Every row here is covered by animatorVocabulary rather than
// by an entry of its own, which is the point: the first six were the fence's whole definition and
// the next nine are APIs that walked through it -- the two a reviewer named
// (AnimatorInflater, SpringAnimation) and seven more found by reading the platform rather than the
// review. Under a denylist each of those needed someone to notice it first.
func TestPBDS8_EveryForbiddenConstructIsCaught(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{"ObjectAnimator", `val a = ObjectAnimator.ofFloat(view, "translationY", 0f, 1f)`},
		{"ValueAnimator", `val a = ValueAnimator.ofFloat(0f, 1f)`},
		{"ViewPropertyAnimator", `val a: ViewPropertyAnimator = view.animate()`},
		{"animate()", `view.animate().alpha(0f).start()`},
		{"startAnimation", `view.startAnimation(anim)`},
		{"TransitionManager", `TransitionManager.beginDelayedTransition(root)`},
		// The two the review named, verbatim.
		{"AnimatorInflater", `AnimatorInflater.loadAnimator(context, id).start()`},
		{"SpringAnimation", `SpringAnimation(view, DynamicAnimation.ALPHA).start()`},
		// And the ones no review named, which is the half that matters.
		{"AnimatorSet", `AnimatorSet().apply { playTogether(a, b) }.start()`},
		{"TimeAnimator", `TimeAnimator().apply { setTimeListener(l) }.start()`},
		{"AnimationUtils", `view.startAnimation(AnimationUtils.loadAnimation(context, id))`},
		{"createCircularReveal", `ViewAnimationUtils.createCircularReveal(view, 0, 0, 0f, 1f).start()`},
		{"stateListAnimator", `view.stateListAnimator = inflated`},
		{"AnimatedVectorDrawable", `(drawable as AnimatedVectorDrawable).start()`},
		{"AnimationDrawable", `(view.background as AnimationDrawable).start()`},
		{"import of a forbidden type", `import androidx.dynamicanimation.animation.SpringAnimation`},
		{"Choreographer", `Choreographer.getInstance().postFrameCallback(callback)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "package dev.swarm.phone.ui\n\nfun build() {\n    " + tc.line + "\n}\n"
			root := pbds8SyntheticTree(t, "dev/swarm/phone/ui/Surface.kt", source)
			found := scanAnimatorConstructsIn(t, root)
			if len(found) != 1 {
				t.Fatalf("PB-DS-8: pattern %q: got %d finding(s) in a file planted with exactly "+
					"one violation, want 1. The construct this pattern names is no longer "+
					"detected:\n\t%v", tc.name, len(found), found)
			}
		})
	}
}

// TestPBDS8_TheFenceCatchesConstructsNobodyListed is the control the inversion exists to make
// possible, and the one a denylist could never pass.
//
// Every identifier below is invented. None appears in this file's rules, in the platform, or in
// any review; each is what "the API nobody thought of" looks like from inside the fence. A list of
// names scores zero here by construction, and that is precisely the defect the review named: a
// fence whose failure mode is "a spelling nobody listed" is not a fence, it is an inventory of
// past mistakes.
func TestPBDS8_TheFenceCatchesConstructsNobodyListed(t *testing.T) {
	for _, line := range []string{
		`val a = FooAnimator.ofFloat(view, "alpha", 0f, 1f)`,
		`BarAnimation(view).start()`,
		`val t = QuuxTransition(root)`,
		`import android.gizmo.HolographicAnimator`,
		`view.applyAnimated(spec)`,
		`val d = context.getSystemService(GIZMO_ANIMATION_SERVICE)`,
	} {
		if id := animatorFault(line); id == "" {
			t.Errorf("PB-DS-8: the fence passes %q. Nothing in this file names that identifier, "+
				"which is the whole point of the probe -- an unlisted animation API must fail on "+
				"its SPELLING, not on someone having met it before.", line)
		}
	}
}

// TestPBDS8_ThePermittedConstructsAreNotFlagged is the other half, and without it the test above
// is satisfied by a fence that refuses everything.
//
// A vocabulary rule earns its keep only if the four things a correct component actually writes go
// through it untouched. If this fails, the fence is unsatisfiable and the next person to hit it
// will widen the permitted set past the point where it means anything -- which is how an
// over-strict guard becomes a disabled one.
func TestPBDS8_ThePermittedConstructsAreNotFlagged(t *testing.T) {
	for _, line := range []string{
		`import android.animation.Animator`,
		`fun slideIn(view: View): Animator = Motion.translateY(context, view, 0f, 1f, 350L)`,
		`val fade: Animator = Motion.colorTransition(context, view, from, to)`,
		`val ms = Motion.duration(context, 350L)`,
		`val a = dev.swarm.phone.ui.kit.Motion.bottomSheetEnter(context, sheet, h)`,
		`setPaddingRelative(0, 0, 0, Kit.dimenPx(context, R.dimen.swarm_space_14))`,
		`val translationPx = Kit.dp(context, KitMetrics.DOT_DP)`,
	} {
		if id := animatorFault(line); id != "" {
			t.Errorf("PB-DS-8: the fence flags %q on the identifier %q. That is what a correct "+
				"component writes: the type Motion hands back, or a primitive reached through "+
				"Motion. A guard that refuses the documented right answer gets widened until it "+
				"refuses nothing.", line, id)
		}
	}

	// The exemption is `Motion.` AND NOTHING LOOSER. A local copy of a primitive is exactly the
	// thing that would bypass the reduced-motion check, and it is spelled almost identically.
	for _, line := range []string{
		`val fade = colorTransition(view, from, to)`,
		`val fade = MyMotion.colorTransition(view, from, to)`,
		`val fade = motion.colorTransition(view, from, to)`,
	} {
		if id := animatorFault(line); id == "" {
			t.Errorf("PB-DS-8: the fence passes %q. Only `Motion.` routes through the reduced-motion "+
				"check; a bare or differently-receivered call of the same name does not, and the two "+
				"differ by one word in review.", line)
		}
	}
}

// TestPBDS8_ASiblingKitFileIsNotExempt is the fence doing the one thing it exists to do.
//
// The exemption is ONE FILE, BY NAME. A package-scoped exemption -- which is what this test
// asserted until the committee read it -- permitted precisely what the fence forbids: any kit
// component could construct its own ObjectAnimator, bypass Motion.duration entirely, and stay
// green, while Motion.kt's own KDoc claimed "there is no second path that constructs an animator
// without it". A sibling kit file is the likeliest place for that to happen, not the least: it is
// where the components that need animation are written.
//
// The prose in the planted source is deliberate: it names the same identifiers Motion.kt's KDoc
// names, so this doubles as proof that the comment stripping applies inside the kit too -- only
// the code line may be flagged.
func TestPBDS8_ASiblingKitFileIsNotExempt(t *testing.T) {
	source := "package dev.swarm.phone.ui.kit\n\n" +
		"/** Mentions ObjectAnimator, ValueAnimator, ViewPropertyAnimator, TransitionManager, " +
		"animate() and startAnimation() in prose, exactly as Motion.kt's own KDoc does. */\n" +
		"fun buildToggleThumbSlide() {\n" +
		"    val a = ObjectAnimator.ofFloat(null, \"translationX\", 0f, 1f)\n" +
		"}\n"
	root := pbds8SyntheticTree(t, "dev/swarm/phone/ui/kit/Toggle.kt", source)
	found := scanAnimatorConstructsIn(t, root)
	if len(found) != 1 {
		t.Fatalf("PB-DS-8: %d finding(s) for a raw animator in a kit file that is NOT Motion.kt, "+
			"want 1. A component beside Motion.kt that builds its own animator has nothing "+
			"enforcing the reduced-motion check -- it must route through Motion's primitives "+
			"(translateY/translateX/colorTransition) or through Motion.duration:\n\t%v", len(found), found)
	}
	if found[0].Line != 5 {
		t.Errorf("PB-DS-8: the one finding is at line %d, want line 5 (the actual construct); the "+
			"KDoc naming the same identifiers in prose must not have been the source of this "+
			"match", found[0].Line)
	}
}

// TestPBDS8_MotionKtIsTheOneExemption is the NEGATIVE control for the test above: the identical
// violating line, planted in Motion.kt itself this time, must NOT be found. Motion.kt is where
// every one of these animators is meant to be constructed, and a scan that flagged it would fail
// the build on its own first run -- so the exemption exists, and it stops there.
func TestPBDS8_MotionKtIsTheOneExemption(t *testing.T) {
	source := "package dev.swarm.phone.ui.kit\n\n" +
		"fun translateX() {\n" +
		"    val a = ObjectAnimator.ofFloat(null, \"translationX\", 0f, 1f)\n" +
		"}\n"
	root := pbds8SyntheticTree(t, "dev/swarm/phone/ui/kit/Motion.kt", source)
	found := scanAnimatorConstructsIn(t, root)
	if len(found) != 0 {
		t.Errorf("PB-DS-8: %d finding(s) in Motion.kt, want 0. Motion.kt is the file the fence "+
			"exempts; flagging it would make the guard unsatisfiable:\n\t%v", len(found), found)
	}
}

// TestPBDS8_TheExemptionIsOneWholePathNotASuffixOrASubstring guards isExemptFile itself. Every
// entry below is a file a suffix match, a basename match or a package-segment match would wave
// through, and not one of them is the file whose animators this gate trusts.
func TestPBDS8_TheExemptionIsOneWholePathNotASuffixOrASubstring(t *testing.T) {
	for _, relPath := range []string{
		"dev/swarm/phone/ui/kit/MotionExtras.kt", // a sibling whose name merely starts with Motion
		"dev/swarm/phone/ui/kit/ToggleMotion.kt", // ... and one whose name merely ends with it
		"dev/swarm/phone/ui/kit/motion/Blink.kt", // a package named after the file
		"dev/swarm/phone/ui/kitchen/Motion.kt",   // the right basename in the wrong package
		"dev/swarm/phone/ui/kit/Toggle.kt",       // the sibling component the old exemption freed
		"other/dev/swarm/phone/ui/kit/Motion.kt", // the right path with something before it
		"dev/swarm/phone/ui/kit/Motion.kt.bak",   // the right path with something after it
	} {
		if isExemptFile(relPath) {
			t.Errorf("PB-DS-8: isExemptFile(%q) = true; only %s is exempt from the fence",
				relPath, motionFile)
		}
	}
	if !isExemptFile(motionFile) {
		t.Errorf("PB-DS-8: isExemptFile did not recognise %s itself; the exemption every test in "+
			"this file depends on would exempt nothing, and the gate would fail against the very "+
			"file it is built around", motionFile)
	}
}

// TestPBDS8_CommentsAndDocsDoNotDefeatOrTriggerTheScan is the same discrimination
// s18_sec3_logscan_test.go documents for the log scan, run over animator identifiers instead of
// log calls: kotlinCodeOnly strips comments, so a KDoc block outside the kit that happens to
// name these identifiers in prose (a code-review comment explaining why NOT to use one, say)
// must not be flagged, while actual code on the very next line still is.
func TestPBDS8_CommentsAndDocsDoNotDefeatOrTriggerTheScan(t *testing.T) {
	source := "package dev.swarm.phone.ui\n\n" +
		"/** Do not build an ObjectAnimator or a ValueAnimator here; call into the kit. */\n" +
		"fun build() {\n" +
		"    val a = ObjectAnimator.ofFloat(null, \"alpha\", 0f, 1f) // still forbidden\n" +
		"}\n"
	root := pbds8SyntheticTree(t, "dev/swarm/phone/ui/Surface.kt", source)
	found := scanAnimatorConstructsIn(t, root)
	if len(found) != 1 {
		t.Fatalf("PB-DS-8: got %d finding(s), want exactly 1 -- the KDoc comment naming both "+
			"identifiers must not count, and the real construct on the code line must still be "+
			"caught even though a trailing line comment sits on the same line:\n\t%v",
			len(found), found)
	}
	if found[0].Line != 5 {
		t.Errorf("PB-DS-8: the one finding is at line %d, want line 5 (the actual construct); "+
			"the KDoc comment on line 3 must not have been the source of this match", found[0].Line)
	}
}
