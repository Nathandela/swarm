package gate

// FAILING-FIRST (TDD RED, GG-5) tests for PB-SEC-13, slice S18:
//
//	"Release builds are debuggable=false, non-profileable, with no debug backdoor;
//	 heap-dump/crash-report exposure considered."
//	Criterion: "Build-config assertion."
//
// WHY EXPLICIT, WHEN AGP ALREADY DEFAULTS RELEASE TO NON-DEBUGGABLE. Because this app's threat
// model is a handset an adversary may be holding, and the two attributes below are the
// difference between "the keys are in a process" and "the keys are in a process anyone with a
// USB cable can dump".
//
//   - android:debuggable lets `adb` attach jdwp to the process. Everything the Go core holds
//     in memory -- the unwrapped content key while the screen is unlocked, decrypted session
//     text, the typed command line -- is readable.
//   - <profileable android:shell="true"> is the one people forget. It is NOT the same
//     attribute, it is not implied by debuggable=false, AGP will inject it on release builds
//     when asked, and it grants shell-side Perfetto and heap-dump access on Android 10+. An
//     app that is correctly non-debuggable and quietly profileable satisfies a naive reading of
//     this requirement while shipping the exposure it exists to prevent -- standing defect
//     class (iv).
//
// The build file's own idiom is already explicitness: it states isMinifyEnabled next to the
// release signing config rather than inheriting it, and the manifest states
// android:exported="false" on a service that would otherwise default differently across AGP
// versions, with a comment saying why. This requirement asks for the same treatment of the two
// attributes that decide whether the process is readable.
//
// RED AT THE TIME OF WRITING: android/app/build.gradle.kts's release build type sets only
// isMinifyEnabled and signingConfig. Nothing states isDebuggable, nothing states profileable,
// and no artifact records a heap-dump or crash-report decision.
//
// THIS FILE NEVER SKIPS: every assertion reads a checked-in file.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func appBuildFile(t *testing.T) string {
	return filepath.Join(appModule(t), "build.gradle.kts")
}

// releaseBuildTypeBlock returns the body of `getByName("release") { ... }` (or
// `release { ... }`) inside buildTypes, brace-balanced.
//
// It returns ok=false when there is no such block, and every caller reports that as a FAILURE
// rather than skipping: a release build type that has vanished is not a reason to stop
// asserting things about release builds, it is the most alarming possible finding.
func releaseBuildTypeBlock(t *testing.T) (string, bool) {
	t.Helper()
	src := stripKotlinComments(readFileOrFail(t, appBuildFile(t), "PB-SEC-13"))
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`getByName\(\s*"release"\s*\)\s*\{`),
		regexp.MustCompile(`\brelease\s*\{`),
	} {
		if loc := re.FindStringIndex(src); loc != nil {
			return braceBody(src, loc[1]-1)
		}
	}
	return "", false
}

// braceBody returns the balanced { ... } body starting at the '{' at index open.
func braceBody(src string, open int) (string, bool) {
	if open < 0 || open >= len(src) || src[open] != '{' {
		return "", false
	}
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : i], true
			}
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// PB-SEC-13.
// ---------------------------------------------------------------------------

// TestPBSEC13_TheReleaseBuildIsExplicitlyNonDebuggable.
func TestPBSEC13_TheReleaseBuildIsExplicitlyNonDebuggable(t *testing.T) {
	body, ok := releaseBuildTypeBlock(t)
	if !ok {
		t.Fatalf("PB-SEC-13: %s declares no release build type. There is nothing to assert "+
			"about release builds, which is reported as a failure rather than a skip: a "+
			"skipped security assertion reads as a green one", mustRel(t, appBuildFile(t)))
	}

	if regexp.MustCompile(`isDebuggable\s*=\s*true`).MatchString(body) {
		t.Fatalf("PB-SEC-13: the release build type sets isDebuggable = true. `adb` can attach "+
			"to the process and read the unwrapped content key, the decrypted session text and "+
			"the typed command line straight out of memory (%s)", mustRel(t, appBuildFile(t)))
	}
	if !regexp.MustCompile(`isDebuggable\s*=\s*false`).MatchString(body) {
		t.Errorf("PB-SEC-13: the release build type does not state isDebuggable = false. AGP's "+
			"default is false today, and that is not what this requirement asks for: the file "+
			"already states isMinifyEnabled explicitly beside it, and the manifest states "+
			"android:exported explicitly for the same reason -- a default is not a decision, "+
			"and nothing fails when a later edit changes it (%s)", mustRel(t, appBuildFile(t)))
	}
}

