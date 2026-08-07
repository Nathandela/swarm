package gate

// FAILING-FIRST (TDD RED, GG-5) for ADR-009 D8.2 -- THE SWEEP GATE.
//
// D5 adds ONE new exception to "no decorative animation": a specular highlight that travels a
// slab's top edge once, at the moment that session's Group becomes NeedsInput. The ADR's own
// sentence is that it "is not decoration" -- an attention signal in the same class as the caret's
// liveness -- and it makes that claim checkable by naming four constraints, each of which this
// file asserts:
//
//	(a) ONE-SHOT. It never loops. A sweep that repeats is the ambient field-register motion D5
//	    bans in the same paragraph, arrived at by forgetting one property.
//	(b) AT MOST ONE PER VIEWPORT, NEWEST WINS. Motion on near-black is amplified ~80:1, which is
//	    why the research constraint is one moving element per viewport; two rows promoted in one
//	    journal event is the normal case, not the exotic one. The superseded sweep COMPLETES
//	    INSTANTLY rather than being cancelled -- `end()` runs the listeners that detach the
//	    streak, `cancel()` leaves a half-drawn highlight on a row nobody is looking at.
//	(c) CONSTRUCTED ONLY INSIDE THE KIT. s23_motion_test.go already fences ANIMATORS to
//	    Motion.kt; it judges an identifier by its spelling, and `specularSweep` contains neither
//	    `animat` nor `transition`, so a screen could hand-roll its own streak past a green lane.
//	    This extends that fence's vocabulary rather than duplicating its machinery -- the
//	    permitted-receiver rule below IS animatorPermitted, called directly.
//	(d) REDUCED MOTION COLLAPSES IT TO NOTHING. Not to a shorter sweep, and not to a final frame:
//	    the streak is never attached at all. The caret's own history is why this is asserted at
//	    the source level as well as the behavioural one -- a zero-duration ValueAnimator still
//	    delivers one update, which is how a reduced-motion caret that DIMMED once shipped under a
//	    test asserting it stayed visible.
//
// WHAT THIS GATE DOES AND WHAT MotionTest DOES, because neither subsumes the other and a reader
// should not have to guess. This file reads SOURCE: it can say that the sweep is named nowhere
// outside the one file that builds it, that its body consults reduced motion before it constructs
// anything, that it ends its predecessor rather than cancelling it, and that every number it
// declares is the design's own. It cannot say what an animator DOES.
// `MotionTest.specularSweep_*` runs them: repeatCount, duration, the listener that fires on the
// superseded animator, the null return under reduced motion. Source shape and runtime behaviour
// fail differently, and the sweep is the one effect in this app that no screenshot can catch.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. Every scan starts at the app module or at a named file,
// so it cannot descend into `.claude/worktrees/`, which holds other agents' full checkouts and has
// already made four gates in this repository report findings about somebody else's private copy.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// (c) The construction site: the sweep is named only where it is built.
// ---------------------------------------------------------------------------

// o4SweepVocabulary is the fence's subject, in the same VOCABULARY form s23_motion_test.go argued
// for and for the same reason: a list of names fails open on every name nobody thought of.
//
// TWO WORDS AND NOT ONE. `specular` catches the builder and its drawable; `sweep` catches a
// hand-rolled one that avoids the adjective. Neither word has an innocent use in this app's
// production Kotlin -- the model that decides WHEN one fires says `promoted`, which is the state
// the row is in rather than the effect it earns, and that is the naming the fence depends on.
var o4SweepVocabulary = regexp.MustCompile(`(?i)sweep|specular`)

// o4SweepFault returns the identifier on one COMMENT-STRIPPED line that D8.2 forbids, or "".
//
// The permitted-receiver test is animatorPermitted, CALLED AND NOT COPIED. `Motion.specularSweep`
// is the one way a component asks for a sweep, which is exactly the shape that file already
// permits for the animators; a second implementation of "is this reached through Motion" is a
// second chance to get the whole-word check wrong, and `MyMotion.specularSweep` is one character
// of difference in review.
func o4SweepFault(line string) string {
	for _, loc := range animatorIdentifier.FindAllStringIndex(line, -1) {
		id := line[loc[0]:loc[1]]
		if !o4SweepVocabulary.MatchString(id) {
			continue
		}
		if animatorPermitted(line, loc[0], id) {
			continue
		}
		return id
	}
	return ""
}

