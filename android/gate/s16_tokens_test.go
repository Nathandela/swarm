package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-TOK-1, REASSIGNED S5 -> S16 on 2026-07-25.
// WIDENED for PB-TOK-5 and PB-TOK-6 by S22a on 2026-07-31.
//
// "One machine-readable token source (JSON) is the single origin for the Android theme.
//  Theme generated from or asserted against the JSON. THE VALUES MUST ACTUALLY AGREE, and the
//  assertion must fail when they diverge."
//
// THE STATE OF THE WORLD when the PB-TOK-1 assertions were written:
//
//	internal/design/tokens.json   --p-bg   #08090a     --p-ink  #f7f8f8
//	android/.../res/values/colors.xml      swarm_background #FF101114
//	                                       swarm_text_primary #FFE6E8EB
//
// They disagree, and nothing anywhere references tokens.json outside internal/design/ -- there
// is no join in either direction, in any language. S5 delivered the JSON and drift-guarded it
// against the design source, which is real and is half the requirement; the other half is only
// satisfiable where the theme lives.
//
// THE VALUES ARE A THIRD COPY, WHICH IS THE PART THAT MAKES THIS WORSE THAN A TYPO. The same
// literals appear again in SwarmTheme.EXPECTED_DARK_COLORS, whose own doc says they are there
// so the theme test "compares the resolved theme against a recorded number rather than against
// itself". That is a sound instinct pointed at the wrong number: recorded from colors.xml, it
// certifies that the app renders what colors.xml says, which is exactly what it would do if
// colors.xml were wrong. Recorded from tokens.json it certifies the requirement.
//
// colors.xml's own comment calls its values "placeholders for the skeleton", so this was
// disclosed rather than hidden -- but disclosure is not delivery, and the placeholder comment
// is precisely what a reader would trust instead of checking.
//
// ---------------------------------------------------------------------------------------------
// WHAT S22a CHANGED, AND WHY THE FIRST VERSION OF THIS FILE COULD NOT HAVE BEEN LEFT ALONE
// ---------------------------------------------------------------------------------------------
//
// PB-TOK-5: the join was correct and UNDER-FED. It ran three tokens (--p-bg, --p-ink, --p-ink2)
// through a fence built for the whole palette, so 14 of 17 colours stayed pinned against a design
// source no screen could consume. Widening the table is the entire fix; nothing about the
// comparison changed, which is the point -- a fence that needed rebuilding to carry more rows was
// never the fence it claimed to be. Note --p-cta-bg and --p-cta-ink are byte-identical to
// --p-hero and --p-hero-ink today. They get their OWN rows and their own resources: a future skin
// can break the alias, and a join that deduplicated by value would not notice it had.
//
// PB-TOK-6: the old comparison sniffed the VALUE -- `strings.HasPrefix(v, "#")` -- and treated
// "does not look like a colour" as "cannot be converted". That was right while nothing consumed
// the other 15 tokens and wrong the moment something did, for three separate reasons:
//
//   - it is silently WRONG about --p-tabbg, which is rgba(8,9,10,0.88): a real colour that the
//     hex sniff files under "no ARGB form" forever;
//   - it cannot tell --p-grain (0.05) from --p-display-wt (650), two bare numbers that mean
//     nothing alike;
//   - a token could change kind -- a radius retyped as a colour -- and the sniff would simply
//     start converting it, because the sniff has no expectation to violate.
//
// So the origin now STATES each token's kind and this gate dispatches on that word. A kind with
// no converter is a hard failure naming the kind, not a skip; and a row whose kind column
// disagrees with the origin fails before any conversion is attempted.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// tokensPath is the ONE machine-readable origin.
const tokensRelPath = "internal/design/tokens.json"

// tokenMapFile is the checked-in join. It is a TSV beside the other checked-in policy tables
// (connectivity-policy.tsv, fcm-priority.tsv, supported-versions.tsv) and for the same reason
// S13 gave: a table both a Go gate and a Kotlin test read is one artifact, and a mapping
// expressed as a naming convention is a mapping nobody can review.
const tokenMapFile = "design-tokens.tsv"

