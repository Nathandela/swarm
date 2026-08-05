package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-w6o3: the wiring half of "a revoked phone must
// not read like a fresh install".
//
// THE DEFECT IS A DECISION ORDER. PhoneSurface.renderReady asks PairOnlyScreen.presentationOf
// FIRST and returns before FacadeBridge.connectionBanner() is ever read, and mobile/relay.go's
// transportEndsPairing folds connRepairRequired and a past-grace connRevoked into paired=false. So
// the two most carefully worded banners in ConnectionUi.kt are unreachable in production and the
// handset they describe opens on the screen a fresh install opens on -- with, on repair_required,
// one control that leads into a pairing the machine fail-fasts while it still holds this device's
// registration (PB-STATE-10). That is PB-APP-10's forbidden failure loop, reached through the
// remedy.
//
// WHY THIS IS A GO GATE AND NOT A KOTLIN TEST, which is d0b8_unpair_test.go's argument for the
// same surface: PhoneRuntime.phone() answers PhoneStartup.Unavailable under Robolectric -- the
// phone core is a gomobile AAR of .so files cross-compiled for Android ABIs -- so no JVM test can
// drive a real handset into a terminal transport state and watch which screen it lands on. What a
// test CAN see is the source: whether the draw consults the transport at all. The presentation
// itself is argued by PairOnlyTerminalReasonTest and PairOnlyViewTest, which are the stronger
// tests for what they cover and cannot cover this.
//
// IT DOES NOT LOOSEN d0b8's FENCE AND MUST NOT. That gate reads the lambda passed to
// presentationOf and fails if it mentions the connection state, because whether this phone is
// USABLY paired is one fact assembled in Go over the durable unpair AND the transport, and a call
// site that rebuilt it here would be a second opinion to keep in step. The reason is a DIFFERENT
// question -- not "is this phone paired" but "what does the screen it has been sent to say" -- and
// it is asked after the gate has already answered.

import (
	"strings"
	"testing"
)

// TestW6O3_TheTerminalTransportStatesReachTheScreenTheySendThePhoneTo.
func TestW6O3_TheTerminalTransportStatesReachTheScreenTheySendThePhoneTo(t *testing.T) {
	code := d0b8Code(t, d0b8PhoneSurface)

	at := strings.Index(code, "reasonFor")
	if at < 0 {
		t.Fatal("agents-tracker-w6o3: PhoneSurface.kt never asks PairOnlyScreen.reasonFor, so the " +
			"pair-only screen is drawn without the reason the pairing ended. A phone whose owner " +
			"ran `swarm remote revoke` reads \"Pair this phone\" -- identical to a fresh install -- " +
			"and a phone whose relay-auth key was destroyed is offered a bare pairing CTA that the " +
			"machine refuses while it still holds this device's registration (PB-STATE-10)")
	}

	// THE READER IS THE TRANSPORT'S OWN STATE and not a second inference from the durable blob.
	// The reason exists because the connection state is the ONLY place the cause survives: the
	// summary carries `paired` and nothing about why it turned false.
	reader, ok := d0b8Balanced(code, at, '{', '}')
	if !ok {
		t.Fatal("agents-tracker-w6o3: the reasonFor call site passes no lambda this fence can read")
	}
	if !strings.Contains(reader, "connectionState") {
		t.Errorf("agents-tracker-w6o3: the pair-only reason is read from %s. The transport state is "+
			"where `revoked` and `repair_required` are reported -- ConnectionState.of over "+
			"App.ConnectionState -- and a reason derived from anything else on this handset is "+
			"guessing at a cause the state summary does not carry", strings.TrimSpace(reader))
	}

	// AND IT REACHES THE DRAW. A reason computed and dropped is the same screen it was.
	body := d0b8FunctionBody(t, code, "drawPairOnly", "PhoneSurface.kt")
	if !strings.Contains(body, "copyFor") {
		t.Errorf("agents-tracker-w6o3: drawPairOnly is %s. It composes pairOnlyView without asking "+
			"PairOnlyScreen.copyFor, so whatever the reason turned out to be, the screen draws the "+
			"first-run constants over it", strings.TrimSpace(body))
	}
}
