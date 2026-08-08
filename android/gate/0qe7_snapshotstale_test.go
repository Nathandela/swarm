package gate

// AMENDED AT SLICE I1's EXIT, and the amendment is the design's rather than this fence's.
//
// WHAT IT ASSERTED, STATED BEFORE IT IS CHANGED: that `PhoneSurface.detailPanel` built its
// `SessionDetail` with `snapshotStale`, and that the value was `grid.stale` -- the SNAPSHOT's own
// staleness, riding the `FacadeBridge.terminalPeek` read the grid came on, rather than a second
// opinion derived on this side. The defect it was written for was real: the session detail drew a
// daemon-rendered grid with only the JOURNAL's stale verdict beside it, which is a different fact
// about a different stream with a different remedy.
//
// WHY IT COULD NOT SURVIVE UNCHANGED. `docs/adr/ADR-009-structured-chat-interaction.md` (1) leaves
// "no terminal emulation and no raw grid anywhere in the app" and (3) dates the well's deletion to
// this slice; (2) stops the phone issuing a `terminal_watch`, so no snapshot frames are appended at
// all. There is no card, no grid and no snapshot for a staleness mark to be ABOUT. A fence over a
// deleted subject can only fail for the wrong reason -- and deleting it outright would drop the
// property it bought, which is that this screen's stale mark is read off the handle that carried
// the thing it marks and is never a second opinion.
//
// SO THE PROPERTY MOVES WITH THE SUBJECT. The screen's content is the interaction transcript now,
// `TranscriptPageView.stale` is the journal's PB-APP-8 verdict read off the handle the items came
// on (IS-LAYER-1/-4: an interaction item IS a journal record and inherits that repair channel), and
// what is asserted below is the same rule over the surviving read, plus the deletion itself.
//
// WHAT THE KOTLIN SUITES CANNOT SEE is unchanged, and it is why this lives in Go: the read sits
// past `PhoneStartup.Ready`, unreachable under Robolectric, because the phone core is a gomobile
// AAR of .so files cross-compiled for Android ABIs. A model that decides the right thing over a
// fact nothing supplies is the defect ADR-007 B83(3) is the record of.

import (
	"strings"
	"testing"
)

// TestT0QE7_TheSessionDetailReadsItsOwnPagesStaleness.
func TestT0QE7_TheSessionDetailReadsItsOwnPagesStaleness(t *testing.T) {
	code := d0b8Code(t, d0b8PhoneSurface)
	body := d0b8FunctionBody(t, code, "detailPanel", "PhoneSurface.kt")

	// THE GRID IS GONE AND MUST NOT COME BACK THROUGH THIS SEAM. `snapshotStale` is the field the
	// original defect was about; the model no longer has one, and a screen re-deriving it would be
	// re-deriving a fact about a surface this app does not draw.
	if strings.Contains(body, "snapshotStale") {
		t.Errorf("ADR-009 (1)/(3): detailPanel still carries a snapshot staleness:\n%s\n"+
			"The terminal well is deleted at slice I1's exit and no phone surface issues a "+
			"terminal_watch, so there is no snapshot on this screen for a mark to qualify",
			strings.TrimSpace(body))
	}

	// AND THE SURVIVING MARK IS THE PAGE'S OWN FACT, which is the property this fence buys. It
	// rides the same read the items came on -- `TranscriptPageView.stale`, the journal's PB-APP-8
	// verdict -- and a phone that decided staleness some other way would be guessing at a fact the
	// reply already carries.
	if !strings.Contains(body, ".stale") {
		t.Errorf("agents-tracker-0qe7: detailPanel builds its SessionDetail without a staleness "+
			"read off the page it drew from:\n%s\n"+
			"A transcript reads as a complete conversation, and a missing tool run or approval in "+
			"the middle of one is invisible unless the screen says the stream had a hole",
			strings.TrimSpace(body))
	}
	if !strings.Contains(body, "journalStale") {
		t.Errorf("agents-tracker-0qe7: detailPanel does not set `journalStale`:\n%s\n"+
			"PB-APP-8's verdict for this screen is the one thing that stops a holed conversation "+
			"being presented as the whole of what the agent did", strings.TrimSpace(body))
	}
}
