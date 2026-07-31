package gate

// FAILING-FIRST (TDD RED, GG-5) for slice S16's ANDROID-side obligations, checked as source
// facts rather than as behaviour: PB-APP-9's taxonomy must reach a rendered state the Kotlin
// side actually declares, PB-APP-4 must render daemon-sanitized text and nothing else,
// PB-SAS-3's code must be compared and never typed, and PB-PAIR-3's scanner dependency must
// be a recorded decision rather than whatever someone added to a Gradle file.
//
// THE PHYSICAL-HANDSET GATE STAYS DEFERRED. Nothing in this file, and nothing in the Kotlin
// tests it guards, claims coverage of a real camera, real FCM delivery, real Doze, or
// hardware Keystore attestation. Those are PB-E2E-5 and PB-E2E-5 is deferred under section
// 13. (Real biometrics left that list with their feature: ADR-007 B133 removed all phone-side
// user authentication, so the app now imports nothing from androidx.biometric at all -- fenced
// below.) Robolectric and JVM tests model POLICY -- which state a screen renders given an
// input -- and the hardware-API check in this file exists to keep it that way, because a UI
// test that appears to prove a hardware behaviour is worse than no test at all.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// kotlinMainRoot is the app's production Kotlin.
func kotlinMainRoot(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "kotlin")
}

func kotlinTestRoot(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "test", "kotlin")
}

// kotlinFiles returns every .kt file under root.
func kotlinFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".kt") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// ---- PB-APP-9: the taxonomy must land on a state the UI declares -----------------

var errorStateEnum = regexp.MustCompile(`(?s)enum\s+class\s+ErrorState\s*(?:\([^)]*\))?\s*\{(.*?)\n\}`)
var enumConstant = regexp.MustCompile(`(?m)^\s*([A-Z][A-Z0-9_]*)\s*(?:\(|,|;|$)`)

// TestS16_EveryErrorClassRendersAStateTheAppDeclares closes PB-APP-9's third direction.
//
// mobile/s16_taxonomy_test.go proves every class on the pinned surface has a row naming a
// rendered state. Nothing there can prove the state EXISTS: the Go side would happily map
// ErrClassGrantLost to "GRANT_LOST" forever while no screen has ever heard of it. This is the
// direction that makes "maps to a rendered state" mean something on a handset, and it is
// checked as SET EQUALITY so a state the app declares and nothing produces fails too -- a
// dead branch in a when() is how a screen acquires an unreachable message that a later reader
// trusts.
func TestS16_EveryErrorClassRendersAStateTheAppDeclares(t *testing.T) {
	rows := taxonomyStates(t)
	declared := kotlinErrorStates(t)

	if len(declared) == 0 {
		t.Fatalf("PB-APP-9: no `enum class ErrorState` under %s.\n"+
			"The taxonomy's rendered_state column names constants of this enum; without it the "+
			"Android side branches on prose, and the Go and Kotlin ends of PB-KEY-6's two custody "+
			"verdicts have already drifted once in this project (a drifted copy degrades a "+
			"PERMANENT invalidation into a prompt the user can never satisfy).",
			mustRel(t, kotlinMainRoot(t)))
	}

	for _, state := range rows {
		if !declared[state] {
			t.Errorf("PB-APP-9: mobile/error_taxonomy.tsv renders class state %q, which no Kotlin "+
				"ErrorState constant declares. The error has a class, a remedy and nowhere to be "+
				"shown", state)
		}
	}
	inTable := map[string]bool{}
	for _, s := range rows {
		inTable[s] = true
	}
	for state := range declared {
		if !inTable[state] {
			t.Errorf("PB-APP-9: Kotlin declares ErrorState.%s and no facade error class maps to "+
				"it. Either a class was removed and its screen outlived it, or the screen shows a "+
				"message nothing can ever produce", state)
		}
	}
}

// taxonomyStates reads the rendered_state column of the checked-in taxonomy.
func taxonomyStates(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "mobile", "error_taxonomy.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-APP-9 requires the error taxonomy at %s: %v\n"+
			"mobile/s16_taxonomy_test.go owns its column contract; this gate reads the "+
			"rendered_state column (column 4) and requires each value to be a Kotlin ErrorState "+
			"constant.", mustRel(t, path), err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) > 3 {
			if s := strings.TrimSpace(cols[3]); s != "" {
				out = append(out, s)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s names no rendered states", mustRel(t, path))
	}
	return out
}

