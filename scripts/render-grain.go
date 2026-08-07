//go:build ignore

// Renders the checked-in grain tile: ADR-009 D4.3's warm-neutral 140x140 raster.
//
//	go run scripts/render-grain.go android/app/src/main/res/drawable-nodpi/swarm_grain.png
//
// WHY THE OUTPUT IS CHECKED IN AND THIS IS NOT RUN AT BUILD TIME. PB-DS-5 states the reason:
// `feTurbulence` output is implementation-defined, so the design's noise is pre-rendered once and
// committed rather than regenerated. A tile that changed with a toolchain would make every
// screenshot in docs/verification/ a different picture for no recorded reason.
//
// WHY THE GENERATOR IS CHECKED IN ANYWAY. A binary blob nobody can re-derive is a design decision
// with no origin -- exactly what the token pipeline exists to prevent one layer up. This file is
// that origin: a fixed seed, a stated distribution, and a stated tint. `scripts/render-play-assets.py`
// is the same arrangement for the store artwork.
//
// `//go:build ignore` keeps it out of `go build ./...`, `go vet ./...` and golangci-lint. It is a
// one-shot renderer, not a package of the product.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"os"
)

// tile is the design's own size: PB-DS-5 and derivation row 21 both say 140x140.
const tile = 140

// seed is what makes this reproducible. It is the date ADR-009 was accepted, and it has no other
// meaning -- what matters is that it never changes, because changing it changes the checked-in
// asset for no reason a reviewer could name.
const seed = 20260807

// SOFT_LIGHT LEAVES A SURFACE UNCHANGED WHERE THE BLEND LAYER IS MID-GREY, which is what makes
// this the correct centre and not a preference. The grain must add texture without shifting the
// ladder: a tile whose mean sat above 128 would lighten every surface in the app by a constant,
// and the hand-tuned near-black steps ADR-009 D3 argues for would all move together.
const centre = 128

// sigma is the noise's spread in 8-bit units. At 4% opacity (--p-grain) this reads as
// microstructure at arm's length and as nothing at all from further away, which is the point:
// grain is material, not an effect.
const sigma = 18.0

// The warm cast, per channel, applied to the neutral centre. It is small on purpose -- ADR-009
// calls the tile "warm-neutral", and a tile with real chroma would tint every surface it lies over
// rather than giving it a texture. R above G above B is the whole of "warm".
//
// GREEN IS LEFT AT THE CENTRE AND THE OTHER TWO MOVE AROUND IT, which is arithmetic rather than
// taste: green carries 71.5% of luminance, so tinting it by even one unit shifts the tile's mean
// LUMA by about the same amount, and a tile whose luma sits above the soft-light neutral lightens
// every surface in the app by a constant. This way the cast is chroma only.
//
// WHAT 4% MAKES OF IT, said plainly so nobody reads more into these numbers than they carry. At
// --p-grain the effective chroma this contributes is about a quarter of an 8-bit unit: invisible.
// The claim "warm-neutral" is therefore mostly a claim about what the tile must NOT be -- cool --
// because a cool grain over a warm ladder is the same contamination the linen key-light exists to
// avoid, and it would be equally invisible in a diff.
var tint = [3]float64{+4, 0, -6}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/render-grain.go <out.png>")
		os.Exit(2)
	}

	// A DETERMINISTIC SOURCE AND NOT THE GLOBAL ONE. `rand.NormFloat64()` off the package-level
	// source is seeded differently on every run since Go 1.20, so the obvious spelling would emit
	// a different tile each time and the checked-in file would be one sample of many.
	rng := rand.New(rand.NewSource(seed))

	img := image.NewNRGBA(image.Rect(0, 0, tile, tile))
	for y := 0; y < tile; y++ {
		for x := 0; x < tile; x++ {
			// ONE DEVIATION PER PIXEL, SHARED ACROSS THE THREE CHANNELS. Drawing three would make
			// the noise CHROMATIC -- coloured speckle over a near-black ground, which reads as
			// sensor noise rather than as material. The tint below is the only chroma there is.
			d := rng.NormFloat64() * sigma
			img.SetNRGBA(x, y, color.NRGBA{
				R: clamp(centre + d + tint[0]),
				G: clamp(centre + d + tint[1]),
				B: clamp(centre + d + tint[2]),
				A: 0xFF,
			})
		}
	}

	out, err := os.Create(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// BestCompression, so the checked-in bytes are as small as the format allows and a re-render
	// with the same seed produces the same file.
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(out, img); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := out.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func clamp(v float64) uint8 {
	return uint8(math.Min(255, math.Max(0, math.Round(v))))
}
