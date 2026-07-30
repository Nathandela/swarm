package gate

// PB-SEC-2's TIMED tier, fenced at the property its per-use sibling already has: that a prompt
// which is ON SCREEN is REGISTERED, so an invalidation has something to find.
//
// THE FINDING THIS IS WRITTEN AGAINST (ADR-007 B96, third entry). ADR-007 B63 closed the
// stale-callback class by giving every prompt an identity -- `PromptTicket` -- minted before the
// platform prompt is shown and presented back by the callback it produces. That fix was applied
// to `PerUseGate` and to nothing else. The timed tier's own prompt path,
// `PhoneSurface.reauthorizeTimedTier`, called `BiometricPrompts.confirmForContent` with NO TICKET
// REGISTERED: the ledger entry was created INSIDE the callback by `grantTimedTier`, which ran
// `beginPrompt` and `endPrompt` back to back on arrival. `promptForContent` had the same shape.
//
// Two consequences, and the second is the one that matters:
//
//	(1) `AuthorizationLedger.beginPrompt` was never asked, so `ConcurrentPromptPolicy.REFUSE_SECOND`
//	    did not apply to this path at all. A per-use prompt on screen did not stop a second,
//	    timed BiometricPrompt being raised over it -- which is the state `BiometricPolicy` says
//	    either replaces the first or throws.
//	(2) AN INVALIDATION HAD NOTHING TO CLEAR. `ContentLockTriggers` invalidates on
//	    ACTION_SCREEN_OFF and on the app going to background; `AuthorizationLedger.invalidate`
//	    empties the in-flight marker and every grant. For a prompt registered nowhere that is a
//	    no-op, and the prompt survives behind the keyguard -- nothing calls
//	    `BiometricPrompt.cancelAuthentication` and the callback lands on the main executor. So
//	    the late success ran `grantTimedTier` and MINTED A FRESH SIXTY-SECOND AUTHORIZATION FOR
//	    THE WHOLE TIER, AFTER the lock that was supposed to have ended it, and then ran the held
//	    action. ADR-007 B44 says the lock destroyed that authority.
//
// The class was recorded as closed. It was closed at one of its sites.
//
// WHAT THIS FILE FENCES, AND WHAT IT DOES NOT -- stated first because the natural objection is
// that a source scan cannot see a lifecycle.
//
// It cannot, and it does not claim to. THE RUNTIME PROPERTY IS CARRIED BY A KOTLIN UNIT TEST,
// `TimedTierGateTest`, on the JVM, over seams: that the ledger holds an in-flight ticket while
// the platform prompt is unanswered, that an invalidation arriving in that window makes the late
// success authorize NOTHING and run NO action, and that a second prompt supersedes the first.
// Those are behavioural assertions and they fail if the registration moves back inside the
// callback.
//
// What is left for a source scan is exactly what a unit test cannot see, and it is the half
// ADR-007 B51 was: WHO REACHES WHAT. A perfectly correct gate class with a screen calling round
// it is green in every Kotlin test that exists, because the screen is not testable on this tier
// (ADR-007 B56 puts androidTest out of reach, and s20_pbsec2_peruse_test.go's check (4) forbids
// a Robolectric shadow standing in for a BiometricPrompt). So the checks below ask only:
//
//	(1) the platform's content prompt is shown from exactly ONE production file, and that file
//	    mints a prompt identity and registers it before showing anything.
//	(2) NOTHING IN PRODUCTION RECORDS AN AUTHORIZATION WITHOUT NAMING THE PROMPT IT BELONGS TO.
//	    `AuthorizationLedger.endPrompt` is the only way a grant is made; every production file
//	    that calls it must also mint a `PromptTicket`. This is the check that fails at the tree
//	    B96 was written against, and it fails on `PhoneSurface.kt` -- a screen minting
//	    authorizations out of a callback it never registered.
//
// It reads checked-in source only: no Android SDK, no JDK, no Gradle, no emulator, no handset.
// This file never skips.

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// contentPromptCall is the platform prompt that re-opens the timed tier -- `BiometricPrompt`
// with no CryptoObject, which is what a key carrying `setUserAuthenticationParameters(60,
// AUTH_BIOMETRIC_STRONG)` needs and all it needs.
//
// It is matched by the METHOD NAME DECLARED IN THE FILE EXCLUDED BELOW, so a rename renames the
// declaration too and this check follows it to whatever it is called next -- unlike a match on a
// call-site receiver, which a renamed local defeats while the call stands.
//
// THERE IS NO OPENING PARENTHESIS ON IT, and that is not sloppiness. The first draft of this
// file matched `confirmForContent(` and reported NO CALLERS AT ALL against the very tree ADR-007
// B96 was written about -- because the method's one argument is a lambda and both call sites use
// Kotlin's trailing-lambda form, `prompts.confirmForContent { outcome -> ... }`. The check would
// have passed vacuously on the defect it exists to find, on a punctuation mark. A needle that a
// language's ordinary call syntax defeats is the fence failure this round has already produced
// twice.
const contentPromptCall = "confirmForContent"

