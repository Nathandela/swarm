package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Wave R6 review ROUND 2's MINOR finding on B9: the
// composer-echo window's MECHANISM is well fenced and its VALUE is not.
//
// THE MUTATION THAT SURVIVED. The reviewer changed `pendingSendTTL` from `10 * time.Second`
// to `1000 * time.Hour` and both existing fences stayed GREEN --
// TestR6Fix_AnExpiredInjectionNoLongerClaimsAnIdenticalOwnerPrompt and its anti-vacuity
// control TestR6Fix_AFreshInjectionStillClaimsItsEcho -- because the test advances the fake
// clock by `pendingSendTTL + time.Second`, which is RELATIVE TO THE CONSTANT IT IS MEASURING.
// A window of a thousand hours is indistinguishable from no window at all, and
// docs/specifications/protocol.md and docs/verification/r6-chat.md's CANNOT YET (iv) both
// promise "10 s" to a reader who cannot check it.
//
// This is the one assertion that closes it, and it is deliberately a CEILING rather than an
// equality: the value may be tuned, and what may not change without a document changing with
// it is the property the promise rests on. That property is written in pendingSendTTL's own
// doc -- "a human who walks away and types the same word at the terminal a minute later is
// never mis-attributed to the phone" -- so one minute is the largest value that sentence can
// survive, and any larger one is a text-correlation window long enough to invent a fact.

import (
	"testing"
	"time"
)

// composerEchoCeiling is the largest window pendingSendTTL's own stated trade survives. See
// the file comment.
const composerEchoCeiling = time.Minute

func TestR6R2_TheComposerEchoWindowIsBoundedByAValueAndNotOnlyByAMechanism(t *testing.T) {
	if pendingSendTTL <= 0 {
		t.Fatalf("pendingSendTTL = %v; a non-positive window expires every injection before "+
			"its echo can arrive, so no phone send is ever attributed to the phone", pendingSendTTL)
	}
	if pendingSendTTL > composerEchoCeiling {
		t.Errorf("pendingSendTTL = %v, above the %v ceiling. Text correlation is what stamps "+
			"`source: phone` on an item, and a window this long is indistinguishable from the "+
			"unbounded one finding B9 was raised against: an owner who types the same short "+
			"word at the terminal inside it is credited to the phone, which INVENTS a fact. "+
			"protocol.md and docs/verification/r6-chat.md both promise the reader a bounded "+
			"window; if this value is meant to change, change them in the same commit",
			pendingSendTTL, composerEchoCeiling)
	}
}
