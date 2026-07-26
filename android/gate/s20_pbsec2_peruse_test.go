package gate

// PB-SEC-2's PER-USE tier, fenced at the one property every earlier check missed: that the
// tier is REACHED.
//
// WHAT WENT WRONG, because the fences here are shaped by it. ADR-007 B51 -- the ninth
// uncalled-symbol instance of the phase -- found that `KeystoreSpecs.forOperation`, the per-use
// `KeyGenParameterSpec` for revoke, kill switch, launch and kill, was referenced from `src/test/`
// alone; that `AuthorizationLedger.beginPrompt/endPrompt/consume` had no production caller; and
// that there was no `BiometricPrompt` anywhere in the app, androidx.biometric not being a
// dependency. `BiometricGateTest` was green throughout. It was green BECAUSE its subject was
// unreachable: every assertion it makes is about a policy object nothing consulted, so the four
// operations were gated by exactly what typing is gated by -- the content KEK's 60-second timed
// window -- which is the per-use-implemented-as-timed downgrade `BiometricPolicy.kt`'s own header
// says that file exists to make impossible.
//
// So a Kotlin unit test cannot close this. The defect is invisible from inside the module: a
// policy object with no caller compiles, tests and lints green forever. What follows reads the
// checked-in sources and asks who calls what.
//
// FOUR CHECKS, AND WHAT EACH WOULD MISS ALONE:
//
//	(1) the per-use SUBJECTS are reached from production Kotlin -- B51's finding, directly.
//	    Alone it would pass over a call in a function nothing invokes.
//	(2) every production call of a per-use FACADE VERB is declared through `perUseButton`.
//	    This is the one that binds the tier to the app: the gate can be perfectly wired and a
//	    second, ungated control can still reach `App.RevokeThisDevice` beside it.
//	(3) the prompt's allowed authenticators and the Keystore spec's agree. A prompt that
//	    allowed a device credential over a key requiring AUTH_BIOMETRIC_STRONG succeeds and
//	    then hands back a key the platform still refuses -- a prompt the user can satisfy that
//	    authorizes nothing. It is bidirectional so ADR-007 B57's decision cannot be half-changed.
//	(4) androidx.biometric stays confined to a named production file and reaches NO unit test.
//	    PB-E2E-5 is deferred (ADR-007 B31) and ADR-007 B56 makes the androidTest tier
//	    unexecutable outright; a Robolectric test driving a shadowed BiometricPrompt to
//	    "succeeded" proves the shadow returned what it was told to, and reads as proof the gate
//	    works.
//
// WHAT THESE CHECKS CANNOT SEE, recorded rather than left to be assumed away, because a limit
// written down is worth more than a check that overclaims:
//
//   - They match TEXT, not types. `s16_ui_test.go` and `boundverbledger_test.go` already accept
//     this limit for the same reason: narrowing it would need a Kotlin type checker. The
//     direction of the error is stated at each site.
//   - They say nothing about what happens on a handset. That a real BiometricPrompt appeared,
//     that a real finger was accepted or refused, that a real TEE withheld a real key: PB-E2E-5,
//     DEFERRED. Nothing here may be read as covering any of it.
//   - Check (2) proves the CONTROL is declared through the gate. It cannot prove the gate's
//     callback ordering is right; `PerUseGateTest` carries that half, on the JVM, over seams.
//
// It reads checked-in source only: no Android SDK, no JDK, no emulator, no handset. This file
// never skips.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// (1) The per-use subjects are reached from production Kotlin.
// ---------------------------------------------------------------------------

// perUseSubjects are the symbols ADR-007 B51 found referenced from `src/test/` alone, plus the
// gate that now carries them. Each maps to the file that DECLARES it, which is excluded from
// the search: a declaration is not a caller, and counting it is how "reached" becomes "exists".
var perUseSubjects = map[string]string{
	// B51's headline. The per-use KeyGenParameterSpec.
	"KeystoreSpecs.forOperation": "Provisioning.kt",
	// The alias table that gives each per-use operation its own Keystore entry.
	"KeystoreAliases.forOperation": "Custody.kt",
	// The three ledger verbs with no production caller.
	"beginPrompt": "BiometricPolicy.kt",
	"endPrompt":   "BiometricPolicy.kt",
	".consume(":   "BiometricPolicy.kt",
	// The gate itself, so this check fails the day it is deleted rather than passing quietly.
	"PerUseGate": "PerUseGate.kt",
}

