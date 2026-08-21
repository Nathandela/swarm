package gate

// WAVE R8 / CLOSING ROUND 2 -- THE BLANK MUST REACH THE LAPSE DECISION
// (closing review, finding 6, second pass).
//
// THE DEFECT. The machine BLANKS the phone's copy when it reaps a watch (round 3) and the phone
// RE-WATCHES when its screen has lapsed (round 4) -- and the round-4 detector asked only about
// the snapshot's AGE, while the blank carries no render time at all. `ageOf(0) = 0` and an
// age-only rule reads zero as "nothing to say", so the detector answered NOT LAPSED for the one
// frame that proves the watch is over. The user sat on a permanently blank terminal.
//
// WHAT THIS FENCE IS, AND WHAT IT IS NOT. The RULE is driven by a real unit test --
// `TerminalFallbackWatchTest` calls `TerminalFallbackBinding.watchLapsed(grid)` with the exact
// values the machine sends and asserts the answer. What no unit test on this side can reach is
// the WIRING: `PhoneSurface.heldWatchLapsed` needs a live `App` (libgojni) to build a binding, so
// which question the surface asks is only visible in the source. That, and only that, is what is
// scanned here -- the surface must ask about the GRID and not about an age it extracted from it.
//
// A rename does not get past it, because the scan is anchored on the surface's own call and on
// the predicate's body rather than on a verb list: a `watchLapsed` that stopped reading the blank
// would fail the unit test, and a surface that stopped passing the grid fails here.

import (
	"regexp"
	"strings"
	"testing"
)

// TestR8R5Gate_TheHeldWatchIsJudgedOnTheWholeGridAndNotOnAnAgeAlone.
func TestR8R5Gate_TheHeldWatchIsJudgedOnTheWholeGridAndNotOnAnAgeAlone(t *testing.T) {
	surface := kotlinCodeOnly(r8AllProductionKotlin(t)[r8PhoneSurface])
	if surface == "" {
		t.Fatalf("%s does not exist", r8PhoneSurface)
	}
	held := kotlinMember(t, surface, "private fun heldWatchLapsed(")
	if held == "" {
		t.Fatalf("%s no longer declares heldWatchLapsed, which is the surface's whole reader of the "+
			"lapse rule", r8PhoneSurface)
	}
	if regexp.MustCompile(`watchLapsed\s*\([^\n]*\.ageMs`).MatchString(held) {
		t.Errorf("ADR-017 T4-b: heldWatchLapsed in %s asks the lapse rule about `.ageMs`. The machine's "+
			"BLANK -- what a reaped watch leaves on the phone -- carries NO render time, and an "+
			"unknown age reads as `nothing to say`, so an age-only question answers NOT LAPSED for "+
			"exactly the frame that says the watch is over: the screen then renews into nothing "+
			"forever and the user reads a blank terminal.", r8PhoneSurface)
	}
	if !regexp.MustCompile(`watchLapsed\s*\(\s*[A-Za-z_][A-Za-z0-9_]*\.grid\(\)\s*\)`).MatchString(held) {
		t.Errorf("ADR-017 T4-b: heldWatchLapsed in %s does not hand the lapse rule the GRID the "+
			"binding holds. Both evidences live there -- the machine SAYING it stopped (a blank with "+
			"no geometry) and the phone INFERRING it from a screen older than the horizon -- and a "+
			"caller that extracts one of them decides which harms are visible.", r8PhoneSurface)
	}

	screen := kotlinCodeOnly(r8AllProductionKotlin(t)[r8FallbackScreen])
	pred := kotlinMember(t, screen, "fun watchLapsed(grid: TerminalGrid")
	if pred == "" {
		t.Fatalf("%s declares no `watchLapsed(grid: TerminalGrid ...)`; the grid-shaped question is "+
			"the one the surface must be able to ask", r8FallbackScreen)
	}
	if !strings.Contains(pred, "machineStoppedRendering") {
		t.Errorf("ADR-017 T4-b: watchLapsed(grid) in %s never consults the blank, so it is the "+
			"age-only rule with a wider signature.", r8FallbackScreen)
	}
	// AND THE BLANK PREDICATE MUST READ THE MACHINE'S FRAME. `machineStoppedRendering` is what
	// tells a reaped screen from a live one, and it does that on the GEOMETRY: a live view always
	// carries the cols and rows its emulator resolved (fenced machine-side by
	// internal/remotegw/r8r5_reapblank_test.go), while a zero render time is also what a machine
	// predating the closing round sends.
	blank := kotlinMember(t, screen, "val machineStoppedRendering")
	if blank == "" || !strings.Contains(blank, "gridRows") {
		t.Errorf("ADR-017 T4-b: %s does not derive `machineStoppedRendering` from the grid's "+
			"GEOMETRY. Age cannot carry it: the blank has no render time, and neither has a machine "+
			"that predates the closing round.", r8FallbackScreen)
	}
}
