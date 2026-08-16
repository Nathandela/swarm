package gate

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-3 review fix-pack (bead
// agents-tracker-hggx.6), surface half of the D3 MAJOR (proven by code trace in
// review): PhoneSurface set `presetOp` when a launch was confirmed and NEVER cleared
// it, so every later render pass re-claimed the old launch outcome and overwrote
// `presetDelivery` -- including in the SAME pass where a refused FETCH PRESETS had
// just written its refusal. `App.Outcome` is a persistent map, not a one-shot, so the
// stale launch sentence won forever: a machine refusal of the fetch verb had NO
// surface at all. That is a silent primary control -- the exact defect class the bead
// names (D3), on the one verb whose refusal round 2 promised was visible.
//
// Source-level pins (the gate idiom for Surface behavior the JVM suite cannot reach;
// the copy half is LaunchPresetRound3Test.kt):
//
//   1. renderPresetFlow one-shots the launch claim: a LANDED (non-pending) outcome
//      clears `presetOp`, so a resolved launch stops overwriting later verbs' answers.
//   2. the fetch refusal renders through the fetch verb's OWN copy
//      (`LaunchPresetScreen.fetchNoticeFor`), not the launch verb's sentence.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func r5Round3Surface(t *testing.T) string {
	t.Helper()
	path := filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone", "PhoneSurface.kt")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PhoneSurface.kt unreadable: %v", err)
	}
	return string(raw)
}

// TestR5Round3_LaunchClaimIsOneShotSoItCannotSwallowALaterRefusal: once the launch
// outcome has landed, the surface must stop re-claiming it -- `presetOp` is cleared.
// Left latched, the launch's old sentence unconditionally overwrites whatever the
// fetch verb just wrote on the shared delivery line, every pass, forever.
func TestR5Round3_LaunchClaimIsOneShotSoItCannotSwallowALaterRefusal(t *testing.T) {
	src := r5Round3Surface(t)
	if !strings.Contains(src, `presetOp = ""`) {
		t.Error("PhoneSurface.kt never clears presetOp: the launch verb's resolved sentence is " +
			"re-rendered on every pass and unconditionally overwrites the fetch verb's refusal " +
			"on the shared delivery line -- the refused fetch is a silent primary control (D3)")
	}
}

// TestR5Round3_FetchRefusalRidesTheFetchVerbsOwnCopy: a wire refusal of the
// launch_presets read renders through fetchNoticeFor -- copy about the verb the user
// actually pressed -- never the launch verb's "not authorized to launch sessions".
func TestR5Round3_FetchRefusalRidesTheFetchVerbsOwnCopy(t *testing.T) {
	src := r5Round3Surface(t)
	if !strings.Contains(src, "fetchNoticeFor(") {
		t.Error("PhoneSurface.kt renders the fetch verb's wire refusal through the LAUNCH verb's " +
			"notice vocabulary (or not at all); the model's fetchNoticeFor is the fetch verb's " +
			"own copy and the surface must ride it")
	}

	model, err := os.ReadFile(filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm",
		"phone", "ui", "screens", "LaunchPresetScreen.kt"))
	if err != nil {
		t.Fatalf("LaunchPresetScreen.kt unreadable: %v", err)
	}
	if !strings.Contains(string(model), "fun fetchNoticeFor(") {
		t.Error("LaunchPresetScreen.kt has no fetchNoticeFor; the fetch verb's refusal copy must " +
			"live in the model where the JVM suite drives it (PB-DS-9), not interpolated in a Surface")
	}
}
