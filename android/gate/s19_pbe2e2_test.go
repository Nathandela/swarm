package gate

// Slice S19 -- PB-E2E-2's PRECONDITIONS.
//
//	"On-emulator smoke: APK installs, pairs against a local relay + daemon, SAS matches,
//	 observes, takes control, types -- including one real `adb shell am force-stop`
//	 mid-session. Evidence (log + screenshots) + reproducible runbook."
//
// WHY THIS FILE IS PRECONDITIONS RATHER THAN THE SMOKE ITSELF. PB-E2E-2's acceptance criterion
// is a runbook plus artifacts, not a green test, and the run takes an emulator boot and several
// minutes -- so the run lives in scripts/pbe2e2-emulator-smoke.sh, where it cannot wedge
// `go test ./...` (which is PB-E2E-4's own gate). What belongs in the always-run suite is the
// set of facts the smoke is IMPOSSIBLE without, because each of them is a thing someone must
// build and none of them is visible from the requirement's wording.
//
// THESE ARE NOT SKIPPED AND MUST NOT BECOME SKIPS. A skipped precondition in an exit
// demonstration is the demonstration not happening. They are also not gated on ANDROID_HOME:
// every assertion below reads checked-in source, so they answer identically on a machine with
// no Android toolchain at all.
//
// WHAT THEY DELIBERATELY DO NOT CLAIM. Passing this file does not mean the smoke ran, and it
// never can: it means the APK has the surfaces the smoke has to drive and the module has an
// instrumented source set to drive them from. The evidence for PB-E2E-2 is the runbook's own
// log and screenshots. Nothing here touches PB-E2E-5 -- real camera, real biometrics, real FCM,
// Doze and hardware Keystore attestation stay deferred, and an emulator is not a handset.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pbe2e2Verbs are the facade verbs PB-E2E-2's five in-app actions cannot be performed without.
// Each is named with the action it serves, so a failure says which clause of the requirement
// is unreachable rather than "a string is missing".
var pbe2e2Verbs = []struct {
	verb, clause string
}{
	{"beginPairing", "\"pairs against a local relay + daemon\": there is no way to enter or scan a QR"},
	{"confirmOrigin", "PB-PAIR-6's destination confirmation, which BeginPairing leaves the app owing"},
	{"sas", "\"SAS matches\": there is nothing that displays the six words to compare"},
	// "types" IS `composerSend` SINCE WAVE R6, and the clause is unchanged -- what changed is
	// the verb that performs it. The smoke drives a control that puts a line into a session and
	// watches it arrive at the machine; until R6 that control sent raw bytes plus a CR on the
	// live keystroke plane behind PB-INPUT-3's lease, and Mirror M2.4 replaced it with the
	// SIGNED composer_send op ("the lease leaves the UX -- composer gated on `online` only").
	// The daemon still TYPES the line into the session's own composer, so the observable effect
	// at the machine is what it always was; what the op adds is an honest `source: phone`
	// attribution, a turn precondition, and a refusal the phone can render. `App.SendInput` has
	// no production caller left and carries its row in android/unbound-verbs.tsv, which is where
	// the replacement is argued at length.
	{"composerSend", "\"types\": there is no control that puts a line into a session"},
	// {"takeControl", "\"takes control\""} WAS HERE, and the clause it served is retired
	// with it (owner ruling R1, 2026-08-26; PB-INPUT-2's amendment in
	// docs/specifications/remote-phaseB-requirements.md).
	//
	// PB-E2E-2's scenario reads "observes, takes control, types". The middle step existed
	// because the raw keystroke plane is lease-gated at the daemon -- and this app has not
	// used that plane since Wave R6 replaced it with composer_send, which takes no lease at
	// any layer. So the smoke's own sequence has nothing between observing and typing: a
	// session with a link and a message sink is typeable, full stop. Keeping the row would
	// require the app to call a verb whose only effect was to un-grey a field.
}

