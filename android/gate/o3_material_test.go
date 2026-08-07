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
