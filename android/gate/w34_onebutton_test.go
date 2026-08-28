package gate

// W3 of the phone refit (docs/specifications/phone-refit-playbook.md section 4, bead
// agents-tracker-d45a.3): one button, and "Stopped" once, under the composer.
//
// THE DEFECT. The full-width Stop's press carried `confirmation = SessionDetail.INTERRUPT_SENT`,
// which `dispatchPress` spends as row 1's toast: a 3.2 second box over the tab bar saying
// "Interrupt sent", on a screen whose composer is where the finger was. W3.4 draws the sealing as
// one notice line under the composer instead, and W3.3 removes the question in front of it.
//
// WHY A GREP OVER FOUR PLACES: TestW23_ARefusedSendIsSaidOnceAndNeverToasted's reason. The Kotlin
// unit-test JVM never reaches a settle -- `PhoneRuntime.phone()` answers Unavailable on every run,
// so no press on a JVM ever runs `Press.settle` -- and what the press DECLARES and what the settle
// DRAWS can only be pinned by reading them. Both halves are read (orchestrator ruling,
// 2026-08-28): that nothing on the stop path toasts, AND that the word is drawn as a notice line
// the composer region holds until the turn it was said over is gone.
//
// THE SECOND HALF WAS RE-CUT BY THE REVIEW ROUND (2026-08-28). "Cleared by the next region draw"
// was the defect: the settle that adds the notice runs inside the dispatch that then renders, and
// a working agent redraws at output rate, so the word never reached a frame. The settle now calls
// `drawStopped()`, which records the open turn (`stoppedOverTurn`) beside the word, and
// `drawComposerRegion` removes the notice only when the drawn panel's turn differs from it. The
// JVM half of that -- the notice surviving a same-turn redraw and dying on the turn closing -- is
// PhoneSurfaceControlsTest's, through `drawStopped()` made internal; this fence holds the seam to
// the settle, so the test's subject stays the production path.

import (
	"strings"
	"testing"
)