// contentPromptDeclaredIn declares it, and is excluded from every search. Without the exclusion
// the confinement check below would count the declaration as a caller and pass over a tree with
// no callers at all.
const contentPromptDeclaredIn = "BiometricPrompts.kt"

// grantCall is the ONLY way an authorization is recorded. `AuthorizationLedger.endPrompt` writes
// `grantedAtMillis`, and ADR-007 B63 spent a fix making the ledger refuse every other route in.
const grantCall = ".endPrompt("

// ticketMint is a prompt identity coming into existence. It is a constructor call on a plain
// class whose whole mechanism is object identity (`PromptTicket`), so there is no factory and no
// other way to obtain one: a caller that has a ticket wrote this.
const ticketMint = "PromptTicket("

// ledgerRegistration is the ledger being TOLD a prompt is going on screen.
const ledgerRegistration = ".beginPrompt("

// ticketDeclaredIn declares both the ticket and the ledger, and is excluded for the same reason
// contentPromptDeclaredIn is.
const ticketDeclaredIn = "BiometricPolicy.kt"

// firstCallOf reports the byte offset of the first CALL of a Kotlin method in already
// comment-stripped source, or -1. A call is an occurrence of the name that is neither a
// DECLARATION (`fun name`) nor a LAMBDA LABEL (`return@name`, `break@name`).
//
// BOTH EXCLUSIONS ARE THINGS THAT ACTUALLY HAPPENED HERE. The first draft of the confinement
// check treated any occurrence as a call, and reported the timed gate's own seam -- `interface
// TimedTierPrompt { fun confirmForContent(...) }`, declared beside the gate exactly as
// `PerUsePrompt` is declared beside `PerUseGate` -- as a call at byte 172, ahead of the
// registration that really does precede the one call in the file. A check that cannot tell a
// declaration from a call reports the file that DEFINES a seam as the file that USES it, which
// would let the real caller sit anywhere.
func firstCallOf(src, name string) int {
	for at := 0; ; {
		i := strings.Index(src[at:], name)
		if i < 0 {
			return -1
		}
		i += at
		prefix := src[:i]
		if !strings.HasSuffix(prefix, "fun ") && !strings.HasSuffix(prefix, "@") {
			return i
		}
		at = i + len(name)
	}
}

