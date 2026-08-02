package conformance_test

// PB-APP-10's THIRD state, added by the 2026-07-25 amendment: paired, keyless, and NOT
// terminal.
//
// WHY IT IS A SEPARATE FILE FROM s16_errorstates_test.go. That file is the RED author's and
// carries the other two states; this state was added to the requirement after it was written,
// and the coverage is the implementer's. Putting it there would put a later amendment inside an
// earlier author's test names. It asserts against the same taxonomy and the same production
// paths.
//
// THE STATE THE AMENDMENT NAMES. A phone that has just paired holds no epoch content key: the
// key arrives as a machine-sealed bootstrap grant on the mailbox (PB-KEY-10), which the gateway
// appends once per session from its persistent sidecar. Until it lands, every send is refused --
// and before this slice the refusal was errNoContentKey, whose advice ("call InstallContentKey
// after unlocking") nothing in production can act on, because InstallContentKey is called from
// Kotlin and Kotlin has no source for the bytes.
//
// The design deliberately refuses to call this terminal, and correctly: the gateway re-appends
// its sidecar every session, so the condition self-heals and the remedy is WAITING rather than a
// re-grant. But nothing required the phone to SAY so, which left the first-pairing window and a
// permanently lost key indistinguishable on screen -- the failure loop PB-APP-10 forbids in its
// other half, reached from the other direction.
//
// NOTHING HERE RE-LITIGATES PB-KEY-3, which S10 owns. The detector is untouched and is not
// exercised: this state is defined by the grant channel NOT being marked, so the assertion is
// that the phone distinguishes "not yet" from "never".

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// TestPBAPP10_APairedKeylessPhoneIsToldToWaitRatherThanToActOnTheMachine.
//
// TerminalWatch is the probe, and the choice is load-bearing. It is an UNSIGNED read, so it
// reaches the send path WITHOUT crossing PB-SYNC-7's fail-closed reconcile gate -- and that gate
// refuses before the key is ever consulted, so a mutating verb here would measure a different
// requirement entirely. It is also the same probe PB-PAIR-5's different-machine case uses, for
// the same reason.
func TestPBAPP10_APairedKeylessPhoneIsToldToWaitRatherThanToActOnTheMachine(t *testing.T) {
	ctx, relayURL, _, open := s10FreshInstall(t)
	m := newS10Machine(t, ctx, relayURL)

	app := open()
	runPairing(t, app, m)

	// PAIRED AND KEYLESS, with nothing installed by hand: the pairing pinned the machine and
	// the bootstrap grant has simply not been delivered yet. This is the first-pairing window
	// every real handset passes through.
	sum, err := app.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary: %v", err)
	}
	if !sum.Restored {
		t.Fatalf("precondition: the pairing pinned no destination, so this phone is not paired "+
			"and the state under test is not the one being measured (summary %+v)", sum)
	}

	werr := app.TerminalWatch(testMachineID + "/sess-awaiting")
	if werr == nil {
		t.Fatalf("precondition: a phone that has never received a bootstrap grant sent a command. "+
			"Either the grant arrived (it should not have -- nothing enrolled) or the send path "+
			"stopped requiring a content key; either way the state under test is unreachable and "+
			"every assertion below is vacuous (summary %+v)", sum)
	}

	// NOT THE TERMINAL ONE. This is the whole amendment: both states are keyless and only one
	// is terminal, so a phone in the ordinary first-pairing window must not be telling its user
	// that the machine has to re-grant -- advice they cannot act on, for a condition that is
	// about to resolve itself.
	if errors.Is(werr, phonecore.ErrGrantLost) {
		t.Errorf("PB-APP-10: a phone in the first-pairing window reports PB-KEY-3's TERMINAL "+
			"grant-loss identity:\n\t%v\n"+
			"The gateway re-appends its bootstrap sidecar once per session, so this condition "+
			"heals on its own and the remedy is waiting. Reporting it as grant loss sends a user "+
			"with a perfectly good handset to the machine, and makes the first-pairing window "+
			"indistinguishable on screen from a key that is permanently gone", werr)
	}

	// AND IT CLASSIFIES AS ITS OWN STATE. A class the taxonomy does not map reaches the screen
	// as an exception message, which is the failure PB-APP-9 exists to remove.
	classify := s16Lookup(t, app, "ErrorClass", "(string) (string, error)", "PB-APP-10",
		"The keyless answers -- wait, pair again, the machine must re-grant -- have to be "+
			"distinguishable from Kotlin, not merely inside Go.")
	class, cerr := s16StringErr(t, classify.Call([]reflect.Value{reflect.ValueOf(werr.Error())}))
	if cerr != nil {
		t.Fatalf("App.ErrorClass: %v", cerr)
	}
	tokens, unknown := s16TaxonomyTokens(t)
	if class == unknown || tokens[class] == "" {
		t.Fatalf("PB-APP-10: the waiting state classified as %q, which is unknown or unmapped", class)
	}

	// DISTINCT FROM EVERY OTHER KEYLESS ANSWER, asserted against the classes those identities
	// resolve to rather than against literals, so a rename cannot silently satisfy it.
	//
	// THE AUTH-REQUIRED PROBE IS GONE WITH ITS CLASS (ADR-007 B133). KeyCustodyAuthRequired is
	// still the token Kotlin stamps, but the facade no longer has a class for it: the verdict
	// is classified as the PERMANENT one, which the second row here already probes. Left in, it
	// would have asserted only that an unclassifiable string differs from this one -- true of
	// any string, which is a probe that cannot fail.
	for _, wrong := range []struct{ msg, what string }{
		{swarmmobile.ErrClassGrantLost + ": the machine must re-grant", "grant loss (the MACHINE must act)"},
		{swarmmobile.KeyCustodyKeyInvalidated + ": key gone", "the PERMANENT custody refusal (re-pair)"},
	} {
		other, oerr := s16StringErr(t, classify.Call([]reflect.Value{reflect.ValueOf(wrong.msg)}))
		if oerr != nil {
			t.Fatalf("App.ErrorClass: %v", oerr)
		}
		if other == class {
			t.Errorf("PB-APP-10: waiting for the first grant and %s both classify as %q. The other "+
				"keyless answers ask the user -- or the machine -- to DO something and this one "+
				"asks them to wait; collapsed, the one state that resolves itself is rendered as "+
				"one that never will", wrong.what, class)
		}
	}

	// AND IT CLEARS ON ITS OWN. No user action, no verb the UI has to know to call: the grant
	// the gateway was always going to append arrives and the phone can send.
	m.enrollAndDeliver()
	eventually(t, "the waiting state never cleared once the bootstrap grant arrived, so it is a "+
		"state the app never leaves -- which is the failure loop this requirement forbids, "+
		"reached by the remedy rather than by the fault", func() bool {
		return app.TerminalWatch(testMachineID+"/sess-awaiting") == nil
	})
}
