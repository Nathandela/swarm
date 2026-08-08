package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.5 -- the three Go-side seams where a
// machine refusal reaches the phone's screen as something no reader can act on.
//
// The three are not one defect with three call sites; they are three different ways of
// answering "why" with something that is not a why:
//
//   - THE WIRE TOKEN AS COPY. app.go's outcomeOf falls back to the reply's `Op` when the
//     machine sends no words, so a refusal seals as the bare token `error` -- and every
//     screen that renders the reply's message renders that token as half a sentence.
//   - THE FOREIGN IDENTITY AS AN INTERNAL FAULT. stampErrorClass defaults anything it does
//     not name to ErrClassInternal, whose copy is "please report it". An EXPIRED PAIRING
//     CODE is not an internal fault: it is the ordinary end of a ten-minute rendezvous, and
//     the user's action is to ask their machine for a fresh code.
//   - THE SPECIFIC CAUSE AS THE GENERIC ONE. mobile/pairing.go already authors three
//     different sentences for the three ways a typed pairing entry can be wrong; all three
//     were stamped ErrClassPairingFailed, whose routed copy is "The pairing call itself
//     failed. Start the pairing again from your machine's code." -- advice that is wrong for
//     all three, because none of them ever reached a call.
//
// Every assertion here is about the CLASS or the COPY, never about a Go chain's text: the
// chain is the thing this bead is removing from the screen.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// ---- the wire token as copy -------------------------------------------------------------

// TestOutcomeOf_ASilentRefusalNeverSpeaksTheWireOp.
//
// `remotegw.refusePushPrefs` seals a refusal with neither code nor words, in its own words
// because "none of the six in the taxonomy describes a machine-side custody failure". The
// fallback made the phone say the reply's Op instead -- so the user read `error`, or, on a
// severed lease, `detach`. Both are protocol tokens; neither is a reason, and both are then
// interpolated into a screen sentence as its second half.
func TestOutcomeOf_ASilentRefusalNeverSpeaksTheWireOp(t *testing.T) {
	for _, op := range []string{"error", "detach", "ok"} {
		got := outcomeOf(schema.Control{Op: op, OperationID: "op-1"})
		if got.Message == op {
			t.Errorf("outcomeOf(Op=%q).Message = %q: the wire op became the machine's reason, "+
				"and every screen that renders a refusal renders that token as half a sentence",
				op, got.Message)
		}
		if got.Message != machineGaveNoReason {
			t.Errorf("outcomeOf(Op=%q).Message = %q, want %q -- a reply that carried no words "+
				"must say so in words", op, got.Message, machineGaveNoReason)
		}
	}
}

// The machine's own words survive untouched; the fallback is only for silence.
func TestOutcomeOf_TheMachinesOwnWordsAreNotReplaced(t *testing.T) {
	got := outcomeOf(schema.Control{Op: "error", OperationID: "op-1", Error: "kill switch off"})
	if got.Message != "kill switch off" {
		t.Fatalf("outcomeOf dropped the machine's reason: %q", got.Message)
	}
}

// ---- the foreign identity as an internal fault ------------------------------------------

// TestStampErrorClass_TheRelaySentinelsAPairingCanEndWithAreNotInternalFaults.
//
// All three are ordinary ends of a pairing ceremony that the relay names precisely, and all
// three landed in stampErrorClass's default arm -- ErrClassInternal, remedy report_bug. A
// user whose ten-minute code expired was told the app had hit an internal fault and asked to
// file a bug.
//
// ErrConsentRetired is the one that is NOT pairing-failed: the relay's own sentence for it
// ends "pair the device again", which is REVOKED's remedy and not a retry of this attempt.
func TestStampErrorClass_TheRelaySentinelsAPairingCanEndWithAreNotInternalFaults(t *testing.T) {
	cases := []struct {
		sentinel error
		want     string
		why      string
	}{
		{
			relay.ErrRendezvousExpired, ErrClassPairingFailed,
			"a rendezvous outlives its TTL on every code the user was slow to type; it is the " +
				"most ordinary pairing ending there is",
		},
		{
			relay.ErrRendezvousBurned, ErrClassPairingFailed,
			"a single-use rendezvous claimed twice is a user pressing the button again, not a " +
				"fault in the app",
		},
		{
			relay.ErrConsentRetired, ErrClassRevoked,
			"the relay's own sentence for it ends `pair the device again`, which is the revoked " +
				"remedy; classing it as a pairing retry sends the user back to a ceremony the " +
				"relay has already retired",
		},
	}
	for _, c := range cases {
		stamped := stampErrorClass(c.sentinel)
		got := classifyMessage(stamped.Error())
		if got == c.want {
			continue
		}
		t.Errorf("stampErrorClass(%v) classified as %q, want %q.\n\t%s", c.sentinel, got, c.want, c.why)
	}
}

