package gate

// FAILING-FIRST (TDD RED, GG-5) for ADR-009 D4 phase O3 -- the material kit.
//
// WHAT THIS FILE FENCES THAT NOTHING ELSE DOES. `s16_tokens_test.go` makes the token join
// bidirectional over the FILES: every colour token has a TSV row, every `<color>` has a row, and
// the values agree. Nothing anywhere asks whether a colour that reached `colors.xml` is ever
// DRAWN. That direction is not a nicety -- ADR-009 D3 added two colour tokens whose entire purpose
// is one surface (`--p-sheet-hi` and `--p-sheet-lo`, the approval sheet's gradient stops), and the
// existing gates went green with both of them arriving in the resource table and reaching no
// component at all. A palette entry nothing paints is "single origin" decaying in the other
// direction: the join says the app has the colour, and the app does not have it.
//
// WHAT THIS FILE DOES NOT CLAIM, said plainly rather than left to be discovered. It asks whether a
// colour is SPENT BY SOMETHING THAT DRAWS -- a kit factory, a theme attribute, an icon layer. It
// cannot ask whether that thing is on screen. `sheetSurface` spends both gradient stops and the
// approval sheet it paints is composed by no screen yet (migration plan, O6); this gate is green
// over that, and it is right to be, because the alternative -- a gate that pretends to check
// composition by reading a resource reference -- is the shape of coverage that is not coverage.
// `TriageInboxViewTest` and `s24_screens_test.go` are where composition is asserted.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. Every scan starts at the app module, so it cannot
// descend into `.claude/worktrees/`, which holds other agents' full checkouts and has already made
// four gates in this repository report findings about somebody else's private copy.

import (
	"bytes"
	"fmt"
	"image"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// o3ColourDeclaration is one `<color name="swarm_x">` in the values table.
var o3ColourDeclaration = regexp.MustCompile(`<color\s+name="([A-Za-z0-9_]+)"`)

// o3KotlinColourSpend is `R.color.swarm_x` in Kotlin.
var o3KotlinColourSpend = regexp.MustCompile(`\bR\.color\.([A-Za-z0-9_]+)\b`)

// o3XmlColourSpend is `@color/swarm_x` in any resource XML.
var o3XmlColourSpend = regexp.MustCompile(`@color/([A-Za-z0-9_]+)\b`)

// o3ColoursFile is the one file colours are DECLARED in. It is excluded from the spend scan for
// the obvious reason: a declaration is not a use, and reading it as one would make every colour
// spend itself and this gate say nothing at all.
const o3ColoursFile = "colors.xml"

func o3ResRoot(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "res")
}

// o3DeclaredColours returns every colour name the values table declares, sorted.
func o3DeclaredColours(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(o3ResRoot(t), "values", o3ColoursFile)
	var out []string
	for _, m := range o3ColourDeclaration.FindAllStringSubmatch(
		readFileOrFail(t, path, "ADR-009 D4"), -1,
	) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("ADR-009 D4: %s declares no <color> at all, so every assertion below would "+
			"iterate an empty set and pass", mustRel(t, path))
	}
	sort.Strings(out)
	return out
}

// o3ResourceXML returns every resource XML that could SPEND a colour: everything under res/ except
// the declaration file itself.
func o3ResourceXML(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(o3ResRoot(t), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".xml") {
			return nil
		}
		if filepath.Base(path) == o3ColoursFile {
			return nil
		}
		out = append(out, path)
		return nil
	})
	sort.Strings(out)
	return out
}

// o3SpentColours reads the corpus into the set of colour names something in it draws with.
//
// THE CORPUS IS PASSED IN RATHER THAN READ HERE, which is what lets the negative control feed this
// the SAME function a perturbed copy of the real sources. A control that rebuilt the scan inline
// would prove the copy works and say nothing about the fence.
//
// KOTLIN IS READ AS CODE, comments stripped: a KDoc naming `R.color.swarm_sheet_gradient_top` in a
// sentence about what a future phase will do is not a component drawing with it, and a fence a
// comment can satisfy is one the next thorough comment turns off.
func o3SpentColours(kotlin, xml []string) map[string]bool {
	spent := map[string]bool{}
	for _, src := range kotlin {
		for _, m := range o3KotlinColourSpend.FindAllStringSubmatch(kotlinCodeOnly(src), -1) {
			spent[m[1]] = true
		}
	}
	for _, src := range xml {
		for _, m := range o3XmlColourSpend.FindAllStringSubmatch(src, -1) {
			spent[m[1]] = true
		}
	}
	return spent
}

