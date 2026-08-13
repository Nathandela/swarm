package attach

// agents-tracker-nx44.7 -- the attach-time takeover note.
//
// THE PREMISE THIS FILE WAS FILED ON IS DISPROVED. It read: a TUI attach and a paired
// device's take_control compete for the SAME single shim subscriber slot (hub.attach
// evicts unconditionally), so the two states are mutually exclusive and no LIVE
// "phone has control" indicator is possible inside an attach -- which is why the note
// below is a one-shot sample taken before the dial "that destroyed the answer".
// TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive
// (internal/skeleton/copresence_test.go, evidence in docs/verification/mirror-m0.md)
// proves the opposite: both live streams survive, in BOTH orders, and the phone's
// control lease still reaches the PTY afterwards. Production runs two protocol Servers
// over one coreAPI, each with its own lease map, and coreAPI.Attach subscribes to the
// SHARED per-session tap, so the shim's single-subscriber slot is reached ONCE per
// session; eviction is real only WITHIN one tier. The dial destroys nothing, and a
// live co-presence indicator IS possible. See internal/tui/attach.go for the same
// correction at the sampling site.
//
// THE STRINGS BELOW ARE STILL THE CURRENT CONTRACT and are pinned as such. The note
// says "took over from phone", which co-presence makes false; replacing it is a design
// change (a live indicator, not a sampled note) plus an authorized rewrite of these
// assertions, tracked as agents-tracker-dwwv.3.1 (M2). Until that lands, these tests
// describe what ships -- they are not a claim that what ships is right.
//
// ORCHESTRATOR RULING pinned here, and unaffected by the above: the note goes LAST in
// the hint string. The ctrl+q escape affordance is safety-critical and must survive a
// narrow terminal; the note is informational and is the first thing to go.
//
// RED at filing time: hintText took no takeover argument, so this file did not compile.

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