// TestStampErrorClass_TheRelaySentinelsAreMatchedByIdentityAndNotByText.
//
// Both halves of one property. `pairing.go` wraps every relay failure ("pairing: claim
// rendezvous: %w"), so an arm that only matched a BARE sentinel would never fire in
// production -- and an arm that matched the sentinel's TEXT would be the prose matching
// errorclass.go's header rejects, where every reword of a relay error becomes a silent
// misroute.
func TestStampErrorClass_TheRelaySentinelsAreMatchedByIdentityAndNotByText(t *testing.T) {
	wrapped := fmt.Errorf("pairing: claim rendezvous: %w", relay.ErrRendezvousExpired)
	if got := classifyMessage(stampErrorClass(wrapped).Error()); got != ErrClassPairingFailed {
		t.Errorf("a %%w-wrapped ErrRendezvousExpired classified as %q, want %q: every production "+
			"site wraps, so an arm that only matched the bare sentinel would never fire",
			got, ErrClassPairingFailed)
	}

	lookalike := errors.New("pairing: claim rendezvous: " + relay.ErrRendezvousExpired.Error())
	if got := classifyMessage(stampErrorClass(lookalike).Error()); got == ErrClassPairingFailed {
		t.Error("an error with the sentinel's TEXT and none of its identity was routed as the " +
			"sentinel; that is prose matching, and it turns every reword of a relay error into " +
			"a silent misroute")
	}
}

// ---- the specific cause as the generic one ----------------------------------------------

// TestPayloadFromShortCode_TheThreeCausesAreThreeClasses.
//
// The sentences already existed at these three sites; what did not exist was a way for the
// screen to tell them apart, because all three carried ErrClassPairingFailed and the router
// has exactly one row for it. So a typo in a ten-character code, a phone that has never seen
// a relay, and an address typed in the wrong shape all read "The pairing call itself failed.
// Start the pairing again from your machine's code." -- which is wrong for all three: no call
// was ever made.
func TestPayloadFromShortCode_TheThreeCausesAreThreeClasses(t *testing.T) {
	cases := []struct {
		name        string
		code        string
		relayURL    string
		want        string
		whatItMeans string
	}{
		{
			"a typo in the ten characters", "not a code", "wss://relay.example:8443",
			ErrClassPairingCodeInvalid,
			"the code is retyped from the machine's own screen; nothing about the pairing needs " +
				"restarting",
		},
		{
			"no relay known and none typed", "K73-M2QF-9TD", "",
			ErrClassRelayUnknown,
			"the phone is missing an address, and the two ways to give it one are the scan and " +
				"the paste",
		},
		{
			"an address in the wrong shape", "K73-M2QF-9TD", "relay.example:8443",
			ErrClassRelayAddressInvalid,
			"the shape is the whole message: a bare host:port is what a person copies off a " +
				"terminal by eye",
		},
	}
	for _, c := range cases {
		_, err := payloadFromShortCode(c.code, c.relayURL)
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if got := classifyMessage(err.Error()); got != c.want {
			t.Errorf("%s: classified as %q, want %q.\n\t%s", c.name, got, c.want, c.whatItMeans)
		}
	}
}

