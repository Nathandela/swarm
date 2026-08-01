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
// and where the KDoc names all six forbidden identifiers in prose.
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
// The denylist.
// ---------------------------------------------------------------------------

// animatorConstructPatterns are every way production Kotlin can build or drive a platform
// animation, per the team's exact list: ObjectAnimator, ValueAnimator, ViewPropertyAnimator,
// View.animate(), View.startAnimation(...) and TransitionManager. Matched against
// COMMENT-STRIPPED source (kotlinCodeOnly) -- unlike the PB-SEC-3 log scan, there is no
// fail-open hazard in stripping here: every pattern below requires an identifier immediately
// followed by `(` or a bare type reference, which prose mentioning the same word without
// exercising the API does not produce, and Motion.kt's own KDoc names all six identifiers in
// running text.
var animatorConstructPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bObjectAnimator\b`),
	regexp.MustCompile(`\bValueAnimator\b`),
	// ADDED 2026-08-01. A reviewer showed the six-string list was an allowlist of APIs, not of
	// behaviour: `AnimatorInflater.loadAnimator(ctx, id).start()` and
	// `SpringAnimation(view, DynamicAnimation.ALPHA).start()` both construct an animation that
	// never passes Motion.duration(), and both walked the fence. PB-DS-8's text is that NO
	// animator is constructed outside the kit, so the list has to cover the platform's other
	// entry points rather than the six that happened to be in use.
	regexp.MustCompile(`\bAnimatorInflater\b`),
	regexp.MustCompile(`\bAnimatorSet\b`),
	regexp.MustCompile(`\bSpringAnimation\b`),
	regexp.MustCompile(`\bFlingAnimation\b`),
	regexp.MustCompile(`\bDynamicAnimation\b`),
	regexp.MustCompile(`\bAlphaAnimation\b`),
	regexp.MustCompile(`\bTranslateAnimation\b`),
	regexp.MustCompile(`\bAnimationUtils\b`),
	regexp.MustCompile(`\bViewPropertyAnimator\b`),
	regexp.MustCompile(`\.animate\s*\(`),
	regexp.MustCompile(`\bstartAnimation\s*\(`),
	regexp.MustCompile(`\bTransitionManager\b`),
}

// animatorConstruct is one forbidden match.
type animatorConstruct struct {
	File string // repo-relative
	Line int
	Text string // the matched line, trimmed
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
			for _, p := range animatorConstructPatterns {
				if p.MatchString(line) {
					out = append(out, animatorConstruct{File: rel, Line: i + 1, Text: strings.TrimSpace(line)})
					break
				}
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
		lines = append(lines, f.File+":"+itoa(f.Line)+": "+f.Text)
	}
	t.Errorf("PB-DS-8: %d animator construct(s) outside %s. ADR-007 B134 decision 3 requires "+
		"reduced motion to be checked at animator construction for every animation the app runs; "+
		"an animator built anywhere else has no mechanism enforcing that check. A KIT COMPONENT "+
		"calls Motion's primitives (translateY, translateX, colorTransition) or routes its own "+
		"duration through Motion.duration, and names the result android.animation.Animator rather "+
		"than ObjectAnimator or ValueAnimator. If this fires against a SURFACE file, do not "+
		"allowlist it here -- PB-DS-11 in S24 owns cleaning surface code, and the requirement was "+
		"reassigned away from S23 specifically because a per-violation allowlist is the defect it "+
		"forbids:\n\t%s", len(found), motionFile, strings.Join(lines, "\n\t"))
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

// TestPBDS8_EveryForbiddenConstructIsCaught is the POSITIVE half: each of the six named ways to
// build or drive a platform animation, planted in a file that is not the exempt one, must be
// found. Table-driven over the team's exact list so a pattern that stops matching (a typo'd
// regex, an accidentally-deleted entry) shows up as a named failure rather than a general "the
// count changed" assertion that does not say which construct went blind.
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
