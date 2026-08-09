// PB-TOK-7: the colours this product COMPUTES rather than declares.
//
// docs/research/remote-control-design-directions.html resolves four values with
// `color-mix(in srgb, ...)`: the attention row's border, the deny button's fill, and the two
// status-dot glows. They are not tokens -- they are functions of tokens -- so every consumer that
// wants one has, until now, had exactly one option: type the resolved hex.
//
// THERE IS NOW A FIFTH AND IT IS NOT THE ARTIFACT'S. `toggle-track-off` comes from
// docs/design/substrate-components.md row 4, for a component Substrate never drew. This sentence
// used to say "four" throughout and was left saying it when the fifth landed, which is the small
// way a file starts describing a thing it no longer is; [Derivations] carries the argument for
// admitting it.
//
// THAT IS THE DEFECT PB-TOK-1 EXISTS TO CATCH, ONE INDIRECTION FURTHER OUT. The PB-TOK-1 join
// pairs a token NAME with a resource name and compares their values, so it sees a palette copied
// into colors.xml. It cannot see #6D5220 typed into a drawable, because #6D5220 is not any
// token's value and no row would ever have named it. A derived colour transcribed once is a
// fourth copy of the palette that the existing fence is structurally blind to.
//
// So they live here, as data plus one blend function, and android/gate/s22_derived_test.go
// asserts that no Kotlin or XML literal anywhere equals what this file computes.
package design

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Transparent is the CSS keyword, which is rgba(0, 0, 0, 0) -- a colour with zero alpha, NOT the
// absence of a colour. The distinction is the whole of [Mix]'s second branch.
const Transparent = "transparent"

// RGBA is a colour with 8-bit channels, which is what both CSS hex notation and an Android
// <color> resource carry. Keeping the public type 8-bit means a value that leaves this package
// has already been quantised exactly once, at the end of the blend, rather than drifting through
// a chain of float round-trips.
type RGBA struct{ R, G, B, A uint8 }

// Hex renders the colour the way the artifact and the Android resource table write it: six
// digits when opaque, and #AARRGGBB otherwise. Alpha leads because that is Android's order, and
// because the four derived values are quoted that way everywhere they are discussed.
func (c RGBA) Hex() string {
	if c.A == 0xFF {
		return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
	}
	return fmt.Sprintf("#%02X%02X%02X%02X", c.A, c.R, c.G, c.B)
}

// rgbaRe is CSS rgba(): three integer channels and a fractional alpha. The Substrate skin uses
// it for exactly one token, --p-tabbg, and typing that token `effect` because this parser could
// not read it is what made PB-TOK-5's "all 16 colours reach the app" true by construction. An
// audit committee found it; the fix is to read the notation, not to reclassify the colour.
var rgbaRe = regexp.MustCompile(`^rgba\(\s*([0-9]{1,3})\s*,\s*([0-9]{1,3})\s*,\s*([0-9]{1,3})\s*,\s*(0|1|0?\.[0-9]+)\s*\)$`)

// ParseColor reads #rrggbb, #aarrggbb, CSS rgba(), or the keyword `transparent`.
//
// It is deliberately strict, and refuses the three-digit shorthand, a missing #, and anything
// with a non-hex digit. A lenient colour parser is how a token that is not a colour -- a font
// stack, a radius, a gradient -- acquires a colour conversion nobody decided to give it, which is
// exactly the failure PB-TOK-6 replaces with typed per-kind converters.
func ParseColor(s string) (RGBA, error) {
	v := strings.TrimSpace(s)
	if v == Transparent {
		return RGBA{}, nil
	}
	if m := rgbaRe.FindStringSubmatch(v); m != nil {
		ch := [3]uint8{}
		for i := 0; i < 3; i++ {
			n, err := strconv.ParseUint(m[i+1], 10, 16)
			if err != nil || n > 255 {
				return RGBA{}, fmt.Errorf("%q: channel %q is not 0-255", s, m[i+1])
			}
			ch[i] = uint8(n)
		}
		a, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			return RGBA{}, fmt.Errorf("%q: alpha %q is not a fraction", s, m[4])
		}
		return RGBA{R: ch[0], G: ch[1], B: ch[2], A: round8(a * 255)}, nil
	}
	if !strings.HasPrefix(v, "#") {
		return RGBA{}, fmt.Errorf("%q is not a hex colour, an rgba() colour, or the keyword %q", s, Transparent)
	}
	digits := v[1:]
	switch len(digits) {
	case 6:
		digits = "FF" + digits
	case 8:
	default:
		return RGBA{}, fmt.Errorf("%q is neither #rrggbb nor #aarrggbb", s)
	}
	n, err := strconv.ParseUint(digits, 16, 32)
	if err != nil {
		return RGBA{}, fmt.Errorf("%q is not hexadecimal: %w", s, err)
	}
	return RGBA{
		A: uint8(n >> 24),
		R: uint8(n >> 16),
		G: uint8(n >> 8),
		B: uint8(n),
	}, nil
}

