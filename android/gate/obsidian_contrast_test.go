package gate

// FAILING-FIRST (TDD RED, GG-5) for ADR-009 D8.1 -- THE CONTRAST GATE.
//
// "A contrast gate (android/gate/, new, RED-first): computes APCA lightness contrast for every
//  ink-on-surface pair the join can derive (ink/ink2/ink3 x bg/card/elev/well, hero-ink on hero,
//  err and hero as text on bg) and fails below Lc 75 for body-size roles, Lc 60 for large/display
//  roles; non-text state indicators hold WCAG >= 3:1 against their adjacent surface. WCAG 2.x
//  alone is known to false-pass ~49% of dark pairs, which is why APCA leads and WCAG certifies.
//  The gate reads token values through the join, so it guards every future skin, not just this
//  one."
//
// THE TWO FLOORS IN THAT QUOTE ARE SUPERSEDED, and the quote stays because the original words are
// what the amendment is an amendment TO. ADR-009 D8.1 now carries an "Amendment (2026-08-07,
// measured calibration)": this gate was the first thing in the repository that ever computed a
// contrast number, it failed twelve of sixteen pairs against those two floors -- on the palette
// SHIPPED to the internal track as well as on the new one -- and one of the twelve was
// unsatisfiable by construction. The amendment replaces the two rungs with per-role floors bound
// to how each ink is actually used, and the constants below are that table.
//
// THE STATE OF THE WORLD before this file: nothing anywhere computed a contrast number. The
// palette was drift-guarded end to end (PB-TOK-1's join, PB-TOK-5's completeness, PB-DS-4's
// radii) and every one of those fences is about AGREEMENT -- that colors.xml says what
// tokens.json says. None of them is about the values being READABLE. A skin could move every ink
// one step toward its ground, agree with itself perfectly, and ship an app nobody can read.
//
// WHY APCA AND NOT WCAG 2.x ALONE. WCAG 2.x's (L1+0.05)/(L2+0.05) ratio is a fixed offset over
// relative luminance and is known to false-pass dark-mode pairs at roughly half the rate it
// judges them -- it has no model of polarity, so light-on-dark and dark-on-light get the same
// answer, and near-black grounds are exactly where the offset dominates. Obsidian is a
// near-black warm ladder: every pair in this file is light-on-dark, which is the regime WCAG is
// least trustworthy in. So APCA (SAPC screen-polarity Lc) LEADS for text, and WCAG CERTIFIES the
// non-text indicators, where APCA declines to give a text-size answer at all.
//
// WHERE THE NUMBERS COME FROM. Not from here. Every colour is read through the join -- the token
// value out of internal/design/tokens.json, and the token must have a row in
// android/design-tokens.tsv pointing at a <color> that colors.xml actually declares. A pair
// whose ink or surface never reaches the app is a pair this gate refuses to judge, loudly,
// because "readable" is a claim about what renders and not about what a JSON file contains.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The thresholds. ADR-009 D8.1, and they are FLOORS: a failing pair moves the
// TOKEN (the ladder is the declared tunable, ADR-009 D3), never the number here.
// ---------------------------------------------------------------------------

// THE FLOORS ARE PER ROLE, AND THEY ARE MEASURED. ADR-009 D8.1 Amendment (2026-08-07, measured
// calibration).
//
// WHAT THEY REPLACED, quoted because a threshold that moves without its predecessor written down
// is a threshold nobody can audit:
//
//	// apcaBodyFloor is the Lc a body-size role must reach. ADR-009 D8.1.
//	apcaBodyFloor = 75.0
//	// apcaLargeFloor is the Lc a large/display-size role must reach.
//	apcaLargeFloor = 60.0
//
// Those two numbers were written before anything in this repository had computed a contrast
// figure, and this gate -- the first thing that ever did -- failed TWELVE of sixteen text pairs
// against them, on Obsidian and on the Substrate palette live on the internal track alike. One of
// the twelve was unsatisfiable BY CONSTRUCTION: the maximum |Lc| any ink can reach on the
// champagne fill is 59.73, so no value of --p-hero-ink could ever clear 75, and the pair misses
// even the large floor of 60 by 0.3. The defect was in the ADR's text.
//
// The amendment replaces them with APCA's own conformance ladder mapped to this app's real type
// roles. The rungs the standard states -- 90 preferred body, 75 body minimum, 60 content, 45
// large-bold and non-content, 30 absolute -- are in apcaRungs below, and each role's floor is
// asserted against its rung so a floor that drops below the ladder has to say so out loud.
//
// THE SUBSTRATE BASELINE IS KEPT IN EACH COMMENT. Two of these floors (45 and 24) are cleared by
// Obsidian and FAILED by the palette that shipped, which is the whole evidence that this
// migration improves legibility rather than merely repainting it; the numbers have to stay
// readable for that comparison to be checkable later.
const (
	// --p-ink, primary body prose.   Substrate -103.0..-102.3 | Obsidian -100.0..-98.7
	apcaFloorPrimary = 90.0
	// --p-ink2, spot-read supplementary status text.
	//                                Substrate  -41.9..-41.1 | Obsidian  -49.7..-48.4
	apcaFloorSupplementary = 45.0
	// --p-ink3, incidental / de-emphasized.
	//                                Substrate  -22.9..-22.1 | Obsidian  -25.6..-24.2
	apcaFloorIncidental = 24.0
	// --p-hero-ink on the champagne fills, the CTA label at 14sp/500.
	//                                Substrate      +64.6    | Obsidian      +58.8
	apcaFloorCTALabel = 55.0
	// --p-hero as text, the LIVE counter and links.
	//                                Substrate      -63.8    | Obsidian      -57.7
	apcaFloorAccentText = 50.0
	// --p-err as text, the deny and revoke labels. Adjudicated 2026-08-09 (ruling R7).
	//                                Substrate      -47.3    | Obsidian      -40.6
	apcaFloorErrorText = 38.0

	// wcagIndicatorFloor is the ratio a NON-TEXT state indicator must hold against the surface
	// behind it. APCA has no answer for a 7dp dot -- it models text -- so the certifying
	// standard is WCAG 2.x's non-text contrast minimum. Unamended: it always passed.
	wcagIndicatorFloor = 3.0
)

// The two floors the amendment retired, kept as named constants because the ceiling proof is
// stated ABOUT them: TestADR009D8_AFloorNoInkCanReachIsAFloorAndNotAPalette shows the champagne
// fill's ceiling sits below both, which is what made the old model impossible rather than merely
// strict. Deleting them would delete the evidence for the amendment along with the numbers.
const (
	apcaRetiredBodyFloor  = 75.0
	apcaRetiredLargeFloor = 60.0
)