// TestPBSEC2_ThePerUseSubjectsAreReachedFromProductionKotlin.
//
// THE MUTATION THAT MUST FAIL IT: deleting the `perUseButton` calls in PhoneSurface.kt and
// restoring the plain `gatedButton` ones -- which is precisely the state ADR-007 B51 found and
// which every Kotlin test in the module was green over.
//
// THE VACUITY CONTROL: the search excludes each symbol's own declaring file, so a symbol that
// is only declared -- never called -- fails. Without that exclusion the check would pass on the
// unreached codebase B51 audited, since `KeystoreSpecs.forOperation` certainly appears in
// Provisioning.kt.
func TestPBSEC2_ThePerUseSubjectsAreReachedFromProductionKotlin(t *testing.T) {
	files := kotlinFiles(t, kotlinMainRoot(t))
	if len(files) < 20 {
		t.Fatalf("PB-SEC-2: found %d production Kotlin files, which is too few to have read the "+
			"module. A scan that found nothing would report every subject unreached, and the "+
			"tempting repair is to relax the check", len(files))
	}

	var unreached []string
	for symbol, declaredIn := range perUseSubjects {
		reached := false
		for _, f := range files {
			if filepath.Base(f) == declaredIn {
				continue
			}
			if strings.Contains(readFileOrFail(t, f, "PB-SEC-2"), symbol) {
				reached = true
				break
			}
		}
		if !reached {
			unreached = append(unreached, symbol+" (declared in "+declaredIn+")")
		}
	}
	sort.Strings(unreached)
	if len(unreached) > 0 {
		t.Errorf("PB-SEC-2: %d per-use symbol(s) are referenced by NO production Kotlin outside "+
			"the file that declares them:\n\t%s\n"+
			"That is ADR-007 B51 exactly: the per-use tier described, tested and never reached, "+
			"so revoke and kill were gated by the content KEK's 60-second TIMED window like "+
			"ordinary typing. A policy nothing consults is a policy the app does not have, and "+
			"the unit tests over it pass because their subject is unreachable",
			len(unreached), strings.Join(unreached, "\n\t"))
	}
}

// ---------------------------------------------------------------------------
// (2) Every per-use facade verb is reached through the gate.
// ---------------------------------------------------------------------------

// perUseFacadeVerbs are the bound facade calls that carry requirements 6.0's per-use
// operations. They are matched with the `app.` receiver so an unrelated Kotlin method of the
// same name does not read as one of these.
//
// LAUNCH IS IN THE LIST AND HAS NO CALL SITE TODAY, and that is stated rather than left to be
// discovered. `App.Launch` is ledgered in android/unbound-verbs.tsv: PB-APP-6's launch screen
// does not exist on the minimal surface, so gating it in production would be a fence guarding a
// path production does not take -- the failure class this phase found ten times. What this row
// does instead is make the gate MANDATORY the day the screen lands.
//
// KILL_SWITCH IS DELIBERATELY ABSENT, and for a stronger reason than "no screen": the phone may
// never SET it. protocol/server.go handleRemoteSetControl refuses the remote tier before
// consulting its backend (PB-SEC-6), so there is no verb to gate -- `App.KillSwitchEngaged` is
// a READ. `ui/MachineAndLaunch.kt`'s GateFreshness table already records the same absence for
// the same reason. Requirements 6.0 lists it because the freshness table is about operations,
// not about which end performs them.
var perUseFacadeVerbs = []string{
	"app.revokeThisDevice(",
	"app.kill(",
	"app.launch(",
}

// perUseCallSiteFloor is the "cannot pass by measuring nothing" floor. Two per-use verbs have
// production call sites today (revoke, kill). A run that finds fewer has stopped reading the
// sources, and would report a clean bill of health over an empty question -- which is the
// defect class this whole file is about, applied to the file itself.
const perUseCallSiteFloor = 2

// kotlinDeclarationOf returns the `val`/`var`/`fun` declaration line that a given offset sits
// under: the nearest preceding line at the top level of a class body.
//
// It matches TEXT, and the limit is stated because it decides what the check can claim: a call
// nested inside a helper function declared elsewhere would be attributed to that helper's
// declaration line. The direction of the error is toward FAILING (the helper's line will not
// name perUseButton), which is the safe direction for a fence.
var kotlinMemberDecl = regexp.MustCompile(`(?m)^\s*(?:private\s+|internal\s+)?(?:val|var|fun)\s`)

func kotlinDeclarationOf(src string, offset int) string {
	locs := kotlinMemberDecl.FindAllStringIndex(src[:offset], -1)
	if len(locs) == 0 {
		return ""
	}
	start := locs[len(locs)-1][0]
	end := strings.Index(src[start:], "\n")
	if end < 0 {
		return src[start:]
	}
	return src[start : start+end]
}