// colourTokenCount is PB-TOK-5's number: the skin declares 17 colour-typed tokens, and all 17 must
// reach the app. It is pinned so that DELETING a row cannot make this gate pass -- without it,
// the completeness assertion is satisfied by an empty join and an empty colors.xml.
const colourTokenCount = 17

// designTokens is the parsed origin. Kinds is PB-TOK-6's addition and is what this gate
// dispatches on, in place of guessing from the value.
type designTokens struct {
	Tokens map[string]string `json:"tokens"`
	Kinds  map[string]string `json:"kinds"`
}

func loadDesignTokens(t *testing.T) designTokens {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(tokensRelPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-TOK-1: the token origin %s is unreadable: %v", tokensRelPath, err)
	}
	var out designTokens
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("PB-TOK-1: %s is not valid JSON: %v", tokensRelPath, err)
	}
	if len(out.Tokens) == 0 {
		t.Fatalf("PB-TOK-1: %s declares no tokens", tokensRelPath)
	}
	if len(out.Kinds) == 0 {
		t.Fatalf("PB-TOK-6: %s declares no \"kinds\" object, so this gate would have to go back to "+
			"guessing each token's kind from the shape of its value -- which is what it did before "+
			"PB-TOK-6 and what silently excluded rgba(8,9,10,0.88) from ever being a colour",
			tokensRelPath)
	}
	return out
}

// ---------------------------------------------------------------------------
// PB-TOK-6: one converter per kind, and no converter means FAIL.
// ---------------------------------------------------------------------------

// resourceConverter is how one kind of token becomes one kind of Android resource.
//
// It is a table rather than a switch so that "a token whose kind has no converter fails the gate
// rather than being skipped" is a LOOKUP MISS -- a state the code cannot forget to handle --
// instead of a default branch someone can quietly widen.
type resourceConverter struct {
	// File is the res/values file that carries this kind, relative to app/src/main/res/values.
	File string
	// Element is the XML element name inside it.
	Element string
	// Convert turns a token's value into the literal that resource must carry, already in the
	// canonical form Normalize produces. It returns an error when the value is not one this
	// kind can legally hold, which is the honest answer -- never a best-effort conversion.
	Convert func(string) (string, error)
	// Normalize is how a literal read out of the XML is compared with a converted one. It is
	// deliberately small: every difference it erases is a difference this gate can no longer
	// see, so it erases only case (for hex) or surrounding space.
	Normalize func(string) string
	// TokenDerivedNamespace says whether EVERY resource of this element in File must have a row
	// in the join, which is the reverse half of "single origin".
	//
	// It is true for colours and false for dimensions, and the asymmetry is a fact about the
	// origin rather than a concession. tokens.json declares the whole palette, so a <color> with
	// no row is a colour that entered the theme from somewhere else -- exactly the decay this
	// gate exists to stop. It declares NO SPACING SCALE at all: the 2/4/6/.../24 dp steps and the
	// frame constants in dimens.xml are read off the design artifact's own spacing histogram, a
	// different origin with its own gate (PB-DS-1). Demanding a token row for them would demand
	// tokens that were deliberately never minted, so the reverse direction for <dimen> is
	// PB-DS-4's to enforce over the four radii, and this gate stays out of a namespace it does
	// not own. The FORWARD direction still applies to every kind: a row here must agree.
	TokenDerivedNamespace bool
}

// kindConverters is the whole of PB-TOK-6's dispatch.
//
// SCOPE, stated so the gaps are visible rather than inferred. S22a owns the two kinds that land
// as res/values resources with an exact conversion: `color` (16 tokens -> <color>) and `dimen`
// (5 radii -> <dimen>). The remaining three kinds are PB-DS's half of PB-TOK-6 -- `font` and
// `weight` and `tracking` land in values/type.xml as text-appearance attributes rather than as
// standalone resources, and `effect` (5 tokens: two shadows, a gradient, a translucent fill and a
// grain opacity) has no Android resource primitive at all, which ADR-007 B134 decision 4 records
// as a custom-Drawable problem rather than a conversion problem. Until an entry exists here, a
// row naming one of those kinds FAILS, loudly, which is the requirement.
var kindConverters = map[string]resourceConverter{
	"color": {
		File:                  "colors.xml",
		Element:               "color",
		Convert:               argbFromToken,
		Normalize:             strings.ToUpper,
		TokenDerivedNamespace: true,
	},
	"dimen": {
		File:                  "dimens.xml",
		Element:               "dimen",
		Convert:               dpFromToken,
		Normalize:             strings.ToLower,
		TokenDerivedNamespace: false,
	},
}

