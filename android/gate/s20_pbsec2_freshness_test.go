package gate

// PB-SEC-2's TIMED tier, fenced at the property its per-use sibling already has: that the tier
// is REACHED.
//
// THE FINDING THIS IS WRITTEN AGAINST (ADR-007 B83(4), verified independently by two reviewers).
// Requirements 6.0 binds a 60-second freshness window to input and take_control, and adds a
// renewal clause: "a typing session crossing the 60 s freshness window must pause input and
// re-authorize, not silently continue or silently drop; the lease itself is not ended by
// freshness expiry". The policy for that exists and is complete -- `InputFreshness.decide`
// returns PAUSE_AND_REAUTHORIZE past the window, `InputFreshness.freshnessExpiryEndsLease` is
// false, and `BiometricGateTest` asserts both. It has ZERO PRODUCTION CALLERS.
//
// The consequence is not theoretical. `ContentLock` installs no foreground timeout and says so
// in its own header, so the only things that end content custody are a screen lock, a
// backgrounding and process death. An unlocked, continuously foregrounded session therefore
// retains shell-input authority for as long as it stays in front of the user -- against a
// requirement that bounds it at sixty seconds.
//
// THERE IS A SIBLING PATH AND IT COVERS ONE OF THE TWO OPERATIONS, checked rather than assumed,
// because a fence written over a property that is live by another route is a false positive.
//
//   - take_control, kill, launch and revoke are SIGNED, and `phonecore.sealedKeyStore.contentStore`
//     unseals the content tier PER OPERATION with nothing memoized -- verified by mutation: adding
//     a memo changes behaviour, so HEAD has none. That unseal is a real Keystore round trip
//     through a KEK carrying `setUserAuthenticationParameters(60, AUTH_BIOMETRIC_STRONG)`, so the
//     PLATFORM refuses these past the window. The bound is real for them, and it is not
//     `InputFreshness` that supplies it.
//   - KEYSTROKES ARE NOT COVERED. `App.resolveSend` (mobile/commands.go) consults the content KEK
//     only when the in-memory epoch content key is ZERO, which happens only after a purge; its own
//     comment says so ("a phone that holds the key answers from memory and never reaches here").
//     While the app stays foregrounded the key never leaves memory, so no Keystore round trip
//     occurs and nothing is bounded.
//
// SO THE FENCE IS SCOPED TO WHAT IS ACTUALLY UNENFORCED: the typing path, and the RENEWAL clause
// for both operations. Even where the platform does refuse, it refuses -- it does not pause a
// running typing session and ask for a fingerprint, which is what the requirement specifies and
// what `InputFreshness` exists to decide.
//
// Neither leg of the platform's own enforcement is proven anywhere in this repository: that a
// timed Keystore key refuses past its window is PB-E2E-5, DEFERRED (ADR-007 B31).
//
// WHY THIS IS A SOURCE CHECK AND NOT A KOTLIN UNIT TEST, stated because the natural objection is
// that a behavioural test would be better. There is no Kotlin runtime assertion that fails at
// HEAD, and that is a fact about the defect rather than a shortcut:
//
//   - the policy object is already correct, so every assertion over it PASSES. `decide` returns
//     the right verdict at 59_999 ms and at 60_000 ms today.
//   - `AuthorizationLedger.authorized` already expires a timed grant on the same window, so an
//     assertion there passes too -- and passes VACUOUSLY, because nothing in production ever
//     grants INPUT in the first place.
//   - the only remaining runtime observable would be a purge, and asserting one would PRESCRIBE
//     a fix the requirement does not ask for: 6.0 says pause and re-authorize, and says in the
//     same clause that the lease is not ended. `ContentLock` declined a foreground purge on its
//     merits and that decision is not this fence's to overturn.
//
// So what is left is exactly ADR-007 B51's shape -- a policy nothing consults is a policy the app
// does not have -- and B51's shape is fenced here, in Go, over checked-in Kotlin, for the reason
// s20_pbsec2_peruse_test.go gives: the defect is invisible from inside the module, because a
// policy object with no caller compiles, tests and lints green forever.
//
// IT PRESCRIBES NO MECHANISM, and one placement. Any production caller satisfies the first
// check -- a timer, a check at send time, a lease-renewal step, a screen state -- and the second
// asks only that the verdict be named in the file that sends, for the reason stated on it. What
// neither accepts is the present state, in which nothing anywhere asks.
//
// It reads checked-in source only: no Android SDK, no JDK, no Gradle, no emulator, no handset.
// This file never skips.

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// freshnessSubjects are the production symbols that carry requirements 6.0's timed window. A
// caller of ANY satisfies the check: `decide` is the decision, `InputGateDecision` is what it
// returns, and a fix that routes the verdict through its own type still names one of them.
//
// `freshnessExpiryEndsLease` is deliberately NOT here. It is a declaration that something must
// NOT happen, so it has nothing to call it, and requiring a caller would demand a lease teardown
// the requirement forbids.
//
// `TimedTierGate` JOINED THEM WHEN THE DECISION MOVED OFF THE SCREEN (ADR-007 B96). The verdict
// used to be reached by a private method of `PhoneSurface`, which is Activity-hosted and so
// unreachable from any test this project can run -- and B96's mutation, replacing that decision
// with `if (true)`, therefore left everything green. It now lives in a plain class the JVM tier
// drives (`TimedTierGateTest`), and the sending file names THAT. Naming the gate is not a
// weakening of the second check below: `InputFreshness` in a file proves the file knows the
// window exists, whereas the gate is the thing that ENFORCES it, so a file that names the gate is
// routed through the decision rather than merely acquainted with it.
var freshnessSubjects = []string{
	"InputFreshness",
	"InputGateDecision",
	"TimedTierGate",
}