// o4ScanSweepConstructsIn walks root and reports every forbidden match in every .kt file except
// Motion.kt, which isExemptFile names. root is a parameter, exactly as scanAnimatorConstructsIn's
// is, so the discrimination can be exercised on a synthetic tree this file controls.
func o4ScanSweepConstructsIn(t *testing.T, root string) []animatorConstruct {
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
			t.Fatalf("ADR-009 D8.2: cannot read %s: %v", path, rerr)
		}
		for i, line := range strings.Split(kotlinCodeOnly(string(raw)), "\n") {
			if id := o4SweepFault(line); id != "" {
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
		t.Fatalf("ADR-009 D8.2: walking %s: %v", root, err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// TestD82_TheSweepIsConstructedOnlyInsideTheKit is constraint (c).
func TestD82_TheSweepIsConstructedOnlyInsideTheKit(t *testing.T) {
	root := productionKotlinRoot(t)
	if n := countScannedKotlinFiles(t, root); n == 0 {
		t.Fatalf("ADR-009 D8.2: scanned zero .kt file(s) under %s; a zero-findings result below "+
			"would be vacuous", mustRel(t, root))
	}
	found := o4ScanSweepConstructsIn(t, root)
	if len(found) == 0 {
		return
	}
	var lines []string
	for _, f := range found {
		lines = append(lines, f.File+":"+itoa(f.Line)+": `"+f.Identifier+"` in: "+f.Text)
	}
	t.Errorf("ADR-009 D8.2: %d sweep construct(s) outside %s. The sweep is the one effect in this "+
		"skin whose constraints are gates rather than guidelines -- one-shot, one per viewport, "+
		"reduced-motion-collapsing -- and every one of them is enforced by the fact that there is "+
		"exactly ONE implementation. A component asks for a sweep as `Motion.specularSweep(...)`; "+
		"it does not build a streak of its own, and it does not name the effect to talk about "+
		"the STATE that earns it -- that state is `promoted`, and it is the model's word:\n\t%s",
		len(found), motionFile, strings.Join(lines, "\n\t"))
}

// TestD82_ASweepBuiltAnywhereElseIsCaught is the positive control, on synthetic sources.
//
// The repository cannot demonstrate rejection -- there is one sweep and it is in the exempt file
// -- which is the same limitation s23_motion_test.go documents for the animator fence and
// pbsec3_logdiscrimination_test.go for the log scan. Each row is a plausible way to reintroduce
// the effect outside the one place its constraints are enforced.
func TestD82_ASweepBuiltAnywhereElseIsCaught(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{"a hand-rolled drawable", `val streak = SpecularSweepDrawable(context)`},
		{"a local builder", `fun sweep(view: View) = ValueAnimator.ofFloat(0f, 1f)`},
		{"a screen calling its own copy", `specularSweep(context, row)`},
		{"a differently-receivered copy", `Effects.specularSweep(context, row)`},
		{"a near-miss receiver", `MyMotion.specularSweep(context, row)`},
		{"an import of somebody else's", `import dev.swarm.phone.fx.SweepAnimator`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "package dev.swarm.phone.ui\n\nfun build() {\n    " + tc.line + "\n}\n"
			root := pbds8SyntheticTree(t, "dev/swarm/phone/ui/Surface.kt", source)
			if found := o4ScanSweepConstructsIn(t, root); len(found) == 0 {
				t.Fatalf("ADR-009 D8.2: pattern %q passed the fence. A sweep built outside %s has "+
					"nothing enforcing its four constraints.", tc.name, motionFile)
			}
		})
	}
}

// TestD82_TheOneWayToAskForASweepIsNotFlagged is the other half, and without it the test above is
// satisfied by a fence that refuses everything -- including the call the design requires.
func TestD82_TheOneWayToAskForASweepIsNotFlagged(t *testing.T) {
	for _, line := range []string{
		`Motion.specularSweep(context, row)`,
		`val played: Animator? = Motion.specularSweep(context, slab)`,
		`dev.swarm.phone.ui.kit.Motion.specularSweep(context, slab)`,
		// The state that EARNS a sweep is not the sweep. The model's word for it carries none of
		// the vocabulary, which is what keeps the fence usable at the call sites that matter.
		`sessionRow(context = context, group = row.group, lit = row.lit, promoted = true)`,
		`val promotions = TriageInboxScreen.promotions(previous, next)`,
	} {
		if id := o4SweepFault(line); id != "" {
			t.Errorf("ADR-009 D8.2: the fence flags %q on the identifier %q. That is what a "+
				"correct component writes. A guard that refuses the documented right answer gets "+
				"widened until it refuses nothing.", line, id)
		}
	}
}

// ---------------------------------------------------------------------------
// (a), (b), (d): the three constraints that live in one function's body.
// ---------------------------------------------------------------------------

// o4SweepBuilder is the function whose body carries the three constraints below.
const o4SweepBuilder = "fun specularSweep("

// o4SweepBody returns the COMMENT-STRIPPED body of Motion.kt's sweep builder.
//
// Comments are stripped for the reason every scan in this lane strips them: prose is where these
// words appear most often, and a constraint a KDoc can satisfy is one the next thorough KDoc turns
// off. The body is found by BALANCING BRACES rather than by a regexp, because the builder contains
// a listener object and a lambda, and `\{[^}]*\}` stops at the first of them.
func o4SweepBody(t *testing.T, src string) string {
	t.Helper()
	code := kotlinCodeOnly(src)
	at := strings.Index(code, o4SweepBuilder)
	if at < 0 {
		t.Fatalf("ADR-009 D8.2: %s declares no `%s`. The sweep is D5's one new named exception; "+
			"without it there is nothing for the three constraints below to be true of.",
			motionFile, o4SweepBuilder)
	}
	open := strings.Index(code[at:], "{")
	if open < 0 {
		t.Fatalf("ADR-009 D8.2: `%s` has no body", o4SweepBuilder)
	}
	open += at
	depth := 0
	for i := open; i < len(code); i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return code[open+1 : i]
			}
		}
	}
	t.Fatalf("ADR-009 D8.2: `%s`'s braces do not balance, so the gate cannot read its body and "+
		"would report it clean", o4SweepBuilder)
	return ""
}