// argbFromToken converts a #rrggbb design token into the opaque #AARRGGBB an Android colour
// resource carries. It is the WHOLE comparison for the colour kind, so it lives in one function
// and is mutation-tested below rather than being inlined at each call site.
// gateRGBARe mirrors internal/design's rgba() notation deliberately: this gate must not import
// the package it audits.
var gateRGBARe = regexp.MustCompile(`^rgba\(\s*([0-9]{1,3})\s*,\s*([0-9]{1,3})\s*,\s*([0-9]{1,3})\s*,\s*(0|1|0?\.[0-9]+)\s*\)$`)

func argbFromToken(token string) (string, error) {
	v := strings.TrimSpace(token)
	// CSS rgba(), which the Substrate skin uses for --p-tabbg. Reading it here is the fix for an
	// audit finding: that token was typed `effect` because this converter could not read it, and
	// the miscategorisation was what made "all the colours reach the app" true by construction.
	if m := gateRGBARe.FindStringSubmatch(v); m != nil {
		ch := [3]uint64{}
		for i := 0; i < 3; i++ {
			n, err := strconv.ParseUint(m[i+1], 10, 16)
			if err != nil || n > 255 {
				return "", fmt.Errorf("token value %q: channel %q is not 0-255", token, m[i+1])
			}
			ch[i] = n
		}
		a, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			return "", fmt.Errorf("token value %q: alpha %q is not a fraction", token, m[4])
		}
		return strings.ToUpper(fmt.Sprintf("#%02x%02x%02x%02x", uint8(math.Round(a*255)), ch[0], ch[1], ch[2])), nil
	}
	if !strings.HasPrefix(v, "#") {
		return "", fmt.Errorf("token value %q is typed `color` but is not a hex or rgba() colour", token)
	}
	switch len(v) {
	case 7: // #rrggbb -> opaque
		return "#FF" + strings.ToUpper(v[1:]), nil
	case 9: // #aarrggbb, already explicit
		return "#" + strings.ToUpper(v[1:]), nil
	}
	return "", fmt.Errorf("token value %q is neither #rrggbb nor #aarrggbb", token)
}

// pxRe is a CSS pixel length. The design mock is drawn at 1:1, so 1 CSS px is 1 dp with no
// scaling factor (inventory section A(b)); the conversion is a unit rename and nothing else,
// which is exactly why it is safe to automate and exactly why it must still be asserted.
var pxRe = regexp.MustCompile(`^(-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?)px$`)

// dpFromToken converts a CSS px radius into the dp literal an Android <dimen> carries.
//
// It refuses a bare number. A radius that lost its unit is indistinguishable from a font weight,
// and inventing `px` for it is precisely the "best-effort conversion" PB-TOK-6 replaces.
func dpFromToken(token string) (string, error) {
	m := pxRe.FindStringSubmatch(strings.TrimSpace(token))
	if m == nil {
		return "", fmt.Errorf("token value %q is typed `dimen` but is not a CSS px length; the "+
			"design artifact is drawn at 1:1, so a dimen token must carry its px unit for the "+
			"1px = 1dp identity to mean anything", token)
	}
	return m[1] + "dp", nil
}

// ---------------------------------------------------------------------------
// Reading the resource files.
// ---------------------------------------------------------------------------

func valuesDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(appModule(t), "src", "main", "res", "values")
}

