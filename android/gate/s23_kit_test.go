package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-7, plus the one PB-DS-5 fence ADR-007 B134
// assigns to the kit rather than to the resources.
//
//	PB-DS-6 "Every visual element is one factory in a single package, styled entirely from the
//	         theme ... Kit coverage is joined to the component inventory bidirectionally."
//	PB-DS-7 "One table, one row per component, each cell either a token, a documented derivation,
//	         or a named exception with its reason ... No cell is a bare hex."
//
// WHAT THIS FILE CAN CHECK AND WHAT IT CANNOT, stated first because the split is the same one
// PB-TOK-1 arrived at and it is load-bearing here too. This gate compares FILES: the kit's
// sources against the design source, the spacing ledger, the checked-in Group join and
// internal/design's derivation table. It cannot say what a component RESOLVES on a running
// resource table, and a value that is right in Kotlin can still be wrong once appcompat,
// camera-view and firebase have merged their resources over the app's. That half is
// PB-DS-10's, and it lives in app/src/test/.../ui/kit/.
//
// SCOPE. S23 builds the INBOX component set: the foundation plus the nine components the triage
// screen needs. The rest of PB-DS-7's 38 are not here and this gate does not pretend they are --
// s23Inbox below is the claim, one row per factory, and both directions are asserted so a
// factory cannot be added without a row and a row cannot survive its factory being deleted.
//
// ui/kit/Motion.kt IS DELIBERATELY OUT OF THE REVERSE DIRECTION. PB-DS-8 owns it, it is being
// written concurrently, and a gate that required every public function in the package to be one
// of this slice's components would fail the moment an animator landed -- turning a fence into a
// coordination problem between two agents. The reverse check is therefore scoped to the files
// s23Inbox names, which is the set this slice is responsible for.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/design"
)

const s23KitPackageDir = "dev/swarm/phone/ui/kit"

// s23ComponentsDoc is PB-DS-7's reviewable table -- the derivation for every component Substrate
// never specified. A kit component with no Substrate CSS rule cites a row in it, and the
// citation is checked, because "derived" with nowhere to look is indistinguishable from invented.
const s23ComponentsDoc = "docs/design/substrate-components.md"

// ---------------------------------------------------------------------------
// The claim: the Inbox component set.
// ---------------------------------------------------------------------------

// s23Component is one factory, and the design authority it answers to.
//
// Origin is a selector in the design source's SHARED structural block -- the Substrate-specified
// components, which need no derivation because the artifact draws them. Derived is a row in
// s23ComponentsDoc, for the parts Substrate never specified. A component may carry BOTH: the
// status dot is drawn by `.pdot` and its four-Group binding is B134's, which only the derivation
// table records.
type s23Component struct {
	Factory string
	File    string
	Origin  string
	Derived string
	Why     string
}

// s23Inbox is the S23 scope. Triage inbox first, because it is the root screen, it exercises the
// most components, and it is where the four-Group identity shows (PB-DS-9's own ordering).
var s23Inbox = []s23Component{
	{
		Factory: "statusDot",
		File:    "StatusDot.kt",
		Origin:  ".pdot",
		Derived: "§4 Status dots, B134 mapping",
		Why: "the 7dp mark and its glow are Substrate's; WHICH Group takes which colour, and " +
			"which two of the four glow at all, is B134's rebinding and exists nowhere else",
	},
	{
		Factory: "sessionRow",
		File:    "SessionRow.kt",
		Origin:  ".prow",
		Why:     "the triage row, with the .prow.attention variant its rail and warmed border",
	},
	{
		Factory: "sessionList",
		File:    "SessionRow.kt",
		Origin:  ".prows",
		Why: "the rows' container carries the 12dp side padding and the 7dp gap. Without it a " +
			"screen types both, which is the PB-DS-6 violation the kit exists to prevent",
	},
	{
		Factory: "workingBar",
		File:    "WorkingBar.kt",
		Origin:  ".workbar",
		Why:     "Substrate's Working affordance is this static gradient plus the dot glow, no pulse",
	},
	{
		Factory: "filterChip",
		File:    "FilterChip.kt",
		Origin:  ".chip",
		Why:     "scope bar chip; .chip.on is the selected variant and .chip .pd the presence dot",
	},
	{
		Factory: "chipRow",
		File:    "FilterChip.kt",
		Origin:  ".chips",
		Why:     "same reason as sessionList: the gap and the side padding belong to the container",
	},
	{
		Factory: "sectionLabel",
		File:    "SectionLabel.kt",
		Origin:  ".plabel",
		Why:     "the Group heading. Uppercase is the component's, per text-transform",
	},
	{
		Factory: "navHeader",
		File:    "NavHeader.kt",
		Origin:  ".pnav",
		Why:     "the root-screen header: big title on the left, live counter pushed right",
	},
	{
		Factory: "liveCounter",
		File:    "NavHeader.kt",
		Origin:  ".pnav .live",
		Why:     "a separate factory because §1.4 ships it beside the badge, counting a different thing",
	},
	{
		Factory: "tabBar",
		File:    "TabBar.kt",
		Origin:  ".ptabs",
		Why:     "the bar, its top rule, its translucency and its four items",
	},
	{
		Factory: "badge",
		File:    "Badge.kt",
		Derived: "#3 Badge",
		Why: "Substrate's artifact has no badge at all -- it uses the live counter instead. " +
			"§1.4 ships both and recolours this one from the mock's retired red to --p-att",
	},
}

// ---------------------------------------------------------------------------
// Reading the kit.
// ---------------------------------------------------------------------------

func s23KitRoot(t *testing.T) string {
	return filepath.Join(kotlinMainRoot(t), filepath.FromSlash(s23KitPackageDir))
}

// s23KitSources returns the kit's files, base name -> RAW source (comments intact).
//
// Raw, because two of the checks below read the machine-parsed `origin:` annotations, which are
// comments. The checks that must not be satisfiable BY a comment -- the elevation fence and the
// colour-literal fence -- strip them with kotlinCodeOnly at their own call site, for the reason
// that helper records.
func s23KitSources(t *testing.T) map[string]string {
	t.Helper()
	root := s23KitRoot(t)
	if !exists(root) {
		t.Fatalf("PB-DS-6: %s does not exist. The requirement's first sentence is that every "+
			"visual element is one factory in a SINGLE PACKAGE styled entirely from the theme; "+
			"today the app's three surface files each build their own views inline, which is how "+
			"24 derived component specs become copy-paste and drift on first edit.",
			mustRel(t, root))
	}
	out := map[string]string{}
	for _, path := range kotlinFiles(t, root) {
		out[filepath.Base(path)] = readFileOrFail(t, path, "PB-DS-6")
	}
	if len(out) == 0 {
		t.Fatalf("PB-DS-6: %s contains no Kotlin; every assertion below would iterate zero times",
			mustRel(t, root))
	}
	return out
}

