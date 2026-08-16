package gate

// FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android/phone slice, part 3 of the scope:
// the google-services PLUGIN WIRING for the production app dev.swarm.phone (ADR-015's
// Firebase facts; playbook R3 "production Firebase project/configuration").
//
// THE CONSTRAINT THIS FENCES. android/app/google-services.json exists LOCALLY and is
// gitignored (.gitignore:60-63 records why). CI clones the repository, so CI builds run
// with the file ABSENT -- and the exact gradle invocations .github/workflows/ci.yml's
// android job runs must stay green in that state. The Google plugin fails the build when
// its config file is missing, so the only honest wiring is CONDITIONAL application: apply
// com.google.gms.google-services exactly when the local config exists, with a comment that
// says so instead of pretending the plugin is always on. A fabricated google-services.json
// is forbidden in every state (there is no second Google project to point one at, and a
// fake file produces an app that fails only on a real handset).
//
// Source-level, like the neighbouring gradle fences: matching is done on comment-stripped
// build-script text (kotlinCodeOnly), because this package was once defeated by a fence a
// comment satisfied. Nothing here runs Gradle.

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestR3A_GoogleServicesPluginIsAppliedConditionally: the app module must apply the
// google-services plugin, and must apply it behind a google-services.json existence
// check -- never unconditionally (that breaks every CI build) and never not at all (that
// ships a production APK that initialises no FirebaseApp and receives no wake).
func TestR3A_GoogleServicesPluginIsAppliedConditionally(t *testing.T) {
	path := filepath.Join(appModule(t), "build.gradle.kts")
	code := kotlinCodeOnly(readFileOrFail(t, path,
		"the app module build script carries the google-services wiring"))

	if !strings.Contains(code, "com.google.gms.google-services") {
		t.Fatalf("%s: the google-services plugin is not wired at all; the production app "+
			"dev.swarm.phone cannot initialise FirebaseApp and receives no FCM wake (ADR-015, R3 scope 3)",
			mustRel(t, path))
	}
	if !strings.Contains(code, "google-services.json") {
		t.Errorf("%s: the plugin application does not reference google-services.json; the "+
			"conditional must be keyed on the local config file's presence", mustRel(t, path))
	}
	if !strings.Contains(code, ".exists()") {
		t.Errorf("%s: no existence check guards the plugin application; an unconditional "+
			"apply fails every build that lacks the gitignored config -- which is every CI build",
			mustRel(t, path))
	}

	// The honesty half: the guard exists for CI, and the build script must say so in
	// prose a contributor reads where the decision lives. This is the one assertion that
	// reads the RAW file, because it is about the comment.
	raw := readFileOrFail(t, path, "the app module build script carries the google-services wiring")
	if !strings.Contains(raw, "gitignored") && !strings.Contains(raw, "gitignore") {
		t.Errorf("%s: the conditional plugin application carries no comment naming the "+
			"gitignored config and the plugin-absent CI shape; the next contributor will "+
			"\"fix\" the conditional into an unconditional apply", mustRel(t, path))
	}
}

// TestR3A_GoogleServicesPluginVersionIsPinned: the plugin must resolve from a pinned
// version stated in the build (root build script or settings), not from whatever the
// runner's cache holds -- the same recorded-decision discipline every other version in
// android/ follows (PB-TOOL-1's shape).
func TestR3A_GoogleServicesPluginVersionIsPinned(t *testing.T) {
	root := kotlinCodeOnly(readFileOrFail(t, filepath.Join(androidRoot(t), "build.gradle.kts"),
		"the root build script"))
	settings := kotlinCodeOnly(readFileOrFail(t, filepath.Join(androidRoot(t), "settings.gradle.kts"),
		"the settings script"))
	if !strings.Contains(root, "com.google.gms") && !strings.Contains(settings, "com.google.gms") {
		t.Fatal("neither android/build.gradle.kts nor android/settings.gradle.kts pins the " +
			"google-services plugin; the app module cannot apply a plugin nothing puts on the classpath")
	}
}

// TestR3A_GoogleServicesJSONStaysOutOfTheRepository: the secrets half (hard rule 6).
// The config must be gitignored at its one blessed path, must never be tracked, and no
// second google-services.json may creep in anywhere in the tree.
func TestR3A_GoogleServicesJSONStaysOutOfTheRepository(t *testing.T) {
	root := repoRoot(t)
	tracked := trackedFiles(t, root)
	for _, f := range tracked {
		if filepath.Base(f) == "google-services.json" {
			t.Fatalf("%s is tracked by git; the Firebase config must never enter the repository", f)
		}
	}

	gitignore := readFileOrFail(t, filepath.Join(root, ".gitignore"), "the repository .gitignore")
	if !strings.Contains(gitignore, "android/app/google-services.json") {
		t.Error(".gitignore no longer covers android/app/google-services.json")
	}
}

// TestR3A_TheCIAndroidJobRunsPluginAbsent: the CI-shape half. The android job's gradle
// invocations are the exact plugin-absent gate: no step may materialise a
// google-services.json (from a secret or otherwise), and the gradle tasks the job runs
// must still include the lint/test and assembleDebug invocations the conditional wiring
// has to keep green.
func TestR3A_TheCIAndroidJobRunsPluginAbsent(t *testing.T) {
	src := readFileOrFail(t, filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"),
		"the CI workflow")
	if strings.Contains(src, "google-services.json") {
		t.Error("ci.yml references google-services.json; CI must run the plugin-absent shape, " +
			"never a materialised secret config")
	}

	var android *ciJob
	jobs := parseCIJobs(src)
	for i := range jobs {
		if jobs[i].id == "android" {
			android = &jobs[i]
		}
	}
	if android == nil {
		t.Fatal("ci.yml has no android job")
	}
	tasks := android.gradleTasks()
	joined := strings.Join(tasks, " ")
	for _, want := range []string{"lint", "test", ":app:assembleDebug"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the android job's gradle invocations %q no longer include %q; the "+
				"plugin-absent gate would stop exercising it", tasks, want)
		}
	}
}

// TestR3A_TheStaleNotWiredRecordIsGone: the build script's PB-PUSH-9 block currently
// records "the com.google.gms.google-services plugin -- which is deliberately NOT
// applied". Once R3 wires the plugin, that record is FALSE, and a decision comment that
// contradicts the build it sits in is worse than none (the no-drift rule every gate in
// this package enforces). The comment must be updated in the same change that applies
// the plugin.
func TestR3A_TheStaleNotWiredRecordIsGone(t *testing.T) {
	raw := readFileOrFail(t, filepath.Join(appModule(t), "build.gradle.kts"),
		"the app module build script")
	if strings.Contains(raw, "deliberately NOT applied") {
		t.Error("android/app/build.gradle.kts still records the google-services plugin as " +
			"\"deliberately NOT applied\"; the R3 wiring must retire that record in the same change")
	}
}
