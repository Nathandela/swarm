package gate

import (
	"path/filepath"
	"strings"
	"testing"
)

// PB-TOOL-7 -- "CI covers the new artifacts (no android lane exists today).
// A CI lane builds the AAR and runs the Gradle gate."
//
// Today .github/workflows/ci.yml has seven jobs -- docs, lint, test,
// test-darwin, fuzz, build, build-darwin, release-dryrun -- and not one of them
// touches Java, Gradle, the Android SDK or gomobile. So the RED here is
// structural: the lane is absent.
//
// Three things beyond "a lane exists" are asserted, each because its absence
// would leave a lane that looks like coverage and is not:
//
//   - continue-on-error / a skippable `if:` turns a red lane green;
//   - a lane that builds the AAR but never runs `gradlew lint test` covers half
//     of PB-TOOL-6;
//   - and the androidgate-tagged Go tests -- the ones that inspect the real
//     artifact for each ABI and for leaked builder paths -- would never run
//     anywhere if the lane did not invoke them, which is exactly how S8's
//     48 leaked paths survived into a shipped AAR.

func ciWorkflowPath(t *testing.T) string {
	return filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml")
}

// androidLane finds the job that owns the Android artifacts.
func androidLane(t *testing.T) (ciJob, bool) {
	t.Helper()
	jobs := parseCIJobs(readFileOrFail(t, ciWorkflowPath(t), "PB-TOOL-7"))
	for _, j := range jobs {
		body := j.allRun()
		if strings.Contains(body, "gradlew") || strings.Contains(body, "build-aar") ||
			j.usesAction("android-actions/") {
			return j, true
		}
	}
	return ciJob{}, false
}

func TestPBTOOL7_AnAndroidLaneExists(t *testing.T) {
	if _, ok := androidLane(t); !ok {
		jobs := parseCIJobs(readFileOrFail(t, ciWorkflowPath(t), "PB-TOOL-7"))
		var ids []string
		for _, j := range jobs {
			ids = append(ids, j.id)
		}
		t.Fatalf("PB-TOOL-7: .github/workflows/ci.yml has no Android lane. Existing jobs: %v. "+
			"None sets up a JDK, an Android SDK or an NDK, so every artifact this slice "+
			"produces is built on exactly one laptop and nowhere else", ids)
	}
}

func TestPBTOOL7_AndroidLaneBuildsTheAAR(t *testing.T) {
	job, ok := androidLane(t)
	if !ok {
		t.Fatalf("PB-TOOL-7: no Android lane (see TestPBTOOL7_AnAndroidLaneExists)")
	}
	if !strings.Contains(job.allRun(), "build-aar") {
		t.Errorf("PB-TOOL-7: the Android lane %q never invokes the AAR build command. "+
			"Steps run:\n%s", job.id, job.allRun())
	}
}

func TestPBTOOL7_AndroidLaneRunsTheGradleGate(t *testing.T) {
	job, ok := androidLane(t)
	if !ok {
		t.Fatalf("PB-TOOL-7: no Android lane (see TestPBTOOL7_AnAndroidLaneExists)")
	}
	body := job.allRun()
	for _, task := range []string{"lint", "test"} {
		if !strings.Contains(body, "gradlew") || !strings.Contains(body, task) {
			t.Errorf("PB-TOOL-7/PB-TOOL-6: the Android lane does not run `./gradlew %s`. "+
				"Steps run:\n%s", task, body)
		}
	}
}