func kotlinErrorStates(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		body := errorStateEnum.FindStringSubmatch(readFileOrFail(t, f, "PB-APP-9"))
		if body == nil {
			continue
		}
		for _, m := range enumConstant.FindAllStringSubmatch(body[1], -1) {
			out[m[1]] = true
		}
	}
	return out
}

// ---- PB-APP-4: daemon-sanitized text, and no second renderer ---------------------

// vtIndicators are the marks of a terminal emulator being reimplemented on the handset.
// Each is specific enough that a match IS the thing rather than a word that happens to
// contain it -- a bare "ansi" would fire on "transient", which is the kind of false
// positive that gets a fence deleted rather than fixed.
var vtIndicators = []string{
	"\x1b", `\x1b`, `\x1B`, `\033`, `\u001B`, `\u001b`,
	"vt100", "VT100", "vt220", "VT220", "xterm",
	"AnsiParser", "ansiParser", "EscapeSequence", "escapeSequence", "CSI[",
}

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled here so no evidence line can count
// it as earned. There is no UI package under android/app/src/main/kotlin yet, so this
// fence has nothing to search and is vacuously green. It is written now, with the RED
// tests it guards, because it is PROPHYLACTIC: the shape it forbids is the one a
// contributor reaches for first, and a fence added after the fact is a fence added after
// the defect. It becomes load-bearing the moment S16 GREEN writes a screen.
// TestS16_NoTerminalEmulatorIsReimplementedOnTheHandset.
//
// ADR-007 D2 puts the VT emulator on the MACHINE: the daemon renders the grid and the phone
// shows the text it was sent (schema.TerminalSnapshot.Lines, joined into Snapshot.Text). A
// Kotlin escape-sequence parser would be a second emulator, disagreeing with the first in
// ways nothing tests -- and it would parse bytes the daemon has already declared sanitized,
// which is how sanitisation gets undone one layer up.
//
// This is a fence on the SCREEN, not on the wire: PB-APP-4's own criterion is "asserts only
// sanitized text is rendered", and the only way to assert that on the Kotlin side is that no
// code exists which could render anything else.
func TestS16_NoTerminalEmulatorIsReimplementedOnTheHandset(t *testing.T) {
	var hits []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := readFileOrFail(t, f, "PB-APP-4")
		for _, needle := range vtIndicators {
			if strings.Contains(src, needle) {
				hits = append(hits, mustRel(t, f)+": "+needle)
			}
		}
	}
	if len(hits) > 0 {
		t.Errorf("PB-APP-4/ADR-007 D2: the Android sources contain terminal-escape handling:\n\t%s\n"+
			"There is no VT emulator on the device. The daemon renders the grid and the phone "+
			"displays swarmmobile.Snapshot.Text verbatim; a second parser on the handset "+
			"disagrees with the first in ways nothing tests, and it re-interprets bytes the "+
			"daemon has already sanitized.", strings.Join(hits, "\n\t"))
	}
}

// ---- PB-SAS-3: compared, never typed --------------------------------------------