// TestPBSEC2_EveryPerUseFacadeVerbIsReachedThroughThePerUseButton.
//
// THE MUTATION THAT MUST FAIL IT: changing `perUseButton("Revoke this device", ...)` back to
// `gatedButton("Revoke this device", ...)` in PhoneSurface.kt. That is a one-word edit, it
// compiles, every Kotlin test stays green, and it silently returns revoke to the timed tier.
//
// THE VACUITY CONTROL: the floor below. If the verb matcher broke -- a rename, a receiver that
// is no longer spelled `app` -- every verb would read as absent and the check would pass over a
// module with no gate at all. The floor makes that case fail as itself rather than as success.
func TestPBSEC2_EveryPerUseFacadeVerbIsReachedThroughThePerUseButton(t *testing.T) {
	type site struct{ where, decl string }
	var sites []site

	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := readFileOrFail(t, f, "PB-SEC-2")
		for _, verb := range perUseFacadeVerbs {
			for at := 0; ; {
				i := strings.Index(src[at:], verb)
				if i < 0 {
					break
				}
				off := at + i
				sites = append(sites, site{
					where: mustRel(t, f) + ": " + verb,
					decl:  kotlinDeclarationOf(src, off),
				})
				at = off + len(verb)
			}
		}
	}

	if len(sites) < perUseCallSiteFloor {
		t.Fatalf("PB-SEC-2: found %d production call site(s) for the per-use facade verbs %v, "+
			"want at least %d. Either the app lost its revoke and kill controls, or this "+
			"check has stopped being able to see them -- and a check that sees nothing reports "+
			"a clean gate over an ungated app", len(sites), perUseFacadeVerbs, perUseCallSiteFloor)
	}

	var ungated []string
	for _, s := range sites {
		if !strings.Contains(s.decl, "perUseButton") {
			ungated = append(ungated, s.where+" declared by: "+strings.TrimSpace(s.decl))
		}
	}
	sort.Strings(ungated)
	if len(ungated) > 0 {
		t.Errorf("PB-SEC-2: %d production call(s) of a PER-USE facade verb are not declared "+
			"through PhoneSurface.perUseButton:\n\t%s\n"+
			"Requirements 6.0 binds revoke, kill switch, launch and kill to a per-use "+
			"(CryptoObject) authorization. A control built with `gatedButton` reaches the verb "+
			"with no fresh authorization at all -- it inherits whatever the content KEK's "+
			"60-second window happens to allow, which is the downgrade ADR-007 B51 found "+
			"shipped and which no Kotlin test in the module can see",
			len(ungated), strings.Join(ungated, "\n\t"))
	}
}

// ---------------------------------------------------------------------------
// (3) The prompt and the key agree on what counts as authentication.
// ---------------------------------------------------------------------------

// TestPBSEC2_ThePromptAuthenticatorsMatchTheKeystoreSpec.
//
// ADR-007 B57 decided AGAINST adding AUTH_DEVICE_CREDENTIAL to the content KEK and the per-use
// entries, and recorded which handsets that strands and what they are told instead. This is the
// fence that keeps the two ends of that decision in step, in BOTH directions:
//
//   - a prompt that allows a device credential over keys that require AUTH_BIOMETRIC_STRONG
//     lets a user satisfy a prompt that authorizes nothing -- the platform releases no key, and
//     the app reports "the unlock was accepted but the key was not released";
//   - a Keystore spec that accepts a device credential while the prompt asks only for a
//     biometric is B57 reversed by half, weakening the key with no visible change anywhere.
//
// THE MUTATION THAT MUST FAIL IT: adding `or AUTH_DEVICE_CREDENTIAL` to either side alone.
// THE VACUITY CONTROL: both files must be found and both constants must be present, so a
// rename that made the check unable to see either end fails rather than passing silently.
func TestPBSEC2_ThePromptAuthenticatorsMatchTheKeystoreSpec(t *testing.T) {
	specs := readFileOrFail(t, filepath.Join(kotlinMainRoot(t),
		"dev", "swarm", "phone", "keys", "Provisioning.kt"), "PB-SEC-2")
	prompts := readFileOrFail(t, filepath.Join(kotlinMainRoot(t),
		"dev", "swarm", "phone", "keys", "BiometricPrompts.kt"), "PB-SEC-2")

	if !strings.Contains(specs, "AUTH_BIOMETRIC_STRONG") {
		t.Fatalf("PB-SEC-2: Provisioning.kt names no AUTH_BIOMETRIC_STRONG, so this check " +
			"cannot see what the keys require and would agree with any prompt at all")
	}
	if !strings.Contains(prompts, "BIOMETRIC_STRONG") {
		t.Fatalf("PB-SEC-2: BiometricPrompts.kt names no BIOMETRIC_STRONG authenticator, so " +
			"this check cannot see what the prompt asks for")
	}

	keyAcceptsCredential := strings.Contains(specs, "AUTH_DEVICE_CREDENTIAL")
	promptAcceptsCredential := strings.Contains(prompts, "Authenticators.DEVICE_CREDENTIAL")
	if keyAcceptsCredential != promptAcceptsCredential {
		t.Errorf("PB-SEC-2: the Keystore specs and the prompt disagree about device "+
			"credentials (KeystoreSpecs accepts one: %t; BiometricPrompts allows one: %t).\n"+
			"ADR-007 B57 decided this once, for both ends. Half of it is worse than either "+
			"whole answer: a prompt that accepts a PIN over a biometric-only key is a prompt "+
			"the user can satisfy that releases no key, and a key that accepts a PIN behind a "+
			"biometric-only prompt is the same weakening with nothing on screen to show for it. "+
			"Change both, and amend B57 with the argument",
			keyAcceptsCredential, promptAcceptsCredential)
	}
}

