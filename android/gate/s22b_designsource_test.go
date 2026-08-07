package gate

// The DESIGN SOURCE, read as data. Shared scaffolding for S22b's PB-DS-1..4 gates.
//
// WHY THE ARTIFACT AND NOT A TRANSCRIPTION. Every assertion in s22b_spacing_test.go and
// s22b_type_test.go compares an Android resource against a number COMPUTED HERE from
// docs/research/remote-control-design-directions.html at test time. That is the whole point of
// the arrangement and it is the lesson PB-TOK-1 paid for: a gate whose expected value is a
// literal transcribed from the implementation certifies that the app renders whatever the
// implementation says, which is exactly what it would do if the implementation were wrong -- and
// colors.xml was wrong for three files' worth of copies before anybody noticed.
//
// So there is no table of sizes in this package. There is a CSS parser.
//
// ONLY THE SHARED STRUCTURAL BLOCK IS READ. The artifact draws four candidate skins; `.d2`,
// `.d3` and `.d4` OVERRIDE the same selectors with different values (`.d2 .pnav .big` is 30px,
// `.d4 .pnav .big` is 21px). PB-TOK-2 chose Substrate, so a parser that swept the whole file
// would resolve `.pnav .big` to whichever skin's rule it met last -- silently, and to a number
// belonging to a design nobody chose. The block below runs from the shared-structure comment to
// the first skin banner and contains exactly the rules Substrate inherits.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// s22bDesignSourceRelPath is the artifact internal/design/tokens.json itself cites as its
// "source", so the two halves of the design system are read from one origin.
const s22bDesignSourceRelPath = "docs/research/remote-control-design-directions.html"

const (
	s22bSharedBlockStart = "/* ---------- shared phone structure ---------- */"
	s22bSharedBlockEnd   = "/* ============ D1 SUBSTRATE ============ */"
)

// s22bMaquetteRelPath is ADR-009 D2's normative design source, and the artifact
// internal/design/tokens.json now cites as its "source".
//
// WHY TWO DESIGN SOURCES ARE READ IN THIS PACKAGE, WHICH IS A SPLIT AND NOT AN OVERSIGHT.
// ADR-009 replaces the SKIN. The maquette redraws every surface the app owns, so app-surface
// spacing is read from it below and the Substrate artifact no longer has any say over a padding.
// Two things the older artifact still carries are NOT in the maquette, and each is named where it
// is used rather than left to be discovered:
//
//   - THE THREE FRAME CONSTANTS (screen_top 54, screen_bottom 76, tabbar_height 74). They are the
//     handset's own geometry -- the status-bar inset and the bar that occupies the bottom of the
//     screen -- not skin values, which is why ADR-009 D1 lists them among the things the direction
//     change deliberately keeps. The maquette draws a 300px gallery phone with no OS chrome and no
//     fixed-height bar, so it states none of the three.
//   - THE TYPE LADDER. The maquette's font sizes are a redraw (the nav title is 22px where
//     Substrate's is 27px), and re-pointing type.xml's origins at it would move nineteen sizes.
//     ADR-009 D3's table changes weight and tracking and no size; D7 keeps the scale's structure;
//     the migration plan gives O5 the visual verification of the styles against the maquette. A
//     type-scale change smuggled inside a token migration is the defect this whole regime exists
//     to prevent, so it is not made here.
//
// The spacing values themselves confirm the maquette is dp-equivalent rather than a scaled
// drawing: it declares 2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,18,24 against Substrate's
// 2,4,5,7,8,9,10,11,12,13,14,15,16,18 -- the same ladder, at the same size, on the same grid.
const s22bMaquetteRelPath = "docs/research/obsidian-maquette.html"

// The maquette's phone-kit block: every component the app draws, and nothing else. It stops at
// the mark, so the gallery furniture around the phones -- the page chrome the file marks "NOT
// part of the skin", the icon tiles, the feature-graphic composition and the component-sheet grid
// -- cannot leak a 30px gallery gap into the app's spacing scale.
const (
	s22bMaquetteKitStart = "/* ---------- kit primitives, drawn at token fidelity ---------- */"
	s22bMaquetteKitEnd   = "/* ---------- the mark ---------- */"
)