// apcaRungs is APCA's own conformance ladder -- the standard's numbers, not this app's. Every
// role below is mapped onto a rung and its floor is checked against it.
var apcaRungs = map[float64]string{
	90: "preferred for body prose",
	75: "minimum for body text",
	60: "content text and headlines",
	45: "large-and-bold, and non-content text (button labels, placeholders, spot-read metadata)",
	30: "absolute minimum for any text",
}

// apcaRole is a job a colour does in this app, with the floor that job has to clear.
//
// A role is DECLARED per pair rather than inferred, because "which ink is allowed to be faint" is
// a design decision, and inference would let a failing pair be quietly reclassified into whatever
// role it happens to satisfy. The closed-list assertion in
// TestADR009D8_EveryRoleFloorIsDeclaredAgainstItsRung is what makes the declaration binding:
// inventing a seventh role with a convenient floor fails as loudly as lowering one of these six.
type apcaRole struct {
	Name string
	// Floor is the |Lc| a pair in this role must reach.
	Floor float64
	// Rung is the APCA conformance rung the role maps to; 0 means the role sits BELOW the
	// standard's ladder entirely and must be a declared deviation.
	Rung float64
	// BelowRung records that the floor is knowingly under its own rung -- a named deviation, not
	// an oversight. O7's device pass judged both on-device 2026-08-09 and the owner adjudicated
	// them (ruling R7, bead agents-tracker-oonj): accepted as designed, no floor moved.
	BelowRung bool
	What      string
}

var (
	roleBodyPrimary = apcaRole{
		Name: "body-primary", Floor: apcaFloorPrimary, Rung: 90,
		What: "primary body prose: the sheet headline, the well, every row title",
	}
	// --p-ink2 maps to the 45 rung on a claim that must stay true: the decision-carrying text in
	// this app renders in --p-ink (sheet headline, well) or --p-hero (the lit need line), NEVER
	// in ink2. ink2 is spot-read supplementary status -- timestamps, counts, the second line of
	// a row -- which is exactly what APCA's 45 rung names as non-content text.
	roleSupplementary = apcaRole{
		Name: "supplementary", Floor: apcaFloorSupplementary, Rung: 45,
		What: "spot-read supplementary status text; never the sole carrier of a decision",
	}
	// THE NAMED DEVIATION, and it is not buried anywhere: 24 sits BELOW APCA's Lc 30 absolute
	// minimum for text. Rung 0 records that the role is off the ladder. It is accepted on two
	// standing rules written into ADR-009 D8.1's amendment: --p-ink3 is never the sole carrier of
	// required information (it labels a group whose rows say the same thing, marks the Completed
	// group that has already resolved, and carries an offline dot that is also a shape change),
	// and the O7 device glance pass was the empirical backstop -- run 2026-08-09, ADJUDICATED:
	// the owner accepted the rendered token as designed (ruling R7, bead agents-tracker-oonj).
	roleIncidental = apcaRole{
		Name: "incidental", Floor: apcaFloorIncidental, Rung: 0, BelowRung: true,
		What: "incidental / de-emphasized; BELOW APCA's absolute 30, never the sole carrier of required information",
	}
	roleCTALabel = apcaRole{
		Name: "cta-label", Floor: apcaFloorCTALabel, Rung: 45,
		What: "the CTA label at 14sp/500 on its champagne fill, whose ceiling is 59.73",
	}
	roleAccentText = apcaRole{
		Name: "accent-text", Floor: apcaFloorAccentText, Rung: 45,
		What: "champagne as text: the LIVE counter, the active tab, links",
	}
	// THE WATCH ITEM, CLOSED. 38 is below the 45 rung; O7's device pass confirmed deny/revoke
	// legibility and the owner accepted the token as designed 2026-08-09 (ruling R7, bead
	// agents-tracker-oonj) -- ADJUDICATED rather than owed. Had it failed on device the TOKEN
	// would have lightened -- ADR-009 D3's ladder rule -- and that remains the remedy for any
	// future regression; this number does not move.
	roleErrorText = apcaRole{
		Name: "error-text", Floor: apcaFloorErrorText, Rung: 45, BelowRung: true,
		What: "terracotta as text: the deny and revoke labels. Adjudicated 2026-08-09 (ruling R7): accepted as designed",
	}
)

// obsidianRoles is the closed list. A pair carrying a role that is not in here is a floor
// invented for a failure.
var obsidianRoles = []apcaRole{
	roleBodyPrimary, roleSupplementary, roleIncidental, roleCTALabel, roleAccentText, roleErrorText,
}

// ---------------------------------------------------------------------------
// The pairs.
// ---------------------------------------------------------------------------

// inkTokens are the three text inks, with the role each claims on every one of the four grounds.
//
// AN INK'S ROLE IS ONE ROLE. The cheapest escape from any floor is to give the failing pair a
// gentler role, so the ink carries the role and not the pair: --p-ink2 cannot be supplementary on
// the well and primary on the card. TestADR009D8_EveryRoleFloorIsDeclaredAgainstItsRung asserts
// the one-role-per-ink property directly rather than trusting this table's shape.
//
// --p-ink3 IS THE DEVIATION AND MUST SAY SO. It is the tertiary ink and it is also the Completed
// group and the offline presence dot (PB-TOK-8), so it is the one ink whose job is to RECEDE.
// Holding it to a body floor would force it lighter until it stopped receding, which is a design
// change wearing an accessibility argument. Its floor is below APCA's absolute minimum and the
// role says so in its own name.
var obsidianInks = []struct {
	Token string
	Role  apcaRole
	Why   string
}{
	{"--p-ink", roleBodyPrimary, "primary text"},
	{"--p-ink2", roleSupplementary, "secondary text -- spot-read status, never the sole carrier of a decision"},
	{"--p-ink3", roleIncidental, "tertiary: section labels and the receded Completed group -- incidental only"},
}

// obsidianSurfaces are the four grounds an ink can land on: the whole ladder plus the well.
var obsidianSurfaces = []string{"--p-bg", "--p-card", "--p-elev", "--p-well"}

// Polarity is which way round a pair is meant to be. It is DECLARED per pair, not inferred.
//
// Almost everything in a dark-only skin is light ink on a dark ground, and the two exceptions
// are the champagne fills: the CTA and the selected chip put a near-black ink on a light
// surface on purpose. Inferring polarity from the values would make an INVERTED ladder --
// a surface that crept above its ink -- indistinguishable from those two intentional fills,
// which is exactly the defect a magnitude-only check cannot see.
const (
	lightOnDark = "light-on-dark"
	darkOnLight = "dark-on-light"
)