// androidResources parses one res/values file for one element kind, name -> literal.
//
// THE VALUE IS CAPTURED LOOSELY AND VALIDATED AFTERWARDS, which is a deliberate change from the
// first version of this gate. That one matched `#[0-9A-Fa-f]{6,8}` inside the element pattern, so
// a malformed <color> simply did not match and became INVISIBLE to the reverse-direction check --
// a colour resource with no origin, hidden by being wrong in a second way. Capturing anything and
// judging it later means a malformed literal is reported rather than skipped.
func androidResources(t *testing.T, file, element string) map[string]string {
	t.Helper()
	path := filepath.Join(valuesDir(t), file)
	raw := readFileOrFail(t, path, "PB-TOK-1")
	re := regexp.MustCompile(`<` + element + `\s+name="([A-Za-z0-9_]+)"\s*>([^<]*)</` + element + `>`)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(raw, -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	if len(out) == 0 {
		t.Fatalf("PB-TOK-1: no <%s> resources parsed from %s", element, mustRel(t, path))
	}
	return out
}

// androidColors is every colour resource the theme can reference, name -> literal.
func androidColors(t *testing.T) map[string]string {
	t.Helper()
	out := androidResources(t, "colors.xml", "color")
	for name, v := range out {
		out[name] = strings.ToUpper(v)
	}
	return out
}

// ---------------------------------------------------------------------------
// The join.
// ---------------------------------------------------------------------------

type tokenMapRow struct {
	Token    string
	Resource string
	Kind     string
	Line     int
}

func loadTokenMap(t *testing.T) []tokenMapRow {
	t.Helper()
	path := filepath.Join(androidRoot(t), tokenMapFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-TOK-1 requires a checked-in join at %s: %v\n"+
			"Columns (tab separated): token<TAB>android_resource<TAB>kind<TAB>note.\n"+
			"`token` is a key of %s's \"tokens\" object (e.g. --p-bg); `android_resource` is the "+
			"resource name it becomes; `kind` must equal the origin's kind for that token.\n"+
			"It is a table rather than a naming convention because a convention is a mapping "+
			"nobody can review, and because the Kotlin theme test must read the SAME artifact "+
			"-- the arrangement android/connectivity-policy.tsv already uses.",
			mustRel(t, path), err, tokensRelPath)
	}
	var rows []tokenMapRow
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 3 {
			t.Errorf("%s:%d: needs at least token, android_resource and kind", tokenMapFile, i+1)
			continue
		}
		rows = append(rows, tokenMapRow{
			Token:    strings.TrimSpace(cols[0]),
			Resource: strings.TrimSpace(cols[1]),
			Kind:     strings.TrimSpace(cols[2]),
			Line:     i + 1,
		})
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no rows; the join would be vacuous", mustRel(t, path))
	}
	return rows
}