// s23TopLevelFun matches a top-level factory declaration. Indented `fun` is a method and is not
// part of the kit's surface.
var s23TopLevelFun = regexp.MustCompile(`(?m)^(?:internal\s+)?fun\s+([A-Za-z][A-Za-z0-9]*)\s*\(`)

// s23AnnotationLine reads one machine-read annotation out of a comment, whatever comment shape
// it is written in: `origin: .prow`, ` * origin: .prow`, `// origin: .prow`.
var s23AnnotationLine = regexp.MustCompile(`^(?:\s|\*|/)*(origin|derived):\s*(.+?)\s*(?:\*/)?\s*$`)

// s23Annotations returns every annotation in a source, kind -> values.
func s23Annotations(src string) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(src, "\n") {
		if m := s23AnnotationLine.FindStringSubmatch(line); m != nil {
			out[m[1]] = append(out[m[1]], m[2])
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// PB-DS-6: the kit is the component inventory, in both directions.
// ---------------------------------------------------------------------------

func TestPBDS6_EveryInboxComponentIsAKitFactory(t *testing.T) {
	sources := s23KitSources(t)
	css := s22bSharedCSS(t)

	for _, c := range s23Inbox {
		src, ok := sources[c.File]
		if !ok {
			t.Errorf("PB-DS-6: the kit has no %s, which is where %s() lives.\n\t%s",
				c.File, c.Factory, c.Why)
			continue
		}
		if !s23DeclaresFun(src, c.Factory) {
			t.Errorf("PB-DS-6: %s declares no top-level `fun %s(`. The requirement is one factory "+
				"per visual element; a component that exists only as inline view-building inside a "+
				"screen is the copy-paste this requirement names.\n\t%s", c.File, c.Factory, c.Why)
			continue
		}

		annotations := s23Annotations(src)
		if c.Origin != "" {
			if _, declared := css[c.Origin]; !declared {
				t.Errorf("PB-DS-6: %s cites `%s` as its design origin, and the shared Substrate "+
					"block declares no such rule. An origin nothing can be read from is a "+
					"component whose values came from somewhere else.", c.File, c.Origin)
			}
			if !s23Contains(annotations["origin"], c.Origin) {
				t.Errorf("PB-DS-6: %s carries no `origin: %s` annotation. The annotation is the "+
					"join -- it is what lets this gate and the Robolectric suite compute every "+
					"expected value from the DESIGN rather than from the implementation they are "+
					"checking, which is the arrangement type.xml's `<!-- origin: -->` comments "+
					"already established.", c.File, c.Origin)
			}
		}
		if c.Derived != "" {
			want := s23ComponentsDoc + " " + c.Derived
			if !s23Contains(annotations["derived"], want) {
				t.Errorf("PB-DS-6: %s carries no `derived: %s` annotation. A component Substrate "+
					"never specified must cite the row that specifies it; a derivation with "+
					"nowhere to look is indistinguishable from an invention.", c.File, want)
			}
		}
		if c.Origin == "" && c.Derived == "" {
			t.Errorf("PB-DS-6: the s23Inbox row for %s names neither an origin nor a derivation, "+
				"so nothing in this gate constrains what it paints", c.Factory)
		}
	}
}

// TestPBDS6_EveryKitFactoryIsAnInboxComponent is the reverse direction, and it is the one that
// makes "bidirectional" mean something: a factory nobody declared is a component that entered
// the kit without passing through the inventory, which is precisely how "single origin" decayed
// into "origin plus a few extras" the first time (PB-TOK-5).
//
// Scoped to the files s23Inbox names -- see the package comment on Motion.kt.
func TestPBDS6_EveryKitFactoryIsAnInboxComponent(t *testing.T) {
	sources := s23KitSources(t)

	declared := map[string]bool{}
	owned := map[string]bool{}
	for _, c := range s23Inbox {
		declared[c.Factory] = true
		owned[c.File] = true
	}

	found := 0
	for file, src := range sources {
		if !owned[file] {
			continue
		}
		for _, m := range s23TopLevelFun.FindAllStringSubmatch(kotlinCodeOnly(src), -1) {
			found++
			if !declared[m[1]] {
				t.Errorf("PB-DS-6: %s declares the factory %s(), which no s23Inbox row names. "+
					"Either it is a component and the inventory must say so -- with its design "+
					"origin, which is what makes it checkable -- or it is a helper and belongs "+
					"behind `private`.", file, m[1])
			}
		}
	}
	if found == 0 {
		t.Fatalf("PB-DS-6: no top-level factories found in the files s23Inbox names; this "+
			"direction passed over an empty set and says nothing")
	}
}

// TestPBDS7_EveryDerivationCitationResolvesToARow follows the `derived:` annotations into
// PB-DS-7's table and requires the row to be there.
func TestPBDS7_EveryDerivationCitationResolvesToARow(t *testing.T) {
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")
	sources := s23KitSources(t)

	cited := 0
	for file, src := range sources {
		for _, raw := range s23Annotations(src)["derived"] {
			ref := strings.TrimSpace(strings.TrimPrefix(raw, s23ComponentsDoc))
			if ref == raw {
				t.Errorf("PB-DS-7: %s cites %q, which does not name %s. The derivation table is "+
					"the only place a non-Substrate component is specified; a citation of "+
					"anything else is a value with no authority behind it.", file, raw, s23ComponentsDoc)
				continue
			}
			cited++
			if !s23RowExists(doc, ref) {
				t.Errorf("PB-DS-7: %s cites `%s`, and no such row exists in %s. Either the row "+
					"was renamed and the component now paints to a specification nobody can "+
					"find, or the citation was written from memory.", file, ref, s23ComponentsDoc)
			}
		}
	}
	if cited == 0 {
		t.Errorf("PB-DS-7: no `derived:` citation found anywhere in the kit, yet %d component(s) "+
			"in s23Inbox are specified only by the derivation table. This check passed over an "+
			"empty set.", s23DerivedCount())
	}
}

// s23RowExists finds `#3 Badge` or `§4 Status dots, B134 mapping` in the table.
//
// The two forms are different tables. §3 is numbered, one row per PB-DS-7 component; §4's
// "adjacent derivations" are not numbered because they are not in PB-DS-7's list of 24 -- they
// are the things the eight screens cannot be built without. Both are checked by their leading
// cell, which is the cell that identifies the row.
func s23RowExists(doc, ref string) bool {
	if n, name, ok := strings.Cut(strings.TrimPrefix(ref, "#"), " "); ok && s23IsNumber(n) {
		return strings.Contains(doc, "| "+n+" | "+name+" |")
	}
	if name, ok := strings.CutPrefix(ref, "§4 "); ok {
		return strings.Contains(doc, "| "+name+" |")
	}
	return false
}

func s23IsNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func s23DerivedCount() int {
	n := 0
	for _, c := range s23Inbox {
		if c.Derived != "" {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// PB-DS-5's fence, which B134 puts here rather than in the resources.
// ---------------------------------------------------------------------------

// s23ElevationReach is every way a View acquires a Material shadow.
//
// `elevation` is the obvious implementation of --p-card-fx AND IT IS THE WRONG ONE. Substrate
// bans drop shadows outright -- its own words are that elevation is one ladder step lighter,
// never a shadow -- so the inset key-light is a layer with a 1dp top-edge rect clipped to the
// card radius, and a card that reached for View.elevation would render the one effect the skin
// forbids while looking, in code, exactly like the effect it asks for.
//
// translationZ and outline shadow colours are here because they are the same shadow reached by
// other names: a card with elevation 0 and translationZ 4 casts precisely the shadow this
// forbids.
//
// THE BARE ASSIGNMENT IS THE ONE THAT MATTERS AND IT WAS MISSING. This list first held only
// `setElevation` and `.elevation`, and the negative control caught it: inside an `apply {}` block
// -- which is how every view in this kit is configured -- the Kotlin spelling is `elevation = 2f`,
// with no receiver and no dot. That is the idiomatic form, so the fence was blind to exactly the
// way the mistake would be written.
var s23ElevationReach = []*regexp.Regexp{
	regexp.MustCompile(`\belevation\s*=`),
	regexp.MustCompile(`\btranslationZ\s*=`),
	regexp.MustCompile(`\bsetElevation\s*\(`),
	regexp.MustCompile(`\bsetTranslationZ\s*\(`),
	regexp.MustCompile(`\.elevation\b`),
	regexp.MustCompile(`\.translationZ\b`),
	regexp.MustCompile(`\bsetOutlineSpotShadowColor\b`),
	regexp.MustCompile(`\bsetOutlineAmbientShadowColor\b`),
	regexp.MustCompile(`android:elevation`),
	regexp.MustCompile(`android:translationZ`),
}

func TestPBDS5_TheKitNeverReachesForElevation(t *testing.T) {
	sources := s23KitSources(t)
	for file, src := range sources {
		code := kotlinCodeOnly(src)
		for _, reach := range s23ElevationReach {
			m := reach.FindString(code)
			if m == "" {
				continue
			}
			t.Errorf("PB-DS-5: %s reaches for `%s`. Substrate bans drop shadows -- elevation is one "+
				"ladder step LIGHTER, never a shadow (ADR-007 B134 decision 4) -- so --p-card-fx "+
				"is an INSET 1dp top-edge highlight clipped to the card radius, and --p-elev is "+
				"what a raised surface is made of. This is the wrong implementation despite being "+
				"the obvious one, which is why it is fenced rather than reviewed.", file, m)
		}
	}

	// The fence must recognise the spelling the mistake would actually be written in. Inside an
	// `apply {}` block -- which is how every view in this kit is configured -- that is a bare
	// assignment with no receiver, and an earlier version of this list could not see it.
	for _, probe := range []string{
		"    elevation = 2f",
		"view.elevation = 2f",
		"    setElevation(2f)",
		"    translationZ = 4f",
	} {
		matched := false
		for _, reach := range s23ElevationReach {
			if reach.MatchString(probe) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("the elevation fence does not recognise %q, so a card written that way "+
				"ships the one effect Substrate forbids", probe)
		}
	}
	// And it must not fire on prose. A fence a comment can trip is one the next thorough
	// commenter turns into noise; kotlinCodeOnly is what keeps that true, and this says so.
	for _, notAReach := range []string{"the elevation ladder", "elevationless", "// elevation = 2f"} {
		for _, reach := range s23ElevationReach {
			if reach.MatchString(kotlinCodeOnly(notAReach)) {
				t.Errorf("the elevation fence fires on %q, which reaches for nothing", notAReach)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// PB-DS-6: "styled entirely from the theme".
// ---------------------------------------------------------------------------

// s23ColourLiteral is the same recogniser s22_derived_test.go uses, and it is a separate copy on
// purpose: that one asks whether a DERIVATION's output was transcribed, this one asks whether any
// colour at all was typed. The first is a fence around four values; this is a fence around the
// palette.
var s23ColourLiteral = regexp.MustCompile(`(?i)(?:#|0x)([0-9a-f]{6}|[0-9a-f]{8})\b`)

func TestPBDS6_NoColourIsTypedInTheKit(t *testing.T) {
	sources := s23KitSources(t)
	scanned := 0
	for file, src := range sources {
		code := kotlinCodeOnly(src)
		scanned += len(code)
		for i, line := range strings.Split(code, "\n") {
			if m := s23ColourLiteral.FindStringSubmatch(line); m != nil {
				t.Errorf("PB-DS-6: %s:%d types the colour literal #%s. Every colour the kit paints "+
					"with is R.color.swarm_*, which android/design-tokens.tsv joins to the origin, "+
					"or a documented blend over those. A literal here is a fourth copy of the "+
					"palette in the one file that was supposed to end them.", file, i+1, m[1])
			}
		}
	}
	if scanned == 0 {
		t.Fatal("PB-DS-6: the colour scan read no code at all")
	}
}

// s23TypefaceReach: the type scale is 18 TextAppearance styles, and a Typeface reference is a
// nineteenth chosen at a call site. The three surface files hold five of these today (PB-DS-11
// removes them with the screens, S24); the kit must never acquire one.
var s23TypefaceReach = []string{"Typeface.", "setTypeface(", "setTextSize(", "setLetterSpacing("}

func TestPBDS6_NoTypefaceIsChosenInTheKit(t *testing.T) {
	for file, src := range s23KitSources(t) {
		code := kotlinCodeOnly(src)
		for _, reach := range s23TypefaceReach {
			if strings.Contains(code, reach) {
				t.Errorf("PB-DS-6: %s calls %s. Size, weight, tracking and family come from ONE "+
					"TextAppearance.Swarm.* style per text role (PB-DS-2); setting any of them at "+
					"a call site re-specifies the scale one view at a time, which is the state "+
					"S22 found the app in -- one Typeface.MONOSPACE, one Typeface.BOLD and no "+
					"setTextSize anywhere.", file, reach)
			}
		}
	}
}

// s23SpacingCall is every setter that places a view relative to another, including the
// RTL-correct spellings.
//
// PB-DS-1's own scan (s22b_spacing_test.go) matches `setPadding` and `setMargins` and stops there,
// so `setPaddingRelative`, `setMarginStart` and `setMarginEnd` -- the forms a layout that respects
// RTL actually uses, and the ones this kit uses throughout -- pass through it untouched. That is a
// hole in a fence rather than a decision, and widening PB-DS-1's scan is S22's to do; this closes
// it over the files S23 owns.
// A NESTED CALL IS THE NORMAL CASE HERE, so the argument list is found by balancing parentheses
// rather than by a regexp. Every correct call in this kit reads
// `setPaddingRelative(Kit.dimen(context, R.dimen.swarm_space_12).toInt(), ...)`, and `[^)]*` stops
// at the FIRST close paren -- inside `Kit.dimen(...)` -- so a regexp would inspect a fragment of
// the argument list and report on the rest by accident. The negative control caught this: a raw
// `12` as the first argument of a multi-line call was invisible to it.
var s23SpacingCall = regexp.MustCompile(
	`\bset(?:Padding|PaddingRelative|Margins|MarginStart|MarginEnd)\s*\(`)

// A whole argument that is nothing but a number. Applied to one already-split top-level argument,
// so leading newlines and indentation are stripped first and there is no "is it after a comma"
// question left to get wrong.
var s23LiteralArg = regexp.MustCompile(`^-?\d+$`)

// s23CallArguments returns the top-level arguments of the call whose opening parenthesis is at
// src[open], or nil when the parentheses do not balance.
func s23CallArguments(src string, open int) []string {
	depth := 0
	start := open + 1
	var args []string
	// Kotlin permits a trailing comma, and this kit uses one throughout, so the last split is
	// routinely empty. Dropping empties here rather than at every call site keeps "how many
	// arguments does this call have" the same question a reader would ask.
	add := func(end int) {
		if arg := strings.TrimSpace(src[start:end]); arg != "" {
			args = append(args, arg)
		}
	}
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				add(i)
				return args
			}
		case ',':
			if depth == 1 {
				add(i)
				start = i + 1
			}
		}
	}
	return nil
}

// TestPBDS6_NoRawDimensionIsTypedInTheKit is the dimension half of "styled entirely from the
// theme".
//
// ZERO IS ALLOWED AND NOTHING ELSE IS. A zero has no unit -- 0 px and 0 dp are the same distance
// -- and the design states plenty of them (`.prows { padding: 0 12px }`). Every other number in a
// padding or margin call is a raw PIXEL value, which is the exact defect PB-DS-1 names: the
// constant it replaced was `PADDING = 24` in pixels, rendering at 8 dp on a 3x handset.
func TestPBDS6_NoRawDimensionIsTypedInTheKit(t *testing.T) {
	owned := map[string]bool{"Kit.kt": true, "ColorMix.kt": true, "Surfaces.kt": true}
	for _, c := range s23Inbox {
		owned[c.File] = true
	}
	checked := 0
	for file, src := range s23KitSources(t) {
		if !owned[file] {
			continue
		}
		code := kotlinCodeOnly(src)
		for _, loc := range s23SpacingCall.FindAllStringIndex(code, -1) {
			checked++
			args := s23CallArguments(code, loc[1]-1)
			if args == nil {
				t.Errorf("PB-DS-6: %s: the call at offset %d has unbalanced parentheses, so its "+
					"arguments were not inspected at all", file, loc[0])
				continue
			}
			for _, arg := range args {
				if !s23LiteralArg.MatchString(arg) || arg == "0" {
					continue
				}
				t.Errorf("PB-DS-6: %s calls %s with the literal %s. Every dimension the kit spends "+
					"comes from R.dimen.swarm_* -- the scale PB-DS-1 decided -- or from "+
					"Kit.dp over a KitMetrics constant the design source can be checked against. "+
					"A bare number here is in PIXELS.",
					file, strings.TrimSpace(code[loc[0]:loc[1]]), arg)
			}
		}
	}
	if checked == 0 {
		t.Error("PB-DS-6: the kit makes no padding or margin call at all, so this scan says " +
			"nothing -- and a component set that spaces nothing is not one")
	}

	// The scan must see what it is looking for, in the two spellings that defeated earlier
	// versions of it: PB-DS-1's own scan misses setPaddingRelative entirely, and a regexp-bounded
	// argument list misses a literal that opens a call whose other arguments are nested calls.
	probe := `setPaddingRelative(
        12,
        Kit.dimen(context, R.dimen.swarm_space_12).toInt(),
        0,
        Kit.dp(context, KitMetrics.DOT_DP).toInt(),
    )`
	loc := s23SpacingCall.FindStringIndex(probe)
	if loc == nil {
		t.Fatal("the call scan does not match setPaddingRelative, which is the whole reason it " +
			"exists beside PB-DS-1's")
	}
	args := s23CallArguments(probe, loc[1]-1)
	if len(args) != 4 {
		t.Fatalf("the argument splitter found %d top-level arguments in a four-argument call: %q. "+
			"A splitter that stops at the first close paren inspects a fragment and reports on "+
			"the rest by accident.", len(args), args)
	}
	literals := 0
	for _, a := range args {
		if s23LiteralArg.MatchString(a) {
			literals++
		}
	}
	if literals != 2 {
		t.Fatalf("the literal recogniser found %d bare numbers in %q, want 2 (the raw 12 and the "+
			"zero)", literals, args)
	}
	if s23LiteralArg.MatchString("Kit.dimen(context, R.dimen.swarm_space_12).toInt()") {
		t.Fatal("the literal recogniser matches a resource lookup, so it would fail on the " +
			"correct implementation as readily as on the wrong one")
	}
}

// ---------------------------------------------------------------------------
// PB-DS-6: the spacing a component spends is the step the ledger assigns to its own CSS.
// ---------------------------------------------------------------------------

// s23Spacing binds one CSS spacing declaration to the scale step the kit must spend for it.
//
// The Dimen column is NOT a free choice and this gate does not take it on trust: the step is
// recomputed from s22bScale, PB-DS-1's recorded absorption ledger, against the value the design
// actually declares. What the table contributes is the CLAIM -- that this component's padding is
// that declaration -- which is a decision a reviewer can disagree with, and which no amount of
// scanning could infer.
var s23Spacing = []struct {
	File     string
	Selector string
	Property string
	Index    int
	Dimen    string
}{
	{"SessionRow.kt", ".prow", "padding", 0, "swarm_space_10"},
	{"SessionRow.kt", ".prow", "padding", 1, "swarm_space_12"},
	{"SessionRow.kt", ".prow .t", "gap", 0, "swarm_space_8"},
	{"SessionRow.kt", ".prow .ln", "margin-top", 0, "swarm_space_4"},
	{"SessionRow.kt", ".prows", "padding", 1, "swarm_space_12"},
	{"SessionRow.kt", ".prows", "gap", 0, "swarm_space_8"},
	{"WorkingBar.kt", ".workbar", "margin", 0, "swarm_space_2"},
	{"FilterChip.kt", ".chip", "padding", 0, "swarm_space_8"},
	{"FilterChip.kt", ".chip", "padding", 1, "swarm_space_10"},
	{"FilterChip.kt", ".chip .pd", "margin-right", 0, "swarm_space_4"},
	{"FilterChip.kt", ".chips", "gap", 0, "swarm_space_8"},
	{"FilterChip.kt", ".chips", "padding", 1, "swarm_space_18"},
	{"FilterChip.kt", ".chips", "padding", 2, "swarm_space_12"},
	{"SectionLabel.kt", ".plabel", "padding", 0, "swarm_space_12"},
	{"SectionLabel.kt", ".plabel", "padding", 1, "swarm_space_18"},
	{"SectionLabel.kt", ".plabel", "padding", 2, "swarm_space_8"},
	{"NavHeader.kt", ".pnav", "padding", 0, "swarm_space_4"},
	{"NavHeader.kt", ".pnav", "padding", 1, "swarm_space_18"},
	{"NavHeader.kt", ".pnav", "padding", 2, "swarm_space_10"},
	{"NavHeader.kt", ".pnav", "gap", 0, "swarm_space_10"},
	{"TabBar.kt", ".ptabs", "padding-bottom", 0, "swarm_space_14"},
	{"TabBar.kt", ".ptabs div", "gap", 0, "swarm_space_4"},
}

func TestPBDS6_EveryKitSpacingIsTheLedgersStep(t *testing.T) {
	sources := s23KitSources(t)
	css := s22bSharedCSS(t)

	absorbs := map[float64]string{}
	for _, step := range s22bScale {
		for _, literal := range step.Absorbs {
			absorbs[literal] = step.Name
		}
	}
	if len(absorbs) == 0 {
		t.Fatal("PB-DS-6: the absorption ledger is empty; every expectation below would be zero")
	}

	for _, s := range s23Spacing {
		rule, ok := css[s.Selector]
		if !ok {
			t.Errorf("PB-DS-6: the design declares no `%s`, so the %s row claiming its %s is "+
				"pointed at nothing", s.Selector, s.File, s.Property)
			continue
		}
		value, ok := rule.Decls[s.Property]
		if !ok {
			t.Errorf("PB-DS-6: `%s` declares no %s", s.Selector, s.Property)
			continue
		}
		fields := strings.Fields(value)
		if s.Index >= len(fields) {
			t.Errorf("PB-DS-6: `%s { %s: %s }` has no field %d", s.Selector, s.Property, value, s.Index)
			continue
		}
		px, ok := s22bPx(fields[s.Index])
		if !ok {
			t.Errorf("PB-DS-6: `%s { %s }` field %d is %q, not a px length",
				s.Selector, s.Property, s.Index, fields[s.Index])
			continue
		}
		want, ok := absorbs[px]
		if !ok {
			t.Errorf("PB-DS-6: the scale absorbs no %gpx, so `%s { %s }` cannot be spent from it "+
				"at all -- which is a hole in PB-DS-1's ledger, not in this component",
				px, s.Selector, s.Property)
			continue
		}
		if want != s.Dimen {
			t.Errorf("PB-DS-6: `%s { %s }` is %gpx, which PB-DS-1's ledger absorbs into %s, but "+
				"the %s row spends %s. The ledger is the authority; a component that rounds a "+
				"design value its own way is where a 2dp grid stops being one.",
				s.Selector, s.Property, px, want, s.File, s.Dimen)
			continue
		}
		src, ok := sources[s.File]
		if !ok {
			t.Errorf("PB-DS-6: %s does not exist, so its spacing cannot be checked", s.File)
			continue
		}
		if !strings.Contains(kotlinCodeOnly(src), "R.dimen."+s.Dimen) {
			t.Errorf("PB-DS-6: %s never references R.dimen.%s, which is the step PB-DS-1's ledger "+
				"assigns to `%s { %s }` = %gpx. A dimension that is not read from the scale is one "+
				"typed at the call site, and the constant this requirement replaced was "+
				"PhoneSurface's `PADDING = 24` in raw pixels.",
				s.File, s.Dimen, s.Selector, s.Property, px)
		}
	}
}

// ---------------------------------------------------------------------------
// PB-DS-7: the numbers the scale does not govern.
// ---------------------------------------------------------------------------

// s23MetricConst matches `const val NAME = 7f` (or 0.045f), which is the only shape a kit
// constant may take: a named Float in KitMetrics.
var s23MetricConst = regexp.MustCompile(`^\s*(?:internal\s+)?const\s+val\s+([A-Z][A-Z0-9_]*)\s*=\s*(-?[0-9.]+)f\s*$`)

// s23MetricCSSOrigin is `origin: .pdot { width }` -- a declaration in the shared block.
var s23MetricCSSOrigin = regexp.MustCompile(`^(?:\s|\*|/)*origin:\s*(\S.*?)\s*\{\s*([a-z-]+)\s*\}\s*(?:\*/)?\s*$`)

// s23MetricDerivedOrigin is `derived: docs/design/substrate-components.md #3 Badge` -- the escape
// hatch for a constant the design source cannot supply because Substrate never specified the
// component. It is checked for CITATION rather than for value: TestPBDS7_EveryDerivationCitation-
// ResolvesToARow follows it into the table, and the arithmetic that number has to satisfy is
// asserted where it is observable, on the running resource table (the badge's radius is half its
// height, so --p-chip-r renders a pill).
var s23MetricDerivedOrigin = regexp.MustCompile(`^(?:\s|\*|/)*derived:\s*(\S.*?)\s*(?:\*/)?\s*$`)

// s23MetricTokenOrigin is `origin: --p-card-fx alpha` -- a part of a token's value, for the four
// effect tokens that have no colour resource and no CSS rule of their own.
var s23MetricTokenOrigin = regexp.MustCompile(`^(?:\s|\*|/)*origin:\s*(--[a-z0-9-]+)\s+(px|alpha|stop)\s*(?:\*/)?\s*$`)

var s23PxRe = regexp.MustCompile(`([0-9]*\.?[0-9]+)px`)
var s23RGBARe = regexp.MustCompile(`rgba\(\s*[0-9]+\s*,\s*[0-9]+\s*,\s*[0-9]+\s*,\s*([0-9]*\.?[0-9]+)\s*\)`)
var s23StopRe = regexp.MustCompile(`([0-9]*\.?[0-9]+)%`)

// TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber joins KitMetrics to the design.
//
// WHY THESE CONSTANTS EXIST AT ALL, since PB-DS-11's whole point is that no visual constant may
// enter the app except through the theme. The 7dp status dot, the 9dp glow radius, the 3dp
// workbar, the 2dp attention rail, the 1dp hairline and the 0.88 tab-bar alpha are values the
// design states and the RESOURCES cannot carry: they are not spacing (a 2dp grid has nothing to
// say about a dot's diameter), not radii, and --p-tabbg / --p-card-fx / --p-workbar are declared
// `effect` in tokens.json, so PB-TOK-6's converters produce no <color> or <dimen> for them.
//
// So they are named constants with a machine-read origin, and this test is what makes that
// survivable: every one of them is COMPUTED from the design source or from the token it names,
// and a constant with no origin annotation fails rather than being skipped -- which is the
// difference between a small documented set and a place to put numbers.
// SCOPED TO THE FILES s23Inbox NAMES, for the reason the package comment gives about Motion.kt:
// PB-DS-8's constants are durations and easing control points, whose origin is the motion
// decision rather than a rule in the shared CSS block, and requiring them to cite a `{ property }`
// there would be this slice failing on another's file.
func TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber(t *testing.T) {
	sources := s23KitSources(t)
	css := s22bSharedCSS(t)
	tokens := s22bTokenValues(t)

	owned := map[string]bool{"Kit.kt": true, "ColorMix.kt": true, "Surfaces.kt": true}
	for _, c := range s23Inbox {
		owned[c.File] = true
	}

	checked := 0
	for file, src := range sources {
		if !owned[file] {
			continue
		}
		pendingCSS := ""
		pendingProp := ""
		pendingToken := ""
		pendingPart := ""
		pendingDerived := ""
		for i, line := range strings.Split(src, "\n") {
			if m := s23MetricTokenOrigin.FindStringSubmatch(line); m != nil {
				pendingToken, pendingPart = m[1], m[2]
				pendingCSS, pendingProp, pendingDerived = "", "", ""
				continue
			}
			if m := s23MetricCSSOrigin.FindStringSubmatch(line); m != nil {
				pendingCSS, pendingProp = m[1], m[2]
				pendingToken, pendingPart, pendingDerived = "", "", ""
				continue
			}
			if m := s23MetricDerivedOrigin.FindStringSubmatch(line); m != nil {
				pendingDerived = m[1]
				pendingCSS, pendingProp, pendingToken, pendingPart = "", "", "", ""
				continue
			}
			m := s23MetricConst.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name, raw := m[1], m[2]
			got, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				t.Errorf("%s:%d: %s = %sf is not a number", file, i+1, name, raw)
				continue
			}

			switch {
			case pendingCSS != "":
				want, err := s23CSSMetric(css, pendingCSS, pendingProp)
				if err != nil {
					t.Errorf("PB-DS-7: %s:%d: %s cites `%s { %s }`: %v",
						file, i+1, name, pendingCSS, pendingProp, err)
				} else if want != got {
					t.Errorf("PB-DS-7: %s:%d: %s = %g, but the design's `%s { %s }` is %g. The "+
						"design's px is Android dp at 1:1, so this is a transcription error and "+
						"nothing else.", file, i+1, name, got, pendingCSS, pendingProp, want)
				}
				checked++
			case pendingToken != "":
				want, err := s23TokenMetric(tokens, pendingToken, pendingPart)
				if err != nil {
					t.Errorf("PB-DS-7: %s:%d: %s cites `%s %s`: %v",
						file, i+1, name, pendingToken, pendingPart, err)
				} else if want != got {
					t.Errorf("PB-DS-7: %s:%d: %s = %g, but %s declares %s = %g in the token origin",
						file, i+1, name, got, pendingToken, pendingPart, want)
				}
				checked++
			case pendingDerived != "":
				// The citation itself is checked by TestPBDS7_EveryDerivationCitationResolvesToARow.
				checked++
			default:
				t.Errorf("PB-DS-7: %s:%d: `const val %s` carries no `origin:` annotation. A number "+
					"in this file with no design behind it is exactly the thing the kit exists to "+
					"stop reaching the screens -- and it is invisible in review, because a "+
					"plausible dp value looks like every other plausible dp value.", file, i+1, name)
			}
			pendingCSS, pendingProp, pendingToken, pendingPart, pendingDerived = "", "", "", "", ""
		}
	}
	if checked == 0 {
		t.Error("PB-DS-7: no annotated metric constant was checked against the design; either the " +
			"kit declares none (and every component's fixed sizes came from somewhere unstated) " +
			"or the annotation parser stopped matching")
	}
}

// s23CSSMetric reads the first px length out of one declaration. One rule for every site --
// `width: 7px`, `border: 1px solid var(--p-hair)`, `box-shadow: 0 0 9px color-mix(...)`,
// `backdrop-filter: blur(16px)` -- because the alternative is a parser per property and four
// chances to read the wrong field.
func s23CSSMetric(css map[string]s22bCSSRule, selector, property string) (float64, error) {
	rule, ok := css[selector]
	if !ok {
		return 0, fmt.Errorf("the shared block declares no such rule")
	}
	value, ok := rule.Decls[property]
	if !ok {
		return 0, fmt.Errorf("the rule declares no %s", property)
	}
	m := s23PxRe.FindStringSubmatch(value)
	if m == nil {
		return 0, fmt.Errorf("%q carries no px length", value)
	}
	return strconv.ParseFloat(m[1], 64)
}

// s23TokenMetric reads a part out of an `effect` token's value.
func s23TokenMetric(tokens map[string]string, token, part string) (float64, error) {
	value, ok := tokens[token]
	if !ok {
		return 0, fmt.Errorf("the token origin declares no %s", token)
	}
	var m []string
	switch part {
	case "px":
		m = s23PxRe.FindStringSubmatch(value)
	case "alpha":
		m = s23RGBARe.FindStringSubmatch(value)
	case "stop":
		m = s23StopRe.FindStringSubmatch(value)
	}
	if m == nil {
		return 0, fmt.Errorf("%q carries no %s", value, part)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	if part == "stop" {
		v /= 100
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// PB-DS-7 / PB-TOK-8: the four Groups, and which two of them glow.
// ---------------------------------------------------------------------------

var s23GroupBinding = regexp.MustCompile(`"([a-z_]+)"\s*->\s*R\.color\.([a-z_]+)`)
var s23GroupShare = regexp.MustCompile(`"([a-z_]+)"\s*->\s*([0-9]*\.?[0-9]+)f`)

// TestPBDS7_TheStatusDotBindingIsTheCheckedInMapping joins the kit's Group table THROUGH the two
// checked-in joins to the origin: group-tokens.tsv says which token a Group is, and
// design-tokens.tsv says which <color> that token is. The kit may not shortcut either hop.
//
// This is the requirement's one genuinely load-bearing colour decision. B134 moved green from
// Completed to ReadyForReview and gave Completed the recessive grey; an implementer reading only
// Substrate's artifact would paint the green dot "Done", because that is what the artifact's demo
// phone labels it.
func TestPBDS7_TheStatusDotBindingIsTheCheckedInMapping(t *testing.T) {
	sources := s23KitSources(t)
	src, ok := sources["Kit.kt"]
	if !ok {
		t.Fatal("PB-DS-7: the kit has no Kit.kt, which is where the Group binding lives")
	}
	code := kotlinCodeOnly(src)

	tokenOf := map[string]string{}
	for _, row := range loadGroupTokenMap(t) {
		tokenOf[row.Value] = row.Token
	}
	resourceOf := map[string]string{}
	for _, row := range loadTokenMap(t) {
		resourceOf[row.Token] = row.Resource
	}
	if len(tokenOf) == 0 || len(resourceOf) == 0 {
		t.Fatal("PB-DS-7: one of the two checked-in joins read empty; the comparison below would " +
			"pass over nothing")
	}

	bound := map[string]string{}
	for _, m := range s23GroupBinding.FindAllStringSubmatch(code, -1) {
		bound[m[1]] = m[2]
	}
	for group, token := range tokenOf {
		want, ok := resourceOf[token]
		if !ok {
			t.Errorf("PB-DS-7: group %s is bound to %s, which android/design-tokens.tsv maps to "+
				"no colour resource, so the kit has nothing to paint it with", group, token)
			continue
		}
		got, ok := bound[group]
		if !ok {
			t.Errorf("PB-DS-7: the kit binds no colour to status.Group %q. All four Groups are "+
				"rendered on the inbox at once; a Group the dot cannot colour is a section of "+
				"sessions with no state.", group)
			continue
		}
		if got != want {
			t.Errorf("PB-DS-7: the kit paints group %s with R.color.%s, but PB-TOK-8 binds it to "+
				"%s = R.color.%s. ADR-007 B134 decision 1 is the rebinding -- green moved to "+
				"ReadyForReview and Completed took the recessive grey -- and Substrate's own demo "+
				"labels the green dot \"Done\", so getting this from the artifact gives the wrong "+
				"answer.", group, got, token, want)
		}
	}
	for group := range bound {
		if _, ok := tokenOf[group]; !ok {
			t.Errorf("PB-DS-7: the kit binds a colour to %q, which is not a status.Group in "+
				"android/group-tokens.tsv. The phone renders the server's Group verbatim and "+
				"never invents one.", group)
		}
	}
}

// TestPBDS7_TheDotGlowsAreTheDeclaredDerivations checks the other half of the dot: which Groups
// glow, and by how much.
//
// The shares are read out of internal/design.Derivations() rather than out of the CSS, because
// that table is what PB-TOK-7 already fences the RESOLVED values against -- so the kit computing
// the blend from a share this gate joined to the same table is the supported way to obtain a
// colour the gate forbids typing.
//
// Substrate's rule is "nothing glows unless it is alive". Exactly two Groups are alive.
func TestPBDS7_TheDotGlowsAreTheDeclaredDerivations(t *testing.T) {
	src, ok := s23KitSources(t)["Kit.kt"]
	if !ok {
		t.Fatal("PB-DS-7: the kit has no Kit.kt")
	}
	code := kotlinCodeOnly(src)

	tokenOf := map[string]string{}
	for _, row := range loadGroupTokenMap(t) {
		tokenOf[row.Value] = row.Token
	}

	// The dot glows, keyed by the token they are a blend of. Selected by Site rather than by name
	// so that a derivation renamed upstream fails loudly instead of silently dropping out.
	wantShare := map[string]float64{}
	for _, d := range design.Derivations() {
		if strings.HasPrefix(d.Site, ".pdot") {
			wantShare[d.Base] = float64(d.Percent) / 100
		}
	}
	if len(wantShare) == 0 {
		t.Fatal("PB-DS-7: internal/design declares no .pdot derivation, so this test would " +
			"require the kit to glow nowhere and pass over an empty set")
	}

	got := map[string]float64{}
	for _, m := range s23GroupShare.FindAllStringSubmatch(code, -1) {
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		got[m[1]] = v
	}

	for group, token := range tokenOf {
		want, glows := wantShare[token]
		switch {
		case glows && got[group] != want:
			t.Errorf("PB-DS-7: group %s (%s) must glow at %g of its own colour -- the derivation "+
				"internal/design declares for `.pdot` -- and the kit declares %g. The glow is "+
				"`Paint.setShadowLayer(9dp, 0, 0, blend)` on a software layer; the blend itself "+
				"may not be typed (PB-TOK-7), so the share is what the kit carries.",
				group, token, want, got[group])
		case !glows && got[group] != 0:
			t.Errorf("PB-DS-7: group %s (%s) glows at %g in the kit, and the design declares no "+
				"glow for it. Substrate's stated rule is that nothing glows unless it is alive: "+
				"ReadyForReview is finished work waiting on a human and Completed is finished, so "+
				"neither is. `.pdot.ok` sets `box-shadow: none` explicitly.",
				group, token, got[group])
		}
	}
}

// ---------------------------------------------------------------------------
// Negative controls.
// ---------------------------------------------------------------------------

// TestPBDS7_TheMetricJoinCanActuallyFail is the control PB-DS-10 names, applied to the half of
// this gate that is arithmetic rather than presence.
//
// Two ways TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber is green while proving nothing: the
// design readers return the same number for everything, or they return nothing and the loop
// never runs. Both are exercised against facts the design states and that are NOT equal.
func TestPBDS7_TheMetricJoinCanActuallyFail(t *testing.T) {
	css := s22bSharedCSS(t)
	tokens := s22bTokenValues(t)

	cases := []struct {
		selector string
		property string
		want     float64
	}{
		{".pdot", "width", 7},
		{".pdot.att", "box-shadow", 9},
		{".workbar", "height", 3},
		{".workbar", "border-radius", 2},
		{".prow", "border", 1},
		{".prow.attention::before", "width", 2},
		{".chip .pd", "width", 5},
		{".ptabs svg", "width", 22},
		{".ptabs", "backdrop-filter", 16},
	}
	seen := map[float64]bool{}
	for _, c := range cases {
		got, err := s23CSSMetric(css, c.selector, c.property)
		if err != nil {
			t.Errorf("`%s { %s }`: %v", c.selector, c.property, err)
			continue
		}
		if got != c.want {
			t.Errorf("the CSS metric reader returns %g for `%s { %s }`, and the design says %g. "+
				"Every expectation in this gate goes through this reader.",
				got, c.selector, c.property, c.want)
		}
		seen[got] = true
	}
	if len(seen) < 6 {
		t.Errorf("the CSS metric reader produced only %d distinct values across %d different "+
			"declarations, so it is not reading the declarations -- and every equality built on "+
			"it passes over values that differ", len(seen), len(cases))
	}

	for _, c := range []struct {
		token string
		part  string
		want  float64
	}{
		{"--p-card-fx", "px", 1},
		{"--p-card-fx", "alpha", 0.045},
		{"--p-tabbg", "alpha", 0.88},
		{"--p-workbar", "stop", 0.85},
	} {
		got, err := s23TokenMetric(tokens, c.token, c.part)
		if err != nil {
			t.Errorf("`%s %s`: %v", c.token, c.part, err)
			continue
		}
		if got != c.want {
			t.Errorf("the token metric reader returns %g for `%s %s`, and the origin declares %g",
				got, c.token, c.part, c.want)
		}
	}

	// And it must refuse what it cannot read, rather than returning zero. A reader that answered
	// 0 for a missing declaration would make every constant that happens to be 0 pass, and would
	// silently accept an origin annotation naming a rule that does not exist.
	if _, err := s23CSSMetric(css, ".no-such-rule", "width"); err == nil {
		t.Error("the CSS metric reader accepted a selector the design does not declare")
	}
	if _, err := s23CSSMetric(css, ".pdot", "no-such-property"); err == nil {
		t.Error("the CSS metric reader accepted a property the rule does not declare")
	}
	if _, err := s23TokenMetric(tokens, "--p-card-fx", "stop"); err == nil {
		t.Error("the token metric reader found a gradient stop in a box-shadow")
	}
}

// TestPBDS6_TheAnnotationParserCanActuallyFail guards the other half: the origin annotations are
// what let this gate and the Robolectric suite compute from the design, and a parser that matched
// nothing would make every "the component cites its origin" assertion fail constantly -- while a
// parser that matched everything would make them pass over any text at all.
func TestPBDS6_TheAnnotationParserCanActuallyFail(t *testing.T) {
	got := s23Annotations(strings.Join([]string{
		"/**",
		" * The triage row.",
		" *",
		" * origin: .prow",
		" * derived: docs/design/substrate-components.md #3 Badge",
		" */",
		"fun sessionRow() {}",
		"// this line mentions an origin story and must not be read as one",
	}, "\n"))

	if !s23Contains(got["origin"], ".prow") {
		t.Errorf("the annotation parser missed `origin: .prow` in a KDoc block: %v", got["origin"])
	}
	if !s23Contains(got["derived"], s23ComponentsDoc+" #3 Badge") {
		t.Errorf("the annotation parser missed the derived citation: %v", got["derived"])
	}
	if len(got["origin"]) != 1 {
		t.Errorf("the annotation parser read %d origins from a source with one: %v",
			len(got["origin"]), got["origin"])
	}

	// The row lookup must distinguish a real row from a plausible one.
	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-7")
	if !s23RowExists(doc, "#3 Badge") {
		t.Error("the row lookup cannot find `#3 Badge`, which the derivation table declares")
	}
	if !s23RowExists(doc, "§4 Status dots, B134 mapping") {
		t.Error("the row lookup cannot find the §4 status-dot row")
	}
	if s23RowExists(doc, "#3 Toast") {
		t.Error("the row lookup matched `#3 Toast`; row 3 is the Badge, so it is matching on the " +
			"number alone and a citation of the wrong row would pass")
	}
	if s23RowExists(doc, "#99 Nothing") {
		t.Error("the row lookup matched a row that does not exist")
	}
}

// ---------------------------------------------------------------------------

func s23DeclaresFun(src, name string) bool {
	for _, m := range s23TopLevelFun.FindAllStringSubmatch(kotlinCodeOnly(src), -1) {
		if m[1] == name {
			return true
		}
	}
	return false
}

func s23Contains(haystack []string, want string) bool {
	for _, v := range haystack {
		if v == want {
			return true
		}
	}
	return false
}