// o4ConstraintFaults judges one sweep body against constraints (a), (b) and (d).
//
// THE JUDGEMENT IS ONE FUNCTION so that every control below feeds its perturbation to the same one
// the repository assertion calls, rather than to a copy of it. Each fault names the constraint it
// breaks and what a reader has to do about it.
func o4ConstraintFaults(body string) []string {
	var faults []string

	// (a) One-shot. Stated explicitly rather than left to ValueAnimator's default: the default is
	// what a `repeatCount = ValueAnimator.INFINITE` one line away also starts from, and "the
	// author wrote 0 on purpose" is the only form of this that survives an edit.
	if !strings.Contains(body, "repeatCount = 0") {
		faults = append(faults, "(a) one-shot: the body never states `repeatCount = 0`. "+
			"A sweep that loops is the ambient field-register motion D5 bans in the same "+
			"paragraph, and the platform's default is not a decision anyone recorded")
	}
	if strings.Contains(body, "INFINITE") {
		faults = append(faults, "(a) one-shot: the body names INFINITE. That is the caret's "+
			"repeat count -- a liveness signal that reports text is still arriving -- and the "+
			"sweep reports a transition that has already happened")
	}

	// (b) One per viewport, newest wins, and the superseded one COMPLETES rather than stopping.
	if !strings.Contains(body, "?.end()") {
		faults = append(faults, "(b) one per viewport: the body never ends an in-flight "+
			"predecessor. Two rows promoted in one journal event is the normal case, and two "+
			"streaks travelling at once on a near-black ground is the ~80:1 amplification the "+
			"research constraint exists for")
	}
	if strings.Contains(body, ".cancel()") {
		faults = append(faults, "(b) one per viewport: the superseded sweep is CANCELLED. "+
			"`cancel()` skips the end listeners, so the streak it drew stays attached to a row "+
			"nobody is looking at; `end()` completes it instantly, which is what D5 says")
	}
	if !strings.Contains(body, o4SweepHolder+" = ") {
		faults = append(faults, "(b) one per viewport: the body never records itself as the one "+
			"in flight, so the next sweep has nothing to supersede")
	}

	// (d) Reduced motion collapses it to NOTHING -- which is a claim about ORDER, not presence.
	reduced := strings.Index(body, "isReducedMotion")
	if reduced < 0 {
		faults = append(faults, "(d) reduced motion: the body never consults isReducedMotion")
	}
	construction := o4FirstConstruction(body)
	if reduced >= 0 && construction >= 0 && reduced > construction {
		faults = append(faults, "(d) reduced motion: the body constructs before it asks. "+
			"Collapsing to nothing means the streak is never attached and the animator is never "+
			"built -- not that a built one is given a zero duration, which is a final frame at "+
			"full alpha (the caret shipped exactly that defect once)")
	}
	if construction >= 0 && !strings.Contains(body[:construction], "return null") {
		faults = append(faults, "(d) reduced motion: nothing returns before the first "+
			"construction, so the reduced-motion path cannot be the one that builds nothing")
	}
	return faults
}

