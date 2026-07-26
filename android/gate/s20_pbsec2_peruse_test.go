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
// Reading Kotlin as CODE and not as prose.
// ---------------------------------------------------------------------------

// kotlinCodeOnly strips `//` line comments and `/* */` block comments, leaving string literals
// intact.
//
// IT EXISTS BECAUSE THE FIRST DRAFT OF THIS FILE WAS DEFEATED BY ITS OWN DOCUMENTATION. Check
// (1) below asks whether a symbol is REFERENCED from production Kotlin. Run against the
// pre-fix sources it correctly reported `KeystoreSpecs.forOperation`, `endPrompt` and
// `.consume(` unreached -- and wrongly reported `PerUseGate` and `beginPrompt` REACHED, because
// a KDoc comment elsewhere in the module names them in a sentence. A fence that a comment can
// satisfy is a fence that the next person to write a thorough comment turns off, silently, and
// that is the same failure class the fence is pointed at: something that reads as coverage and
// is not.
//
// The scan is a small state machine rather than a regexp because the two hazards pull opposite
// ways: `//` inside a string literal is code, and a quote inside a comment is prose. Getting
// either wrong changes what the check can see.
func kotlinCodeOnly(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	const (
		code = iota
		lineComment
		blockComment
		str
		charLit
	)
	state := code
	for i := 0; i < len(src); i++ {
		c := src[i]
		next := byte(0)
		if i+1 < len(src) {
			next = src[i+1]
		}
		switch state {
		case code:
			switch {
			case c == '/' && next == '/':
				state = lineComment
				i++
			case c == '/' && next == '*':
				state = blockComment
				i++
			case c == '"':
				state = str
				out.WriteByte(c)
			case c == '\'':
				state = charLit
				out.WriteByte(c)
			default:
				out.WriteByte(c)
			}
		case lineComment:
			if c == '\n' {
				state = code
				out.WriteByte(c)
			}
		case blockComment:
			if c == '*' && next == '/' {
				state = code
				i++
			} else if c == '\n' {
				// Kept so reported line numbers and the shape of the file survive.
				out.WriteByte(c)
			}
		case str, charLit:
			out.WriteByte(c)
			// A backslash escapes whatever follows, including the closing quote.
			if c == '\\' {
				if i+1 < len(src) {
					out.WriteByte(next)
					i++
				}
				continue
			}
			if (state == str && c == '"') || (state == charLit && c == '\'') {
				state = code
			}
		}
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// (1) The per-use subjects are reached from production Kotlin.
// ---------------------------------------------------------------------------

// perUseSubjects is the CHAIN from the button a person presses to the per-use
// KeyGenParameterSpec, one link per row. Each symbol maps to the file that DECLARES it, which
// is excluded from the search: a declaration is not a caller, and counting it is exactly how
// "reached" degrades into "exists".
//
// IT IS A CHAIN AND NOT A SET, because the defect it is pointed at is a broken link rather than
// a missing symbol. Every one of these existed and was tested when ADR-007 B51 found the tier
// unimplemented; what was missing was that anything called them.
//
//	PhoneSurface -> perUseCiphers -> KeystorePerUseCiphers -> provisionGate -> forOperation
//	PhoneSurface -> PerUseGate -> beginPrompt / endPrompt / consume
var perUseSubjects = map[string]string{
	// The gate itself, so this fails the day it is deleted rather than passing quietly.
	"PerUseGate": "PerUseGate.kt",
	// The three ledger verbs B51 found with no production caller.
	"beginPrompt": "BiometricPolicy.kt",
	"endPrompt":   "BiometricPolicy.kt",
	".consume(":   "BiometricPolicy.kt",
	// The Keystore half: the screen asks the runtime, the runtime builds the cipher source, the
	// cipher source provisions the per-use entry.
	"perUseCiphers":         "PhoneRuntime.kt",
	"KeystorePerUseCiphers": "BiometricPrompts.kt",
	"provisionGate":         "Provisioning.kt",
	// The alias table that gives each per-use operation its own Keystore entry, so no one
	// authorization can be pointed at whichever operation the caller picks.
	"KeystoreAliases.forOperation": "Custody.kt",

	// ADR-007 B44's EXIT, in the same map because it is the same property and the same failure.
	// B44 made a screen lock return the content tier to locked and left the resume path
	// asserting the Keystore "will answer"; when it does not, these two are the whole of the way
	// back in. A policy object deciding when to offer a prompt, with nothing calling it, would
	// be the identical defect one requirement over -- and it is the reason `ContentLock`
	// declined a foreground timer in the first place.
	"ContentUnlockPolicy": "ContentLock.kt",
	"confirmForContent":   "BiometricPrompts.kt",

	// ADR-007 B57's BILL, and the row most likely to be the next instance if it is dropped.
	// Refusing AUTH_DEVICE_CREDENTIAL makes an enrolled Class-3 biometric mandatory, and the
	// platform refuses to GENERATE the content KEK without one -- which `DeviceCapabilities.probe`
	// cannot see, because it answers USER_AUTH_PER_USE from the API level. Unreached, these two
	// leave a PIN-only handset with an app that will not start and a remedy of NONE.
	"provisioningFor":             "ContentLock.kt",
	"deviceBiometricAvailability": "BiometricPrompts.kt",
}

// bodyMustName pins a call INSIDE a named function, which is the half a cross-file search
// cannot do. Both rows exist because a cross-file search was measurably not enough:
//
//   - `KeystoreSpecs.forOperation`'s only caller, `CustodyProvisioning.provisionGate`, lives in
//     the same file that declares it, so excluding the declaring file would report it unreached
//     however well wired it is. The chain proves `provisionGate` is reached; this proves
//     `provisionGate` is what reaches the spec.
//   - `refuseAHandsetThatCannotHoldTheContentKek` was WRAPPED AND ORPHANED as a probe while this
//     file was being written: commenting out its one call left `provisioningFor` and
//     `deviceBiometricAvailability` still textually present in PhoneRuntime.kt, and the chain
//     check passed over a handset gate that no longer ran. That is the documented limit of a
//     name search -- "a call inside a function nothing invokes satisfies it" -- observed rather
//     than assumed, so the row below closes it at the one place it mattered.
var bodyMustName = map[string]string{
	"provisionGate": "KeystoreSpecs.forOperation",
	"construct":     "refuseAHandsetThatCannotHoldTheContentKek",
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

	// CODE ONLY. A KDoc sentence naming a symbol is not a caller, and the first draft of this
	// check was defeated by exactly that -- see kotlinCodeOnly.
	code := map[string]string{}
	for _, f := range files {
		code[f] = kotlinCodeOnly(readFileOrFail(t, f, "PB-SEC-2"))
	}

	var unreached []string
	for symbol, declaredIn := range perUseSubjects {
		reached := false
		for _, f := range files {
			if filepath.Base(f) == declaredIn {
				continue
			}
			if strings.Contains(code[f], symbol) {
				reached = true
				break
			}
		}
		if !reached {
			unreached = append(unreached, symbol+" (declared in "+declaredIn+")")
		}
	}

	// And the links no cross-file search can make: a call inside the body of a named function.
	for fn, spec := range bodyMustName {
		found := false
		for _, f := range files {
			body, ok := kotlinFunBody(code[f], fn)
			if !ok {
				continue
			}
			found = true
			if !strings.Contains(body, spec) {
				unreached = append(unreached, spec+" (not named in the body of "+fn+")")
			}
		}
		if !found {
			unreached = append(unreached, fn+" (no such function in production Kotlin)")
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
//
// The indentation class is `[ \t]` and NOT `\s`, which matches newlines: with `\s*` the match
// start walks back over any blank lines above the declaration and the reported line comes out
// empty. That is not cosmetic -- an empty declaration string contains no "perUseButton", so
// every call site reads as ungated and the check fails over correct code. It showed up the
// moment comment-stripping turned KDoc blocks into blank lines.
var kotlinMemberDecl = regexp.MustCompile(`(?m)^[ \t]*(?:private[ \t]+|internal[ \t]+)?(?:val|var|fun)[ \t]`)

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
		// CODE ONLY: a verb named in a KDoc sentence is not a call site, and counting one would
		// fail this check over a comment.
		src := kotlinCodeOnly(readFileOrFail(t, f, "PB-SEC-2"))
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