// TestPBTOK1_TheAndroidThemeColoursAreTheDesignTokens is the requirement, now dispatching on kind.
func TestPBTOK1_TheAndroidThemeColoursAreTheDesignTokens(t *testing.T) {
	tokens := loadDesignTokens(t)
	rows := loadTokenMap(t)

	// mapped[kind][resource] -- the reverse direction is checked per resource file, because a
	// <dimen> with no row and a <color> with no row are the same defect in two different files.
	mapped := map[string]map[string]bool{}
	for kind := range kindConverters {
		mapped[kind] = map[string]bool{}
	}

	// Each converter's resource file is read ONCE, up front, and a missing one is recorded as an
	// absence rather than parsed. Reading inside the row loop instead would abort the whole test
	// on the first row of a kind whose file does not exist yet -- so one such row would hide
	// every other row's verdict, which is the opposite of what a gate is for.
	declared := map[string]map[string]string{}
	for kind, conv := range kindConverters {
		if exists(filepath.Join(valuesDir(t), conv.File)) {
			declared[kind] = androidResources(t, conv.File, conv.Element)
		}
	}

	for _, r := range rows {
		value, ok := tokens.Tokens[r.Token]
		if !ok {
			t.Errorf("%s:%d: token %q is not declared in %s", tokenMapFile, r.Line, r.Token, tokensRelPath)
			continue
		}

		// PB-TOK-6, first gate: the join's word and the origin's word must be the same word.
		// The origin wins -- the join is downstream of it -- so a disagreement is reported
		// before any conversion, since converting under the wrong kind produces a plausible
		// resource that nobody designed.
		originKind, ok := tokens.Kinds[r.Token]
		if !ok {
			t.Errorf("%s:%d: %s declares no kind for token %q", tokenMapFile, r.Line, tokensRelPath, r.Token)
			continue
		}
		if r.Kind != originKind {
			t.Errorf("%s:%d: the join calls %s a %q and %s calls it a %q. The origin is upstream, "+
				"so this row would convert the token with the wrong converter and land a resource "+
				"that looks right and is not.",
				tokenMapFile, r.Line, r.Token, r.Kind, tokensRelPath, originKind)
			continue
		}

		// PB-TOK-6, second gate: no converter is a FAILURE, not a skip.
		conv, ok := kindConverters[originKind]
		if !ok {
			t.Errorf("PB-TOK-6: %s:%d maps %s, whose kind %q has no converter in this gate.\n"+
				"The old behaviour here was to refuse any token without an ARGB form, which is "+
				"why 15 of 31 tokens could never ship. The replacement is not to skip them: a "+
				"kind reaching a res/values file needs a converter that produces an exact value "+
				"or names its substitute, and %q has neither yet. Converters exist for: %s.",
				tokenMapFile, r.Line, r.Token, originKind, originKind,
				strings.Join(sortedKeys(kindConverters), ", "))
			continue
		}
		mapped[originKind][r.Resource] = true

		want, err := conv.Convert(value)
		if err != nil {
			t.Errorf("%s:%d: %v", tokenMapFile, r.Line, err)
			continue
		}
		got, ok := declared[originKind][r.Resource]
		if !ok {
			t.Errorf("%s:%d: maps token %q to <%s name=%q>, which %s does not declare",
				tokenMapFile, r.Line, r.Token, conv.Element, r.Resource, conv.File)
			continue
		}
		if conv.Normalize(got) != conv.Normalize(want) {
			t.Errorf("PB-TOK-1: the Android theme and the token origin DISAGREE.\n"+
				"\t%s = %s  (%s)\n\t<%s name=%q> = %s  (%s)\n"+
				"The requirement is that one JSON source is the SINGLE ORIGIN for the theme, and "+
				"its criterion says in as many words that the values must actually agree. A phone "+
				"painted from a divergent resource renders a product nobody designed, and every "+
				"screen built on it inherits the wrong surface.",
				r.Token, value, tokensRelPath, conv.Element, r.Resource, got, conv.File)
		}
	}

	// THE OTHER DIRECTION, over the namespaces the token origin wholly owns. A resource with no
	// row is a value that entered the theme without passing through the origin, which is the same
	// defect arriving from the other side -- and it is how "single origin" decays into "origin
	// plus a few extras".
	//
	// The file is scanned when it EXISTS, whether or not the join has rows of that kind yet, so a
	// resource file that lands with a hand-typed value and no row fails immediately rather than
	// waiting for someone to add the first row of its kind.
	for _, kind := range sortedKeys(kindConverters) {
		conv := kindConverters[kind]
		if declared[kind] == nil {
			if len(mapped[kind]) > 0 {
				t.Errorf("PB-TOK-6: %s has rows of kind %q but %s does not exist",
					tokenMapFile, kind, conv.File)
			}
			continue
		}
		if !conv.TokenDerivedNamespace {
			continue
		}
		var unmapped []string
		for name := range declared[kind] {
			if !mapped[kind][name] {
				unmapped = append(unmapped, name)
			}
		}
		sort.Strings(unmapped)
		if len(unmapped) > 0 {
			t.Errorf("PB-TOK-1: %d <%s> resource(s) in %s have no row in %s and therefore no "+
				"origin:\n\t%s",
				len(unmapped), conv.Element, conv.File, tokenMapFile, strings.Join(unmapped, "\n\t"))
		}
	}

	// AND THE EXHAUSTIVE DIRECTION MUST NOT BE SWITCHED OFF EVERYWHERE. TokenDerivedNamespace is
	// a per-kind opt-out, which is exactly the kind of flag that gets flipped to silence a
	// failure. At least one namespace must still be checked exhaustively, or "a resource with no
	// origin fails" is a sentence with nothing behind it.
	exhaustive := 0
	for _, conv := range kindConverters {
		if conv.TokenDerivedNamespace {
			exhaustive++
		}
	}
	if exhaustive == 0 {
		t.Error("PB-TOK-1: no resource namespace is checked exhaustively any more, so a colour " +
			"resource with no row would pass. The reverse direction is half the requirement.")
	}
}