// The three classes are three, and the fence is that they never share a token: a class whose
// token duplicated another's would route to the other's row and the taxonomy would still pass.
func TestTheThreePairingEntryClassesAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, class := range []struct{ name, token string }{
		{"ErrClassPairingCodeInvalid", ErrClassPairingCodeInvalid},
		{"ErrClassRelayUnknown", ErrClassRelayUnknown},
		{"ErrClassRelayAddressInvalid", ErrClassRelayAddressInvalid},
		{"ErrClassPairingFailed", ErrClassPairingFailed},
	} {
		if prev, dup := seen[class.token]; dup {
			t.Errorf("%s and %s share the token %q; the router keys on the token, so one of them "+
				"is unreachable and its copy is never seen", prev, class.name, class.token)
		}
		seen[class.token] = class.name
		if !strings.HasPrefix(class.token, "swarm/") {
			t.Errorf("%s = %q: every class token this facade authors is namespaced, so a foreign "+
				"message cannot collide with one", class.name, class.token)
		}
	}
}

// ---- the skew chain as a banner ---------------------------------------------------------

// TestClockBannerText_IsCopyAndNotThePhonecoreChain.
//
// mobile/relay.go passed `err.Error()` straight into the clock event and into the
// App.ClockVerdict pull surface, and dev.swarm.phone.ui.ConnectionUi renders what it is given
// -- so the banner above every screen read
//
//	phonecore: this device's clock is out of sync with the machine: measured 1m45.3018s off
//	(machine minus phone), outside the +/-30s budget
//
// A package prefix, a signed duration at full time.Duration precision, and the phrase
// "machine minus phone", which is the SIGN CONVENTION of the measurement rather than
// anything a person needs. What the reader needs is the size of the problem and the one
// setting that fixes it, and the offset is right there in phonecore.Skew to say the first
// with.
func TestClockBannerText_IsCopyAndNotThePhonecoreChain(t *testing.T) {
	got := clockBannerText(phonecore.Skew{Offset: 105 * time.Second, RTT: 40 * time.Millisecond, Known: true})

	want := "This phone's clock is about 105 seconds off your machine's -- too far to send " +
		"commands safely. Turn on automatic date and time in Android settings."
	if got != want {
		t.Errorf("clockBannerText = %q,\nwant %q", got, want)
	}
	for _, leak := range []string{"phonecore", "machine minus phone", "budget", "+/-"} {
		if strings.Contains(got, leak) {
			t.Errorf("the banner carries %q, which is the Go chain reaching the screen", leak)
		}
	}
}

// THE SIGN IS NOT THE USER'S. Offset is MACHINE MINUS PHONE, so a phone running AHEAD
// measures negative -- and "about -105 seconds off" is a sign convention rendered as copy.
// Both directions are the same fact to the reader: the clock is wrong by that much.
func TestClockBannerText_APhoneRunningAheadReadsTheSameAsOneRunningBehind(t *testing.T) {
	ahead := clockBannerText(phonecore.Skew{Offset: -105 * time.Second, Known: true})
	behind := clockBannerText(phonecore.Skew{Offset: 105 * time.Second, Known: true})
	if ahead != behind {
		t.Errorf("a phone 105s fast reads %q and one 105s slow reads %q; the sign of a bracket "+
			"the user never sees is not a difference they can act on", ahead, behind)
	}
}

// NO MEASUREMENT, NO NUMBER. SkewMonitor.Check can only fail after a completed bracket, so
// this is defence rather than a reachable state -- and the defence has to be the sentence
// WITHOUT the figure, never a figure of zero, which would tell a user with a broken clock
// that it is exactly right.
func TestClockBannerText_WithoutAMeasurementTheSentenceKeepsItsRemedy(t *testing.T) {
	got := clockBannerText(phonecore.Skew{})
	if strings.Contains(got, "0 seconds") || strings.Contains(got, "about") {
		t.Errorf("clockBannerText with no measurement = %q: it invented a figure", got)
	}
	if !strings.Contains(got, "automatic date and time") {
		t.Errorf("clockBannerText with no measurement = %q: the remedy is the half that must "+
			"survive, because it is the only thing the reader can act on", got)
	}
}