// s22bCSSRule is one parsed rule: one selector and its declarations, in declaration order.
type s22bCSSRule struct {
	Selector string
	Decls    map[string]string
	Order    []string
}

var (
	s22bCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	s22bRuleRe    = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	s22bVarRe     = regexp.MustCompile(`var\(\s*(--[A-Za-z0-9-]+)\s*\)`)
	// An at-rule that CONTAINS rules -- @keyframes, @media -- is removed whole before the flat
	// rule regexp runs. s22bRuleRe cannot see nesting: fed `@keyframes s { 0% { left: 0 } }` it
	// matches the inner block and then walks out of phase with the rest of the file, pairing
	// selectors with the wrong declarations. It does that SILENTLY, which is worse than failing:
	// the maquette's sweep keyframes sit in the middle of the kit block, and the first version of
	// this reader attributed `.empty { padding }` to a value from three rules away.
	s22bAtRuleRe = regexp.MustCompile(`(?s)@[a-zA-Z-]+[^{;]*\{(?:[^{}]*\{[^{}]*\})*[^{}]*\}`)
)

// s22bSharedCSS parses the Substrate-inherited structural rules, selector -> declarations.
//
// It is kept, narrowly, for the two things ADR-009's maquette does not state: the three frame
// constants and the type ladder. See s22bMaquetteRelPath for why each stays here.
func s22bSharedCSS(t *testing.T) map[string]s22bCSSRule {
	t.Helper()
	return s22bParseCSSBlock(t, s22bDesignSourceRelPath, s22bSharedBlockStart, s22bSharedBlockEnd)
}

// s22bMaquetteKitCSS parses the Obsidian maquette's phone-kit block: the app's own surfaces, at
// ADR-009 D2's normative design source. This is where every app spacing value comes from.
func s22bMaquetteKitCSS(t *testing.T) map[string]s22bCSSRule {
	t.Helper()
	return s22bParseCSSBlock(t, s22bMaquetteRelPath, s22bMaquetteKitStart, s22bMaquetteKitEnd)
}

// s22bParseCSSBlock parses one delimited CSS block of one design source, selector -> declarations.
//
// A selector list (`.grain, .fx { ... }`) yields one entry per selector, and a selector that
// appears twice merges later declarations over earlier ones, which is what a browser does.
func s22bParseCSSBlock(t *testing.T, relPath, blockStart, blockEnd string) map[string]s22bCSSRule {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(relPath))
	raw := readFileOrFail(t, path, "PB-DS-1..4")

	start := strings.Index(raw, blockStart)
	end := strings.Index(raw, blockEnd)
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("PB-DS-1..4: %s no longer delimits its structural block with\n\t%q\n\t%q\n"+
			"Every expected value computed from that block would come from an empty map, and "+
			"every assertion over it would pass vacuously.",
			mustRel(t, path), blockStart, blockEnd)
	}
	block := s22bCommentRe.ReplaceAllString(raw[start:end], "\n")
	block = s22bAtRuleRe.ReplaceAllString(block, "\n")
	// The flat rule regexp is only correct over a block with no remaining nesting, so that is
	// checked rather than assumed: an unbalanced or nested residue means the parse below is
	// out of phase and every value it reports belongs to some other rule.
	if depth, deepest := s22bBraceDepth(block); depth != 0 || deepest > 1 {
		t.Fatalf("PB-DS-1..4: the block %q..%q of %s does not flatten to unnested rules "+
			"(deepest nesting %d, unbalanced by %d). The rule parser cannot see nesting and "+
			"would pair selectors with declarations from other rules without saying so.",
			blockStart, blockEnd, mustRel(t, path), deepest, depth)
	}

	out := map[string]s22bCSSRule{}
	for _, m := range s22bRuleRe.FindAllStringSubmatch(block, -1) {
		for _, sel := range strings.Split(m[1], ",") {
			sel = strings.Join(strings.Fields(sel), " ")
			if sel == "" || strings.HasPrefix(sel, "@") {
				continue
			}
			rule, seen := out[sel]
			if !seen {
				rule = s22bCSSRule{Selector: sel, Decls: map[string]string{}}
			}
			for _, decl := range strings.Split(m[2], ";") {
				prop, value, ok := strings.Cut(decl, ":")
				if !ok {
					continue
				}
				prop = strings.TrimSpace(prop)
				value = strings.TrimSpace(value)
				if prop == "" || value == "" {
					continue
				}
				if _, dup := rule.Decls[prop]; !dup {
					rule.Order = append(rule.Order, prop)
				}
				rule.Decls[prop] = value
			}
			out[sel] = rule
		}
	}
	if len(out) == 0 {
		t.Fatalf("PB-DS-1..4: no CSS rules parsed from the shared block of %s", mustRel(t, path))
	}
	return out
}