// TestPBTOK5_EveryColourTokenReachesTheApp is PB-TOK-5, and it is the direction neither existing
// assertion covered.
//
// PB-TOK-1's join is bidirectional between the TABLE and the RESOURCES: a row whose token does
// not exist fails, a resource with no row fails. Both are silent about a token that simply has no
// row at all, which is how the join sat at three rows while the origin declared sixteen. Twelve
// of the sixteen -- every surface, every hairline, every status colour -- were pinned against a
// design source that no screen could consume, and the fence around them was green the entire
// time. This is the third direction: ORIGIN -> table.
func TestPBTOK5_EveryColourTokenReachesTheApp(t *testing.T) {
	tokens := loadDesignTokens(t)
	rows := loadTokenMap(t)

	mappedTokens := map[string]int{}
	for _, r := range rows {
		mappedTokens[r.Token]++
	}

	var colours []string
	for name, kind := range tokens.Kinds {
		if kind == "color" {
			colours = append(colours, name)
		}
	}
	sort.Strings(colours)

	// The floor is pinned, so deleting rows AND retyping the tokens they named cannot make this
	// assertion pass by emptying the set it iterates.
	if len(colours) < colourTokenCount {
		t.Fatalf("PB-TOK-5: %s types %d tokens as colours; the Substrate skin declares %d. This "+
			"assertion is about all of them reaching the app. This is a FLOOR, not an equality: "+
			"it will run over a LARGER set, which is how --p-tabbg was added after an audit found "+
			"it excluded. What it refuses is a SMALLER one, so retyping a colour away cannot make "+
			"this pass by emptying the set it iterates.", tokensRelPath, len(colours), colourTokenCount)
	}

	var missing []string
	for _, name := range colours {
		if mappedTokens[name] == 0 {
			missing = append(missing, name+" = "+tokens.Tokens[name])
		}
	}
	if len(missing) > 0 {
		t.Errorf("PB-TOK-5: %d of %d colour tokens have no row in %s, so they have no Android "+
			"representation at all:\n\t%s\n"+
			"The join is correct and under-fed. A token pinned against the design source that no "+
			"screen can consume is a token that has not shipped, and the fence around it is green "+
			"because there is nothing on the other side to disagree with.",
			len(missing), len(colours), tokenMapFile, strings.Join(missing, "\n\t"))
	}

	// THE ALIAS MUST SURVIVE. --p-cta-bg and --p-cta-ink hold the same bytes as --p-hero and
	// --p-hero-ink today, and a join that collapsed them -- or a colors.xml that pointed two
	// names at one resource -- would look identical on screen and would silently lose the seam
	// the design put there. A future skin that gives the CTA its own colour must be a one-line
	// token edit, not a refactor.
	aliases := [][2]string{{"--p-cta-bg", "--p-hero"}, {"--p-cta-ink", "--p-hero-ink"}}
	resourceOf := map[string]string{}
	for _, r := range rows {
		resourceOf[r.Token] = r.Resource
	}
	for _, pair := range aliases {
		alias, base := pair[0], pair[1]
		if mappedTokens[alias] == 0 {
			t.Errorf("PB-TOK-5: %s has no row, and it holds the same value as %s today. The "+
				"mapping must still name it separately: a future skin can break the alias, and a "+
				"join that deduplicated by value would not notice.", alias, base)
			continue
		}
		if resourceOf[alias] == resourceOf[base] {
			t.Errorf("PB-TOK-5: %s and %s both map to <color name=%q>. They are value-aliases, "+
				"not the same token, and one resource for both makes breaking the alias a "+
				"refactor instead of a token edit.", alias, base, resourceOf[alias])
		}
	}
}