// ---------------------------------------------------------------------------
// (4) androidx.biometric is confined.
// ---------------------------------------------------------------------------

// biometricProductionOwners are the production files permitted to import androidx.biometric.
//
// It is an allowlist and not a count, because the property is WHERE rather than HOW MANY.
// Nothing in that import can be asserted by a unit test without asserting against a Robolectric
// shadow, so every line of it is untestable by construction; keeping it in one translation file
// is what keeps the untestable surface auditable by reading. A second file importing it is a
// second place a policy decision can be made where no test can reach it.
var biometricProductionOwners = map[string]bool{
	"BiometricPrompts.kt": true,
}

// TestPBSEC2_AndroidxBiometricIsConfinedAndReachesNoUnitTest.
//
// THE MUTATION THAT MUST FAIL IT, in each direction: importing androidx.biometric into
// PhoneSurface.kt (production sprawl), or into any file under src/test (a Robolectric shadow
// driven to "succeeded" and read as proof a biometric gate works).
//
// THE VACUITY CONTROL: the owner file must exist and must actually carry the import. Without
// that, deleting BiometricPrompts.kt would make this pass -- reporting perfect confinement of a
// dependency the app no longer uses, over the ungated codebase ADR-007 B51 found.
func TestPBSEC2_AndroidxBiometricIsConfinedAndReachesNoUnitTest(t *testing.T) {
	const api = "androidx.biometric"

	owners := map[string]bool{}
	var strays []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		if !strings.Contains(readFileOrFail(t, f, "PB-SEC-2"), "import "+api) {
			continue
		}
		base := filepath.Base(f)
		owners[base] = true
		if !biometricProductionOwners[base] {
			strays = append(strays, mustRel(t, f))
		}
	}
	for want := range biometricProductionOwners {
		if !owners[want] {
			t.Fatalf("PB-SEC-2: %s does not import %s. Either the per-use prompt is gone -- "+
				"which is ADR-007 B51's finding restored -- or this check is looking at the "+
				"wrong file and would report confinement of an import nothing has", want, api)
		}
	}
	sort.Strings(strays)
	if len(strays) > 0 {
		t.Errorf("PB-SEC-2: %d production file(s) outside the allowlist import %s:\n\t%s\n"+
			"Nothing that touches this API can be asserted by a unit test without asserting "+
			"against a Robolectric shadow, so it is confined to one translation file where the "+
			"whole untestable surface can be audited by reading it",
			len(strays), api, strings.Join(strays, "\n\t"))
	}

	// The other direction, and the one PB-E2E-5 turns on. s16_ui_test.go already forbids this
	// for `dev/swarm/phone/ui`; the per-use gate lives in `keys`, so the fence is widened to
	// the whole unit-test source set rather than left pointed at the package the last defect
	// happened to be in.
	var claimed []string
	for _, f := range kotlinFiles(t, kotlinTestRoot(t)) {
		if strings.Contains(readFileOrFail(t, f, "PB-E2E-5"), "import "+api) {
			claimed = append(claimed, mustRel(t, f))
		}
	}
	sort.Strings(claimed)
	if len(claimed) > 0 {
		t.Errorf("PB-E2E-5: %d unit test(s) import %s:\n\t%s\n"+
			"On the JVM that resolves to a Robolectric shadow, so the test asserts that a "+
			"shadow returned what the test told it to and records it as coverage of a "+
			"biometric prompt. PB-E2E-5 is DEFERRED (ADR-007 B31) and ADR-007 B56 makes the "+
			"androidTest tier unexecutable besides -- the emulator's keymint reports "+
			"SECURITY_LEVEL_SOFTWARE and PB-KEY-8 fails the app closed before a screen renders. "+
			"Model the POLICY over a seam, as PerUseGateTest does, and leave the hardware to "+
			"the physical gate", len(claimed), api, strings.Join(claimed, "\n\t"))
	}
}