// obsidianExtraPairs are the ink-on-surface pairs the ladder cross product does not contain.
var obsidianExtraPairs = []inkPair{
	{Ink: "--p-hero-ink", Surface: "--p-hero", Role: roleCTALabel, Polarity: darkOnLight, Where: "ink on a champagne fill: selected chip, badge, toggle knob"},
	{Ink: "--p-hero-ink", Surface: "--p-cta-bg", Role: roleCTALabel, Polarity: darkOnLight, Where: "the CTA's label on its fill (--p-cta-bg value-aliases --p-hero and keeps its own row)"},
	{Ink: "--p-err", Surface: "--p-bg", Role: roleErrorText, Polarity: lightOnDark, Where: "terracotta as TEXT: the deny label, the destructive row action"},
	{Ink: "--p-hero", Surface: "--p-bg", Role: roleAccentText, Polarity: lightOnDark, Where: "champagne as TEXT: the LIVE counter, the active tab, the peek foreground"},
}

// obsidianIndicators are the non-text colours: the four Group indicators (PB-TOK-8), the presence
// dots, and the sync status pill's mark. --p-ink3 carries both the Completed group and the offline
// dot, which is why it appears once rather than twice; --p-err is here for the pill alone, since no
// Group and no presence state is a failure.
var obsidianIndicators = []struct {
	Token string
	Why   string
}{
	{"--p-att", "Group NeedsInput status dot"},
	{"--p-work", "Group Working status dot and the workbar's opaque stop"},
	{"--p-ok", "Group ReadyForReview status dot, and the ONLINE presence dot"},
	{"--p-ink3", "Group Completed status dot, and the OFFLINE presence dot"},
	// agents-tracker-nx44.2: the sync status pill's mark, which takes the three status tokens at
	// their existing meanings. `--p-work` and `--p-att` are already rows above; this is the site
	// that puts `--p-err` on a DOT for the first time, and a dot is judged by the non-text floor
	// rather than by APCA -- so it needs a row here or the new site is measured by nothing.
	{"--p-err", "the sync status pill's BROKEN mark"},
}

// obsidianIndicatorGrounds are the two surfaces a dot is ever drawn on: a row sits on a card, a
// chip's presence dot sits on the window ground.
var obsidianIndicatorGrounds = []string{"--p-bg", "--p-card"}

type inkPair struct {
	Ink      string
	Surface  string
	Role     apcaRole
	Polarity string
	Where    string
}

// obsidianTextPairs is the full derivable set: the ladder cross product plus the named extras.
func obsidianTextPairs() []inkPair {
	var out []inkPair
	for _, ink := range obsidianInks {
		for _, surface := range obsidianSurfaces {
			out = append(out, inkPair{
				Ink: ink.Token, Surface: surface, Role: ink.Role,
				Polarity: lightOnDark, Where: ink.Why,
			})
		}
	}
	return append(out, obsidianExtraPairs...)
}

// ---------------------------------------------------------------------------
// The colour maths. Implemented here rather than imported: this package must not
// import internal/design, which is one of the artifacts it audits.
// ---------------------------------------------------------------------------

type srgb struct{ R, G, B float64 } // channels in 0..255

// parseSRGB reads the two notations a colour token can carry. Alpha is REFUSED rather than
// composited: a translucent ink over an unknown backdrop has no single contrast answer, and
// inventing one by compositing over the token's nominal ground would report a number for a
// rendering nobody specified.
func parseSRGB(token, value string) (srgb, error) {
	v := strings.TrimSpace(value)
	if m := gateRGBARe.FindStringSubmatch(v); m != nil {
		if m[4] != "1" {
			return srgb{}, fmt.Errorf("%s = %q is translucent; a contrast figure for it would be a "+
				"figure for a composite nobody specified", token, value)
		}
		var ch [3]float64
		for i := 0; i < 3; i++ {
			n, err := strconv.ParseFloat(m[i+1], 64)
			if err != nil || n > 255 {
				return srgb{}, fmt.Errorf("%s = %q: channel %q is not 0-255", token, value, m[i+1])
			}
			ch[i] = n
		}
		return srgb{R: ch[0], G: ch[1], B: ch[2]}, nil
	}
	if !strings.HasPrefix(v, "#") || len(v) != 7 {
		return srgb{}, fmt.Errorf("%s = %q is not an opaque #rrggbb colour", token, value)
	}
	n, err := strconv.ParseUint(v[1:], 16, 32)
	if err != nil {
		return srgb{}, fmt.Errorf("%s = %q is not hexadecimal: %w", token, value, err)
	}
	return srgb{R: float64(n >> 16 & 0xFF), G: float64(n >> 8 & 0xFF), B: float64(n & 0xFF)}, nil
}

// APCA-W3 / SAPC-98 constants, 0.1.9 (the revision the WCAG 3 draft carries). They are spelled
// out rather than folded into the expressions so a reader can check them against the published
// table instead of against this file's arithmetic.
const (
	apcaTRC = 2.4

	apcaRco = 0.2126729
	apcaGco = 0.7151522
	apcaBco = 0.0721750

	apcaNormBG  = 0.56
	apcaNormTXT = 0.57
	apcaRevTXT  = 0.62
	apcaRevBG   = 0.65

	apcaBlkThrs = 0.022
	apcaBlkClmp = 1.414

	apcaScale     = 1.14
	apcaLoOffset  = 0.027
	apcaLoClip    = 0.1
	apcaDeltaYMin = 0.0005
)

// apcaY is the APCA screen luminance: a simple 2.4 power curve on each channel, NOT the piecewise
// sRGB EOTF WCAG 2.x uses. The difference matters precisely in the near-black region this skin
// lives in, which is the whole reason APCA leads here.
func apcaY(c srgb) float64 {
	y := apcaRco*math.Pow(c.R/255, apcaTRC) +
		apcaGco*math.Pow(c.G/255, apcaTRC) +
		apcaBco*math.Pow(c.B/255, apcaTRC)
	// Soft black clamp. Below the threshold, flare and the panel's own floor mean the extra
	// darkness does not buy contrast, so it is discounted rather than counted.
	if y < apcaBlkThrs {
		y += math.Pow(apcaBlkThrs-y, apcaBlkClmp)
	}
	return y
}