// sasInputIndicators name a SAS that is being ENTERED rather than compared.
var sasInputIndicators = []regexp.Regexp{
	*regexp.MustCompile(`(?i)\bsas\w*(field|input|entry|edit|text)\b`),
	*regexp.MustCompile(`(?i)\b(enter|type|typed|submit|input)\w*sas\b`),
	*regexp.MustCompile(`(?i)EditText[^\n]*[sS]as`),
	*regexp.MustCompile(`(?i)TextField[^\n]*[sS]as`),
}

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled here so no evidence line can count
// it as earned. There is no UI package under android/app/src/main/kotlin yet, so this
// fence has nothing to search and is vacuously green. It is written now, with the RED
// tests it guards, because it is PROPHYLACTIC: the shape it forbids is the one a
// contributor reaches for first, and a fence added after the fact is a fence added after
// the defect. It becomes load-bearing the moment S16 GREEN writes a screen.
// TestS16_TheSASIsNeverTypedOnTheHandset is PB-SAS-3's Kotlin half.
//
// The Go half (mobile/conformance/s16_pairing_test.go) proves no facade verb ingests a SAS,
// so a typed screen cannot reach the core. This one stops it existing at all: a text field
// that collected six emoji and compared them locally would satisfy every Go fence while
// moving the comparison off the two screens it belongs on.
func TestS16_TheSASIsNeverTypedOnTheHandset(t *testing.T) {
	var hits []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := readFileOrFail(t, f, "PB-SAS-3")
		for i := range sasInputIndicators {
			if loc := sasInputIndicators[i].FindString(src); loc != "" {
				hits = append(hits, mustRel(t, f)+": "+loc)
			}
		}
	}
	if len(hits) > 0 {
		t.Errorf("PB-SAS-3: the pairing UI appears to COLLECT a SAS:\n\t%s\n"+
			"The short authentication string is presented on both screens and compared by the "+
			"person holding them. A typed code moves the comparison from the human -- who can "+
			"see the machine's screen -- to the phone, which sees one string and whatever the "+
			"attacker relayed, and the six-emoji alphabet was chosen because a human compares it.",
			strings.Join(hits, "\n\t"))
	}
}

// ---- PB-PAIR-3: the scanner dependency is a recorded decision --------------------

// scannerLibraries are the plausible choices, so the check can be bidirectional: the ADR must
// name one, and the build must not carry one the ADR does not name.
var scannerLibraries = []string{
	"com.google.mlkit", "barcode-scanning", "com.google.android.gms:play-services-mlkit",
	"com.journeyapps:zxing", "com.google.zxing", "androidx.camera", "camera-mlkit",
}

// TestPBPAIR3_TheScannerChoiceIsRecordedInTheADR.
//
// PB-PAIR-3's whole content is that the choice is EXPLICIT: ML Kit pulls Google Play
// Services, which is in tension with PB-SEC-14's minimal dependency set, and the requirement
// asks for the tradeoff to be stated rather than resolved by whoever adds the Gradle line.
// So the fence is two-way -- an ADR with no decision fails, and a dependency the ADR does not
// name fails too, because that is exactly the shape "someone added a scanner" takes.
func TestPBPAIR3_TheScannerChoiceIsRecordedInTheADR(t *testing.T) {
	adr := filepath.Join(repoRoot(t), "docs", "adr", "ADR-007-remote-access.md")
	text := readFileOrFail(t, adr, "PB-PAIR-3")
	lower := strings.ToLower(text)

	named := map[string]bool{}
	for _, lib := range scannerLibraries {
		if strings.Contains(lower, strings.ToLower(lib)) {
			named[lib] = true
		}
	}
	if len(named) == 0 {
		t.Errorf("PB-PAIR-3: ADR-007 records no QR-scanner decision. The requirement is that the "+
			"choice be EXPLICIT: ML Kit pulls Google Play Services, which is in tension with "+
			"PB-SEC-14's minimal dependency set, and PB-PAIR-2 additionally needs a manual-entry "+
			"fallback for a denied camera. An amendment must name the library, the cost it "+
			"carries, and the alternative it was chosen over. Candidates the build could plausibly "+
			"use: %s", strings.Join(scannerLibraries, ", "))
	}
	if !strings.Contains(text, "PB-SEC-14") {
		t.Errorf("PB-PAIR-3: ADR-007's scanner decision does not cite PB-SEC-14, which is the " +
			"requirement it trades against. A decision recorded without its cost is a preference")
	}

	// The other direction: what the build actually pulls.
	gradle := readFileOrFail(t, filepath.Join(appModule(t), "build.gradle.kts"), "PB-PAIR-3")
	for _, lib := range scannerLibraries {
		if strings.Contains(gradle, lib) && !named[lib] {
			t.Errorf("PB-PAIR-3: android/app/build.gradle.kts declares %q and ADR-007 does not "+
				"mention it. The dependency arrived without the decision", lib)
		}
	}
}

// ---- PB-E2E-5 stays deferred ----------------------------------------------------