// o4SweepHolder is the field that makes "at most one" a mechanism rather than a promise.
const o4SweepHolder = "inFlightSweep"

// o4FirstConstruction is where the body first builds something the user could see, or -1.
//
// TWO MARKERS, because either alone is satisfiable while the other does the damage: an animator
// with no streak animates nothing, and a streak with no animator is a static highlight left on a
// row. Both are constructions, and reduced motion must precede the earlier of them.
func o4FirstConstruction(body string) int {
	first := -1
	for _, marker := range []string{"ValueAnimator.", "overlay.add("} {
		if at := strings.Index(body, marker); at >= 0 && (first < 0 || at < first) {
			first = at
		}
	}
	return first
}

// TestD82_TheSweepBodyHoldsItsThreeConstraints is the real-repo assertion for (a), (b) and (d).
func TestD82_TheSweepBodyHoldsItsThreeConstraints(t *testing.T) {
	body := o4SweepBody(t, o4MotionSource(t))
	if faults := o4ConstraintFaults(body); len(faults) > 0 {
		t.Errorf("ADR-009 D8.2: %d of the sweep's constraints are not met in %s:\n\t%s",
			len(faults), motionFile, strings.Join(faults, "\n\t"))
	}
}

// TestD82_EachConstraintCanActuallyFail is the negative control PB-DS-10 requires, fed to the SAME
// function the assertion above calls.
//
// THE PERTURBATION IS IN MEMORY AND NEVER ON DISK. A control that edited Motion.kt to prove the
// gate works would leave the repository in whatever state the test process died in; this takes the
// real body, changes one thing in the copy it holds, and asks the same judgement what it now sees.
// A SOUND body is fed first: if the real one reports a fault, every perturbation below would
// "fail" for the wrong reason and this control would certify nothing.
func TestD82_EachConstraintCanActuallyFail(t *testing.T) {
	body := o4SweepBody(t, o4MotionSource(t))
	if faults := o4ConstraintFaults(body); len(faults) > 0 {
		t.Fatalf("ADR-009 D8.2: the real sweep body already reports %d fault(s), so every "+
			"perturbation below would pass for the wrong reason:\n\t%s",
			len(faults), strings.Join(faults, "\n\t"))
	}
	for _, tc := range []struct {
		name      string
		perturbed string
	}{
		{"a sweep that loops", strings.Replace(body, "repeatCount = 0", "repeatCount = ValueAnimator.INFINITE", 1)},
		{"a sweep with no repeat decision", strings.Replace(body, "repeatCount = 0", "", 1)},
		{"a superseded sweep that is cancelled", strings.Replace(body, "?.end()", "?.cancel()", 1)},
		{"a sweep that records nothing", strings.Replace(body, o4SweepHolder+" = ", "unused = ", 1)},
		{"a sweep that never asks about reduced motion", strings.Replace(body, "isReducedMotion", "isSomethingElse", 1)},
		{"a sweep that asks after it has already built the streak", o4MoveReducedCheckLast(body)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.perturbed == body {
				t.Fatalf("the perturbation changed nothing, so it proves nothing about the gate: "+
					"the body no longer contains the text %q expects to replace", tc.name)
			}
			if faults := o4ConstraintFaults(tc.perturbed); len(faults) == 0 {
				t.Errorf("ADR-009 D8.2: %q passes the constraint scan. A gate that cannot see it "+
					"is a gate that will not see it in review either.", tc.name)
			}
		})
	}
}