// TestPBSEC13_TheReleaseBuildIsNotProfileable is the clause that is easy to miss and is the
// whole heap-dump exposure.
//
// Two spellings grant it and both must be absent: the Gradle DSL's `isProfileable = true` on
// the release build type, and a <profileable android:shell="true"/> element in the manifest.
// They are separate mechanisms -- neither implies the other, and an app can ship with either.
func TestPBSEC13_TheReleaseBuildIsNotProfileable(t *testing.T) {
	body, ok := releaseBuildTypeBlock(t)
	if !ok {
		t.Fatalf("PB-SEC-13: %s declares no release build type", mustRel(t, appBuildFile(t)))
	}
	if regexp.MustCompile(`isProfileable\s*=\s*true`).MatchString(body) {
		t.Errorf("PB-SEC-13: the release build type sets isProfileable = true, which grants "+
			"shell-side Perfetto and heap-dump access on Android 10+ (%s)",
			mustRel(t, appBuildFile(t)))
	}

	// The manifest half. This one is a LEGITIMATE PASSER today (no such element is declared)
	// and exists so a library manifest merge, or a later hand edit, cannot add one silently.
	for _, p := range parseXMLFile(t, manifestPath(t), "PB-SEC-13").findAll("profileable") {
		if p.attrs["shell"] == "true" {
			t.Errorf("PB-SEC-13: the manifest declares <profileable android:shell=\"true\"/>. "+
				"The process is heap-dumpable from a shell even though it is not debuggable "+
				"(%s)", mustRel(t, manifestPath(t)))
		}
	}

	// And the explicit statement, for the same reason as isDebuggable.
	if !regexp.MustCompile(`isProfileable\s*=\s*false`).MatchString(body) {
		t.Errorf("PB-SEC-13: the release build type says nothing about isProfileable. It is a "+
			"SEPARATE attribute from isDebuggable and is not implied by it: an app that is "+
			"correctly non-debuggable and quietly profileable meets a naive reading of this "+
			"requirement while shipping exactly the heap-dump exposure it names (%s)",
			mustRel(t, appBuildFile(t)))
	}
}

// TestPBSEC13_TheManifestNeverHardcodesDebuggable.
//
// LEGITIMATE PASSER TODAY. android:debuggable in the manifest OVERRIDES the build type, so an
// app can have a perfectly configured release block and still ship a debuggable APK. It is
// also the single most common way this requirement is broken in practice, usually by a
// left-over debugging edit.
func TestPBSEC13_TheManifestNeverHardcodesDebuggable(t *testing.T) {
	app := applicationElement(t, "PB-SEC-13")
	if got, ok := app.attrs["debuggable"]; ok {
		t.Errorf("PB-SEC-13: the manifest hardcodes android:debuggable=%q on <application>. "+
			"It overrides the build type, so the release block's configuration is irrelevant "+
			"(%s)", got, mustRel(t, manifestPath(t)))
	}
}

