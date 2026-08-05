package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-0qe7's wiring: **the session detail's stale
// mark must come from the SNAPSHOT the card is drawn from.**
//
// WHAT THE KOTLIN SUITES ALREADY SAY. That the mark is the snapshot's own fact and not the
// journal's verdict is `SessionDetailPanelTest`'s; that it is drawn beside the card rather than
// written into the grid is `SessionDetailViewTest`'s. Both are models and views this JVM can build.
//
// WHAT THEY CANNOT SEE is where the fact comes from on a real handset. `PhoneSurface.detailPanel`
// reads `FacadeBridge.terminalPeek` for the grid and drops `TerminalPeek.stale` on the floor, and
// that read sits past `PhoneStartup.Ready` -- unreachable under Robolectric, because the phone core
// is a gomobile AAR of .so files cross-compiled for Android ABIs. A model that decides the right
// thing over a fact nothing supplies is the defect ADR-007 B83(3) is the record of, and this issue
// is another instance of it: `TerminalPeek.stale` has been on the wire and read by the peek all
// along.

import (
	"strings"
	"testing"
)

// TestT0QE7_TheSessionDetailReadsTheSnapshotsOwnStaleness.
func TestT0QE7_TheSessionDetailReadsTheSnapshotsOwnStaleness(t *testing.T) {
	code := d0b8Code(t, d0b8PhoneSurface)
	body := d0b8FunctionBody(t, code, "detailPanel", "PhoneSurface.kt")

	if !strings.Contains(body, "snapshotStale") {
		t.Errorf("agents-tracker-0qe7: detailPanel builds its SessionDetail without snapshotStale:\n%s\n"+
			"The card prints a grid the machine may have stopped sending frames for, and a "+
			"terminal is the one surface a user reads AS live. The screen's only stale mark is "+
			"the journal's verdict, which is a different fact with a different remedy",
			strings.TrimSpace(body))
	}

	// AND IT IS THE PEEK'S OWN FACT. `terminalPeek` is already read here for the grid, and its
	// `stale` is the daemon's answer about that same frame; anything else derived on this side
	// would be a second opinion to keep in step with the first.
	if !strings.Contains(body, "grid.stale") {
		t.Errorf("agents-tracker-0qe7: detailPanel's snapshot staleness is not the peek's:\n%s\n"+
			"`TerminalPeek.stale` rides the same read the grid does, and a phone that decided "+
			"staleness some other way would be guessing at a fact the reply already carries",
			strings.TrimSpace(body))
	}
}