// o4MoveReducedCheckLast rewrites the body so the reduced-motion question is asked AFTER the first
// construction -- the ordering defect that produces a final-frame flash rather than nothing.
func o4MoveReducedCheckLast(body string) string {
	stripped := strings.Replace(body, "isReducedMotion", "isSomethingElse", 1)
	return stripped + "\n    if (isReducedMotion(context)) return null\n"
}

// o4MotionSource reads Motion.kt from the app module.
func o4MotionSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(productionKotlinRoot(t), filepath.FromSlash(motionFile))
	return readFileOrFail(t, path, "ADR-009 D8.2")
}

// ---------------------------------------------------------------------------
// The sweep's NUMBERS are the design's own.
// ---------------------------------------------------------------------------

// WHY THIS IS HERE AND NOT IN s23_kit_test.go, which recomputes every other kit constant from the
// design. That gate iterates s23OwnedFiles, and Motion.kt is deliberately not one of them -- the
// split is recorded at s23MotionFile: PB-DS-8's constants are durations and easing points whose
// origin is a motion DECISION rather than a rule in the shared CSS block, and demanding they cite
// a `{ property }` there would be one slice failing on another's file. The sweep is the first
// motion constant set with a design source that can be read -- the token carries its duration and
// its colour, the maquette draws its geometry -- so this is the join that was previously
// impossible, made for the eight numbers that now have one.

// o4SweepRule is the maquette's own drawing of the streak.
const o4SweepRule = ".slab.lit.sweep::after"

// o4SweepToken is the effect token that carries its duration and its colour. It is typed `effect`,
// so it has no `res/values` converter and no TSV row (ADR-009 D8.1's closing note): tokens.json is
// the only place it exists, which is exactly why a constant derived from it needs a gate.
const o4SweepToken = "--p-sweep-fx"

var (
	o4MsRe      = regexp.MustCompile(`(\d+)\s*ms`)
	o4RGBARe    = regexp.MustCompile(`rgba\(([^)]*)\)`)
	o4SkewRe    = regexp.MustCompile(`skewX\(\s*(-?[0-9.]+)deg\s*\)`)
	o4PercentRe = regexp.MustCompile(`^(-?[0-9.]+)%$`)
	o4PxRe      = regexp.MustCompile(`^(-?[0-9.]+)px$`)
	// A Kotlin constant with a plain numeric value: `const val SWEEP_HEIGHT_DP = 1.5f`.
	o4ConstRe = regexp.MustCompile(`(?m)^\s*(?:internal\s+|private\s+)?const val ([A-Za-z][A-Za-z0-9_]*)\s*=\s*(-?[0-9.]+)[fL]?\s*$`)
)

// o4KeyframeStops returns `@keyframes <name>`'s stops as stop -> declarations, read out of the
// maquette. s22bMaquetteKitCSS cannot: it STRIPS at-rules whole, because a flat rule parser fed a
// nested block walks out of phase and pairs selectors with other rules' declarations. The sweep's
// travel is stated in that stripped block and nowhere else, so it is parsed here, deliberately.
func o4KeyframeStops(t *testing.T, name string) map[string]map[string]string {
	t.Helper()
	raw := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s22bMaquetteRelPath)), "ADR-009 D8.2")
	at := strings.Index(raw, "@keyframes "+name)
	if at < 0 {
		t.Fatalf("ADR-009 D8.2: %s declares no `@keyframes %s`; the sweep's travel is stated "+
			"there and nowhere else", s22bMaquetteRelPath, name)
	}
	open := strings.Index(raw[at:], "{")
	if open < 0 {
		t.Fatalf("ADR-009 D8.2: `@keyframes %s` has no body", name)
	}
	open += at
	depth := 0
	end := -1
	for i := open; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatalf("ADR-009 D8.2: `@keyframes %s`'s braces do not balance", name)
	}
	out := map[string]map[string]string{}
	for _, m := range s22bRuleRe.FindAllStringSubmatch(raw[open+1:end], -1) {
		decls := map[string]string{}
		for _, decl := range strings.Split(m[2], ";") {
			prop, value, ok := strings.Cut(decl, ":")
			if !ok {
				continue
			}
			decls[strings.TrimSpace(prop)] = strings.TrimSpace(value)
		}
		out[strings.TrimSpace(m[1])] = decls
	}
	if len(out) == 0 {
		t.Fatalf("ADR-009 D8.2: no stops parsed from `@keyframes %s`", name)
	}
	return out
}