// o3UnspentColours is the fault list: the declared colours nothing in the corpus draws with.
func o3UnspentColours(declared []string, spent map[string]bool) []string {
	var out []string
	for _, name := range declared {
		if !spent[name] {
			out = append(out, name)
		}
	}
	return out
}

func o3Corpus(t *testing.T) (kotlin, xml []string) {
	t.Helper()
	for _, path := range kotlinFiles(t, kotlinMainRoot(t)) {
		kotlin = append(kotlin, readFileOrFail(t, path, "ADR-009 D4"))
	}
	for _, path := range o3ResourceXML(t) {
		xml = append(xml, readFileOrFail(t, path, "ADR-009 D4"))
	}
	if len(kotlin) == 0 || len(xml) == 0 {
		t.Fatalf("ADR-009 D4: the spend corpus is empty (%d Kotlin sources, %d resource XML), so "+
			"every colour would report as unspent for a reason that has nothing to do with the app",
			len(kotlin), len(xml))
	}
	return kotlin, xml
}

// TestPBDS5_EveryColourResourceIsSpentBySomethingThatDraws closes the token join's open direction.
func TestPBDS5_EveryColourResourceIsSpentBySomethingThatDraws(t *testing.T) {
	declared := o3DeclaredColours(t)
	kotlin, xml := o3Corpus(t)

	for _, name := range o3UnspentColours(declared, o3SpentColours(kotlin, xml)) {
		t.Errorf("ADR-009 D4: <color name=%q> is declared, joined to a design token, and drawn by "+
			"nothing. The existing gates are green over it -- they check that the value in the "+
			"resource table matches the origin, which it does -- so a colour can arrive in the "+
			"palette and reach no surface, and the join goes on reporting that the app has it. "+
			"Either a component must spend it, or it does not belong in the palette.", name)
	}
}

// TestPBDS5_TheColourSpendScanCanActuallyFail is the negative control PB-DS-10 requires, fed to
// the SAME functions the assertion above calls.
//
// THE PERTURBATION IS IN MEMORY AND NEVER ON DISK. A control that edited a source file to prove
// the fence works would leave the repository in whatever state the test process died in; this
// takes the real corpus, removes one real reference from the copy it holds, and asks the same
// scan what it now sees.
func TestPBDS5_TheColourSpendScanCanActuallyFail(t *testing.T) {
	declared := o3DeclaredColours(t)
	kotlin, xml := o3Corpus(t)

	// A colour spent from Kotlin, and one spent only from resource XML: the scan has two halves
	// and a control that exercised one would leave the other unproven.
	for _, probe := range []struct {
		name string
		what string
	}{
		{"swarm_hero", "Kotlin"},
		{"swarm_background", "resource XML"},
	} {
		if !o3SpentColours(kotlin, xml)[probe.name] {
			t.Fatalf("ADR-009 D4: the control's probe %q is not spent in the real corpus, so "+
				"removing its reference proves nothing about the scan", probe.name)
		}
		blinded := make([]string, len(kotlin))
		copy(blinded, kotlin)
		blindedXML := make([]string, len(xml))
		copy(blindedXML, xml)
		for i := range blinded {
			blinded[i] = strings.ReplaceAll(blinded[i], "R.color."+probe.name, "R.color.o3Probe")
		}
		for i := range blindedXML {
			blindedXML[i] = strings.ReplaceAll(blindedXML[i], "@color/"+probe.name, "@color/o3Probe")
		}

		faults := o3UnspentColours(declared, o3SpentColours(blinded, blindedXML))
		found := false
		for _, name := range faults {
			if name == probe.name {
				found = true
			}
		}
		if !found {
			t.Errorf("ADR-009 D4: with every %s reference to %q removed, the scan still reports "+
				"it spent (%v). The fence would not notice a colour that stopped being drawn.",
				probe.what, probe.name, faults)
		}
	}
}