// hardwareAPIs are the platform surfaces whose BEHAVIOUR only a physical handset can
// establish. A JVM or Robolectric test that imported one would be asserting against a shadow
// and reporting it as coverage.
var hardwareAPIs = []string{
	// androidx.biometric is a harder case than the rest: since ADR-007 B133 its feature does
	// not exist, so it is forbidden EVERYWHERE, not just here -- see
	// TestB133_TheAppImportsNothingFromAndroidxBiometric.
	"androidx.biometric",
	"android.hardware.camera2",
	"androidx.camera",
	"com.google.firebase.messaging",
}

// s16UIPackage is the only tree this fence covers, and the scope is deliberate.
// dev.swarm.phone.keys.KeystoreSpecTest imports android.security.keystore to assert the
// KeyProperties CONSTANTS a KeyGenParameterSpec must carry -- a statement about what the app
// asks the platform for, which is exactly the policy half PB-KEY-8 owns and is not a claim
// about what the platform then does. Widening this fence to that package would turn a
// shipped, correct test red and teach the next reader that the fence is noise.
const s16UIPackage = "dev/swarm/phone/ui"

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled here so no evidence line can count
// it as earned. There is no UI package under android/app/src/main/kotlin yet, so this
// fence has nothing to search and is vacuously green. It is written now, with the RED
// tests it guards, because it is PROPHYLACTIC: the shape it forbids is the one a
// contributor reaches for first, and a fence added after the fact is a fence added after
// the defect. It becomes load-bearing the moment S16 GREEN writes a screen.
// TestS16_NoUnitTestClaimsAPhysicalHandsetProperty keeps the deferral honest.
//
// PB-E2E-5 -- a real camera, real FCM delivery, real Doze, hardware Keystore attestation --
// is deferred under section 13 and may NOT be reclassified as an accepted limit by a test
// that appears to cover it. (Real biometrics were on this list until ADR-007 B133 removed
// the feature they belonged to.) The failure mode is specific and this project has hit its
// shape six times: a fence that guards a path production does not take. A Robolectric test
// that drives a shadowed camera capture or FCM delivery to "succeeded" has proved that the
// SHADOW returns what it was told to, and an auditor reading the test name reads it as
// coverage of the hardware.
//
// Policy tests are the right thing and are unaffected: dev.swarm.phone.runtime.FcmPriorityPolicy
// is a pure function of stated inputs and its test imports none of these.
func TestS16_NoUnitTestClaimsAPhysicalHandsetProperty(t *testing.T) {
	var hits []string
	for _, f := range kotlinFiles(t, filepath.Join(kotlinTestRoot(t), filepath.FromSlash(s16UIPackage))) {
		src := readFileOrFail(t, f, "PB-E2E-5")
		for _, api := range hardwareAPIs {
			if strings.Contains(src, "import "+api) {
				hits = append(hits, mustRel(t, f)+": import "+api)
			}
		}
	}
	if len(hits) > 0 {
		t.Errorf("PB-E2E-5: unit tests import platform APIs whose behaviour only a physical "+
			"handset can establish:\n\t%s\n"+
			"On the JVM these resolve to Robolectric shadows, so the test asserts that a shadow "+
			"returned what the test told it to and records it as coverage of a camera or an "+
			"FCM delivery. PB-E2E-5 is deferred and must stay deferred; model the "+
			"POLICY (which state is rendered for which input) and leave the hardware to the "+
			"physical gate.", strings.Join(hits, "\n\t"))
	}
}

// TestB133_TheAppImportsNothingFromAndroidxBiometric is PB-SEC-2's confinement fence,
// inverted and widened, and it is the STRONGER form: ADR-007 B133 removed all phone-side user
// authentication with the code deleted, because a disabled gate that still compiles is a gate
// someone re-enables by accident. An `import androidx.biometric` anywhere in the app module --
// production or test, any package -- is that accident's first line, so the fence covers both
// source sets rather than the one package the old confinement check pointed at.
func TestB133_TheAppImportsNothingFromAndroidxBiometric(t *testing.T) {
	var scanned int
	var hits []string
	for _, root := range []string{kotlinMainRoot(t), kotlinTestRoot(t)} {
		for _, f := range kotlinFiles(t, root) {
			scanned++
			if strings.Contains(readFileOrFail(t, f, "ADR-007 B133"), "import androidx.biometric") {
				hits = append(hits, mustRel(t, f))
			}
		}
	}
	if scanned == 0 {
		t.Fatalf("ADR-007 B133: no Kotlin files found under either source set; this fence is " +
			"scanning nothing and its green would be vacuous")
	}
	if len(hits) > 0 {
		t.Errorf("ADR-007 B133: %d file(s) import androidx.biometric:\n\t%s\n"+
			"All phone-side user authentication was REMOVED from the product, code deleted. "+
			"Nothing may import this API: a biometric prompt reappearing changes the security "+
			"posture of the whole product and needs its own ADR entry before any code",
			len(hits), strings.Join(hits, "\n\t"))
	}
}

