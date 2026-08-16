package gate

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-4 review fix-pack (bead
// agents-tracker-hggx.6), surface half of MEDIUM 3 (D3 class, narrowed by round 3 and
// NOT closed).
//
// Round 3 made the launch claim one-shot, which removed the FOREVER swallow. The
// window it left is the whole in-flight life of any launch: `renderPresetFlow` writes
// the fetch refusal to `presetDelivery` first, then the launch block overwrites the
// SAME field unconditionally while `presetOp` is non-empty, clearing the claim only on
// a NON-PENDING outcome. So during every pending launch, every fetch refusal is
// replaced by "The launch is on its way to the machine and has not resolved yet."
//
// The ROOT CAUSE is one slot for two verbs. The fix gives the fetch verb its own slot
// end to end -- its own surface field, its own panel field, its own drawn line -- so
// neither verb's answer can be a function of the other verb's state.
//
// Source-level pins (the gate idiom for Surface behaviour the JVM suite cannot reach;
// the copy/model half is LaunchPresetRound4Test.kt).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func r5Round4Read(t *testing.T, rel ...string) string {
	t.Helper()
	parts := append([]string{appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone"}, rel...)
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("%v unreadable: %v", rel, err)
	}
	return string(raw)
}

// TestR5Round4_FetchVerbHasItsOwnSurfaceSlot: PhoneSurface holds the fetch verb's
// answer in a field of its own, so the launch block cannot reach it.
func TestR5Round4_FetchVerbHasItsOwnSurfaceSlot(t *testing.T) {
	src := r5Round4Read(t, "PhoneSurface.kt")
	if !strings.Contains(src, "presetFetchDelivery") {
		t.Fatal("PhoneSurface.kt has no dedicated fetch-notice field: the fetch refusal and the " +
			"launch delivery still share one slot, so every fetch refusal is overwritten for the " +
			"whole in-flight window of any launch (D3: a primary control refusing in silence)")
	}
	// The fetch block must write ONLY its own slot.
	if regexp.MustCompile(`presetDelivery = LaunchPresetScreen\.fetchNoticeFor`).MatchString(src) {
		t.Error("the fetch verb still writes the LAUNCH verb's delivery slot; the shared line is " +
			"the root cause, and the launch block overwrites it on the very next statement")
	}
	if !regexp.MustCompile(`presetFetchDelivery = LaunchPresetScreen\.fetchNoticeFor`).MatchString(src) {
		t.Error("the fetch verb's refusal is not written to the fetch verb's own slot")
	}
}

// TestR5Round4_FetchNoticeIsDrawnAsItsOwnLine: a slot nothing draws is the same silence
// with more fields. The panel carries it and the view gives it its own tagged line.
func TestR5Round4_FetchNoticeIsDrawnAsItsOwnLine(t *testing.T) {
	screen := r5Round4Read(t, "ui", "screens", "LaunchPresetScreen.kt")
	// `val fetchNotice`, not a bare substring: `fetchNoticeFor` (the round-3 copy
	// function) already lives in this file and would satisfy a loose match.
	if !regexp.MustCompile(`val fetchNotice\b`).MatchString(screen) {
		t.Fatal("LaunchPresetPanel has no fetchNotice field: the drawn panel still has one slot " +
			"for two verbs")
	}
	view := r5Round4Read(t, "ui", "screens", "LaunchPresetView.kt")
	if !strings.Contains(view, "FETCH_DELIVERY") {
		t.Error("LaunchPresetTag has no FETCH_DELIVERY tag: the fetch verb's answer has no line " +
			"of its own on screen and cannot be asserted by any UI test")
	}
	if !strings.Contains(view, "panel.fetchNotice") {
		t.Error("launchPresetView never draws panel.fetchNotice; a refusal held in a field and " +
			"drawn by nobody is the silence this finding is about")
	}
}

// TestR5Round4_SurfaceFeedsTheFetchSlotIntoThePanel: the field and the drawn panel are
// connected, or the two halves above are each individually satisfied and jointly inert.
func TestR5Round4_SurfaceFeedsTheFetchSlotIntoThePanel(t *testing.T) {
	src := r5Round4Read(t, "PhoneSurface.kt")
	if !regexp.MustCompile(`fetchNotice\s*=\s*presetFetchDelivery`).MatchString(src) {
		t.Error("presetPanelOnScreen does not hand the fetch slot to the panel; the surface holds " +
			"the fetch refusal and the screen never receives it")
	}
}