// s22bBraceDepth returns the block's final brace balance and its deepest nesting.
func s22bBraceDepth(block string) (balance, deepest int) {
	for _, r := range block {
		switch r {
		case '{':
			balance++
			if balance > deepest {
				deepest = balance
			}
		case '}':
			balance--
		}
	}
	return balance, deepest
}

// s22bTokenValues is internal/design/tokens.json's whole token map, colours included.
//
// It is a SEPARATE reader from s16_tokens_test.go's loadDesignTokens, which returns the same
// thing -- deliberately, because that file belongs to PB-TOK-1 and this slice must not reach
// into it. Both parse the same JSON with encoding/json, so they cannot disagree about a value;
// what they do not share is a helper one slice could change under the other.
func s22bTokenValues(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash("internal/design/tokens.json"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-DS-1..4: the token origin is unreadable: %v", err)
	}
	var parsed struct {
		Tokens map[string]string `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("PB-DS-1..4: internal/design/tokens.json is not valid JSON: %v", err)
	}
	if len(parsed.Tokens) == 0 {
		t.Fatalf("PB-DS-1..4: internal/design/tokens.json declares no tokens")
	}
	return parsed.Tokens
}

// s22bResolveVars substitutes every var(--p-*) in a CSS value with the token origin's literal.
//
// An unresolvable var is an ERROR rather than a pass-through: the artifact carries one already
// (`.panelframe .cap` names `var(--mono)`, which no skin declares), and a resolver that quietly
// left it alone would compare an Android font family against the string "var(--mono)" and
// report a mismatch that reads like an implementation bug.
func s22bResolveVars(value string, tokens map[string]string) (string, error) {
	var bad []string
	out := s22bVarRe.ReplaceAllStringFunc(value, func(m string) string {
		name := s22bVarRe.FindStringSubmatch(m)[1]
		v, ok := tokens[name]
		if !ok {
			bad = append(bad, name)
			return m
		}
		return v
	})
	if len(bad) > 0 {
		return "", fmt.Errorf("%q references %s, which the token origin does not declare",
			value, strings.Join(bad, ", "))
	}
	return out, nil
}

// s22bPx parses a CSS `<number>px` into a float. Unitless zero is accepted because the artifact
// writes `padding: 0 12px`.
func s22bPx(value string) (float64, bool) {
	v := strings.TrimSpace(value)
	if v == "0" {
		return 0, true
	}
	if !strings.HasSuffix(v, "px") {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(v, "px"), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// s22bEm parses a CSS `<number>em`. Android's letterSpacing is in em too, so the number crosses
// unchanged -- which is the one lossless row in the typography inventory.
func s22bEm(value string) (float64, bool) {
	v := strings.TrimSpace(value)
	if !strings.HasSuffix(v, "em") {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(v, "em"), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// ---------------------------------------------------------------------------
// Android resource readers.
// ---------------------------------------------------------------------------

var s22bDimenRe = regexp.MustCompile(`<dimen\s+name="([A-Za-z0-9_]+)"\s*>\s*([-0-9.]+)(dp|sp|px)\s*</dimen>`)

type s22bDimen struct {
	Name  string
	Value float64
	Unit  string
}

func s22bDimensPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(appModule(t), "src", "main", "res", "values", "dimens.xml")
}

func s22bDimens(t *testing.T) map[string]s22bDimen {
	t.Helper()
	path := s22bDimensPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-DS-1: %s does not exist. The requirement's criterion is that this file "+
			"carries the declared spacing scale (2/4/6/8/10/12/14/16/18 dp plus 24dp), the three "+
			"frame constants and PB-DS-4's four radii. Today the app's entire spatial output is "+
			"`setPadding(24)` in raw PIXELS, which renders at 8dp on a 3x handset.\n\t%v",
			mustRel(t, path), err)
	}
	out := map[string]s22bDimen{}
	for _, m := range s22bDimenRe.FindAllStringSubmatch(string(raw), -1) {
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			t.Errorf("PB-DS-1: <dimen name=%q> value %q is not a number", m[1], m[2])
			continue
		}
		out[m[1]] = s22bDimen{Name: m[1], Value: v, Unit: m[3]}
	}
	if len(out) == 0 {
		t.Fatalf("PB-DS-1: no <dimen> resources parsed from %s; every assertion over the scale "+
			"would be vacuous", mustRel(t, path))
	}
	return out
}

// s22bStyle is one TextAppearance.Swarm.* and the CSS selector it declares as its origin.
type s22bStyle struct {
	Name   string
	Origin string
	Items  map[string]string
	Line   int
}

// The join is carried IN type.xml, as a machine-read comment immediately above each style:
//
//	<!-- origin: .pnav .big -->
//	<style name="TextAppearance.Swarm.Display.NavTitle" ...>
//
// WHAT IS RECORDED THERE IS THE MAPPING, NOT THE VALUES. Which CSS rule a named style descends
// from is a design decision a reviewer has to be able to see and disagree with; the sizes,
// weights and tracking are then READ OUT OF THE ARTIFACT and compared. So an implementer who
// edits a textSize without editing the design breaks this gate, and one who re-points a style
// at a different rule is making a visible claim.
var s22bStyleRe = regexp.MustCompile(
	`(?s)<!--\s*origin:\s*(.*?)\s*-->\s*<style\s+name="([^"]+)"[^>]*>(.*?)</style>`)

