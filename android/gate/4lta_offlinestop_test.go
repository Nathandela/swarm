package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-4lta: **the offline Stop press must resolve to
// something the user can see.**
//
// THE HALF THIS FENCE OWNS. That an offline Stop is not put behind a confirmation it cannot
// honour, and that the not-sent notice waits for a press instead of for the link, are model facts
// and are asserted in Kotlin where they belong (ui/SessionStopOfflineTest.kt, SessionScreensTest,
// SessionDetailPanelTest, SessionDetailViewTest). What no JVM test can reach is the SURFACE's arm
// for the value: `PhoneRuntime.phone()` answers PhoneStartup.Unavailable on every Robolectric run
// -- the phone core is a gomobile AAR carrying .so files cross-compiled for Android ABIs -- so the
// press path past that branch is out of reach, and a Robolectric assertion about it would pass
// vacuously. That is pbapp6_pbinput2_surface_test.go's argument in full and it holds here
// unchanged.
//
// WHAT THE SOURCE SHOWS THAT THE SCREEN CANNOT. `PhoneSurface`'s Stop plan branched on
// `confirmedStopAction` with arms for SEND_INTERRUPT and ACQUIRE_LEASE_FIRST and `else -> null`
// for the third value, deferring to a notice that was already on screen before the press. So the
// one state where the user most needs to be told something -- they pressed Stop and the machine
// never heard it -- was the one state where the press wrote nothing at all. With the notice now
// press-gated, an arm that still resolved to null would leave the screen with nothing to draw at
// all: the same dead press, one layer quieter.
//
// IT ASKS FOR AN EXPLICIT ARM AND NOT FOR PARTICULAR WORDS. The sentence is the screen model's
// (PB-DS-9), and it is asserted against the model rather than against a second copy of itself here.
//
// RE-ANCHORED BY PHONE REFIT W3 (agents-tracker-d45a.3). The full-width Stop (`val stop =
// actionButton(...)`) left the composer region; the interrupt is planned once, in
// `PhoneSurface.interruptPlan()`, and pressed from the composer's square while the agent works
// and the field is empty, and from the header menu's Stop row. The four assertions below are
// unchanged; only the subject they read moved.

import (
	"strings"
	"testing"
)

// TestT4LTA_AnOfflineStopPressSaysSoRatherThanResolvingToNothing.
func TestT4LTA_AnOfflineStopPressSaysSoRatherThanResolvingToNothing(t *testing.T) {
	code := d0b8Code(t, d0b8PhoneSurface)

	at := strings.Index(code, "private fun interruptPlan(")
	if at < 0 {
		t.Fatal("agents-tracker-4lta: PhoneSurface.kt no longer plans its interrupt in " +
			"interruptPlan(), so this fence's subject has changed shape. A fence whose subject " +
			"silently disappeared reports clean forever")
	}
	plan, ok := d0b8Balanced(code, at, '{', '}')
	if !ok {
		t.Fatal("agents-tracker-4lta: interruptPlan() has no body this fence can read")
	}

	if !strings.Contains(plan, "StopAction.NOT_SENT") {
		t.Errorf("agents-tracker-4lta: the Stop plan has no arm for StopAction.NOT_SENT:\n%s\n"+
			"The value the link decides is the one the plan does not name, so the press falls to "+
			"the catch-all: nothing is sent, nothing is written, and the screen is identical to "+
			"the one the user pressed. PB-APP-9 is against exactly this -- a control that reaches "+
			"a machine and answers with nothing", strings.TrimSpace(plan))
	}

	// THE PRESS IS RECORDED, which is what makes the screen's notice a report of something that
	// happened. PB-INPUT-1's sentence is gated on this fact now, so a press that latched nothing
	// leaves the screen with nothing to say -- the dead press again, one layer quieter.
	if !strings.Contains(plan, "stopNotSent") {
		t.Errorf("agents-tracker-4lta: the Stop plan never records that the press was not sent:\n%s\n"+
			"`SessionDetail.notSentNotice` is a function of the PRESS now and not of the link, so "+
			"a press that leaves no mark draws no notice at all", strings.TrimSpace(plan))
	}

	// AND THE PRESS IS ANSWERED WHERE THE FINGER IS. Stop sits below the transcript and the notice
	// above it, so on a long session log the screen changes somewhere the user is not looking.
	// `say` is the surface's one way of putting a press's answer in front of them.
	if !strings.Contains(plan, "say(") {
		t.Errorf("agents-tracker-4lta: the Stop plan never calls say():\n%s\n"+
			"A press that resolves without reaching the wire still has an outcome, and the user "+
			"who pressed the button is the person it is for", strings.TrimSpace(plan))
	}

	// AND THE SENTENCE IS THE SCREEN'S. PB-DS-9 assigns copy to the screen model; a string typed
	// at this call site would be a second copy of a sentence `SessionDetail` already owns, and the
	// two would drift the first time either was edited.
	if !strings.Contains(plan, "NOT_SENT_NOTICE") {
		t.Errorf("agents-tracker-4lta: the Stop plan's answer does not carry "+
			"SessionDetail.NOT_SENT_NOTICE:\n%s\n"+
			"PB-DS-9 assigns copy to the screen, and the model already writes the sentence that "+
			"says a Stop did not reach the machine and was not held for later",
			strings.TrimSpace(plan))
	}
}
