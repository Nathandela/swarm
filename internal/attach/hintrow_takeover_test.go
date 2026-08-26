package attach

// The reserved row's hint, after the takeover note was DELETED (conversation surface,
// Wave G; agents-tracker-dwwv.3.1, plan §9 G.1).
//
// WHAT THIS FILE USED TO PIN, AND WHAT BECAME OF EACH ASSERTION. The note said "took over
// from phone" and was sampled once, before the dial. Both halves were false: M0.1
// (docs/verification/mirror-m0.md) proved an owner attach evicts no phone -- two protocol
// Servers over one coreAPI, separate lease maps, one shared per-session tap, both live
// streams surviving in BOTH orders -- and a value sampled before the dial could not have
// remained true afterwards in any case. The file itself recorded the disproof and kept
// pinning the strings as "the current contract"; this is the authorized rewrite it named.
//
//	TestHintText_TakeoverNoteGoesLast              DELETED -- there is no takeover to order.
//	TestHintText_TakeoverStillTruncatesToTheRow    MOVED   -> TestHintText_TruncatesToTheRow.
//	TestHintText_NarrowRowKeepsTheEscapeAffordance MOVED   -> the same, plus the width bound.
//	TestHintText_NoNoteWithoutATakeover            MOVED   -> TestHintText_SaysNothingAboutAPhone,
//	                                                          strengthened from "not on this
//	                                                          attach" to "never".
//	TestChromeHint_PaintsTheTakeoverNote           MOVED   -> TestChromeHint_PaintsNoPhoneNote.
//
// NOTHING REPLACES IT ON THIS ROW YET, and that is deliberate rather than unfinished. A live
// co-presence indicator inside a running attach needs a side channel into the passthrough
// loop, and attach.Session has none (Snapshot/Frames/Input/Resize/Detach/Generation). Until
// that exists the board row carries the phone marker, which is where the state can be shown
// at all -- and a row that says nothing beats a row that says something untrue.

import (
	"strings"
	"testing"
)

// returnHint is the escape route the reserved row exists to carry.
const returnHint = "returns to swarm"

// TestHintText_SaysNothingAboutAPhone is the note's deletion, pinned so it cannot come
// back by accident. It is stronger than the assertion it replaces: that one said an attach
// which took nothing over carries no note, leaving the other branch alive; this says the
// row never speaks about a phone at all, because there is no longer a fact here to speak
// from.
func TestHintText_SaysNothingAboutAPhone(t *testing.T) {
	s := hintText("claude", DefaultDetachKey, 120)
	for _, banned := range []string{"took over", "took ", "phone"} {
		if strings.Contains(s, banned) {
			t.Errorf("the reserved row says %q in %q. The attach row has no phone fact to "+
				"report: it cannot see one arrive, and the value it used to sample was read "+
				"before the dial and never updated after", banned, s)
		}
	}
	if !strings.Contains(s, returnHint) {
		t.Errorf("the hint lost the return affordance: %q", s)
	}
}

// TestHintText_TruncatesToTheRow carries forward the two truncation assertions that had a
// surviving subject: a wrap on the reserved bottom row SCROLLS, so the hint is never wider
// than its row, and the escape affordance is what the row exists to carry.
func TestHintText_TruncatesToTheRow(t *testing.T) {
	const cols = 12
	if got := len([]rune(hintText("a-long-session-name", DefaultDetachKey, cols))); got > cols {
		t.Errorf("hint is %d cells wide, over the %d-column row", got, cols)
	}

	// A row with room for the whole hint keeps every cell of it.
	full := hintText("s", DefaultDetachKey, 0)
	if got := hintText("s", DefaultDetachKey, len([]rune(full))+4); got != full {
		t.Errorf("a row with room to spare altered the hint: %q, want %q", got, full)
	}
	if !strings.Contains(full, returnHint) {
		t.Fatalf("the untruncated hint has no escape affordance: %q", full)
	}
}

// TestChromeHint_PaintsNoPhoneNote pins the plumbing at the painter, where the flag used
// to be threaded through.
func TestChromeHint_PaintsNoPhoneNote(t *testing.T) {
	got := string(chromeHint("claude", DefaultDetachKey, 120, 24))
	if strings.Contains(got, "took over") || strings.Contains(got, "phone") {
		t.Errorf("the reserved row painted a phone note; got %q", got)
	}
	if !strings.Contains(got, "claude") {
		t.Errorf("the reserved row lost the session name; got %q", got)
	}
}