// Mix is `color-mix(in srgb, x <fraction>%, y)`: the ONE blend function PB-TOK-7 asks for.
//
// THE TWO FORMS THE ARTIFACT USES LOOK ALIKE AND BEHAVE DIFFERENTLY, and one function has to get
// both right or the three derived values become three hand-transcriptions again:
//
//   - `color-mix(in srgb, --p-att 36%, --p-hair)` blends toward a second OPAQUE colour. Both
//     alphas are 1, so this is the plain weighted average of the channels.
//   - `color-mix(in srgb, --p-att 50%, transparent)` blends toward rgba(0,0,0,0). This does NOT
//     darken the colour: CSS interpolates in PREMULTIPLIED space, transparent's premultiplied
//     contribution is zero on every channel, and un-premultiplying by the resulting alpha
//     returns the base colour's RGB untouched with alpha 0.50. Since owner ruling R8
//     (2026-08-09), this is the maquette's `.sdot.att { box-shadow: 0 0 9px rgba(201,168,118,0.5)
//     }` — a literal rgba() rather than a color-mix() call, and mathematically the same operation:
//     an alpha-only blend over transparent is exactly what stating the base colour's own RGB at a
//     reduced alpha means.
//
// Interpolating un-premultiplied gets the alpha right and the hue wrong, and the result still
// reads as "a dimmer version of the token" in a diff -- so the mistake survives review. Doing the
// premultiplied form uniformly gets both cases from one expression, because when both alphas are
// 1 the premultiply and the divide cancel.
func Mix(x RGBA, fraction float64, y RGBA) RGBA {
	p := fraction
	ax, ay := float64(x.A)/255, float64(y.A)/255
	a := p*ax + (1-p)*ay
	if a == 0 {
		// Fully transparent: CSS serialises rgba(0,0,0,0) and there is no colour to recover,
		// since dividing by the alpha below would be dividing by zero.
		return RGBA{}
	}
	// Premultiply, interpolate, divide back out. When both alphas are 1 the multiply and the
	// divide cancel and this is the plain weighted average; when y is transparent its term is
	// zero and the divide by a = p returns x's channel unchanged.
	channel := func(cx, cy uint8) uint8 {
		premultiplied := p*ax*float64(cx) + (1-p)*ay*float64(cy)
		return round8(premultiplied / a)
	}
	return RGBA{
		R: channel(x.R, y.R),
		G: channel(x.G, y.G),
		B: channel(x.B, y.B),
		A: round8(a * 255),
	}
}

// round8 quantises one channel to 8 bits. Rounding happens ONCE, at the end of the blend -- an
// implementation that quantised its intermediates would accumulate a unit of error per operation
// and land beside the artifact rather than on it.
//
// Half rounds away from zero, which is CSS's own rule and browsers' own practice, and it used to
// be load-bearing rather than incidental in a way this table could show directly: before owner
// ruling R8 (2026-08-09) the needs-input glow's alpha was 0.70 * 255 = 178.5, an exact tie, and
// the artifact rendered 0xB3 = 179 -- away from zero. Rounding half to even would have produced
// 178, wrong by one on the one derivation whose arithmetic landed on the boundary. R8 moved that
// derivation's share to 50%, whose own tie (0.50 * 255 = 127.5) happens to round to 128 either
// way, so no row in this table exercises the disagreement today; the rule stays `math.Round`'s
// own (away from zero) because that is still the CSS behaviour a future derivation may need.
// TestPBTOK7_MixingWithAColourBlendsRGBAndMixingWithTransparentScalesAlpha asserts 127.5 -> 128
// directly, independent of which derivation currently lands there.
func round8(v float64) uint8 {
	r := math.Round(v)
	if r <= 0 {
		return 0
	}
	if r >= 255 {
		return 255
	}
	return uint8(r)
}

// Derivation is one colour the artifact computes from tokens with `color-mix(in srgb, ...)`.
//
// It is data rather than four named constants so that the gate can iterate it: "no literal
// anywhere equals a derivation's output" is only enforceable against a list something can walk.
type Derivation struct {
	// Name is the stable identifier a consumer and the gate both use.
	Name string
	// Base is the token whose share is Percent.
	Base string
	// Percent is Base's share, in CSS percent.
	Percent int
	// Over is the token mixed into, or [Transparent].
	Over string
	// Site is where the artifact applies it, so a reviewer can check the derivation against the
	// design rather than against this file.
	Site string
}

