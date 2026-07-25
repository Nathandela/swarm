package gate

// FAILING-FIRST (TDD RED, GG-5) for PB-TOK-1, REASSIGNED S5 -> S16 on 2026-07-25.
//
// "One machine-readable token source (JSON) is the single origin for the Android theme.
//  Theme generated from or asserted against the JSON. THE VALUES MUST ACTUALLY AGREE, and the
//  assertion must fail when they diverge."
//
// THE STATE OF THE WORLD, verified before these assertions were written:
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

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

var androidColorRe = regexp.MustCompile(`<color\s+name="([A-Za-z0-9_]+)"\s*>\s*(#[0-9A-Fa-f]{6,8})\s*</color>`)

// designTokens is the parsed origin.
type designTokens struct {
	Tokens map[string]string `json:"tokens"`
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
	return out
}

// androidColors is every colour resource the theme can reference, name -> literal.
func androidColors(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join(appModule(t), "src", "main", "res", "values", "colors.xml")
	raw := readFileOrFail(t, path, "PB-TOK-1")
	out := map[string]string{}
	for _, m := range androidColorRe.FindAllStringSubmatch(raw, -1) {
		out[m[1]] = strings.ToUpper(m[2])
	}
	if len(out) == 0 {
		t.Fatalf("PB-TOK-1: no <color> resources parsed from %s", mustRel(t, path))
	}
	return out
}

// argbFromToken converts a #rrggbb design token into the opaque #AARRGGBB an Android colour
// resource carries. It is the WHOLE comparison, so it lives in one function and is
// mutation-tested below rather than being inlined at each call site.
func argbFromToken(token string) (string, error) {
	v := strings.TrimSpace(token)
	if !strings.HasPrefix(v, "#") {
		return "", fmt.Errorf("token value %q is not a hex colour; only the colour tokens may "+
			"be mapped to an Android <color> (a font stack or a radius has no ARGB form)", token)
	}
	switch len(v) {
	case 7: // #rrggbb -> opaque
		return "#FF" + strings.ToUpper(v[1:]), nil
	case 9: // #aarrggbb, already explicit
		return "#" + strings.ToUpper(v[1:]), nil
	}
	return "", fmt.Errorf("token value %q is neither #rrggbb nor #aarrggbb", token)
}

type tokenMapRow struct {
	Token    string
	Resource string
	Line     int
}

func loadTokenMap(t *testing.T) []tokenMapRow {
	t.Helper()
	path := filepath.Join(androidRoot(t), tokenMapFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-TOK-1 requires a checked-in join at %s: %v\n"+
			"Columns (tab separated): token<TAB>android_color<TAB>note.\n"+
			"`token` is a key of %s's \"tokens\" object (e.g. --p-bg); `android_color` is a "+
			"<color name=...> in app/src/main/res/values/colors.xml.\n"+
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
		if len(cols) < 2 {
			t.Errorf("%s:%d: needs at least token and android_color", tokenMapFile, i+1)
			continue
		}
		rows = append(rows, tokenMapRow{
			Token: strings.TrimSpace(cols[0]), Resource: strings.TrimSpace(cols[1]), Line: i + 1,
		})
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no rows; the join would be vacuous", mustRel(t, path))
	}
	return rows
}

// TestPBTOK1_TheAndroidThemeColoursAreTheDesignTokens is the requirement.
func TestPBTOK1_TheAndroidThemeColoursAreTheDesignTokens(t *testing.T) {
	tokens := loadDesignTokens(t)
	colors := androidColors(t)
	rows := loadTokenMap(t)

	mappedResources := map[string]bool{}
	for _, r := range rows {
		mappedResources[r.Resource] = true

		value, ok := tokens.Tokens[r.Token]
		if !ok {
			t.Errorf("%s:%d: token %q is not declared in %s", tokenMapFile, r.Line, r.Token, tokensRelPath)
			continue
		}
		want, err := argbFromToken(value)
		if err != nil {
			t.Errorf("%s:%d: %v", tokenMapFile, r.Line, err)
			continue
		}
		got, ok := colors[r.Resource]
		if !ok {
			t.Errorf("%s:%d: maps token %q to <color name=%q>, which colors.xml does not declare",
				tokenMapFile, r.Line, r.Token, r.Resource)
			continue
		}
		if got != want {
			t.Errorf("PB-TOK-1: the Android theme and the token origin DISAGREE.\n"+
				"\t%s = %s  (%s)\n\t<color name=%q> = %s  (colors.xml)\n"+
				"The requirement is that one JSON source is the SINGLE ORIGIN for the theme, and "+
				"its criterion says in as many words that the values must actually agree. Today "+
				"colors.xml declares its own literals and calls them placeholders; a phone painted "+
				"from them renders a product nobody designed, and every screen S16 adds inherits "+
				"the wrong surface.",
				r.Token, value, tokensRelPath, r.Resource, got)
		}
	}

	// THE OTHER DIRECTION. A colour resource with no row is a value that entered the theme
	// without passing through the origin, which is the same defect arriving from the other
	// side -- and it is how "single origin" decays into "origin plus a few extras".
	var unmapped []string
	for name := range colors {
		if !mappedResources[name] {
			unmapped = append(unmapped, name)
		}
	}
	sort.Strings(unmapped)
	if len(unmapped) > 0 {
		t.Errorf("PB-TOK-1: %d colour resource(s) have no row in %s and therefore no origin:\n\t%s",
			len(unmapped), tokenMapFile, strings.Join(unmapped, "\n\t"))
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
	// The real divergence in the tree today, as a live negative control.
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
		if v, ok := tokens.Tokens[r.Token]; ok {
			if argb, err := argbFromToken(v); err == nil {
				allowed[strings.TrimPrefix(argb, "#")] = true
			}
		}
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