// callersOf returns the files whose CODE calls name, excluding the file with the given base name.
func callersOf(code map[string]string, name, excludeBase string) []string {
	var out []string
	for f, src := range code {
		if filepath.Base(f) == excludeBase {
			continue
		}
		if firstCallOf(src, name) >= 0 {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// TestPBSEC2_TheContentPromptIsRegisteredBeforeItIsShown.
//
// THE MUTATION THAT MUST FAIL IT: moving the `confirmForContent` call back onto a screen, or
// adding a second caller beside the gate. Both are the shape B96 found.
//
// WHAT IT CANNOT SEE, recorded rather than assumed away: source order is not execution order.
// Clause (c) reads the two calls' positions in the file, which is a smoke check and not a proof
// -- a file could declare its registration helper above its prompt helper and satisfy it while
// calling them in the wrong order. The ORDER IS PROVEN AT RUNTIME BY `TimedTierGateTest`, which
// asserts the ledger refuses a concurrent prompt while the platform prompt is still unanswered;
// that assertion is false unless the registration really did happen first.
func TestPBSEC2_TheContentPromptIsRegisteredBeforeItIsShown(t *testing.T) {
	code := productionKotlinCode(t)

	callers := callersOf(code, contentPromptCall, contentPromptDeclaredIn)
	if len(callers) == 0 {
		t.Fatalf("PB-SEC-2: no production Kotlin outside %s calls %q, so this check has no "+
			"subject. Either the content tier lost its way back in -- ADR-007 B44's missing exit, "+
			"reopened -- or the prompt was renamed, and both need this fence updated rather than "+
			"passing quietly", contentPromptDeclaredIn, contentPromptCall)
	}

	// (a) CONFINEMENT. One caller, so there is one lifecycle to reason about. Two callers is how
	// the per-use tier and the timed tier came to disagree about what a prompt is in the first
	// place.
	if len(callers) > 1 {
		var names []string
		for _, f := range callers {
			names = append(names, filepath.Base(f))
		}
		sort.Strings(names)
		t.Errorf("PB-SEC-2: %d production files show the platform's content prompt: %s\n"+
			"It must be shown from ONE place, which owns the whole lifecycle -- mint the ticket, "+
			"register it, show the prompt, resolve the callback against the ticket. A second "+
			"caller is a second lifecycle, and ADR-007 B96 is what the first divergence cost: the "+
			"per-use path got ADR-007 B63's identity fix and the timed path did not",
			len(callers), strings.Join(names, ", "))
	}

	for _, f := range callers {
		src := code[f]
		base := filepath.Base(f)

		// (b) IDENTITY. A prompt shown without a ticket is a prompt an invalidation cannot find
		// and a late callback cannot be refused for.
		if !strings.Contains(src, ticketMint) {
			t.Errorf("PB-SEC-2: %s shows the platform's content prompt and never mints a %s\n"+
				"This is ADR-007 B96 exactly. A prompt registered nowhere is one that "+
				"`AuthorizationLedger.invalidate` cannot clear, so a screen lock arriving while it "+
				"is on screen empties a ledger the prompt was never in -- and the queued success "+
				"then mints a fresh sixty-second authorization for the whole timed tier, AFTER the "+
				"lock ADR-007 B44 says destroyed that authority, and runs the action it was "+
				"holding",
				base, ticketMint)
			continue
		}

		// (c) ORDER, as far as text can see it. See the doc comment: the proof is in
		// TimedTierGateTest, and this only catches a registration that is not even written above
		// the prompt it belongs to.
		registration := firstCallOf(src, ledgerRegistration)
		shown := firstCallOf(src, contentPromptCall)
		if registration < 0 {
			t.Errorf("PB-SEC-2: %s shows the platform's content prompt and never calls %s\n"+
				"The ticket must be handed to the ledger, not merely constructed: it is "+
				"`beginPrompt` that puts it in flight, and the in-flight marker is what "+
				"`invalidate` clears and what refuses a second concurrent prompt",
				base, ledgerRegistration)
			continue
		}
		if registration > shown {
			t.Errorf("PB-SEC-2: in %s the first %s appears at byte %d, AFTER the first %s at byte "+
				"%d\nThe ticket goes in flight BEFORE the platform is asked to show anything. "+
				"Registering afterwards -- or from inside the callback, which is what ADR-007 B96 "+
				"found -- leaves the whole time the prompt is actually on screen unregistered, "+
				"and that window is the entire defect: it is exactly when the screen locks",
				base, ledgerRegistration, registration, contentPromptCall, shown)
		}
	}
}

// TestPBSEC2_NoProductionCodeMintsAnAuthorizationWithoutAPromptIdentity.
//
// THE CHECK THAT FAILS AT THE TREE ADR-007 B96 WAS WRITTEN AGAINST, and it fails on
// `PhoneSurface.kt`: a screen calling `beginPrompt`/`endPrompt` back to back inside a biometric
// callback it never registered, minting a sixty-second grant for both timed operations on the
// strength of a `PromptOutcome` enum.
//
// WHY THE PAIRING IS THE PROPERTY. `endPrompt` is the only writer of an authorization. Its
// `ticket` parameter is the identity of the prompt the callback belongs to, and a call that
// omits it is discriminated by its OPERATION alone -- which is precisely what cannot tell two
// prompts for the same operation apart, the half ADR-007 B63 recorded as still open and
// `PromptTicket` closes. So a file that records authorizations and has no prompt identity
// anywhere in it is, by construction, resolving callbacks it cannot identify.
//
// WHAT IT CANNOT SEE: it matches TEXT, not types, and it asks whether a ticket EXISTS in the
// file rather than whether the right one reaches the right call -- a `PromptTicket` constructed
// and dropped on the floor satisfies it. That residual is why the gates' callback ordering is
// asserted at runtime instead: `PerUseGateTest` and `TimedTierGateTest` each drive a prompt,
// invalidate under it, and require the late success to authorize nothing.
func TestPBSEC2_NoProductionCodeMintsAnAuthorizationWithoutAPromptIdentity(t *testing.T) {
	code := productionKotlinCode(t)

	minters := filesNaming(code, grantCall, ticketDeclaredIn)
	if len(minters) == 0 {
		t.Fatalf("PB-SEC-2: no production Kotlin outside %s calls %q, so this check has no "+
			"subject -- and PB-SEC-2's whole gate would have no production caller, which is "+
			"ADR-007 B51's finding returning", ticketDeclaredIn, grantCall)
	}

	var blind []string
	for _, f := range minters {
		if !strings.Contains(code[f], ticketMint) {
			blind = append(blind, filepath.Base(f))
		}
	}
	sort.Strings(blind)
	if len(blind) > 0 {
		t.Errorf("PB-SEC-2: %d production file(s) record an authorization and name no prompt "+
			"identity at all:\n\t%s\n"+
			"%s is the only writer of a grant, and its `ticket` argument is what says WHICH prompt "+
			"the callback resolving it belongs to. Without one the call is discriminated by its "+
			"operation alone, so a queued callback from a prompt that an invalidation or a second "+
			"request already superseded resolves against whatever is in flight now -- or, when "+
			"the caller never registered anything, against nothing at all, and mints a fresh "+
			"authorization on the strength of a UI event. That is ADR-007 B96: the timed tier "+
			"kept the shape ADR-007 B63 removed from the per-use tier, and the class was recorded "+
			"as closed",
			len(blind), strings.Join(blind, "\n\t"), grantCall)
	}
}
