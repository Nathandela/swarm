package gate

// FAILING-FIRST (TDD RED, GG-5) is demonstrated on SYNTHETIC sources for this file, not on the
// repository: PB-DS-8's fence is preventative, and the repository has never contained an
// animator construct outside dev.swarm.phone.ui.kit (verified by hand before this file was
// written: `grep -rn 'ObjectAnimator\|ValueAnimator\|ViewPropertyAnimator\|\.animate(\|
// startAnimation\|TransitionManager' android/app/src/main/kotlin/` finds nothing). A real-repo
// run can therefore only ever demonstrate ACCEPTANCE of a clean tree, the same limitation
// pbsec3_logdiscrimination_test.go documents for the log scan -- so the guard's ability to
// REJECT is proved here on sources built to contain the violation, in the style established
// there and by scanLogSinksIn's roots parameter.
//
// "PB-DS-8: Motion: Substrate is static, and the exceptions are named." ADR-007 B134 decision 3:
// no decorative animation anywhere. Only the bottom sheet, the push banner and the streaming
// caret move, and dev/swarm/phone/ui/kit/Motion.kt is where they are built -- reduced motion
// (Settings.Global.ANIMATOR_DURATION_SCALE == 0) checked once, at construction, for every one
// of them. A fence with no mechanical form would let a screen build its own ObjectAnimator
// beside a call into Motion and skip that check silently; this is the mechanical form.
//
// SCOPED TO PRODUCTION KOTLIN under android/app/src/main/kotlin, and specifically EXCLUDING the
// dev/swarm/phone/ui/kit package (any path segment "ui/kit"): that is where Motion.kt itself
// lives, where its own KDoc names every one of the forbidden identifiers in prose, and where a
// sibling component (a Toggle view, built concurrently and outside this file's ownership) is
// expected to construct the animators [Motion]'s translateX/translateY/colorTransition return.
// Excluding the whole package rather than only Motion.kt is deliberate: PB-DS-8's requirement is
// "outside the kit", not "outside this one file".
//
// KNOWN, NOT FIXED HERE: if a surface file (PhoneSurface.kt, SessionScreens.kt, TriageInbox.kt,
// ...) is ever found constructing an animator directly, this gate must NOT grow a per-file
// allowlist for it -- PB-DS-11 in S24 owns cleaning surface code, and an allowlist here is the
// exact defect PB-DS-11 was reassigned away from S23 to avoid (docs/specifications/
// remote-phaseB-requirements.md §6.20, S23/S24 row). At the time of writing no such violation
// exists; see TestPBDS8_NoAnimatorIsConstructedOutsideTheKit's own comment for the measurement.

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

// isKitPath reports whether a repo-relative Kotlin path is inside dev/swarm/phone/ui/kit --
// checked as a path SEGMENT PAIR ("ui" immediately followed by "kit"), not a substring of the
// whole path, so a hypothetical "ui/kitchen" or "toolkit/ui" elsewhere could not slip through by
// sharing four letters with the package this gate exempts.
func isKitPath(relPath string) bool {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "ui" && parts[i+1] == "kit" {
			return true
		}
	}
	return false
}

