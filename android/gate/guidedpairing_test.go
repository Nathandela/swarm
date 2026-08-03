package gate

// FAILING-FIRST (TDD RED, GG-5) for the GUIDED pairing screen (agents-tracker-qx9m, second half).
//
// WHAT THE OWNER FOUND. They installed the internal-testing build, found the pairing screen, and it
// gave them a bare text field with no camera and no instructions. The camera half of that is
// fenced by `PairingPanelScreenTest` -- the scan control was offered only where the permission was
// already granted, and the app's only `requestPermissions(CAMERA)` is that control's own listener.
// This file is about the OTHER half: a person holding an unpaired phone was expected to already
// know that a computer has to run `swarm remote pair` first.
//
// WHY A GO GATE AND NOT ONLY A KOTLIN TEST. The Kotlin suites assert the model and the composition,
// and they are the right tests for both. What neither can durably assert is the JOIN between them:
// `PairingPanel.controls` is a set of enum values and `PairingSlots.controls` is a map the surface
// builds, and the composition resolves one against the other with `requireNotNull`. That crash is
// reachable only in the step that offers the missing control -- so a control added to the enum and
// to a step, and forgotten in the surface, type-checks, renders every other step correctly, and
// takes the screen down at the moment it is needed. On this screen the controls in question are
// the two SAS answers, which after ADR-007 B133 are the only human-in-the-loop security check left
// in the product.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. Every scan starts at the app module, so it cannot descend
// into `.claude/worktrees/`, which holds other agents' full checkouts and has already made four
// gates in this repository report findings about somebody else's private copy as findings about
// this tree.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	guidedPanelFile   = "dev/swarm/phone/ui/screens/PairingPanel.kt"
	guidedViewFile    = "dev/swarm/phone/ui/screens/PairingPanelView.kt"
	guidedSurfaceFile = "dev/swarm/phone/PairingSurface.kt"
)

// guidedControlValue matches one `PairingControl` enum constant: a bare SCREAMING_SNAKE name on its
// own line inside the enum body. Anchored on the line so a mention inside a comment or a `when`
// arm cannot be read as a declaration -- comments are stripped before this runs, and an arm has a
// `->` after it.
var guidedControlValue = regexp.MustCompile(`(?m)^\s{4}([A-Z][A-Z0-9_]*)\s*,\s*$`)

// guidedSlotKey matches one entry in the surface's slot map: `PairingControl.SCAN to startScan`.
var guidedSlotKey = regexp.MustCompile(`PairingControl\.([A-Z][A-Z0-9_]*)\s+to\s`)

func guidedSource(t *testing.T, rel string) string {
	t.Helper()
	return readFileOrFail(t, filepath.Join(kotlinMainRoot(t), filepath.FromSlash(rel)),
		"agents-tracker-qx9m")
}

// guidedControls reads the PairingControl enum body out of PairingPanel.kt.
//
// IT READS THE BODY AND NOT THE WHOLE FILE, because the file also declares `PairingPanel`'s
// properties and a `PairingGuidance` data class, and a scan over the file would take any
// capitalised constant it found for a control.
func guidedControls(t *testing.T) []string {
	t.Helper()
	code := kotlinCodeOnly(guidedSource(t, guidedPanelFile))
	start := strings.Index(code, "enum class PairingControl {")
	if start < 0 {
		t.Fatalf("agents-tracker-qx9m: %s declares no `enum class PairingControl`, so the join "+
			"below has no subject and would pass over an empty set", guidedPanelFile)
	}
	body := code[start:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	}
	var out []string
	for _, m := range guidedControlValue.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatalf("agents-tracker-qx9m: no enum constant was read out of PairingControl in %s. The "+
			"reader is broken, and every assertion below would report a screen whose controls all "+
			"resolve", guidedPanelFile)
	}
	return out
}

// guidedSlots reads the controls a surface source supplies views for.
//
// It takes the SOURCE rather than reading the file itself so the negative control below can feed a
// perturbed one to the same function the real assertion calls. A control that rebuilds the
// comparison inline proves something about the copy and nothing about the assertion; this package
// has shipped that mistake before.
func guidedSlots(surface string) map[string]bool {
	supplied := map[string]bool{}
	for _, m := range guidedSlotKey.FindAllStringSubmatch(kotlinCodeOnly(surface), -1) {
		supplied[m[1]] = true
	}
	return supplied
}

// guidedUnsuppliedControls reports every control the panel can offer that the surface cannot draw.
func guidedUnsuppliedControls(controls []string, supplied map[string]bool) []string {
	var missing []string
	for _, control := range controls {
		if !supplied[control] {
			missing = append(missing, control)
		}
	}
	return missing
}