func TestW34_AStopIsSaidOnceUnderTheComposerAndNeverToasted(t *testing.T) {
	code := d0b8Code(t, d0b8PhoneSurface)

	// HALF ONE: NOTHING ON THE STOP PATH TOASTS.
	//
	// The square's plan takes the interrupt from interruptPlan(); the press that GOES declares an
	// empty confirmation (`PressFeedback.saysNothing`) and never hands INTERRUPT_SENT to the
	// dispatcher. The `say(` that IS in interruptPlan belongs to the NOT_SENT arm and is
	// TestT4LTA's own requirement -- a Stop the link could not carry is reported, not sealed --
	// so what is read here is the Press(...) block alone, not the whole plan.
	square := d0b8Lambda(t, code, "private val send: ImageView = pressable(composerAction(activity))",
		"the composer's one control is not built as W3.2 specifies")
	if !strings.Contains(square, "interruptPlan()") {
		t.Errorf("the square's press never reaches interruptPlan(), so the one button cannot stop "+
			"anything (W3.2). Body:\n%s", square)
	}
	if strings.Contains(square, "say(") || strings.Contains(square, "Toast") {
		t.Errorf("the square's press body toasts; a refused send is the composer notice's (W2.3) and "+
			"a sealed Stop is drawn under the composer (W3.4). Body:\n%s", square)
	}
	plan := r6SurfaceFunc(t, "interruptPlan", "nothing on the surface plans an interrupt (W3.2)")
	if !strings.Contains(plan, "app.interrupt(") {
		t.Errorf("interruptPlan never calls app.interrupt(...), so the one button cannot stop "+
			"anything (phone refit W3.2). Body:\n%s", plan)
	}
	press := d0b8Lambda(t, plan, "Press(", "interruptPlan declares no Press")
	if strings.Contains(press, "INTERRUPT_SENT") {
		t.Errorf("interruptPlan still hands SessionDetail.INTERRUPT_SENT to the press, which "+
			"dispatchPress spends as a toast; W3.4 draws it under the composer instead. Press:\n%s", press)
	}
	if !strings.Contains(press, `confirmation = ""`) {
		t.Errorf("interruptPlan's press declares no empty confirmation, so a later reader cannot "+
			"tell silence chosen from a sentence forgotten (W3.4). Press:\n%s", press)
	}
	if strings.Contains(press, "say(") {
		t.Errorf("interruptPlan's press says something on its own; the sealing is the settle's to "+
			"draw (W3.4). Press:\n%s", press)
	}

	// HALF TWO: THE WORD IS A NOTICE LINE THE REGION HOLDS, ADDED BY THE SETTLE THROUGH
	// drawStopped() AND KEPT UNTIL THE TURN IT WAS SAID OVER IS GONE.
	if !strings.Contains(code, "val stoppedNotice: TextView = noticeLine(") {
		t.Errorf("PhoneSurface.kt declares no `val stoppedNotice: TextView = noticeLine(`, so the " +
			"sealing has no notice line of its own under the composer (W3.4)")
	}
	settle := d0b8Lambda(t, code, "private fun rememberInterrupt(answer: Any?)",
		"the Stop latches no operation (r6 M2.4)")
	if !strings.Contains(settle, "drawStopped()") {
		t.Errorf("rememberInterrupt never calls drawStopped(), so a sealed Stop changes nothing on "+
			"screen until the machine answers (W3.4, review round). Body:\n%s", settle)
	}
	if strings.Contains(settle, "say(") || strings.Contains(settle, "Toast") {
		t.Errorf("rememberInterrupt toasts; \"Stopped\" is a notice under the composer and not a "+
			"toast (W3.4). Body:\n%s", settle)
	}
	said := d0b8Lambda(t, code, "internal fun drawStopped()",
		"nothing draws the sealing under the composer (W3.4, review round)")
	if !strings.Contains(said, "INTERRUPT_SENT") {
		t.Errorf("drawStopped never draws SessionDetail.INTERRUPT_SENT, so the sealing is a notice "+
			"with no word on it (W3.4). Body:\n%s", said)
	}
	if !strings.Contains(said, "composerRegion.addView(stoppedNotice") {
		t.Errorf("drawStopped never adds stoppedNotice to composerRegion, so the word is written on "+
			"a view nothing holds (W3.4). Body:\n%s", said)
	}
	if !strings.Contains(said, "stoppedOverTurn = ") {
		t.Errorf("drawStopped records no turn in stoppedOverTurn, so the region draw cannot tell the "+
			"turn the word was said over from the next one (review round). Body:\n%s", said)
	}
	if strings.Contains(said, "say(") || strings.Contains(said, "Toast") {
		t.Errorf("drawStopped toasts; \"Stopped\" is a notice under the composer and not a toast "+
			"(W3.4). Body:\n%s", said)
	}
	region := r6SurfaceFunc(t, "drawComposerRegion", "the composer region is never drawn")
	if !strings.Contains(region, "composerRegion.removeView(stoppedNotice)") {
		t.Errorf("drawComposerRegion never clears stoppedNotice, so \"Stopped\" outlives the turn it "+
			"reports on -- said once means cleared once that turn is gone (W3.4). Body:\n%s", region)
	}
	if !strings.Contains(region, "stoppedOverTurn") {
		t.Errorf("drawComposerRegion clears stoppedNotice without reading stoppedOverTurn, so the "+
			"word comes off on the next draw -- over a working agent, before a frame (review round). "+
			"Body:\n%s", region)
	}

	// THE CONTROL: a refused Stop is not the sealing and keeps its say() (W2.3's own control,
	// `renderInterruptVerdict` -> `interruptNoticeFor(`).
	verdict := r6SurfaceFunc(t, "renderInterruptVerdict", "the Stop claims no answer at all")
	if !strings.Contains(verdict, "say(") {
		t.Errorf("renderInterruptVerdict no longer calls say(...); a refused Stop must still say "+
			"its reason where the finger was. Body:\n%s", verdict)
	}
}

// d0b8Lambda returns the balanced block that follows the first occurrence of anchor in code:
// the `{ ... }` of a declaration whose initialiser is a lambda, or the `( ... )` of a call.
func d0b8Lambda(t *testing.T, code, anchor, missing string) string {
	t.Helper()
	at := strings.Index(code, anchor)
	if at < 0 {
		t.Fatalf("PhoneSurface.kt has no %q: %s", anchor, missing)
	}
	open, close := byte('{'), byte('}')
	if strings.HasSuffix(anchor, "(") {
		open, close = '(', ')'
		at += len(anchor) - 1
	}
	body, ok := d0b8Balanced(code, at, open, close)
	if !ok {
		t.Fatalf("%q opens a block this fence cannot balance", anchor)
	}
	return body
}
