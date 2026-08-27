package gate

// W2.3 of the phone refit (docs/specifications/phone-refit-playbook.md section 3, bead
// agents-tracker-d45a.2): one refusal, said once.
//
// THE DEFECT. renderComposerVerdict wrote composerRefusal (drawn under the composer as
// DetailTag.COMPOSER_NOTICE) AND called say(PressFeedback.ofRefusal(...)), which wrote the outcome
// line and a toast carrying the same sentence: one refused send, said three times. The composer
// notice is the single surface now, and the machine's own words -- which only the toast's mono
// suffix used to carry -- ride beside it as composerRefusalDetail.
//
// WHY A GREP OVER ONE FUNCTION: TestR6R2_EveryChatVerbsMachineAnswerIsClaimedOnTheSurface's reason.
// The Kotlin unit-test JVM never sees PhoneSurface, so what that function CALLS can only be pinned
// by reading it; r6SurfaceFunc reads exactly the one production function that has to make (or
// stop making) the call, comments stripped, so the fence fails on the deletion it exists to catch.

import (
	"strings"
	"testing"
)

func TestW23_ARefusedSendIsSaidOnceAndNeverToasted(t *testing.T) {
	composer := r6SurfaceFunc(t, "renderComposerVerdict", "the composer claims no answer at all")
	if strings.Contains(composer, "say(") {
		t.Errorf("renderComposerVerdict still calls say(...): the refusal goes to the outcome line "+
			"and a toast on top of the composer notice, so one refused send says its sentence three "+
			"times (phone refit W2.3). Body:\n%s", composer)
	}
	if !strings.Contains(composer, "composerRefusalDetail = verdict.detail") {
		t.Errorf("renderComposerVerdict never keeps verdict.detail as composerRefusalDetail, so once "+
			"the toast is gone the machine's own words reach nobody (W2.3's detail cell; the F4 class "+
			"TestR6R3_TheTwoM3ReadsSayWhatTheMachineSaid fences). Body:\n%s", composer)
	}
	// THE CONTROL: a refused Stop is not a composer refusal and keeps its toast
	// (SessionDetailVerdictTest, `a refused interrupt says the turn was not stopped ...`).
	stop := r6SurfaceFunc(t, "renderInterruptVerdict", "the Stop claims no answer at all")
	if !strings.Contains(stop, "say(") {
		t.Errorf("renderInterruptVerdict no longer calls say(...); a refused Stop must still say its "+
			"reason where the finger was. Body:\n%s", stop)
	}
}