// freshnessDeclaredIn is the file that DECLARES the subjects above. It is excluded from the
// search, which is the vacuity control the whole check rests on: without it, `InputFreshness`
// certainly appears in BiometricPolicy.kt and the fence would pass over the unreached state
// ADR-007 B83(4) found.
const freshnessDeclaredIn = "BiometricPolicy.kt"

// inputPathVerbs are the bound facade calls that requirements 6.0 windows at 60 seconds. They
// are matched with the `app.` receiver so an unrelated Kotlin method of the same name does not
// read as one of these.
//
// BOTH are here because 6.0's row names both: "60 s for input/take_control". take_control is the
// step that opens a typing session and input is the typing, and a window enforced on one of them
// is a window a user crosses by not letting go of the keyboard.
var inputPathVerbs = []string{
	"app.sendInput(",
	"app.takeControl(",
}

// TestPBSEC2_TheTimedFreshnessWindowIsReachedFromProductionKotlin.
//
// THE MUTATION THAT MUST FAIL IT once a fix lands: deleting the freshness call from whatever
// production file acquires one, which returns the tree to the state this test was written
// against.
//
// WHAT IT CANNOT SEE, recorded rather than left to be assumed away: it matches TEXT, not types,
// and a call inside a function nothing invokes satisfies it. That limit is why the second test
// below exists -- it is not a duplicate, it asks whether the call is on the INPUT PATH.
func TestPBSEC2_TheTimedFreshnessWindowIsReachedFromProductionKotlin(t *testing.T) {
	code := productionKotlinCode(t)

	var unreached []string
	for _, symbol := range freshnessSubjects {
		if len(filesNaming(code, symbol, freshnessDeclaredIn)) == 0 {
			unreached = append(unreached, symbol)
		}
	}
	if len(unreached) == len(freshnessSubjects) {
		t.Errorf("PB-SEC-2: requirements 6.0's 60-second window for input and take_control is "+
			"REACHED BY NOTHING. %s is referenced by no production Kotlin outside %s, so the "+
			"renewal clause -- pause input and re-authorize on crossing the window -- is decided "+
			"by a policy object with no caller.\n"+
			"`ContentLock` installs no foreground timeout by its own admission, so nothing else "+
			"bounds it either: an unlocked, continuously foregrounded session keeps shell-input "+
			"authority for as long as it stays in front of the user. That is ADR-007 B51's shape "+
			"one requirement over -- a policy nothing consults is a policy the app does not have, "+
			"and the unit tests over it pass because their subject is unreachable",
			strings.Join(unreached, " and "), freshnessDeclaredIn)
	}
}

