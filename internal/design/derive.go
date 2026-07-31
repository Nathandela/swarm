// PB-TOK-7: the four colours the design artifact COMPUTES rather than declares.
//
// docs/research/remote-control-design-directions.html resolves four values with
// `color-mix(in srgb, ...)`: the attention row's border, the deny button's fill, and the two
// status-dot glows. They are not tokens -- they are functions of tokens -- so every consumer that
// wants one has, until now, had exactly one option: type the resolved hex.
//
// THAT IS THE DEFECT PB-TOK-1 EXISTS TO CATCH, ONE INDIRECTION FURTHER OUT. The PB-TOK-1 join
// pairs a token NAME with a resource name and compares their values, so it sees a palette copied
// into colors.xml. It cannot see #6D5220 typed into a drawable, because #6D5220 is not any
// token's value and no row would ever have named it. A derived colour transcribed once is a
// fourth copy of the palette that the existing fence is structurally blind to.
//
// So the four live here, as data plus one blend function, and android/gate/s22_derived_test.go
// asserts that no Kotlin or XML literal anywhere equals what this file computes.
package design

import (
	"fmt"
	"math"
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

// ParseColor reads #rrggbb, #aarrggbb or the keyword `transparent`.
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
	if !strings.HasPrefix(v, "#") {
		return RGBA{}, fmt.Errorf("%q is not a hex colour or the keyword %q", s, Transparent)
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
// both right or the four derived values become four hand-transcriptions again:
//
//   - `color-mix(in srgb, --p-att 36%, --p-hair)` blends toward a second OPAQUE colour. Both
//     alphas are 1, so this is the plain weighted average of the channels.
//   - `color-mix(in srgb, --p-att 70%, transparent)` blends toward rgba(0,0,0,0). This does NOT
//     darken the colour: CSS interpolates in PREMULTIPLIED space, transparent's premultiplied
//     contribution is zero on every channel, and un-premultiplying by the resulting alpha
//     returns the base colour's RGB untouched with alpha 0.70.
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
// Half rounds away from zero, and that is load-bearing rather than incidental: the needs-input
// glow's alpha is 0.70 * 255 = 178.5, an exact tie, and the artifact renders 0xB3 = 179. Rounding
// half to even would produce 178 and the value would be wrong by one on the one derivation whose
// arithmetic lands on the boundary.
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

// Derivations is every colour the Substrate artifact resolves with color-mix.
//
// SCOPE. These four are the ones the artifact's own CSS declares. Requirements section 6.13 also
// names a destructive-outline and an approval-card tint; those are read off the RETIRED iOS mock,
// not off Substrate, and ADR-007 B134 assigns authoring them to PB-DS-7. They belong in this
// table the moment a Substrate spec exists for them -- the blend function already covers their
// form, which is `X P%, transparent`.
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
			Percent: 70,
			Over:    Transparent,
			Site:    ".pdot.att box-shadow 0 0 9px -- the NeedsInput status dot's halo",
		},
		{
			Name:    "working-dot-glow",
			Base:    "--p-work",
			Percent: 55,
			Over:    Transparent,
			Site:    ".pdot.wrk box-shadow 0 0 9px -- the Working status dot's halo",
		},
	}
}