// TestPBE2E2_TheAppCanPerformEveryActionTheSmokeDrives is the blocking precondition.
//
// The smoke drives the APK, not the Go facade. An instrumented test that reached
// swarmmobile.App directly would prove the Go core runs on an Android runtime -- worth
// something, and NOT this requirement, which is about the app a user installs. So the app's
// own Kotlin must reach each verb the five clauses need.
//
// TODAY IT FAILS ON FOUR OF FIVE, and that is the finding rather than the test being unfinished.
// S18 shipped a minimal Activity whose scope ruling reads "enough Window and View to carry these
// assertions and S19's smoke"; what landed is three buttons -- take control, kill, revoke --
// and a pairing line that renders PairingFlow's SCAN message and, in PhoneSurface's own words,
// "does not run a pairing on its own". There is no scanner, no destination confirmation, no SAS
// display, no confirm control and no keyboard. Every clause of PB-E2E-2 except "takes control"
// therefore has no subject in the shipped APK.
func TestPBE2E2_TheAppCanPerformEveryActionTheSmokeDrives(t *testing.T) {
	body := stripKotlinComments(appKotlinSource(t))
	for _, want := range pbe2e2Verbs {
		// A CALL, not a mention. Every one of these verbs is discussed in the module's prose --
		// PairingFlow's own doc names the SAS and the scan step -- so a substring check would
		// pass on the comment explaining that the control does not exist.
		//
		// AND NOT A DECLARATION EITHER (Wave R6 round 3, the closing review's sweep). This is the
		// module-wide form that let four R6 symbols match their own `fun` one file over, and it
		// is safe here for a structural reason rather than by luck: the pattern requires a
		// LEADING DOT, so it matches only a receiver-qualified call. A Kotlin declaration is
		// `fun composerSend(` with no receiver, and the verb these five name is declared in Go
		// behind the gomobile AAR, which this blob does not contain at all.
		call := regexp.MustCompile(`(?i)\.` + regexp.QuoteMeta(want.verb) + `\s*\(`)
		if !call.MatchString(body) {
			t.Errorf("PB-E2E-2: no Kotlin in android/app/src/main CALLS the facade verb %q, so "+
				"the smoke cannot perform %s", want.verb, want.clause)
		}
	}
}

// TestPBE2E2_TheModuleHasAnInstrumentedSourceSet is the other half, and it is a separate fact:
// a surface with the right controls is still undrivable with nothing to drive it from.
//
// An on-device test needs a `testInstrumentationRunner` in the module's defaultConfig and an
// androidTest source set holding the smoke. Neither exists today, and adding them is not a
// line change: this module pins its dependency graph (dependencyLocking + Gradle verification
// metadata, PB-SEC-14), so every androidx.test artifact the runner pulls in has to be locked
// and justified. That is the cost this assertion exists to make visible before someone
// discovers it mid-run.
func TestPBE2E2_TheModuleHasAnInstrumentedSourceSet(t *testing.T) {
	build := readFileOrFail(t, moduleBuildFile(t), "PB-E2E-2")
	if !strings.Contains(build, "testInstrumentationRunner") {
		t.Errorf("PB-E2E-2: %s declares no testInstrumentationRunner, so the module has no "+
			"instrumented test task at all and `connectedAndroidTest` is a no-op",
			mustRel(t, moduleBuildFile(t)))
	}
	dir := filepath.Join(appModule(t), "src", "androidTest")
	if !exists(dir) {
		t.Errorf("PB-E2E-2: %s does not exist, so there is no on-device test to install "+
			"alongside the APK", mustRel(t, dir))
		return
	}
	if !anyKotlinUnder(t, dir) {
		t.Errorf("PB-E2E-2: %s holds no Kotlin, so the instrumented source set compiles nothing",
			mustRel(t, dir))
	}
}

// TestPBE2E2_TheRunbookExistsAndIssuesTheForceStop pins the acceptance criterion PB-E2E-2
// actually states: a REPRODUCIBLE RUNBOOK, plus the one step that was upgraded into the
// requirement on purpose.
//
// The force-stop is not interchangeable with a process kill and the requirement says why: it
// also puts the package in the STOPPED state, so no implicit broadcast -- BOOT_COMPLETED
// included -- reaches the app until a person launches it by hand. A runbook that killed the
// process instead would satisfy every other word of the requirement and skip the clause it was
// upgraded for.
func TestPBE2E2_TheRunbookExistsAndIssuesTheForceStop(t *testing.T) {
	path := filepath.Join(repoRoot(t), "scripts", "pbe2e2-emulator-smoke.sh")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-E2E-2: no runbook at %s: %v", mustRel(t, path), err)
	}
	if !strings.Contains(string(body), "am force-stop") {
		t.Errorf("PB-E2E-2: %s never issues `adb shell am force-stop`; a plain process kill "+
			"leaves the package out of the STOPPED state, which is the clause this requirement "+
			"was upgraded to cover", mustRel(t, path))
	}
	if info, serr := statFile(path); serr == nil && info.Mode()&0o111 == 0 {
		t.Errorf("PB-E2E-2: %s is not executable, so the runbook is not runnable as written",
			mustRel(t, path))
	}
}

// appKotlinSource is every main-source-set Kotlin file concatenated. The smoke drives whatever
// the APK contains, so the question is about the module rather than about one file.
func appKotlinSource(t *testing.T) string {
	t.Helper()
	root := filepath.Join(appModule(t), "src", "main")
	var b strings.Builder
	for _, f := range kotlinFiles(t, root) {
		b.WriteString(readFileOrFail(t, f, "PB-E2E-2"))
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		t.Fatalf("PB-E2E-2: no Kotlin under %s", mustRel(t, root))
	}
	return b.String()
}

func anyKotlinUnder(t *testing.T, root string) bool {
	t.Helper()
	return len(kotlinFiles(t, root)) > 0
}