// ---------------------------------------------------------------------------
// ADR-009 D4.3 / PB-DS-5: the grain, which is the requirement's other unmet count.
// ---------------------------------------------------------------------------

// o3GrainRasterRelPath is where the checked-in tile lives, relative to the app's res/.
//
// `drawable-nodpi` AND NOT `drawable`, which is the difference between a texture and a picture.
// A raster in a density-qualified folder is SCALED to the device -- a 140 px tile becomes 385 px
// on a 2.75x handset -- so the grain would be coarse on one phone and fine on another, and the
// design's "140x140 tile" would describe no device. `nodpi` is the qualifier that means "these are
// the pixels", which is what a noise tile is.
const o3GrainRasterRelPath = "drawable-nodpi/swarm_grain.png"

// o3GrainRow is the derivation table's cell for it. The tile's size is read from there rather than
// written here, for the reason every other number in this gate is read from somewhere.
const o3GrainRow = "#21 Grain overlay"

// o3ScaffoldRelPath is derivation row 20's screen scaffold, relative to the production Kotlin root.
const o3ScaffoldRelPath = "dev/swarm/phone/ui/screens/PhoneScaffoldView.kt"

// o3SoftLightNeutral is the source value at which SOFT_LIGHT leaves a backdrop unchanged.
//
// IT IS THE BLEND MODE'S OWN NUMBER AND NOT A CHOICE. The W3C/PDF soft-light formula returns the
// backdrop exactly where the source is 0.5, so a grain tile whose mean sits above 128 lightens
// every surface in the app by a constant -- and ADR-009 D3's near-black ladder is hand-tuned in
// steps of one or two 8-bit units, so a constant is not a rounding detail, it is the ladder moving.
const o3SoftLightNeutral = 128.0

// o3GrainMeanTolerance is how far the tile's mean LUMA may sit from that neutral, in 8-bit units.
//
// It is not zero because the warm cast is deliberate and because rounding a sampled distribution
// to whole units leaves a fraction behind. One and a half units at `--p-grain`'s 4% is under a
// twentieth of a unit on screen; one whole unit of ladder step is what this exists to protect.
const o3GrainMeanTolerance = 1.5

// o3GrainStats is what the checked-in tile actually is.
type o3GrainStats struct {
	Width, Height int
	MeanR         float64
	MeanG         float64
	MeanB         float64
	// StdDev of the green channel: whether this is noise at all, or a flat rectangle that would
	// pass every mean-based check and render nothing.
	StdDev float64
	// ChromaOffsets counts the distinct (R-G, G-B) pairs. ONE means every pixel carries the same
	// cast, which is what makes this a monochrome tile with a tint rather than colour speckle.
	ChromaOffsets int
}

// MeanLuma is the tile's mean under the sRGB luma weights -- which is what SOFT_LIGHT's lightening
// or darkening is perceived as, and why green's offset matters ten times more than blue's.
func (s o3GrainStats) MeanLuma() float64 {
	return 0.2126*s.MeanR + 0.7152*s.MeanG + 0.0722*s.MeanB
}

// o3ReadGrain measures the raster. It takes BYTES rather than a path so the checks below and their
// negative control drive the same code.
func o3ReadGrain(t *testing.T, raw []byte) o3GrainStats {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ADR-009 D4.3: the grain raster does not decode as an image: %v", err)
	}
	b := img.Bounds()
	stats := o3GrainStats{Width: b.Dx(), Height: b.Dy()}
	offsets := map[[2]int]bool{}
	var sumR, sumG, sumB, sumGG float64
	n := float64(b.Dx() * b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			// The 16-bit channels image/color returns, back down to the 8 bits the file holds.
			r16, g16, b16, _ := img.At(x, y).RGBA()
			r, g, bl := float64(r16>>8), float64(g16>>8), float64(b16>>8)
			sumR += r
			sumG += g
			sumB += bl
			sumGG += g * g
			offsets[[2]int{int(r - g), int(g - bl)}] = true
		}
	}
	stats.MeanR, stats.MeanG, stats.MeanB = sumR/n, sumG/n, sumB/n
	stats.StdDev = math.Sqrt(math.Max(0, sumGG/n-stats.MeanG*stats.MeanG))
	stats.ChromaOffsets = len(offsets)
	return stats
}