// TestPBTOOL7_AndroidLaneRunsTheTaggedArtifactAssertions closes the orphan hole.
// The expensive half of this package -- build the AAR, inspect every declared
// ABI, scan for leaked builder paths, build and verify the debug APK -- sits
// behind the `androidgate` build tag so `go test ./...` stays fast. Behind a tag
// and not in CI is behind a tag and never run.
func TestPBTOOL7_AndroidLaneRunsTheTaggedArtifactAssertions(t *testing.T) {
	job, ok := androidLane(t)
	if !ok {
		t.Fatalf("PB-TOOL-7: no Android lane (see TestPBTOOL7_AnAndroidLaneExists)")
	}
	body := job.allRun()
	if !strings.Contains(body, "androidgate") {
		t.Errorf("PB-TOOL-7: the Android lane never runs `go test -tags androidgate "+
			"./android/gate/...`. Those are the assertions that inspect the real AAR for "+
			"each required ABI and for absolute builder paths; unrun, the ABI and "+
			"reproducibility requirements are unenforced. Steps run:\n%s", body)
	}
}

// TestPBTOOL7_AndroidLaneCannotBeSilentlyGreen rejects the two annotations that
// make a failing lane report success.
func TestPBTOOL7_AndroidLaneCannotBeSilentlyGreen(t *testing.T) {
	job, ok := androidLane(t)
	if !ok {
		t.Fatalf("PB-TOOL-7: no Android lane (see TestPBTOOL7_AnAndroidLaneExists)")
	}
	if job.continueOnError {
		t.Errorf("PB-TOOL-7: the Android lane sets continue-on-error: true, so its failures "+
			"do not fail CI. Job: %q", job.id)
	}
	if job.ifCond != "" {
		t.Errorf("PB-TOOL-7: the Android lane is conditional (`if: %s`). A lane that can "+
			"skip itself is not coverage", job.ifCond)
	}
}

// TestPBTOOL7_AndroidLaneProvisionsTheToolchain asserts the lane actually
// installs what it needs. A lane whose every step fails at "java: not found" is
// caught by the runner, but a lane that quietly uses a preinstalled SDK of an
// unpinned version defeats PB-TOOL-1.
func TestPBTOOL7_AndroidLaneProvisionsTheToolchain(t *testing.T) {
	job, ok := androidLane(t)
	if !ok {
		t.Fatalf("PB-TOOL-7: no Android lane (see TestPBTOOL7_AnAndroidLaneExists)")
	}
	if !job.usesAction("actions/setup-java") {
		t.Errorf("PB-TOOL-7: the Android lane does not set up a JDK (actions/setup-java)")
	}
	if !job.usesAction("actions/setup-go") {
		t.Errorf("PB-TOOL-7: the Android lane does not set up Go, but the AAR is a Go " +
			"cross-compile through gomobile")
	}
	body := job.allRun()
	hasSDK := job.usesAction("android-actions/setup-android") ||
		strings.Contains(body, "sdkmanager") ||
		strings.Contains(body, "ANDROID_HOME")
	if !hasSDK {
		t.Errorf("PB-TOOL-7: the Android lane never provisions an Android SDK/NDK")
	}
	if !strings.Contains(body, "ndk") && !strings.Contains(body, "NDK") {
		t.Errorf("PB-TOOL-7: the Android lane never provisions an NDK; `gomobile bind` " +
			"cannot cross-compile libgojni.so without one")
	}
}

// TestPBTOOL5_ExistingGoLanesAreUnchanged is the guard on the other direction:
// this slice adds a lane, it does not get to weaken the ones that exist.
func TestPBTOOL5_ExistingGoLanesAreUnchanged(t *testing.T) {
	jobs := parseCIJobs(readFileOrFail(t, ciWorkflowPath(t), "PB-TOOL-5"))
	byID := map[string]ciJob{}
	for _, j := range jobs {
		byID[j.id] = j
	}
	test, ok := byID["test"]
	if !ok {
		t.Fatalf("PB-TOOL-5: the `test` job is gone from ci.yml")
	}
	body := test.allRun()
	for _, want := range []string{"go vet ./...", "go test -race ./..."} {
		if !strings.Contains(body, want) {
			t.Errorf("PB-TOOL-5: the `test` job no longer runs `%s`", want)
		}
	}
	if test.continueOnError {
		t.Errorf("PB-TOOL-5: the `test` job became continue-on-error")
	}
}