// o4DesignNumbers computes the design's value for every number the sweep declares.
//
// THE THREE SOURCES ARE PARAMETERS rather than read here, which is what lets the negative control
// feed this the SAME function with one perturbed input. A control that rebuilt the derivation
// inline would prove the copy works and say nothing about the gate.
func o4DesignNumbers(
	token string,
	rule map[string]string,
	stops map[string]map[string]string,
) (map[string]float64, error) {
	out := map[string]float64{}

	ms := o4MsRe.FindStringSubmatch(token)
	if ms == nil {
		return nil, fmt.Errorf("%s = %q states no duration in ms", o4SweepToken, token)
	}
	out["SWEEP_DURATION_MS"], _ = strconv.ParseFloat(ms[1], 64)

	rgba := o4RGBARe.FindStringSubmatch(token)
	if rgba == nil {
		return nil, fmt.Errorf("%s = %q states no rgba() colour", o4SweepToken, token)
	}
	parts := strings.Split(rgba[1], ",")
	if len(parts) != 4 {
		return nil, fmt.Errorf("%s's rgba() carries %d values, want 4", o4SweepToken, len(parts))
	}
	for i, name := range []string{"SWEEP_TINT_R", "SWEEP_TINT_G", "SWEEP_TINT_B", "SWEEP_PEAK_ALPHA"} {
		v, err := strconv.ParseFloat(strings.TrimSpace(parts[i]), 64)
		if err != nil {
			return nil, fmt.Errorf("%s's rgba() component %d: %v", o4SweepToken, i, err)
		}
		out[name] = v
	}

	skew := o4SkewRe.FindStringSubmatch(rule["transform"])
	if skew == nil {
		return nil, fmt.Errorf("%s declares no skewX(<deg>) (it declares transform: %q)",
			o4SweepRule, rule["transform"])
	}
	out["SWEEP_SKEW_DEG"], _ = strconv.ParseFloat(skew[1], 64)

	band, ok := o4Percent(rule["width"])
	if !ok {
		return nil, fmt.Errorf("%s's width %q is not a percentage", o4SweepRule, rule["width"])
	}
	out["SWEEP_BAND_SHARE"] = band

	height, ok := o4Px(rule["height"])
	if !ok {
		return nil, fmt.Errorf("%s's height %q is not a px length", o4SweepRule, rule["height"])
	}
	out["SWEEP_HEIGHT_DP"] = height

	from, ok := o4Percent(rule["left"])
	if !ok {
		return nil, fmt.Errorf("%s's left %q is not a percentage", o4SweepRule, rule["left"])
	}
	out["SWEEP_FROM_SHARE"] = from

	// THE TRAVEL IS A DERIVATION AND NOT A STOP READ BY NAME. `@keyframes sweep` states three
	// stops and only two distinct positions -- the rule's own `left` at 0%, and the far side held
	// from the travel stop to 100% (that hold is what makes a 500ms sweep out of a 6s display
	// loop, which the maquette's own comment says). Reading "the 8.3% stop" would bind this gate
	// to the maquette's DISPLAY timing; reading "the position that is not the start" binds it to
	// the geometry, which is the part that ships.
	seen := map[float64]bool{}
	for _, decls := range stops {
		if p, ok := o4Percent(decls["left"]); ok {
			seen[p] = true
		}
	}
	if len(seen) != 2 {
		return nil, fmt.Errorf("@keyframes sweep states %d distinct left positions, want 2 "+
			"(a start and a far side)", len(seen))
	}
	if !seen[from] {
		return nil, fmt.Errorf("@keyframes sweep starts nowhere near %s's own left %q",
			o4SweepRule, rule["left"])
	}
	for p := range seen {
		if p != from {
			out["SWEEP_TO_SHARE"] = p
		}
	}
	return out, nil
}

func o4Percent(value string) (float64, bool) {
	m := o4PercentRe.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v / 100, true
}