// o3GrainFaults is every way the tile is not the one ADR-009 D4.3 describes.
//
// @param tile the size derivation row 21 states, read from the row rather than written here.
func o3GrainFaults(s o3GrainStats, tile int) []string {
	var faults []string
	if s.Width != tile || s.Height != tile {
		faults = append(faults, fmt.Sprintf(
			"the tile is %dx%d and derivation row 21 states %dx%d", s.Width, s.Height, tile, tile))
	}
	if math.Abs(s.MeanLuma()-o3SoftLightNeutral) > o3GrainMeanTolerance {
		faults = append(faults, fmt.Sprintf(
			"the tile's mean luma is %.2f against SOFT_LIGHT's neutral %.0f. A grain that is not "+
				"centred does not add texture, it shifts every surface it lies over by a constant "+
				"-- and the near-black ladder is hand-tuned in single 8-bit units.",
			s.MeanLuma(), o3SoftLightNeutral))
	}
	if s.StdDev < 1 {
		faults = append(faults, fmt.Sprintf(
			"the tile's deviation is %.3f, so it is a flat rectangle rather than noise. It would "+
				"pass every mean-based check above and render nothing at all.", s.StdDev))
	}
	if s.MeanR <= s.MeanG || s.MeanG <= s.MeanB {
		faults = append(faults, fmt.Sprintf(
			"the tile's channel means are R %.2f, G %.2f, B %.2f, which is not warm. ADR-009 calls "+
				"it warm-neutral, and the load-bearing half of that is NOT COOL: a cool grain over "+
				"a warm ladder is the same contamination the linen key-light exists to avoid, and "+
				"it is equally invisible in a diff.", s.MeanR, s.MeanG, s.MeanB))
	}
	if s.ChromaOffsets != 1 {
		faults = append(faults, fmt.Sprintf(
			"the tile carries %d distinct channel offsets, so its noise is CHROMATIC. Coloured "+
				"speckle over a near-black ground reads as sensor noise rather than as material, "+
				"which is the register ADR-009 spends its whole material section avoiding.",
			s.ChromaOffsets))
	}
	return faults
}

// TestPBDS5_TheGrainRasterIsTheCheckedInWarmNeutralTile closes the first of PB-DS-5's two unmet
// counts: the phase-B status note's "no grain raster exists".
func TestPBDS5_TheGrainRasterIsTheCheckedInWarmNeutralTile(t *testing.T) {
	path := filepath.Join(o3ResRoot(t), filepath.FromSlash(o3GrainRasterRelPath))
	if !exists(path) {
		t.Fatalf("ADR-009 D4.3: %s does not exist. PB-DS-5 says the noise is pre-rendered once "+
			"and checked in BECAUSE feTurbulence output is implementation-defined; until the file "+
			"is there, the grain is a requirement with no artifact.", mustRel(t, path))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ADR-009 D4.3: %s is unreadable: %v", mustRel(t, path), err)
	}

	doc := readFileOrFail(t, filepath.Join(repoRoot(t), filepath.FromSlash(s23ComponentsDoc)), "PB-DS-5")
	row, ok := s23FindRow(doc, o3GrainRow)
	if !ok {
		t.Fatalf("ADR-009 D4.3: %s has no `%s` row, so the tile's size would be a number this "+
			"gate wrote down rather than one the design states", s23ComponentsDoc, o3GrainRow)
	}
	tile, err := s23DocMetric(row, "tile")
	if err != nil {
		t.Fatalf("ADR-009 D4.3: `%s` states no tile size: %v", o3GrainRow, err)
	}

	for _, fault := range o3GrainFaults(o3ReadGrain(t, raw), int(tile)) {
		t.Errorf("ADR-009 D4.3: %s: %s", mustRel(t, path), fault)
	}
}