// apcaLc is the screen-polarity lightness contrast of text on a background.
//
// THE SIGN IS THE POLARITY AND IT IS NOT COSMETIC. A positive Lc is dark text on a light ground;
// a negative Lc is light text on a dark ground. Obsidian is entirely the second, and an
// implementation that returned math.Abs would silently accept a pair whose polarity had inverted
// -- a light surface that crept above its ink -- while reporting a healthy number.
func apcaLc(text, background srgb) float64 {
	ytxt, ybg := apcaY(text), apcaY(background)
	if math.Abs(ybg-ytxt) < apcaDeltaYMin {
		return 0
	}
	if ybg > ytxt { // normal polarity: dark text on a light ground
		sapc := (math.Pow(ybg, apcaNormBG) - math.Pow(ytxt, apcaNormTXT)) * apcaScale
		if sapc < apcaLoClip {
			return 0
		}
		return (sapc - apcaLoOffset) * 100
	}
	// reverse polarity: light text on a dark ground -- every pair in this skin
	sapc := (math.Pow(ybg, apcaRevBG) - math.Pow(ytxt, apcaRevTXT)) * apcaScale
	if sapc > -apcaLoClip {
		return 0
	}
	return (sapc + apcaLoOffset) * 100
}

// wcagLuminance is WCAG 2.x relative luminance, with its piecewise linearisation. It exists here
// only to certify the NON-TEXT indicators; it deliberately does not judge any text pair.
func wcagLuminance(c srgb) float64 {
	lin := func(v float64) float64 {
		v /= 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// wcagRatio is (lighter + 0.05) / (darker + 0.05), which is order-independent by construction.
func wcagRatio(a, b srgb) float64 {
	la, lb := wcagLuminance(a), wcagLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// ---------------------------------------------------------------------------
// The join. A colour this gate judges must actually reach the app.
// ---------------------------------------------------------------------------

// obsidianJoinedColours returns token -> value for every colour-typed token that has a row in
// android/design-tokens.tsv naming a <color> colors.xml declares.
//
// GOING THROUGH THE JOIN IS THE POINT, not ceremony. A gate that read tokens.json alone would
// certify that a JSON file is readable; what has to be readable is the app. So a token whose row
// is missing, or whose resource is not in colors.xml, is excluded here and then reported as an
// unjudgeable pair below -- which is a failure, not a skip.
func obsidianJoinedColours(t *testing.T) map[string]string {
	t.Helper()
	tokens := loadDesignTokens(t)
	rows := loadTokenMap(t)
	colours := androidColors(t)

	out := map[string]string{}
	for _, r := range rows {
		if tokens.Kinds[r.Token] != "color" {
			continue
		}
		value, ok := tokens.Tokens[r.Token]
		if !ok {
			continue
		}
		if _, declared := colours[r.Resource]; !declared {
			continue
		}
		out[r.Token] = value
	}
	if len(out) == 0 {
		t.Fatalf("ADR-009 D8.1: no colour token survives the join (tokens.json -> %s -> colors.xml), "+
			"so every contrast assertion below would iterate an empty set and pass saying nothing",
			tokenMapFile)
	}
	return out
}

func obsidianColour(t *testing.T, joined map[string]string, token string) (srgb, bool) {
	t.Helper()
	value, ok := joined[token]
	if !ok {
		t.Errorf("ADR-009 D8.1: %s does not reach the app through the join "+
			"(tokens.json -> %s -> colors.xml), so no pair using it can be judged. A colour that "+
			"is not joined is not rendered, and a contrast figure for it would be a figure for "+
			"nothing.", token, tokenMapFile)
		return srgb{}, false
	}
	c, err := parseSRGB(token, value)
	if err != nil {
		t.Errorf("ADR-009 D8.1: %v", err)
		return srgb{}, false
	}
	return c, true
}

// ---------------------------------------------------------------------------
// The requirement.
// ---------------------------------------------------------------------------

// TestADR009D8_EveryInkOnSurfacePairClearsItsAPCAFloor is the gate.
func TestADR009D8_EveryInkOnSurfacePairClearsItsAPCAFloor(t *testing.T) {
	joined := obsidianJoinedColours(t)

	var report []string
	for _, p := range obsidianTextPairs() {
		ink, okI := obsidianColour(t, joined, p.Ink)
		surface, okS := obsidianColour(t, joined, p.Surface)
		if !okI || !okS {
			continue
		}
		lc := apcaLc(ink, surface)
		floor := p.Role.Floor
		report = append(report, fmt.Sprintf("%-13s on %-11s  Lc %7.1f  (%s floor %.0f)  %s",
			p.Ink, p.Surface, lc, p.Role.Name, floor, p.Where))
		if apcaFails(lc, floor) {
			// WHICH FIX THIS FAILURE ACTUALLY ADMITS. "Move the token" is the remedy only where
			// the floor is reachable on this surface; above the surface's ceiling no ink value
			// exists that would clear it, and the redness is a statement about the floor.
			remedy := "The floor does not move. ADR-009 D3 declares the ladder the tunable and " +
				"the direction fixed: if a pair fails, the TOKEN moves (and the maquette is then " +
				"wrong too, which is an owner decision, not an edit)."
			if ceiling := apcaCeiling(surface); ceiling < floor {
				remedy = fmt.Sprintf("NO INK CLEARS THIS FLOOR ON THIS SURFACE. The best any "+
					"colour can do on %s is |Lc| %.1f, below the %s floor of %.0f, so moving %s "+
					"cannot fix it and neither could any value nobody has drawn yet. What is "+
					"failing here is the FLOOR over a mid-luminance fill, not the palette -- the "+
					"same defect ADR-009 D8.1's amendment was written to fix, arriving again. "+
					"That one is an owner decision on the ADR; see "+
					"docs/verification/obsidian-o2-evidence.md.",
					p.Surface, ceiling, p.Role.Name, floor, p.Ink)
			}
			t.Errorf("ADR-009 D8.1: %s on %s is Lc %.1f, and the %s floor is %.0f.\n"+
				"\t%s = %s, %s = %s -- %s\n%s",
				p.Ink, p.Surface, lc, p.Role.Name, floor,
				p.Ink, joined[p.Ink], p.Surface, joined[p.Surface], p.Where, remedy)
		}
		// Polarity, asserted against the DECLARATION rather than assumed. Every ladder pair is
		// light ink on a dark ground and its Lc is negative; the two champagne fills are the
		// declared exceptions and theirs is positive. A ladder pair that came out positive is a
		// surface that has crept ABOVE its ink -- which |Lc| alone would score as healthy.
		if want := polarityOf(lc); lc != 0 && want != p.Polarity {
			t.Errorf("ADR-009 D8.1: %s on %s is declared %s and measures %s (Lc %.1f). Polarity is "+
				"declared per pair precisely so an inverted ladder rung cannot hide behind the "+
				"two champagne fills, which are dark-on-light on purpose.",
				p.Ink, p.Surface, p.Polarity, want, lc)
		}
	}
	sort.Strings(report)
	t.Logf("ADR-009 D8.1 APCA ledger (%d text pairs):\n\t%s", len(report), strings.Join(report, "\n\t"))
}

// TestADR009D8_EveryRoleFloorIsDeclaredAgainstItsRung is what keeps the per-role floors from
// becoming six ways to pass.
//
// WHAT IT REPLACED, quoted per the house test-rewrite rule. The old assertion guarded a two-rung
// model that no longer exists:
//
//	func TestADR009D8_TheLargeOnlyExemptionIsDeclaredAndNarrow(t *testing.T) {
//		if apcaLargeFloor >= apcaBodyFloor {
//			t.Fatalf("ADR-009 D8.1: the large floor (%.0f) is not below the body floor (%.0f), so the "+
//				"exemption is meaningless and this assertion guards nothing", apcaLargeFloor, apcaBodyFloor)
//		}
//		if roleOf["--p-ink3"] != roleLarge { ... "The exemption is legitimate and it must be DECLARED." }
//		if ink.Token != "--p-ink3" && ink.Role != roleBody { ... "Only the tertiary ink is large-only" }
//		if p.Role != roleBody { ... "the accent, the CTA and the error text are all body-size roles" }
//
// Its worry survives the amendment unchanged and only changes shape. Under two floors the cheap
// escape was RELABELLING a failing pair as large; under six the cheap escape is INVENTING a
// seventh role, or quietly lowering one of the six until the palette fits. So:
//
//   - the role list is CLOSED, and every pair's role must be in it by value -- a floor that does
//     not appear in the ADR's amendment table cannot be used;
//   - each floor is PINNED to the number the amendment states, so moving one is a test rewrite
//     that has to quote what it replaced, which is the whole point of writing floors down;
//   - each floor is checked against APCA's own rung, and the two that sit BELOW their rung must
//     say so in the declaration (BelowRung) rather than being discovered by a reader with a
//     calculator;
//   - an ink carries ONE role across all four grounds, because an ink that is supplementary on
//     the well and primary on the card is an ink whose role is chosen per failure.
func TestADR009D8_EveryRoleFloorIsDeclaredAgainstItsRung(t *testing.T) {
	// THE PIN. These are ADR-009 D8.1's amendment table, transcribed. If a floor moves, this is
	// the assertion that has to be rewritten quoting itself -- which is exactly the friction a
	// threshold deserves.
	wantFloors := map[string]float64{
		"body-primary":  90,
		"supplementary": 45,
		"incidental":    24,
		"cta-label":     55,
		"accent-text":   50,
		"error-text":    38,
	}
	if len(obsidianRoles) != len(wantFloors) {
		t.Fatalf("ADR-009 D8.1: the gate declares %d roles and the amendment states %d. A role "+
			"this table does not know is a floor that was not adjudicated.",
			len(obsidianRoles), len(wantFloors))
	}
	byName := map[string]apcaRole{}
	for _, r := range obsidianRoles {
		byName[r.Name] = r
		want, known := wantFloors[r.Name]
		if !known {
			t.Errorf("ADR-009 D8.1: role %q is not in the amendment's table. Six roles were "+
				"adjudicated; a seventh with its own floor is a threshold nobody signed.", r.Name)
			continue
		}
		if r.Floor != want {
			t.Errorf("ADR-009 D8.1: role %q has floor %.0f and the amendment states %.0f. The "+
				"floors are the adjudicated part: if a pair fails, the TOKEN moves.",
				r.Name, r.Floor, want)
		}
		// The rung must be one APCA actually states, or 0 for a declared off-ladder role.
		if r.Rung != 0 {
			if _, ok := apcaRungs[r.Rung]; !ok {
				t.Errorf("ADR-009 D8.1: role %q maps to rung %.0f, which is not one of APCA's "+
					"(90/75/60/45/30). A rung invented for a role is a justification invented "+
					"for a floor.", r.Name, r.Rung)
			}
		}
		// AND THE HONESTY BIT. A floor under its own rung is allowed; a floor under its own rung
		// that does not admit it is how a deviation becomes invisible.
		under := r.Rung == 0 || r.Floor < r.Rung
		if under != r.BelowRung {
			t.Errorf("ADR-009 D8.1: role %q has floor %.0f against rung %.0f and declares "+
				"BelowRung=%v. The declaration must match the arithmetic: the two deviations in "+
				"this system (--p-ink3 below APCA's absolute 30, --p-err below the 45 rung) are "+
				"accepted BECAUSE they are named, and an unnamed one is not accepted at all.",
				r.Name, r.Floor, r.Rung, r.BelowRung)
		}
	}

	// THE TWO DEVIATIONS ARE EXACTLY TWO, and they are these two. A third role slipping below its
	// rung is the failure mode this whole per-role model could decay into.
	var deviations []string
	for _, r := range obsidianRoles {
		if r.BelowRung {
			deviations = append(deviations, r.Name)
		}
	}
	sort.Strings(deviations)
	if got := strings.Join(deviations, ","); got != "error-text,incidental" {
		t.Errorf("ADR-009 D8.1: the roles below their rung are [%s]; the amendment adjudicates "+
			"exactly two -- incidental (--p-ink3, below APCA's absolute 30, accepted only because "+
			"ink3 is never the sole carrier of required information) and error-text (--p-err at "+
			"38, adjudicated 2026-08-09, ruling R7). Any other role drifting below its rung is a "+
			"floor that was lowered rather than argued.", got)
	}
	// --p-ink3's floor is the one number in this file below APCA's absolute text minimum, and the
	// assertion says the quiet part: it is a deviation, it is bounded, and the bound is 30.
	if roleIncidental.Floor >= 30 {
		t.Errorf("ADR-009 D8.1: the incidental floor is %.0f, at or above APCA's absolute 30. If "+
			"that is true the deviation the amendment names is not a deviation and the two "+
			"standing rules attached to it (never the sole carrier; O7 glance pass) are being "+
			"carried for nothing.", roleIncidental.Floor)
	}
	if roleBodyPrimary.Floor <= roleSupplementary.Floor {
		t.Errorf("ADR-009 D8.1: the primary floor (%.0f) is not above the supplementary one "+
			"(%.0f). The hierarchy the direction depends on is that prose is the readable thing "+
			"and status text recedes; floors that collapse describe a skin with no hierarchy.",
			roleBodyPrimary.Floor, roleSupplementary.Floor)
	}

	// ONE INK, ONE ROLE, everywhere it lands.
	roleOf := map[string]string{}
	for _, p := range obsidianTextPairs() {
		if _, ok := byName[p.Role.Name]; !ok {
			t.Errorf("ADR-009 D8.1: the pair %s on %s carries role %q, which is not in the closed "+
				"list", p.Ink, p.Surface, p.Role.Name)
		}
		if prev, seen := roleOf[p.Ink]; seen && prev != p.Role.Name {
			t.Errorf("ADR-009 D8.1: %s is %q on one surface and %q on another. An ink's role is a "+
				"statement about what it is asked to say, not about which floor it happens to "+
				"clear on which ground.", p.Ink, prev, p.Role.Name)
		}
		roleOf[p.Ink] = p.Role.Name
	}
	if roleOf["--p-ink3"] != roleIncidental.Name {
		t.Errorf("ADR-009 D8.1: --p-ink3 is declared %q. It is the tertiary ink and the receded "+
			"Completed group; holding it to a body floor would force it lighter until it stopped "+
			"receding. The deviation is legitimate and it must be DECLARED.", roleOf["--p-ink3"])
	}
	// BOTH POLARITIES MUST BE IN USE. A table that declared every pair light-on-dark would make
	// the polarity assertion unfalsifiable in one direction and would fail the two champagne
	// fills for being what they are; a table that declared everything dark-on-light would pass
	// an entirely inverted skin.
	polarities := map[string]int{}
	for _, p := range obsidianTextPairs() {
		if p.Polarity != lightOnDark && p.Polarity != darkOnLight {
			t.Errorf("ADR-009 D8.1: %s on %s declares polarity %q, which is neither %q nor %q",
				p.Ink, p.Surface, p.Polarity, lightOnDark, darkOnLight)
		}
		polarities[p.Polarity]++
	}
	if polarities[lightOnDark] == 0 || polarities[darkOnLight] == 0 {
		t.Errorf("ADR-009 D8.1: the pair table declares %d light-on-dark and %d dark-on-light "+
			"pairs. Both must occur: the ladder is light-on-dark and the two champagne fills are "+
			"not, and a table with only one polarity cannot contradict an inverted skin.",
			polarities[lightOnDark], polarities[darkOnLight])
	}

	// Every declared ink must actually appear in the derived set, or a role row is a decision
	// recorded against nothing.
	seen := map[string]bool{}
	for _, p := range obsidianTextPairs() {
		seen[p.Ink] = true
	}
	for _, ink := range obsidianInks {
		if !seen[ink.Token] {
			t.Errorf("ADR-009 D8.1: %s carries a role and is in no pair", ink.Token)
		}
	}
}

// TestADR009D8_TheStateIndicatorsClearWCAGNonTextContrast is D8.1's certifying half.
//
// A 7dp status dot is not text and APCA has no size-based answer for it, so the standard that
// applies is WCAG 2.x's non-text minimum of 3:1 against the adjacent surface. Both surfaces are
// checked because the same dot renders in both places: on a card in a session row, on the window
// ground in a filter chip.
func TestADR009D8_TheStateIndicatorsClearWCAGNonTextContrast(t *testing.T) {
	joined := obsidianJoinedColours(t)

	// The four Group indicators must be PAIRWISE DISTINCT (PB-TOK-8, kept by ADR-009 D6). A skin
	// that fixed a contrast failure by collapsing two Groups onto one colour would clear every
	// ratio below and destroy the taxonomy.
	byValue := map[string][]string{}
	for _, ind := range obsidianIndicators {
		if v, ok := joined[ind.Token]; ok {
			byValue[strings.ToLower(v)] = append(byValue[strings.ToLower(v)], ind.Token)
		}
	}
	for value, tokens := range byValue {
		if len(tokens) > 1 {
			sort.Strings(tokens)
			t.Errorf("ADR-009 D6/PB-TOK-8: %s all hold %s. The four Groups must stay pairwise "+
				"distinct; collapsing two onto one colour clears every ratio below by deleting a "+
				"distinction the status taxonomy depends on.", strings.Join(tokens, " and "), value)
		}
	}

	var report []string
	for _, ind := range obsidianIndicators {
		dot, ok := obsidianColour(t, joined, ind.Token)
		if !ok {
			continue
		}
		for _, groundToken := range obsidianIndicatorGrounds {
			ground, ok := obsidianColour(t, joined, groundToken)
			if !ok {
				continue
			}
			ratio := wcagRatio(dot, ground)
			report = append(report, fmt.Sprintf("%-9s on %-9s  %.2f:1  (%s)",
				ind.Token, groundToken, ratio, ind.Why))
			if ratio < wcagIndicatorFloor {
				t.Errorf("ADR-009 D8.1: %s on %s is %.2f:1, below the %.1f:1 non-text minimum.\n"+
					"\t%s = %s, %s = %s -- %s\n"+
					"This is the certifying check: APCA leads for text and declines to judge a "+
					"7dp dot, so a state indicator holds WCAG's non-text floor or it is a signal "+
					"nobody can see.",
					ind.Token, groundToken, ratio, wcagIndicatorFloor,
					ind.Token, joined[ind.Token], groundToken, joined[groundToken], ind.Why)
			}
		}
	}
	sort.Strings(report)
	t.Logf("ADR-009 D8.1 WCAG non-text ledger (%d indicator/ground pairs):\n\t%s",
		len(report), strings.Join(report, "\n\t"))
}

// TestADR009D8_TheContrastCheckerCanActuallyFail is the NEGATIVE CONTROL, and it is the assertion
// this file is worth least without.
//
// Every assertion above is satisfiable by an apcaLc that returns -1000 for everything, or by a
// wcagRatio that returns 21. Both would be green over the real palette forever. So the two
// functions are exercised against published reference values AND against deliberately failing
// pairs -- IN MEMORY. Nothing here writes to disk: perturbing tokens.json to prove a gate can
// fail is how a perturbation gets committed, which this repository has paid for before.
func TestADR009D8_TheContrastCheckerCanActuallyFail(t *testing.T) {
	black := srgb{0, 0, 0}
	white := srgb{255, 255, 255}

	// The two published APCA-W3 0.1.9 anchors. If these move, the implementation is not APCA and
	// every Lc reported above is a number belonging to some other formula.
	if lc := apcaLc(black, white); math.Abs(lc-106.04) > 0.05 {
		t.Errorf("apcaLc(black on white) = %.2f, want 106.04 (APCA-W3 0.1.9). The implementation "+
			"is not the algorithm ADR-009 D8.1 names.", lc)
	}
	if lc := apcaLc(white, black); math.Abs(lc+107.88) > 0.05 {
		t.Errorf("apcaLc(white on black) = %.2f, want -107.88 (APCA-W3 0.1.9)", lc)
	}
	// Polarity is real: the two anchors above are not each other's negation, which is exactly
	// what distinguishes APCA from a symmetric ratio.
	if math.Abs(apcaLc(black, white)) == math.Abs(apcaLc(white, black)) {
		t.Error("apcaLc is symmetric in its arguments, so it has no polarity model and the " +
			"screen-polarity claim in ADR-009 D8.1 is unbacked")
	}
	// Identical colours have no contrast, and the delta-Y guard must catch it rather than
	// returning a tiny non-zero number that a magnitude check could still fail loudly on.
	if lc := apcaLc(black, black); lc != 0 {
		t.Errorf("apcaLc(black on black) = %.4f, want exactly 0", lc)
	}
	// And the sign must be read the way the gate reads it.
	if polarityOf(apcaLc(white, black)) != lightOnDark {
		t.Error("polarityOf calls white-on-black dark-on-light; every polarity verdict is inverted")
	}
	if polarityOf(apcaLc(black, white)) != darkOnLight {
		t.Error("polarityOf calls black-on-white light-on-dark; every polarity verdict is inverted")
	}

	// THE DELIBERATELY FAILING PAIR, fed to the checker rather than to the tree. #171310 is
	// Obsidian's --p-card and #1f1a13 is --p-elev: two ADJACENT rungs of the ladder. They are
	// meant to be distinguishable as SURFACES and are nowhere near readable as ink on ground,
	// so a checker that passed them would pass anything.
	//
	// IT IS JUDGED AGAINST THE LOWEST FLOOR IN THE SYSTEM, not the highest. The amendment's
	// gentlest role is incidental at Lc 24, and a control that only proved the pair fails 75
	// would prove nothing about the floors this gate actually spends most of its time applying.
	adjacent := apcaLc(srgb{0x1f, 0x1a, 0x13}, srgb{0x17, 0x13, 0x10})
	lowest := obsidianRoles[0].Floor
	highest := obsidianRoles[0].Floor
	for _, r := range obsidianRoles {
		lowest = math.Min(lowest, r.Floor)
		highest = math.Max(highest, r.Floor)
	}
	if math.Abs(adjacent) >= lowest {
		t.Errorf("apcaLc(--p-elev on --p-card) = %.1f, which clears even the lowest floor in the "+
			"system (%.0f, the incidental role). Two adjacent rungs of a near-black ladder are not "+
			"a readable text pair; a checker that says they are cannot fail on anything this gate "+
			"judges.", adjacent, lowest)
	}
	// And it must fail the way a real failure fails: through the same comparison the gate uses.
	if !apcaFails(adjacent, lowest) {
		t.Errorf("the floor comparison passes a pair with almost no lightness difference against "+
			"the lowest floor (%.0f), so every APCA assertion above is vacuous", lowest)
	}
	if apcaFails(apcaLc(white, black), highest) {
		t.Errorf("the floor comparison FAILS white on black against the highest floor (%.0f), so "+
			"it would fail the correct implementation as readily as the wrong one", highest)
	}

	// The same, for the certifying half.
	if r := wcagRatio(white, black); math.Abs(r-21) > 0.001 {
		t.Errorf("wcagRatio(white, black) = %.4f, want 21.0000", r)
	}
	if r := wcagRatio(black, black); r != 1 {
		t.Errorf("wcagRatio(black, black) = %.4f, want 1", r)
	}
	// A near-miss that a sloppy implementation rounds into a pass: #595959 on white is the
	// canonical 7:1 pair, and #767676 on white is the canonical 4.5:1 one. Neither is 3:1, so
	// the function must separate them.
	if a, b := wcagRatio(srgb{0x59, 0x59, 0x59}, white), wcagRatio(srgb{0x76, 0x76, 0x76}, white); math.Abs(a-b) < 1 {
		t.Errorf("wcagRatio cannot separate the canonical 7:1 and 4.5:1 pairs (%.2f vs %.2f)", a, b)
	}
	// The deliberately failing indicator pair, in memory: --p-card on --p-bg. Two ladder rungs
	// again -- they are DESIGNED to be a barely-visible step, so a 3:1 verdict on them would mean
	// the ratio cannot discriminate.
	if r := wcagRatio(srgb{0x17, 0x13, 0x10}, srgb{0x0e, 0x0b, 0x08}); r >= wcagIndicatorFloor {
		t.Errorf("wcagRatio(--p-card, --p-bg) = %.2f:1, at or above the %.1f:1 indicator floor. "+
			"Adjacent ladder rungs are a one-step lightness change by design; a ratio that scores "+
			"them as a visible non-text signal cannot fail on a real indicator either.",
			r, wcagIndicatorFloor)
	}

	// The parser must refuse what it cannot judge, rather than inventing a composite.
	if _, err := parseSRGB("--p-tabbg", "rgba(14,11,8,0.88)"); err == nil {
		t.Error("parseSRGB accepted a translucent colour; a contrast figure for it is a figure " +
			"for a composite nobody specified")
	}
	for _, bad := range []string{"#fff", "650", "0.04", "linear-gradient(90deg, #6fa7a4, transparent 85%)"} {
		if _, err := parseSRGB("--p-x", bad); err == nil {
			t.Errorf("parseSRGB(%q) invented a colour", bad)
		}
	}
}

// apcaFails is the one comparison the gate makes, extracted so the negative control can exercise
// the SAME code path the assertions use rather than a re-typed copy of it.
func apcaFails(lc, floor float64) bool { return math.Abs(lc) < floor }

// apcaCeiling is the largest |Lc| ANY ink can reach on a surface: the surface's contrast ceiling.
//
// Only the two achromatic corners are tried. apcaY is a positively weighted sum of per-channel
// powers, so it is monotone increasing in every channel: over the sRGB cube, Ytxt is smallest at
// pure black and largest at pure white, and |Lc| grows with the distance between Ytxt and the
// fixed Ybg in both polarity branches. The extreme is therefore at one of those two corners. The
// argument is not taken on faith -- TestADR009D8_AFloorNoInkCanReachIsAFloorAndNotAPalette sweeps
// the whole grey axis over a mid-luminance fill and requires the corner answer to win.
func apcaCeiling(surface srgb) float64 {
	return math.Max(
		math.Abs(apcaLc(srgb{0, 0, 0}, surface)),
		math.Abs(apcaLc(srgb{255, 255, 255}, surface)),
	)
}

// TestADR009D8_AFloorNoInkCanReachIsAFloorAndNotAPalette is the assertion that tells the two
// failure modes apart, and without it this file misdirects every fix it demands.
//
// The gate's whole remedy sentence is "if a pair fails, the TOKEN moves" (ADR-009 D3: the ladder
// is the declared tunable). That sentence is only true when the floor is REACHABLE on that
// surface -- when SOME ink value would clear it. A surface has a contrast ceiling: the best any
// ink can do on it. On a near-black ground the ceiling is enormous, so a failing ink really is an
// ink that must move. On a MID-LUMINANCE fill the ceiling is low, and a floor above it is a
// requirement no token in the file can satisfy -- at which point "move the token" sends the next
// reader on a search with no answer in it, forever.
//
// So the ceiling is computed, and a floor above its own ceiling is reported as a defect in the
// FLOOR rather than in the palette. This does not lower anything: the floors are untouched and
// every pair is still judged against them.
func TestADR009D8_AFloorNoInkCanReachIsAFloorAndNotAPalette(t *testing.T) {
	black := srgb{0, 0, 0}
	white := srgb{255, 255, 255}

	// The ceiling of the two extreme grounds is the two published anchors, unsigned: on white the
	// best possible ink is black, on black it is white. If the ceiling function disagreed with
	// apcaLc here it would be measuring something else.
	if got := apcaCeiling(white); math.Abs(got-106.04) > 0.05 {
		t.Errorf("apcaCeiling(white) = %.2f, want 106.04 -- the best ink on white is black, and "+
			"apcaLc(black on white) is the published anchor", got)
	}
	if got := apcaCeiling(black); math.Abs(got-107.88) > 0.05 {
		t.Errorf("apcaCeiling(black) = %.2f, want 107.88 -- the best ink on black is white", got)
	}

	// THE SHORTCUT MUST BE THE ANSWER. apcaCeiling only tries the two achromatic corners, on the
	// argument that APCA's Y is monotone increasing in every channel, so the extremes of Ytxt are
	// pure black and pure white and the largest |Lc| is at one of them. That argument is checked
	// rather than believed: an exhaustive sweep of the achromatic axis over a mid-luminance fill
	// must not beat the corner answer.
	champagne := srgb{0xc9, 0xa8, 0x76}
	ceiling := apcaCeiling(champagne)
	for i := 0; i <= 255; i++ {
		grey := srgb{float64(i), float64(i), float64(i)}
		if lc := math.Abs(apcaLc(grey, champagne)); lc > ceiling+0.005 {
			t.Fatalf("apcaCeiling(#c9a876) = %.2f but grey #%02x beats it at |Lc| %.2f, so the "+
				"two-corner shortcut is not the maximum and every unreachability verdict below "+
				"is a verdict about the wrong number", ceiling, i, lc)
		}
	}

	// AND THE FINDING THIS TEST EXISTS FOR, WHICH IS NOW THE EVIDENCE FOR AN AMENDMENT. --p-hero is
	// #c9a876, a mid-luminance champagne, and it is the ground under --p-hero-ink for the CTA
	// label, the selected chip and the toggle knob. Its ceiling is 59.73: below the retired body
	// floor of 75 AND below the retired large floor of 60. So NO value of --p-hero-ink ever
	// cleared that pair -- not Obsidian's, not Substrate's, not one nobody has drawn. ADR-009
	// D8.1's original two-rung model was UNSATISFIABLE BY CONSTRUCTION for any palette with a
	// mid-luminance accent fill carrying a label, which is why the amendment moved the floor and
	// not the ink. That claim is asserted here rather than only narrated in the ADR.
	if ceiling >= apcaRetiredBodyFloor {
		t.Errorf("apcaCeiling(--p-hero #c9a876) = %.2f, at or above the RETIRED body floor of %.0f. "+
			"If that is true the old two-rung model was satisfiable after all and ADR-009 D8.1's "+
			"amendment rests on a measurement that is wrong.", ceiling, apcaRetiredBodyFloor)
	}
	if ceiling >= apcaRetiredLargeFloor {
		t.Errorf("apcaCeiling(--p-hero #c9a876) = %.2f, at or above the RETIRED large floor of %.0f. "+
			"The champagne pairs missed even that rung -- by 0.3 -- and the amendment quotes the "+
			"margin.", ceiling, apcaRetiredLargeFloor)
	}
	// AND THE OTHER HALF OF THE SAME PROOF, which is what makes the amended floor honest rather
	// than merely lower: the CTA floor the amendment chose is REACHABLE on this fill. A floor set
	// above its own ceiling would have reproduced the original defect one rung down.
	if roleCTALabel.Floor > ceiling {
		t.Errorf("the cta-label floor (%.0f) is above the champagne fill's ceiling (%.2f), so the "+
			"amendment replaced an unsatisfiable requirement with another one and 'move the token' "+
			"is still advice with no answer in it.", roleCTALabel.Floor, ceiling)
	}

	// A near-black ground must NOT be reported unreachable, or the diagnostic would excuse every
	// ink in the skin instead of the two it applies to. They are checked against the HIGHEST floor
	// in the system -- the primary ink's 90 -- because that is the strongest demand any of them
	// has to be able to satisfy.
	for _, surface := range []struct {
		Token string
		Value srgb
	}{
		{"--p-bg", srgb{0x0e, 0x0b, 0x08}},
		{"--p-card", srgb{0x17, 0x13, 0x10}},
		{"--p-elev", srgb{0x1f, 0x1a, 0x13}},
		{"--p-well", srgb{0x09, 0x07, 0x05}},
	} {
		if c := apcaCeiling(surface.Value); c < roleBodyPrimary.Floor {
			t.Errorf("apcaCeiling(%s) = %.2f, below the highest floor in the system (%.0f, the "+
				"primary ink). The ladder's own rungs must stay reachable: an ink that fails on one "+
				"of them is an ink that has to move, and a diagnostic that called that unreachable "+
				"would forgive the whole palette.",
				surface.Token, c, roleBodyPrimary.Floor)
		}
	}
}

// polarityOf names which way round a measured pair actually is.
func polarityOf(lc float64) string {
	if lc < 0 {
		return lightOnDark
	}
	return darkOnLight
}
