package gate

// FAILING-FIRST (TDD RED, GG-5) for ADR-009 D5's precondition: **the sweep announces a transition
// that happened IN FRONT OF THE USER.**
//
// D5 says the sweep "fires exactly once, at the moment a session's Group becomes NeedsInput", and
// `TriageInboxScreen.promotions` states the same rule in its own KDoc, in capitals: "IT COMPARES
// SCREENS AND NOT ROSTERS, which is what makes 'in front of the user' true rather than
// approximately true ... a sweep is an announcement, and there is nobody to announce to."
//
// THE FUNCTION IS RIGHT AND THE WIRING WAS NOT. `promotions(previous, next)` is pure, correct, and
// covered by TriageInboxScreenTest -- including the `previous == null` case, which returns the
// empty set precisely because nothing can have transitioned in front of a user who has not been
// shown anything. What it is HANDED is `PhoneSurface.inboxDrawn`, "what the inbox last drew", and
// that field had exactly two write sites: its declaration and one assignment at the bottom of
// `drawInbox`. Nothing reset it. So it froze for as long as the user was anywhere other than the
// inbox list -- on Machines, Activity or Settings, or inside a session drill-down -- and every
// Group transition into `needs_input` during that window was still pending in the comparison when
// they came back.
//
// THE FAILURE THAT PRODUCES. A user taps a session, reads the transcript for two minutes while
// three others block on input, then backs out. `closeSessionDetail` -> `render()` -> `drawInbox`,
// `inboxDrawn` is the screen from before the drill-down, `promotions` returns three ids: the phone
// fires the NEEDS_YOU two-pulse and builds three rows `promoted = true`, announcing transitions
// that happened while the inbox was not on screen. Same on any tab round-trip. "The last screen
// DRAWN" and "the last screen SEEN" are the same thing only while the inbox is what is showing,
// and the difference between them is every departure from it.
//
// WHY THIS IS A SOURCE GATE AND NOT A ROBOLECTRIC TEST, stated so the choice reads as a boundary
// rather than as laziness. `PhoneRuntime.phone()` answers `PhoneStartup.Unavailable` on every JVM
// run -- the phone core is a gomobile AAR carrying .so files cross-compiled for Android ABIs -- so
// `render()` reaches `drawInbox(null)` and nothing past it. The argument is
// PhoneSurfaceNavigationTest's and pbapp6_pbinput2_surface_test.go's, in full. An inbox screen
// cannot be built on this classpath, so the promotion path cannot be exercised there at all; what
// CAN be checked is the shape of the code that decides it, which is what this file does, with
// in-memory perturbation as its control.

import (
	"path/filepath"
	"strings"
	"testing"
)

// o4MemoField is the memo whose staleness this file is about.
const o4MemoField = "inboxDrawn"

// o4PhoneSurface reads the surface's production source, comments stripped.
//
// COMMENTS ARE STRIPPED FOR THE USUAL REASON, and it matters more here than usual: `drawInbox`'s
// own body carries a paragraph about what `inboxDrawn` is, so a scan for the assignment over raw
// text would find the word in prose and report the wiring present on a file that had lost it.
func o4PhoneSurface(t *testing.T) string {
	t.Helper()
	path := filepath.Join(
		kotlinMainRoot(t), "dev", "swarm", "phone", "PhoneSurface.kt",
	)
	return kotlinCodeOnly(readFileOrFail(t, path, "ADR-009 D5"))
}

// o4Body returns the body of the named function -- the text between its opening brace and the
// matching close.
//
// BRACE-MATCHED RATHER THAN LINE-COUNTED, because the alternative is a fixed window that silently
// stops covering the function on the first edit that lengthens it, and reports a clean scan.
func o4Body(src, signature string) (string, bool) {
	start := strings.Index(src, signature)
	if start < 0 {
		return "", false
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		return "", false
	}
	open += start
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : i], true
			}
		}
	}
	return "", false
}

// o4ForgetsTheMemo is the check itself, as a function so the assertion and its control run the
// same code over the real source and over a perturbed copy of it.
//
// TWO CLAUSES, AND EACH ALONE IS SATISFIABLE BY THE BUG.
//
//   - `drawContent` must CLEAR the memo. Without this, nothing forgets and the comparison spans
//     the user's absence.
//   - The inbox-list arm must RETURN before reaching the clear. Without this, the clear runs on
//     every draw including the inbox's own -- which forgets what the user is looking at right now,
//     so the very next redraw announces every waiting session as newly promoted. That is the
//     opposite failure and it is louder, not quieter.
//
// It is deliberately a check on `drawContent` and not on "some write site somewhere". `drawInbox`
// has exactly one caller and it is `drawContent`'s `Destination.INBOX` arm; the decision "is the
// inbox list what is on screen" is made there and nowhere else, so that is where forgetting
// belongs and where a reader will look for it.
func o4ForgetsTheMemo(src string) []string {
	var faults []string
	body, ok := o4Body(src, "private fun drawContent(")
	if !ok {
		return []string{"PhoneSurface.kt declares no `drawContent`, so the destination switch " +
			"this gate is about no longer exists under that name"}
	}
	if !strings.Contains(body, o4MemoField+" = null") {
		faults = append(faults, "`drawContent` never clears `"+o4MemoField+"`. The memo is what "+
			"`TriageInboxScreen.promotions` compares against, and its KDoc's claim is that it is "+
			"the last screen the user SAW; a memo that survives a tab change or a drill-down is "+
			"the last screen DRAWN, which is a different thing the moment the inbox leaves the "+
			"viewport. Every promotion that happens while the user is elsewhere is then announced "+
			"-- a two-pulse haptic and a sweep -- when they come back.")
	}
	if !strings.Contains(body, "drawInbox(") {
		faults = append(faults, "`drawContent` no longer calls `drawInbox`, so this gate is "+
			"reading a switch that has stopped being the one that decides what is on screen")
	} else if !strings.Contains(body, "return") {
		faults = append(faults, "`drawContent` clears `"+o4MemoField+"` on every path, including "+
			"the one that draws the inbox list. Forgetting what the user is looking at RIGHT NOW "+
			"means the next redraw treats every waiting session as newly promoted, which is the "+
			"same defect louder. The inbox-list arm must return before the clear.")
	}
	return faults
}