// TestPBDS5_TheGrainReaderCanActuallyFail is the negative control, through the SAME function.
//
// Five synthetic tiles, each wrong in exactly one of the ways o3GrainFaults names, so a check that
// stopped recognising one is visible as a failure here rather than as a silence above. A sound
// tile is fed first: if THAT reports a fault, every perturbation below would "pass" for the wrong
// reason and the control would certify nothing.
func TestPBDS5_TheGrainReaderCanActuallyFail(t *testing.T) {
	sound := o3GrainStats{
		Width: 140, Height: 140, MeanR: 132, MeanG: 128, MeanB: 122, StdDev: 18, ChromaOffsets: 1,
	}
	if faults := o3GrainFaults(sound, 140); len(faults) != 0 {
		t.Fatalf("ADR-009 D4.3: the control's SOUND tile is reported faulty (%v), so every "+
			"perturbation below would report a fault for the wrong reason", faults)
	}
	for _, probe := range []struct {
		what  string
		apply func(o3GrainStats) o3GrainStats
	}{
		{"a tile of the wrong size", func(s o3GrainStats) o3GrainStats { s.Width = 128; return s }},
		{"a tile centred above the soft-light neutral", func(s o3GrainStats) o3GrainStats {
			s.MeanR, s.MeanG, s.MeanB = 142, 138, 132
			return s
		}},
		{"a flat tile that is not noise at all", func(s o3GrainStats) o3GrainStats {
			s.StdDev = 0
			return s
		}},
		{"a COOL tile", func(s o3GrainStats) o3GrainStats {
			s.MeanR, s.MeanB = 122, 132
			return s
		}},
		{"a chromatic tile", func(s o3GrainStats) o3GrainStats { s.ChromaOffsets = 900; return s }},
	} {
		if faults := o3GrainFaults(probe.apply(sound), 140); len(faults) == 0 {
			t.Errorf("ADR-009 D4.3: %s passes the grain check, so the check would not catch it "+
				"in the checked-in raster either", probe.what)
		}
	}
}

// o3GrainDrawableSpend is the raster reached from Kotlin.
var o3GrainDrawableSpend = regexp.MustCompile(`\bR\.drawable\.swarm_grain\b`)

// o3GrainFactoryCall is the kit's overlay, composed by a screen.
var o3GrainFactoryCall = regexp.MustCompile(`\bgrainOverlay\s*\(`)

// TestPBDS5_TheGrainOverlayReachesTheScaffold closes the second half: an asset nothing draws is
// the same silence as no asset.
//
// TWO HOPS, BECAUSE EITHER ALONE IS SATISFIED BY SOMETHING THAT RENDERS NOTHING. A kit that names
// the raster with no screen composing it is a drawable in a factory nobody calls; a screen calling
// a factory that never touches the raster is an overlay of nothing. What this CANNOT say is that
// the overlay is on TOP -- that is `PhoneScaffoldViewTest`'s, in Robolectric, where a foreground
// can be told from a background.
func TestPBDS5_TheGrainOverlayReachesTheScaffold(t *testing.T) {
	spends := false
	for _, path := range kotlinFiles(t, s23KitRoot(t)) {
		if o3GrainDrawableSpend.MatchString(kotlinCodeOnly(readFileOrFail(t, path, "PB-DS-5"))) {
			spends = true
		}
	}
	if !spends {
		t.Errorf("ADR-009 D4.3: nothing in %s names R.drawable.swarm_grain. Derivation row 21's "+
			"whole cell is that the noise is an ASSET rather than a colour; a checked-in asset the "+
			"kit never opens is the same silence as no asset at all.", s23KitPackageDir)
	}

	scaffold := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(o3ScaffoldRelPath))
	if !o3GrainFactoryCall.MatchString(kotlinCodeOnly(readFileOrFail(t, scaffold, "PB-DS-5"))) {
		t.Errorf("ADR-009 D4.3: %s composes no grain overlay. Derivation row 20 puts it over the "+
			"screen scaffold -- \"the grain overlay (row 21) sits above it, non-interactive\" -- "+
			"and the scaffold is the one host that covers all four destinations at once while "+
			"leaving the pair-only screen, which draws the QR tile row 6 exempts, alone.",
			mustRel(t, scaffold))
	}
}