func o4Px(value string) (float64, bool) {
	m := o4PxRe.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// o4DeclaredNumbers reads every `const val NAME = <number>` out of Motion.kt.
func o4DeclaredNumbers(src string) map[string]float64 {
	out := map[string]float64{}
	for _, m := range o4ConstRe.FindAllStringSubmatch(kotlinCodeOnly(src), -1) {
		if v, err := strconv.ParseFloat(m[2], 64); err == nil {
			out[m[1]] = v
		}
	}
	return out
}

// o4NumberFaults compares what Motion declares against what the design states.
func o4NumberFaults(declared, design map[string]float64) []string {
	var faults []string
	for _, name := range o4SortedKeys(design) {
		want := design[name]
		got, ok := declared[name]
		if !ok {
			faults = append(faults, fmt.Sprintf("%s is not declared. The design states it "+
				"(%g) and nothing in the app carries it, so whatever the sweep uses instead came "+
				"from somewhere unstated", name, want))
			continue
		}
		if got != want {
			faults = append(faults, fmt.Sprintf("%s = %g, and the design states %g", name, got, want))
		}
	}
	return faults
}

func o4SortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestD82_TheSweepsNumbersAreTheDesignsOwn joins Motion.kt's eight sweep constants to the two
// documents that state them.
func TestD82_TheSweepsNumbersAreTheDesignsOwn(t *testing.T) {
	tokens := s22bTokenValues(t)
	token, ok := tokens[o4SweepToken]
	if !ok {
		t.Fatalf("ADR-009 D8.2: the token origin declares no %s. O2 added it; without it every "+
			"number below would be read out of nothing.", o4SweepToken)
	}
	rule, ok := s22bMaquetteKitCSS(t)[o4SweepRule]
	if !ok {
		t.Fatalf("ADR-009 D8.2: the maquette's kit block draws no `%s`. ADR-009 D2 makes that "+
			"file normative, so a component the app animates and the maquette does not draw is an "+
			"effect with no design behind it.", o4SweepRule)
	}
	design, err := o4DesignNumbers(token, rule.Decls, o4KeyframeStops(t, "sweep"))
	if err != nil {
		t.Fatalf("ADR-009 D8.2: %v", err)
	}
	if faults := o4NumberFaults(o4DeclaredNumbers(o4MotionSource(t)), design); len(faults) > 0 {
		t.Errorf("ADR-009 D8.2: %d of the sweep's numbers are not the design's:\n\t%s",
			len(faults), strings.Join(faults, "\n\t"))
	}
}

// TestD82_TheNumberJoinCanActuallyFail is its negative control, in memory, through the same two
// functions.
func TestD82_TheNumberJoinCanActuallyFail(t *testing.T) {
	tokens := s22bTokenValues(t)
	rule := s22bMaquetteKitCSS(t)[o4SweepRule]
	stops := o4KeyframeStops(t, "sweep")

	// A perturbed TOKEN moves the duration and the peak alpha; a perturbed RULE moves the
	// geometry. Both halves are exercised, because a comparison that only ever read one of them
	// would be green over a drifted other.
	perturbedToken := strings.Replace(tokens[o4SweepToken], "500ms", "800ms", 1)
	design, err := o4DesignNumbers(perturbedToken, rule.Decls, stops)
	if err != nil {
		t.Fatalf("ADR-009 D8.2: %v", err)
	}
	declared := o4DeclaredNumbers(o4MotionSource(t))
	if faults := o4NumberFaults(declared, design); len(faults) == 0 {
		t.Error("ADR-009 D8.2: a sweep token stating 800ms still agrees with Motion. The join " +
			"reads something other than the token it names.")
	}

	perturbedRule := map[string]string{}
	for k, v := range rule.Decls {
		perturbedRule[k] = v
	}
	perturbedRule["transform"] = "skewX(-40deg)"
	design, err = o4DesignNumbers(tokens[o4SweepToken], perturbedRule, stops)
	if err != nil {
		t.Fatalf("ADR-009 D8.2: %v", err)
	}
	if faults := o4NumberFaults(declared, design); len(faults) == 0 {
		t.Error("ADR-009 D8.2: a maquette skewing the streak -40deg still agrees with Motion. " +
			"The join reads something other than the rule it names.")
	}
}