// Resolve computes the derivation over a token set, which is tokens.json's "tokens" object.
// Taking the tokens as an argument rather than reading the file is what lets the gate assert
// PB-TOK-7's second criterion -- that moving a base token moves the derived value.
func (d Derivation) Resolve(tokens map[string]string) (RGBA, error) {
	baseVal, ok := tokens[d.Base]
	if !ok {
		return RGBA{}, fmt.Errorf("derivation %s: no token %s in the origin", d.Name, d.Base)
	}
	base, err := ParseColor(baseVal)
	if err != nil {
		return RGBA{}, fmt.Errorf("derivation %s: base %s: %w", d.Name, d.Base, err)
	}
	over := RGBA{}
	if d.Over != Transparent {
		overVal, ok := tokens[d.Over]
		if !ok {
			return RGBA{}, fmt.Errorf("derivation %s: no token %s in the origin", d.Name, d.Over)
		}
		if over, err = ParseColor(overVal); err != nil {
			return RGBA{}, fmt.Errorf("derivation %s: over %s: %w", d.Name, d.Over, err)
		}
	}
	return Mix(base, float64(d.Percent)/100, over), nil
}

// Derivations is every colour this product computes with color-mix rather than declares.
//
// SCOPE: the three the artifact's own CSS declares, and nothing else today.
//
// THERE WAS A FIFTH AND ADR-009 RETIRED IT. `toggle-track-off` -- `--p-ink3` at 40% over
// transparent -- was the only entry whose authority was docs/design/substrate-components.md
// instead, added under this comment's own invitation ("mock-derived values belong in this table
// the moment a Substrate spec exists for them") because Substrate drew no toggle and row 4 was the
// whole specification for one. `docs/research/obsidian-maquette.html` draws the toggle, and it
// gives the off track `--p-elev` inside a `--p-hair` border: two tokens and no blend. A derivation
// whose only consumer has stopped consuming it is a colour nothing can get wrong, and keeping it
// would leave the next reader looking for the component that spends it.
//
// THERE WAS A FOURTH TOO, AND OWNER RULING R8 RETIRED IT (2026-08-09, bead agents-tracker-oonj,
// specimens https://claude.ai/code/artifact/cf7206b3-787c-43d7-b275-a46fa7e8320b). ADR-009 D6
// recorded an OPEN CONFLICT between the two design sources' own status-dot glows: the original
// directions artifact's `.pdot.att`/`.pdot.wrk` glow NeedsInput at 70% and Working at 55%, and
// the owner-signed maquette's `.sdot.att` glows only NeedsInput, at a literal 50%, with no
// `box-shadow` at all on `.sdot.work`. R8 picked the maquette's reading -- "one glow means one
// meaning: the light marks the session that needs you" -- so `working-dot-glow` retires with its
// only consumer, `Kit.groupGlow`'s `"working"` branch, for [toggle-track-off]'s exact reason: a
// derivation nothing spends is a colour nothing can get wrong, and a table entry with no caller
// is a caller waiting to be rediscovered. `needs-input-dot-glow` survives, its Percent moved from
// 70 to 50 and its Site re-pointed at the maquette's own selector.
//
// The invitation stands. A row here is still what lets a specified-but-undrawn blend in.
//
// WHAT IS STILL OUTSIDE. Requirements section 6.13 also names a destructive-outline and an
// approval-card tint; those are read off the RETIRED iOS mock and have no derivation-table row of
// their own yet. Same door, same key: a row that specifies one is what lets it in. The blend
// function already covers their form, which is `X P%, transparent`.
func Derivations() []Derivation {
	return []Derivation{
		{
			Name:    "attention-row-border",
			Base:    "--p-att",
			Percent: 36,
			Over:    "--p-hair",
			Site:    ".prow.attention border-color -- the NeedsInput row's warmed hairline",
		},
		{
			Name:    "deny-fill",
			Base:    "--p-err",
			Percent: 13,
			Over:    Transparent,
			Site:    ".a2-no background -- the deny button's tint over whatever is behind it",
		},
		{
			Name:    "needs-input-dot-glow",
			Base:    "--p-att",
			Percent: 50,
			Over:    Transparent,
			// RULING R8 (2026-08-09): moved from the original directions artifact's `.pdot.att`
			// (70%, color-mix) to the owner-signed maquette's own selector for the status dot,
			// `.sdot.att` -- ADR-009 D2 makes the maquette normative and `.pdot` there names a
			// DIFFERENT dot (machine presence, `.chip .pd`), so `.sdot` is the honest citation.
			// `.sdot.att`'s box-shadow is a literal `rgba(201,168,118,0.5)`, mathematically the
			// same operation Mix already computes for a color-mix()-over-transparent share.
			Site: ".sdot.att box-shadow 0 0 9px rgba(...,0.5) -- the NeedsInput status dot's halo",
		},
	}
}