// TestPBSEC13_TheHeapDumpAndCrashReportDecisionIsRecorded covers the clause the criterion
// phrases as "considered".
//
// "Considered" cannot be asserted as behaviour, and this project has already ruled that
// "reviewed" is unenforceable (PB-SEC-3 and PB-SEC-8 both carry an evidence-artifact criterion
// for that reason). So what is asserted is the same thing those two ask for: a written
// decision, in the build file where it is read.
//
// The decision has real content to record. This app ships NO crash reporter -- PB-SEC-8
// forbids one -- which means an uncaught exception produces a system tombstone and nothing is
// uploaded anywhere. That is a deliberate posture and the opposite of most Android projects'.
// A reader who does not find it stated will eventually add Crashlytics to "fix" the gap.
func TestPBSEC13_TheHeapDumpAndCrashReportDecisionIsRecorded(t *testing.T) {
	src := readFileOrFail(t, appBuildFile(t), "PB-SEC-13")
	lower := strings.ToLower(src)

	mentionsHeap := strings.Contains(lower, "heap") || strings.Contains(lower, "profileable")
	mentionsCrash := strings.Contains(lower, "crash") || strings.Contains(lower, "tombstone")

	if !mentionsHeap || !mentionsCrash {
		t.Errorf("PB-SEC-13: %s records no heap-dump/crash-report decision (heap mentioned: %v, "+
			"crash mentioned: %v). The requirement's word is \"considered\", and this project "+
			"has already ruled that an unwritten consideration is unenforceable -- PB-SEC-3 and "+
			"PB-SEC-8 both carry an evidence-artifact criterion for exactly that reason. There "+
			"is something specific to say: the app ships no crash reporter at all (PB-SEC-8 "+
			"forbids one), so a crash produces a system tombstone and uploads nothing, which is "+
			"a deliberate posture a later reader will otherwise mistake for an omission",
			mustRel(t, appBuildFile(t)), mentionsHeap, mentionsCrash)
	}
}

// TestPBSEC13_NoDebugBackdoorGatesPrivilegedBehaviour is the "no debug backdoor" clause.
//
// The shape it hunts is a privileged path behind a build-type flag: `if (BuildConfig.DEBUG) {
// ... }` wrapped around something that bypasses a gate. Two facts make this checkable rather
// than hand-wavy here: BuildConfig is not currently generated for this module at all (no
// buildConfig = true, no buildConfigField), and there is exactly one debug-shaped constant
// surface in the Kotlin sources. So any appearance is new, and new is what a fence is for.
//
// LEGITIMATE PASSER TODAY, and deliberately narrow: it fences the specific construct rather
// than trying to define "backdoor", because a fence that cannot say what it forbids gets
// deleted the first time it fires on something innocent.
func TestPBSEC13_NoDebugBackdoorGatesPrivilegedBehaviour(t *testing.T) {
	// The verbs that must never sit behind a build flag. They are the gates themselves: if a
	// debug build can reach any of these differently from a release build, the gate is not a
	// gate. Both spellings, because gobind lowercases the first letter for Java and a Kotlin
	// call site therefore carries the lowered name (s17_pushclient_test.go records the three
	// assertions this project already lost to matching the Go casing alone).
	privileged := []string{
		"SendInput", "Paste", "Resize", "TakeControl", "Interrupt", "Kill", "Launch",
		"InstallContentKey", "InstallWakeKey", "PurgeKeys", "RevokeThisDevice",
	}

	debugFlag := regexp.MustCompile(`BuildConfig\.DEBUG|isDebuggable\b|\bDEBUG_[A-Z_]+`)

	var findings []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := stripKotlinComments(readFileOrFail(t, f, "PB-SEC-13"))
		for _, loc := range debugFlag.FindAllStringIndex(src, -1) {
			// Look at the balanced block that follows, if there is one.
			brace := strings.IndexByte(src[loc[1]:], '{')
			if brace < 0 || brace > 80 {
				continue
			}
			body, ok := braceBody(src, loc[1]+brace)
			if !ok {
				continue
			}
			for _, verb := range privileged {
				lowered := strings.ToLower(verb[:1]) + verb[1:]
				if strings.Contains(body, verb+"(") || strings.Contains(body, lowered+"(") {
					findings = append(findings, mustRel(t, f)+": "+verb+
						" is reachable from a block gated on a debug flag")
				}
			}
		}
	}
	if len(findings) > 0 {
		t.Errorf("PB-SEC-13: %d debug-gated privileged call site(s). A build-type flag that "+
			"changes what the app may do IS the debug backdoor this requirement names:\n\t%s",
			len(findings), strings.Join(findings, "\n\t"))
	}
}