// TestADR009D5_ThePromotionMemoIsForgottenWhenTheInboxLeavesTheScreen is the assertion.
func TestADR009D5_ThePromotionMemoIsForgottenWhenTheInboxLeavesTheScreen(t *testing.T) {
	for _, fault := range o4ForgetsTheMemo(o4PhoneSurface(t)) {
		t.Errorf("ADR-009 D5: %s", fault)
	}
}

// TestADR009D5_TheMemoIsWrittenInExactlyTheTwoPlacesThatDecideIt pins the write sites.
//
// A THIRD ONE IS THE DEFECT THIS WHOLE FILE IS ABOUT, ARRIVING AGAIN. The memo's meaning -- "the
// last inbox screen the user saw" -- is only true if the two events that can change it are the
// only two that write it: the inbox drew a screen, and the inbox left the viewport. A write
// anywhere else (a refresh path priming it to skip work, a pull-to-refresh clearing it to force a
// rebuild) is a fourth opinion about what the user has seen, and it would be invisible to the
// check above.
func TestADR009D5_TheMemoIsWrittenInExactlyTheTwoPlacesThatDecideIt(t *testing.T) {
	src := o4PhoneSurface(t)

	// The DECLARATION is not counted: it is written `private var inboxDrawn: InboxScreen? = null`,
	// with the type between the name and the `=`. Two assignments is the whole of it.
	const wantWrites = 2
	writes := strings.Count(src, o4MemoField+" =")
	if writes != wantWrites {
		t.Errorf("ADR-009 D5: `%s` is assigned %d times in PhoneSurface.kt, want %d -- the "+
			"record at the end of `drawInbox`, and the clear in `drawContent`. A third write is "+
			"a third opinion about what the user has seen, and both the sweep and the NEEDS_YOU "+
			"haptic are computed from it.",
			o4MemoField, writes, wantWrites)
	}

	if body, ok := o4Body(src, "private fun drawInbox("); !ok {
		t.Error("ADR-009 D5: PhoneSurface.kt declares no `drawInbox`")
	} else if !strings.Contains(body, o4MemoField+" = screen") {
		t.Errorf("ADR-009 D5: `drawInbox` no longer records the screen it drew in `%s`, so the "+
			"memo is cleared and never set and no promotion can ever be detected", o4MemoField)
	}
}

// TestADR009D5_TheMemoGateCanActuallyFail is the negative control, and it PERTURBS A COPY IN
// MEMORY -- android/gate/guidedpairing_test.go's discipline. A control that edited the file would
// be a gate that can corrupt the thing it guards.
func TestADR009D5_TheMemoGateCanActuallyFail(t *testing.T) {
	src := o4PhoneSurface(t)
	if faults := o4ForgetsTheMemo(src); len(faults) != 0 {
		t.Fatalf("ADR-009 D5: the real source fails the check, so the perturbations below prove "+
			"nothing about it: %s", strings.Join(faults, "; "))
	}

	// 1. THE BUG AS IT SHIPPED: the clear is deleted and nothing else changes.
	withoutClear := strings.Replace(src, o4MemoField+" = null", "", 1)
	if withoutClear == src {
		t.Fatal("ADR-009 D5: the perturbation changed nothing, so the control below is asserting " +
			"that the unmodified source passes -- which the check above already says")
	}
	if faults := o4ForgetsTheMemo(withoutClear); len(faults) == 0 {
		t.Error("ADR-009 D5: a `drawContent` with no clear at all passes the check. That is the " +
			"exact shape the surface shipped with, so the check is asserting nothing.")
	}

	// 2. THE OPPOSITE FAILURE: the clear is there and the inbox arm falls through to it.
	//    A gate that only looked for the assignment would pass this, and it announces MORE than
	//    the bug it replaced rather than less.
	body, ok := o4Body(src, "private fun drawContent(")
	if !ok {
		t.Fatal("ADR-009 D5: `drawContent` not found for the second perturbation")
	}
	fallsThrough := strings.Replace(src, body, strings.ReplaceAll(body, "return", ""), 1)
	if faults := o4ForgetsTheMemo(fallsThrough); len(faults) == 0 {
		t.Error("ADR-009 D5: a `drawContent` whose inbox arm does NOT return -- so the clear runs " +
			"on the inbox's own draw -- passes the check. That forgets the screen the user is " +
			"looking at, and the next redraw announces every waiting session as a promotion.")
	}

	// 3. And the body reader must actually be reading a body. A brace matcher that returned the
	//    whole file would make every `strings.Contains` above true for a reason unrelated to
	//    `drawContent`.
	if ok && len(body) >= len(src) {
		t.Error("ADR-009 D5: the brace matcher returned the whole source as `drawContent`'s body, " +
			"so every containment check above is really a check on PhoneSurface.kt as a whole")
	}
	if ok && strings.Contains(body, "private fun drawInbox(") {
		t.Error("ADR-009 D5: `drawContent`'s extracted body contains `drawInbox`'s DECLARATION, " +
			"so the brace matcher ran past the end of the function")
	}
}