// scanAnimatorConstructsIn walks root and reports every forbidden match in every .kt file NOT
// under a ui/kit path segment pair. root is a parameter, exactly as scanLogSinksIn's roots are,
// so the discrimination (kit is exempt, everything else is not) can be exercised on a SYNTHETIC
// tree this file controls rather than only on whatever the repository happens to contain today.
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
		if isKitPath(rel) {
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

// countNonKitKotlinFiles is the sanity check behind TestPBDS8_NoAnimatorIsConstructedOutsideTheKit's
// zero-findings branch: a scan that walked no files reports the same "0 findings" as a scan that
// walked hundreds and found nothing wrong, and only one of those is the requirement satisfied.
func countNonKitKotlinFiles(t *testing.T, root string) int {
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
		if !isKitPath(rel) {
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

// TestPBDS8_NoAnimatorIsConstructedOutsideTheKit is the requirement's mechanical form.
//
// GREEN AT THE TIME OF WRITING, and that is the correct state to record rather than paper over:
// `grep -rn 'ObjectAnimator\|ValueAnimator\|ViewPropertyAnimator\|\.animate(\|startAnimation\|
// TransitionManager' android/app/src/main/kotlin/` found zero matches before Motion.kt existed,
// so this test has never had a violation to catch in the repository itself. Its rejection power
// is proved on synthetic sources below, exactly as PB-SEC-3's argument check is.
func TestPBDS8_NoAnimatorIsConstructedOutsideTheKit(t *testing.T) {
	root := productionKotlinRoot(t)
	if n := countNonKitKotlinFiles(t, root); n == 0 {
		t.Fatalf("PB-DS-8: scanned zero non-kit .kt file(s) under %s; a zero-findings result "+
			"below would be vacuous -- either the production tree moved or isKitPath is "+
			"exempting more than the kit package", mustRel(t, root))
	}

	found := scanAnimatorConstructsIn(t, root)
	if len(found) == 0 {
		return
	}
	var lines []string
	for _, f := range found {
		lines = append(lines, f.File+":"+itoa(f.Line)+": "+f.Text)
	}
	t.Errorf("PB-DS-8: %d animator construct(s) outside dev/swarm/phone/ui/kit. ADR-007 B134 "+
		"decision 3 requires reduced motion to be checked at animator construction for every "+
		"animation the app runs; an animator built outside dev.swarm.phone.ui.kit.Motion has no "+
		"mechanism enforcing that check. If this fires against a SURFACE file (not a new kit "+
		"component), do not allowlist it here -- PB-DS-11 in S24 owns cleaning surface code, and "+
		"the requirement was reassigned away from S23 specifically because a per-violation "+
		"allowlist is the defect it forbids:\n\t%s", len(found), strings.Join(lines, "\n\t"))
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

// TestPBDS8_EveryForbiddenConstructIsCaughtOutsideTheKit is the POSITIVE half: each of the six
// named ways to build or drive a platform animation, planted in a file that is NOT under ui/kit,
// must be found. Table-driven over the team's exact list so a pattern that stops matching (a
// typo'd regex, an accidentally-deleted entry) shows up as a named failure rather than a general
// "the count changed" assertion that does not say which construct went blind.
func TestPBDS8_EveryForbiddenConstructIsCaughtOutsideTheKit(t *testing.T) {
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
					"detected outside the kit:\n\t%v", tc.name, len(found), found)
			}
		})
	}
}

// TestPBDS8_TheKitPackageIsExempt is the NEGATIVE control for the positive test above: the
// identical violating line, planted under a ui/kit path this time, must NOT be found. Without
// this, a scan that flagged EVERYTHING (including Motion.kt's own KDoc, which names every one
// of these identifiers in prose) would still pass the positive test and would fail the build on
// its own first run.
func TestPBDS8_TheKitPackageIsExempt(t *testing.T) {
	source := "package dev.swarm.phone.ui.kit\n\n" +
		"/** Mentions ObjectAnimator, ValueAnimator, ViewPropertyAnimator, TransitionManager, " +
		"animate() and startAnimation() in prose, exactly as Motion.kt's own KDoc does. */\n" +
		"fun buildToggleThumbSlide() {\n" +
		"    val a = ObjectAnimator.ofFloat(null, \"translationX\", 0f, 1f)\n" +
		"}\n"
	root := pbds8SyntheticTree(t, "dev/swarm/phone/ui/kit/Toggle.kt", source)
	found := scanAnimatorConstructsIn(t, root)
	if len(found) != 0 {
		t.Errorf("PB-DS-8: %d finding(s) inside a ui/kit path, want 0. The kit package is where "+
			"Motion's own animators -- and a sibling component built on them -- are meant to be "+
			"constructed; a scan that cannot tell that apart from a surface file flags the very "+
			"code the requirement exists to keep unguarded elsewhere:\n\t%v", len(found), found)
	}
}

// TestPBDS8_KitExemptionIsPathScopedNotSubstringScoped guards isKitPath itself: a package that
// merely CONTAINS the four letters "ui" and "kit" as a substring of some other name must not be
// exempted by accident.
func TestPBDS8_KitExemptionIsPathScopedNotSubstringScoped(t *testing.T) {
	for _, relPath := range []string{
		"dev/swarm/phone/uikit/Surface.kt",      // one path segment, not two
		"dev/swarm/phone/toolkit/ui/Surface.kt", // "kit" then "ui", reversed
		"dev/swarm/phone/ui/kitchen/Surface.kt", // "kit" is a prefix of the segment, not the segment
	} {
		if isKitPath(relPath) {
			t.Errorf("PB-DS-8: isKitPath(%q) = true; only an actual .../ui/kit/... path segment "+
				"pair should be exempt from the fence", relPath)
		}
	}
	if !isKitPath("dev/swarm/phone/ui/kit/Motion.kt") {
		t.Error("PB-DS-8: isKitPath did not recognise the real kit package; the exemption this " +
			"test suite depends on would exempt nothing")
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