// TestS16_EveryErrorClassTokenAppearsVerbatimInTheKotlinSources.
//
// The class rides the error MESSAGE -- gomobile leaves nothing else of a Go error -- so the
// Android side has to hold the token strings, and the unit-test JVM does not load the AAR, so
// it holds them as LITERALS (dev.swarm.phone.keys.GoCustodyFailure already does exactly this,
// with mobile/s14_custody_test.go checking them against the Go constants).
//
// A second copy of a discriminator is a second thing to get wrong and the failure is silent:
// an unrecognised token falls through to the unknown branch, which for the PERMANENT custody
// verdict means a prompt the user can never satisfy. Go is authoritative and Kotlin is checked
// against it, which is the direction this runs in.
func TestS16_EveryErrorClassTokenAppearsVerbatimInTheKotlinSources(t *testing.T) {
	tokens := taxonomyTokens(t)

	var kotlin strings.Builder
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		kotlin.WriteString(readFileOrFail(t, f, "PB-APP-9"))
		kotlin.WriteString("\n")
	}
	src := kotlin.String()

	for _, tok := range tokens {
		if !strings.Contains(src, `"`+tok+`"`) {
			t.Errorf("PB-APP-9: error-class token %q appears in mobile/error_taxonomy.tsv and in no "+
				"Kotlin source. The class rides the exception message and the Android side has "+
				"nothing to match it against, so every error carrying it falls through to the "+
				"unknown branch", tok)
		}
	}
}

func taxonomyTokens(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "mobile", "error_taxonomy.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-APP-9 requires the error taxonomy at %s: %v", mustRel(t, path), err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) > 1 {
			if tok := strings.TrimSpace(cols[1]); tok != "" {
				out = append(out, tok)
			}
		}
	}
	return out
}

// TestS16_TheScreenModelsAreDrivenByTheBoundFacade is the fence that stops S16's screen tests
// becoming standing defect class (v): a fence guarding a path production does not take.
//
// The screen models are pure Kotlin so their mapping is testable without an Activity -- the
// shape PermissionStateResolver already established here. The hazard is the obvious one: a
// beautifully-tested TriageInbox nothing constructs from swarmmobile.App, with the real screen
// reading the facade directly and disagreeing with every assertion. This project has hit that
// exact shape six times, most expensively when a real-components integration test supplied a
// missing input by hand and nobody noticed the app had no way to obtain it in production.
//
// So: the ui package must reference the bound facade somewhere. It is a weak check on purpose
// -- it cannot prove the wiring is right -- and it fails loudly on the one case that matters,
// a ui package with no connection to the facade at all.
func TestS16_TheScreenModelsAreDrivenByTheBoundFacade(t *testing.T) {
	root := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(s16UIPackage))
	files := kotlinFiles(t, root)
	if len(files) == 0 {
		t.Skipf("no %s package yet; S16 GREEN creates it and this fence becomes load-bearing then",
			s16UIPackage)
	}
	for _, f := range files {
		if strings.Contains(readFileOrFail(t, f, "PB-APP-2"), "swarmmobile.") {
			return
		}
	}
	t.Errorf("PB-BIND-3: %d file(s) under %s and not one references swarmmobile. The screen "+
		"models are pure Kotlin so they can be tested without an Activity, which is right -- "+
		"but nothing here reads the bound facade, so every screen test is asserting about a "+
		"model the real screen may not use", len(files), s16UIPackage)
}