// TestPBTOK6_AKindWithNoConverterFailsTheGate is PB-TOK-6's criterion, executed.
//
// "A token whose kind has no converter fails the gate rather than being skipped" is a statement
// ABOUT THE GATE, and the shape it forbids -- `if !ok { continue }` -- is both the obvious code
// and invisible in a green run. So the dispatch is exercised directly: every kind the origin
// uses is either converted or absent from the table, and a lookup miss must be a miss rather
// than a permissive default.
func TestPBTOK6_AKindWithNoConverterFailsTheGate(t *testing.T) {
	tokens := loadDesignTokens(t)

	// The registry must not have grown a catch-all. If every kind resolved, the failure branch
	// the requirement asks for would be unreachable and this gate would be decorative.
	kindsInUse := map[string]bool{}
	for _, kind := range tokens.Kinds {
		kindsInUse[kind] = true
	}
	if len(kindsInUse) < 2 {
		t.Fatalf("PB-TOK-6: the origin uses %d kind(s); the dispatch cannot be meaningful",
			len(kindsInUse))
	}
	var unconverted []string
	for kind := range kindsInUse {
		if _, ok := kindConverters[kind]; !ok {
			unconverted = append(unconverted, kind)
		}
	}
	if len(unconverted) == 0 {
		t.Fatal("PB-TOK-6: every kind the origin uses has a converter, so the \"no converter is a " +
			"failure\" branch is unreachable and cannot be trusted. If that is genuinely true, " +
			"delete this assertion deliberately rather than letting it pass vacuously.")
	}

	// And the miss must be a miss. A registry read through a helper that substituted a default
	// converter would pass every assertion in this file and ship an invented conversion.
	for _, kind := range unconverted {
		if _, ok := kindConverters[kind]; ok {
			t.Errorf("PB-TOK-6: kind %q resolves to a converter after all", kind)
		}
	}
	if _, ok := kindConverters["a-kind-that-does-not-exist"]; ok {
		t.Error("PB-TOK-6: the converter registry answers for a kind that does not exist, so a " +
			"typo in the join's kind column would be converted rather than reported")
	}
}

// PASSES TODAY, AND IT MUST -- this one is the NEGATIVE CONTROL itself, not a fence awaiting
// an implementation, so it is green now and stays green. Labelled anyway so no evidence line
// counts it among the assertions S16 has to earn: it proves the comparator can distinguish
// two colours, and it would go red the day someone "simplified" the comparison into something
// that cannot fail.
//
// TestPBTOK1_TheComparisonCanActuallyFail is the criterion's second half, executed rather
// than asserted in prose.
//
// "The assertion must fail when they diverge" is a statement ABOUT THE TEST, and this project
// has had to reject the other shape more than once: a check that reads both files, compares
// nothing meaningful, and is green forever. So the comparator is mutated here -- one hex digit
// -- and must report the difference. A comparator that normalised too eagerly (case, alpha, a
// three-digit shorthand) or that compared a value against itself passes every assertion above
// and fails this one.
func TestPBTOK1_TheComparisonCanActuallyFail(t *testing.T) {
	got, err := argbFromToken("#08090a")
	if err != nil {
		t.Fatalf("argbFromToken: %v", err)
	}
	if got != "#FF08090A" {
		t.Fatalf("argbFromToken(#08090a) = %q, want #FF08090A: an opaque alpha and upper-case "+
			"hex, which is the form colors.xml carries", got)
	}
	// The real divergence in the tree at the time PB-TOK-1 was written, as a live negative control.
	if got == "#FF101114" {
		t.Fatal("the comparator equates #08090a with #101114, so PB-TOK-1's assertion cannot fail")
	}
	// And a one-digit mutation must still be caught.
	mutated, err := argbFromToken("#08090b")
	if err != nil {
		t.Fatalf("argbFromToken: %v", err)
	}
	if mutated == got {
		t.Fatal("the comparator cannot distinguish #08090a from #08090b; every value assertion " +
			"above is vacuous")
	}
	// A non-colour must be refused rather than best-effort converted. This is the half of the old
	// value-sniffing behaviour that was RIGHT and had to survive PB-TOK-6: dispatching on kind
	// decides WHICH converter runs, and the converter still has to reject a value its kind cannot
	// legally hold.
	// rgba() is DELIBERATELY NOT in the list below, and it used to be. That was the defect an
	// audit committee found on 2026-08-01: the skin writes --p-tabbg as rgba(8,9,10,0.88), this
	// converter refused it, so the token was typed `effect` -- and PB-TOK-5's "every colour token
	// reaches the app" became true by excluding the colour it could not read. Reading the notation
	// is the fix; reclassifying the colour was the bug.
	if v, err := argbFromToken("rgba(8,9,10,0.88)"); err != nil || v != "#E008090A" {
		t.Errorf("argbFromToken(rgba(8,9,10,0.88)) = %q, %v; want #E008090A", v, err)
	}
	// A non-colour must still be refused rather than best-effort converted. This is the half of
	// the old value-sniffing behaviour that was RIGHT and had to survive PB-TOK-6: dispatching on
	// kind decides WHICH converter runs, and the converter still has to reject a value its kind
	// cannot legally hold. Malformed rgba() is refused too, so widening the notation did not
	// widen the tolerance.
	for _, bad := range []string{"9px", "650", "-0.008em", "#fff", "rgba(8,9,10)", "rgba(8,9,300,0.5)", "rgba(8,9,10,2)"} {
		if v, err := argbFromToken(bad); err == nil {
			t.Errorf("argbFromToken(%q) invented the colour %q", bad, v)
		}
	}
}

