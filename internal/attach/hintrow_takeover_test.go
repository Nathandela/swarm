package attach

// agents-tracker-nx44.7 -- the attach-time takeover note. A TUI attach and a paired
// device's take_control compete for the SAME single shim subscriber slot (hub.attach
// evicts unconditionally), so the two states are mutually exclusive and no LIVE
// "phone has control" indicator is possible inside an attach. What is honest is a
// note on the reserved row saying this attach took the session FROM the phone,
// sampled once before the dial that destroyed the answer.
//
// ORCHESTRATOR RULING pinned here: the note goes LAST in the hint string. The ctrl+q
// escape affordance is safety-critical and must survive a narrow terminal; the note
// is informational and is the first thing to go.
//
// RED today: hintText takes no takeover argument, so this file does not compile.

import (
	"strings"
	"testing"
)

const returnAffordance = "ctrl+q returns to swarm"

// TestHintText_TakeoverNoteGoesLast: on a wide row both segments are present, and
// the note follows the return affordance.
func TestHintText_TakeoverNoteGoesLast(t *testing.T) {
	s := hintText("claude", DefaultDetachKey, 120, true)
	iAff := strings.Index(s, returnAffordance)
	if iAff < 0 {
		t.Fatalf("hint lost the return affordance: %q", s)
	}
	iNote := strings.Index(s, "took over")
	if iNote < 0 {
		t.Fatalf("an attach that evicted the phone must say so; got %q", s)
	}
	if iNote < iAff {
		t.Errorf("the takeover note must come AFTER the return affordance so it is cut first; got %q", s)
	}
	if !strings.Contains(s, "phone") {
		t.Errorf("the note must name what was taken over from; got %q", s)
	}
}

// TestHintText_NarrowRowKeepsTheEscapeAffordanceWhole: when the row cannot hold both,
// the note is dropped WHOLE and the escape route survives intact -- not sliced into a
// fragment that eats the affordance's tail.
func TestHintText_NarrowRowKeepsTheEscapeAffordanceWhole(t *testing.T) {
	base := hintText("s", DefaultDetachKey, 0, false)
	cols := len([]rune(base)) + 4 // room for the affordance, nowhere near the note

	s := hintText("s", DefaultDetachKey, cols, true)
	if !strings.Contains(s, returnAffordance) {
		t.Fatalf("a narrow row dropped the escape affordance: %q (cols=%d)", s, cols)
	}
	if strings.Contains(s, "took over") || strings.Contains(s, "took ") {
		t.Errorf("the note must be dropped whole on a row that cannot hold it, not sliced in; got %q", s)
	}
	if got := len([]rune(s)); got > cols {
		t.Errorf("hint is %d cells wide, over the %d-column row (a wrap on the reserved row scrolls)", got, cols)
	}
}

// TestHintText_NoNoteWithoutATakeover: an ordinary attach's row is unchanged.
func TestHintText_NoNoteWithoutATakeover(t *testing.T) {
	s := hintText("claude", DefaultDetachKey, 120, false)
	if strings.Contains(s, "took over") || strings.Contains(s, "phone") {
		t.Errorf("an attach that took nothing over must carry no note; got %q", s)
	}
}

// TestHintText_TakeoverStillTruncatesToTheRow: even with the note, a row narrower
// than the base hint is truncated to fit (a wrap on the reserved bottom row scrolls).
func TestHintText_TakeoverStillTruncatesToTheRow(t *testing.T) {
	const cols = 12
	if got := len([]rune(hintText("a-long-session-name", DefaultDetachKey, cols, true))); got > cols {
		t.Errorf("hint is %d cells wide, over the %d-column row", got, cols)
	}
}

// TestChromeHint_PaintsTheTakeoverNote pins the plumbing: the reserved-row painter
// carries the flag through to the text it emits.
func TestChromeHint_PaintsTheTakeoverNote(t *testing.T) {
	got := string(chromeHint("claude", DefaultDetachKey, 120, 24, true))
	if !strings.Contains(got, "took over") {
		t.Errorf("the reserved row must paint the takeover note; got %q", got)
	}
	got = string(chromeHint("claude", DefaultDetachKey, 120, 24, false))
	if strings.Contains(got, "took over") {
		t.Errorf("the reserved row painted a takeover note for an ordinary attach; got %q", got)
	}
}