var s22bItemRe = regexp.MustCompile(`<item\s+name="([A-Za-z0-9:_]+)"\s*>\s*([^<]*?)\s*</item>`)

func s22bTypePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(appModule(t), "src", "main", "res", "values", "type.xml")
}

func s22bStyles(t *testing.T) map[string]s22bStyle {
	t.Helper()
	path := s22bTypePath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-DS-2: %s does not exist. The criterion is that it defines "+
			"TextAppearance.Swarm.* for all 18 product text styles, each carrying size, weight, "+
			"tracking, family and line-height -- rather than six attributes re-specified per call "+
			"site. The app expresses zero of them today.\n\t%v", mustRel(t, path), err)
	}
	text := string(raw)
	out := map[string]s22bStyle{}
	for _, m := range s22bStyleRe.FindAllStringSubmatchIndex(text, -1) {
		origin := strings.Join(strings.Fields(text[m[2]:m[3]]), " ")
		name := text[m[4]:m[5]]
		body := text[m[6]:m[7]]
		items := map[string]string{}
		for _, im := range s22bItemRe.FindAllStringSubmatch(body, -1) {
			items[im[1]] = im[2]
		}
		out[name] = s22bStyle{
			Name:   name,
			Origin: origin,
			Items:  items,
			Line:   1 + strings.Count(text[:m[0]], "\n"),
		}
	}
	if len(out) == 0 {
		t.Fatalf("PB-DS-2: no `<!-- origin: ... -->` + <style> pairs parsed from %s. Every style "+
			"must declare the CSS rule it descends from, or the join to the design is a naming "+
			"convention nobody can review.", mustRel(t, path))
	}
	// A style declared twice would silently win at whichever definition aapt met last; the map
	// above would keep one and the count assertions would still balance.
	var dupes []string
	for name := range out {
		if strings.Count(text, `name="`+name+`"`) > 1 {
			dupes = append(dupes, name)
		}
	}
	sort.Strings(dupes)
	if len(dupes) > 0 {
		t.Errorf("PB-DS-2: %s declares these styles more than once: %s",
			mustRel(t, path), strings.Join(dupes, ", "))
	}
	return out
}