// TestPBSEC2_TheInputPathNamesTheFreshnessWindow closes the limit the test above states about
// itself: that a call in a corner of the app nothing types from would satisfy it.
//
// IT ASKS OF THE SENDING FILE, AND NOT OF THE MODULE, and the difference is what makes it a
// second question rather than a restatement of the first. A guard is worth something where the
// bytes leave; the check above is satisfied by a reference anywhere at all.
//
// A TRANSITIVE WALK WAS WRITTEN FIRST AND THROWN AWAY, recorded because it is the obvious way to
// write this and it does not work. Counting a file as guarded when it names a top-level symbol
// from a file that names the window makes almost every file in a module this size guarded:
// `PhoneSurface` constructs `SettingsSurface`, `PairingSurface` and half the runtime, so a
// freshness call dropped into any of those would have turned the fence green while the send path
// went on asking nothing. The direct form below refuses that, and the mutation run for this file
// is exactly it -- a call placed in `SettingsSurface.kt` satisfies the check above and must
// still fail this one.
//
// SO IT PRESCRIBES ONE THING AND ONLY ONE: that the verdict is named where the send is. It does
// not say which mechanism produces it, where the logic lives, or what the app does about it --
// a call to `decide`, a handler for an `InputGateDecision`, or a guard object of the
// implementer's own that the call site consults, all satisfy it. It is the same discipline the
// per-use tier already carries one file over, where every per-use verb must be declared through
// `perUseButton` at its own call site.
//
// WHAT IT CANNOT SEE: it matches TEXT, not types, and it cannot prove the decision is consulted
// BEFORE the bytes go out -- only that the sending file knows the window exists. A fix that
// deliberately keeps the verdict out of this file belongs in this fence as a stated exception
// with its reason, the way android/unbound-verbs.tsv records the same kind of choice, rather
// than silently.
func TestPBSEC2_TheInputPathNamesTheFreshnessWindow(t *testing.T) {
	code := productionKotlinCode(t)

	// The files that type. A verb with no call site at all is not silently tolerated: it would
	// mean this fence has no subject, which is how a check comes to guard nothing.
	callers := map[string][]string{}
	for _, verb := range inputPathVerbs {
		for _, f := range filesNaming(code, verb, "") {
			callers[f] = append(callers[f], verb)
		}
	}
	if len(callers) == 0 {
		t.Fatalf("PB-SEC-2: no production Kotlin calls any of %v, so this check has no subject. "+
			"Either the keyboard was removed from the app or the receiver these are matched with "+
			"changed; both need this fence updated rather than passing quietly", inputPathVerbs)
	}

	var unguarded []string
	for f, verbs := range callers {
		guarded := false
		for _, symbol := range freshnessSubjects {
			if strings.Contains(code[f], symbol) {
				guarded = true
				break
			}
		}
		if !guarded {
			sort.Strings(verbs)
			unguarded = append(unguarded, filepath.Base(f)+" calls "+strings.Join(verbs, ", "))
		}
	}
	sort.Strings(unguarded)
	if len(unguarded) > 0 {
		t.Errorf("PB-SEC-2: %d production file(s) send on the timed tier and name the freshness "+
			"window NOWHERE:\n\t%s\n"+
			"Requirements 6.0 bounds input and take_control at 60 seconds and requires a crossing "+
			"to pause and re-authorize. take_control is bounded by the platform anyway -- it is "+
			"signed, and the signature unseals the content tier per operation through a KEK with a "+
			"60-second window. KEYSTROKES ARE NOT: `App.resolveSend` consults that KEK only when "+
			"the in-memory epoch content key is zero, which happens only after a purge, so a "+
			"foregrounded session types on whatever authority the last unlock left behind and no "+
			"later event takes it away. And on neither operation does anything PAUSE and "+
			"re-authorize, which is the clause the requirement actually writes",
			len(unguarded), strings.Join(unguarded, "\n\t"))
	}
}

// ---------------------------------------------------------------------------
// The scan.
// ---------------------------------------------------------------------------

// productionKotlinCode is every production Kotlin file with its comments stripped, keyed by path.
//
// COMMENTS ARE STRIPPED because a KDoc sentence naming a symbol is not a caller, and this module
// documents itself heavily enough that the distinction decides the answer: `ContentLock.kt`'s own
// header discusses the foreground timeout it does not install, and `PerUseGate.kt` names the
// timed tier in prose. A fence a comment can satisfy is a fence the next thorough comment turns
// off. kotlinCodeOnly is s20_pbsec2_peruse_test.go's, for exactly this reason.
func productionKotlinCode(t *testing.T) map[string]string {
	t.Helper()
	files := kotlinFiles(t, kotlinMainRoot(t))
	if len(files) < 20 {
		t.Fatalf("PB-SEC-2: found %d production Kotlin files, which is too few to have read the "+
			"module. A scan that found nothing would report every subject unreached, and the "+
			"tempting repair is to relax the check", len(files))
	}
	code := map[string]string{}
	for _, f := range files {
		code[f] = kotlinCodeOnly(readFileOrFail(t, f, "PB-SEC-2"))
	}
	return code
}

// filesNaming returns the files whose CODE contains needle, excluding the file with the given
// base name. An empty exclusion excludes nothing.
func filesNaming(code map[string]string, needle, excludeBase string) []string {
	var out []string
	for f, src := range code {
		if excludeBase != "" && filepath.Base(f) == excludeBase {
			continue
		}
		if strings.Contains(src, needle) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}