// TestGuidedPairing_EveryControlThePanelOffersIsOneTheSurfaceCanPerform is the join described in
// the file comment: the enum is what a step may offer, the surface's map is what can actually be
// put on screen, and `pairingPanelView` throws when a step offers something the map has no view
// for.
func TestGuidedPairing_EveryControlThePanelOffersIsOneTheSurfaceCanPerform(t *testing.T) {
	surface := guidedSource(t, guidedSurfaceFile)

	supplied := guidedSlots(surface)
	if len(supplied) == 0 {
		t.Fatalf("agents-tracker-qx9m: %s supplies no `PairingControl.X to` slot at all, so the "+
			"comparison below would report every control missing OR, if the enum were also empty, "+
			"report a screen that cannot draw a single control as correct", guidedSurfaceFile)
	}

	for _, control := range guidedUnsuppliedControls(guidedControls(t), supplied) {
		t.Errorf("agents-tracker-qx9m: PairingControl.%s is a control the panel can offer and %s "+
			"supplies no view for it. `pairingPanelView` resolves the two with requireNotNull, so "+
			"this does not render a shorter screen -- it throws, in the one step that offers the "+
			"control, which is how a control added to a step and forgotten in the surface reaches "+
			"a handset.", control, guidedSurfaceFile)
	}

	// THE NEGATIVE CONTROL, through the same two functions the assertion above calls. A slot
	// removed from the real source has to move the answer, or the loop is comparing the surface
	// with itself. `REVEAL_TYPED_PAYLOAD` is the subject because it is the control this slice
	// added -- the failure mode being fenced is a NEW control wired into a step and forgotten in
	// the surface, which is exactly what perturbing this one reproduces.
	const dropped = "REVEAL_TYPED_PAYLOAD"
	if !supplied[dropped] {
		t.Fatalf("agents-tracker-qx9m: the surface supplies no %s slot, so the control below "+
			"perturbs nothing", dropped)
	}
	crippled := strings.Replace(surface, "PairingControl."+dropped+" to", "// removed", 1)
	if crippled == surface {
		t.Fatalf("agents-tracker-qx9m: the perturbation changed nothing, so the control below " +
			"says nothing about the reader")
	}
	if missing := guidedUnsuppliedControls([]string{dropped}, guidedSlots(crippled)); len(missing) == 0 {
		t.Errorf("agents-tracker-qx9m: the slot reader still finds %s in a source that no longer "+
			"supplies it, so the assertion above would pass over a screen that throws the moment "+
			"the step offering that control is reached", dropped)
	}
}

// TestGuidedPairing_TheSurfaceSuppliesNoControlTheEnumDoesNotName is the reverse direction, and it
// is what keeps the forward one honest: a slot for a control nobody can offer is dead weight that
// makes the map look complete while the enum drifts past it.
func TestGuidedPairing_TheSurfaceSuppliesNoControlTheEnumDoesNotName(t *testing.T) {
	declared := map[string]bool{}
	for _, c := range guidedControls(t) {
		declared[c] = true
	}

	surface := kotlinCodeOnly(guidedSource(t, guidedSurfaceFile))
	for _, m := range guidedSlotKey.FindAllStringSubmatch(surface, -1) {
		if !declared[m[1]] {
			t.Errorf("agents-tracker-qx9m: %s supplies a view for PairingControl.%s, which "+
				"%s does not declare", guidedSurfaceFile, m[1], guidedPanelFile)
		}
	}
}

// TestGuidedPairing_TheCommandIsWrittenDownOnce fences the one string on this screen whose
// correctness is not a matter of taste.
//
// A PERSON RETYPES IT INTO A SHELL. A second spelling is a second thing that can be wrong, and the
// failure it produces is `command not found` on the machine the user was told to run it on -- with
// the phone still showing them the instruction they followed. The screen model is where copy lives
// (PB-DS-9), so the literal belongs there and the view takes it as data.
func TestGuidedPairing_TheCommandIsWrittenDownOnce(t *testing.T) {
	const command = `"swarm remote pair"`

	panel := guidedSource(t, guidedPanelFile)
	if !strings.Contains(kotlinCodeOnly(panel), command) {
		t.Fatalf("agents-tracker-qx9m: %s does not declare the pairing command %s. The screen the "+
			"owner opened had no instructions at all; the command is the instruction.",
			guidedPanelFile, command)
	}

	for _, rel := range []string{guidedViewFile, guidedSurfaceFile} {
		if strings.Contains(kotlinCodeOnly(guidedSource(t, rel)), command) {
			t.Errorf("agents-tracker-qx9m: %s spells the pairing command itself. Copy belongs to "+
				"the screen model and exists once (PB-DS-9) -- a second literal is a second thing "+
				"that can be a typo, and this one is typed into a shell by a person reading it "+
				"off a phone.", rel)
		}
	}
}

// TestGuidedPairing_TheGuidedCopyIsTheScreenModels is the general form of the assertion above:
// the composition may name no user-visible sentence of its own.
//
// It is scoped to the STEP copy rather than to every string, because the view legitimately carries
// non-copy strings -- the `PairingTag` values are tags, and the requireNotNull message is a
// developer diagnostic that never reaches a screen.
func TestGuidedPairing_TheGuidedCopyIsTheScreenModels(t *testing.T) {
	view := kotlinCodeOnly(guidedSource(t, guidedViewFile))

	for _, sentence := range []string{"On your computer", "It shows a QR code", "Scan QR code",
		"Enter code instead"} {
		if strings.Contains(view, sentence) {
			t.Errorf("agents-tracker-qx9m: %s writes %q inline. PB-DS-9 puts copy on the screen "+
				"model so a suite asserting what is drawn cannot agree with a re-worded copy of "+
				"itself.", guidedViewFile, sentence)
		}
	}
}