// TestPBTOK6_TheDimenConverterCanActuallyFail is the same negative control for the second kind.
//
// It matters more than it looks: px -> dp is a UNIT RENAME, so the laziest correct-looking
// implementation is `strings.Replace(v, "px", "dp", 1)`, which happily converts "650" to "650"
// and "-0.008em" to "-0.008em" and reports success. A converter that cannot fail turns the
// per-kind dispatch back into the silent skip it replaced.
func TestPBTOK6_TheDimenConverterCanActuallyFail(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"9px", "9dp"},
		{"14px", "14dp"},
		{"0.5px", "0.5dp"},
	} {
		got, err := dpFromToken(tc.in)
		if err != nil {
			t.Errorf("dpFromToken(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("dpFromToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"9", "9dp", "#08090a", "650", "-0.008em", "9 px", "", "px"} {
		if v, err := dpFromToken(bad); err == nil {
			t.Errorf("dpFromToken(%q) invented the dimension %q; a value that is not a CSS px "+
				"length has no dp form, and guessing one is how a font weight becomes a radius",
				bad, v)
		}
	}
	if a, _ := dpFromToken("9px"); a == "14dp" {
		t.Fatal("the dimen converter cannot distinguish 9px from 14dp")
	}
}

// TestPBTOK1_TheThemesRecordedColoursComeFromTheOrigin closes the third copy.
//
// SwarmTheme.EXPECTED_DARK_COLORS holds the same literals a third time. The instinct behind it
// is right -- a theme test that reads the theme's own colours proves only that Android
// resolved them -- but the number it records must come from the ORIGIN, or the fence certifies
// that the app renders whatever colors.xml happens to say.
func TestPBTOK1_TheThemesRecordedColoursComeFromTheOrigin(t *testing.T) {
	tokens := loadDesignTokens(t)
	rows := loadTokenMap(t)

	allowed := map[string]bool{}
	for _, r := range rows {
		if tokens.Kinds[r.Token] != "color" {
			continue
		}
		if v, ok := tokens.Tokens[r.Token]; ok {
			if argb, err := argbFromToken(v); err == nil {
				allowed[strings.TrimPrefix(argb, "#")] = true
			}
		}
	}
	if len(allowed) == 0 {
		t.Fatal("PB-TOK-1: no mapped colour resolved, so the scan below would reject every " +
			"literal for the wrong reason")
	}

	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash("dev/swarm/phone/theme/SwarmTheme.kt"))
	src := readFileOrFail(t, path, "PB-TOK-1")
	literal := regexp.MustCompile(`0x([0-9A-Fa-f]{8})\.toInt\(\)`)

	for _, m := range literal.FindAllStringSubmatch(src, -1) {
		if !allowed[strings.ToUpper(m[1])] {
			t.Errorf("PB-TOK-1: SwarmTheme records colour 0x%s, which is not any mapped token's "+
				"value. It is a THIRD copy of the palette: colors.xml, this constant, and %s all "+
				"state the same fact, and the test built on this constant certifies the app "+
				"renders whatever colors.xml says rather than what the origin says.\n"+
				"Either derive it from the staged tokens.json at test time, or delete it and let "+
				"the Kotlin theme test read the origin.", m[1], tokensRelPath)
		}
	}
}
