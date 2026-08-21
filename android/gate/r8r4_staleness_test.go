package gate

// WAVE R8 / CLOSING ROUND -- THE FROZEN SCREEN (closing review, finding 6).
//
// THE FINDING, as what the user sees. `PhoneSurface.reconcileTerminalWatch` opened a watch
// ONLY when the DISPLAYED SESSION CHANGED; for the same session it always took the
// `renewHeldWatch()` early return, and `TerminalWatcher.Renew` is a documented no-op for a
// session with NO LIVE WATCH. So ONE lapsed horizon -- 60 s of machine-side wall against a
// 20 s tick, three missed ticks on a descheduled UI thread or a short offline window -- ended
// the stream PERMANENTLY for that screen, and the phone renewed into nothing forever.
//
// AND BOTH COMPENSATING SIGNALS WERE INERT. `grid()` hardcoded `ageMs = 0` because
// `rendered_at` was not on the wire, so the screen asserted freshness about an arbitrarily old
// terminal; and `streamStale` is a SEQUENCE-GAP flag, which by construction does not fire when
// a machine simply STOPS SENDING. A screen that silently freezes is the worst failure mode
// this surface has, because the user acts on what they see.
//
// Finding 5 put `rendered_at` on the wire, which is what makes an honest age possible, so the
// two are fixed together and fenced together.

import (
	"regexp"
	"strings"
	"testing"
)

// TestR8R4Gate_TheGridDerivesItsAgeFromTheMachinesRenderTime is the staleness half.
//
// It bans the LITERAL ZERO as well as requiring the read, because "ageMs = 0L" is exactly what
// was there: a field the screen's staleness sentence is computed from, hardcoded to the value
// that means FRESH.
func TestR8R4Gate_TheGridDerivesItsAgeFromTheMachinesRenderTime(t *testing.T) {
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[r8FallbackScreen])
	if code == "" {
		t.Fatalf("%s does not exist", r8FallbackScreen)
	}
	grid := kotlinMember(t, code, "fun grid()")
	if !strings.Contains(grid, "renderedAtMillis") {
		t.Errorf("ADR-017 T4-b: grid() in %s never reads the machine's render time, so the age it "+
			"reports is not derived from when the screen was RENDERED. Arrival time is not a "+
			"substitute -- a replayed backlog arrives all at once and a held relay delivers old "+
			"content at a new instant -- and a hardcoded zero asserts freshness about an "+
			"arbitrarily old terminal.", r8FallbackScreen)
	}
	if regexp.MustCompile(`ageMs\s*=\s*0L\s*,`).MatchString(grid) {
		t.Errorf("ADR-017 T4-b: grid() in %s still passes `ageMs = 0L` for a REAL grid. Zero is the "+
			"value that means `rendered just now`; a screen that says it about a frozen terminal is "+
			"the exact lie T4-b names.", r8FallbackScreen)
	}
}

// TestR8R4Gate_ALapsedWatchIsReEstablishedAndNotMerelyRenewed is the re-watch half.
//
// The assertion is written over reconcileTerminalWatch's SAME-SESSION branch, because that is
// the branch the defect lives in: `current == session` unconditionally renewed, and a renewal
// into a reaped watch is a no-op forever.
func TestR8R4Gate_ALapsedWatchIsReEstablishedAndNotMerelyRenewed(t *testing.T) {
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[r8PhoneSurface])
	if code == "" {
		t.Fatalf("%s does not exist", r8PhoneSurface)
	}
	body := kotlinMember(t, code, "private fun reconcileTerminalWatch(")
	same := regexp.MustCompile(`current\s*!=\s*null\s*&&\s*current\s*==\s*session`)
	if !same.MatchString(body) {
		t.Fatalf("reconcileTerminalWatch in %s no longer has a same-session branch this fence can "+
			"read; the scan looks for `current != null && current == session`.", r8PhoneSurface)
	}
	// ONE HOP IS ALLOWED -- `heldWatchLapsed()` is the surface's own reader of the predicate --
	// but the LAPSE must be consulted in this branch and not somewhere a redraw may not reach.
	if !regexp.MustCompile(`[Ww]atchLapsed\s*\(`).MatchString(body) {
		t.Errorf("ADR-017 T4-b: reconcileTerminalWatch in %s renews the held watch for the same "+
			"session UNCONDITIONALLY. `TerminalWatcher.Renew` is a documented no-op for a session "+
			"with no live watch, so one lapsed horizon ends the stream permanently for that screen "+
			"and the phone renews into nothing forever. The lapse has to be DETECTED and the watch "+
			"RE-ESTABLISHED; renewing harder is not a fix.", r8PhoneSurface)
	}
	// AND THE PREDICATE MUST BE A REAL ONE. A `watchLapsed` that answers false always is the
	// same defect with a longer name.
	screen := kotlinCodeOnly(r8AllProductionKotlin(t)[r8FallbackScreen])
	if !regexp.MustCompile(`fun\s+watchLapsed\s*\(`).MatchString(screen) {
		t.Fatalf("ADR-017 T4-b: %s declares no `watchLapsed`. The lapse predicate belongs beside the "+
			"binding that holds the watch, where it is one pure function a unit test can drive.",
			r8FallbackScreen)
	}
	pred := kotlinMember(t, screen, "fun watchLapsed(")
	if !strings.Contains(pred, "horizon") {
		t.Errorf("ADR-017 T4-b: watchLapsed in %s does not read a horizon, so it cannot be answering "+
			"the question the machine's reap wall poses.", r8FallbackScreen)
	}
	if regexp.MustCompile(`=\s*false\s*$`).MatchString(strings.TrimSpace(pred)) {
		t.Errorf("ADR-017 T4-b: watchLapsed in %s is constantly false", r8FallbackScreen)
	}
}
