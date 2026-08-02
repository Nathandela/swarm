package gate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// PB-TOOL-6 -- "Android lint + unit tests in the gate. `./gradlew lint test` green."
//
// "Green" is the weakest possible criterion, because both halves are trivially
// green when they do nothing:
//
//   - `lint` is green when abortOnError is false, when every issue is severity
//     'informational', or when a lint-baseline.xml records the current findings
//     as accepted. A baseline is the standard way an Android project turns lint
//     into a no-op while keeping the word "lint" in the build log.
//   - `test` is green when there are no tests. It is also green when the tests
//     exist as files but sit outside the source set Gradle compiles -- a
//     checked-in *Test.kt that is never compiled is invisible in review and in
//     CI alike.
//
// So the assertions here are: lint must be able to fail, and every checked-in
// Kotlin test must be able to run.

func TestPBTOOL6_LintIsConfiguredToFailTheBuild(t *testing.T) {
	body := readFileOrFail(t, moduleBuildFile(t), "PB-TOOL-6")

	if regexp.MustCompile(`abortOnError\s*=\s*false`).MatchString(body) {
		t.Errorf("PB-TOOL-6: lint sets abortOnError = false, so `./gradlew lint` is green " +
			"by construction")
	}
	if !strings.Contains(body, "lint") {
		t.Errorf("PB-TOOL-6: %s has no lint block. AGP's defaults leave abortOnError true "+
			"but leave checkDependencies and the fatal set unstated; the gate must be a "+
			"decision", mustRel(t, moduleBuildFile(t)))
	}
	if regexp.MustCompile(`checkReleaseBuilds\s*=\s*false`).MatchString(body) {
		t.Errorf("PB-TOOL-6: lint sets checkReleaseBuilds = false")
	}
}

// TestPBTOOL6_NoLintBaselineSuppressesFindings. A baseline is legitimate on a
// legacy codebase. This module has no code yet, so a baseline here can only
// record findings the first implementation introduced -- which is the definition
// of a gate that never gated.
func TestPBTOOL6_NoLintBaselineSuppressesFindings(t *testing.T) {
	root := repoRoot(t)
	for _, f := range trackedFiles(t, root) {
		if !strings.HasPrefix(f, "android/") {
			continue
		}
		base := filepath.Base(f)
		if base == "lint-baseline.xml" || base == "baseline.xml" {
			body := readFileOrFail(t, filepath.Join(root, f), "PB-TOOL-6")
			if n := strings.Count(body, "<issue"); n > 0 {
				t.Errorf("PB-TOOL-6: %s accepts %d lint findings as a baseline, so "+
					"`./gradlew lint` is green while those findings stand. This module is "+
					"new -- there is no legacy to baseline", f, n)
			}
		}
	}
	body := readFileOrFail(t, moduleBuildFile(t), "PB-TOOL-6")
	if regexp.MustCompile(`baseline\s*=`).MatchString(body) {
		t.Errorf("PB-TOOL-6: %s configures a lint baseline", mustRel(t, moduleBuildFile(t)))
	}
}

// TestPBTOOL6_UnitTestSourceSetIsWiredForKotlin. The Kotlin tests this slice
// writes live in android/app/src/test/kotlin. If the module is configured
// without the Kotlin Android plugin, or with a redefined test source set, those
// files are inert.
func TestPBTOOL6_UnitTestSourceSetIsWiredForKotlin(t *testing.T) {
	body := readFileOrFail(t, moduleBuildFile(t), "PB-TOOL-6")
	if !strings.Contains(body, "kotlin.android") && !strings.Contains(body, "kotlin(\"android\")") {
		t.Errorf("PB-TOOL-6: %s does not apply the Kotlin Android plugin, so nothing "+
			"compiles src/test/kotlin", mustRel(t, moduleBuildFile(t)))
	}
	if !strings.Contains(body, "robolectric") && !strings.Contains(body, "Robolectric") {
		t.Errorf("PB-TOOL-6/PB-RUN-2/PB-TOK-4: %s declares no Robolectric dependency. The "+
			"permission-state, manifest and theme assertions need an Android runtime, and "+
			"the alternative (an instrumented test on the emulator) is the tier §10 warns "+
			"rots", mustRel(t, moduleBuildFile(t)))
	}
	if !strings.Contains(body, "unitTests") {
		t.Errorf("PB-TOOL-6: %s does not configure testOptions.unitTests. Robolectric needs "+
			"isIncludeAndroidResources = true to read the merged manifest and resources; "+
			"without it the PB-TOK-4 and PB-RUN-2 manifest assertions cannot resolve "+
			"anything", mustRel(t, moduleBuildFile(t)))
	}
}

// TestPBTOOL6_EveryCheckedInKotlinTestIsDiscoverable is the file-level half of
// "the test actually runs". The execution-level half lives in the tagged test,
// which reads the JUnit XML the gate produced and asserts one report per file.
func TestPBTOOL6_EveryCheckedInKotlinTestIsDiscoverable(t *testing.T) {
	testRoot := filepath.Join(appModule(t), "src", "test", "kotlin")
	files := kotlinTestFiles(t, testRoot)
	if len(files) == 0 {
		t.Fatalf("PB-TOOL-6: no Kotlin unit tests under %s. PB-RUN-2 (permission denial "+
			"paths), PB-RUN-5 (lifecycle convergence) and PB-TOK-4's behavioural half are "+
			"not expressible in Go", mustRel(t, testRoot))
	}
	for _, f := range files {
		body := readFileOrFail(t, f, "PB-TOOL-6")
		if !strings.Contains(body, "@Test") {
			t.Errorf("PB-TOOL-6: %s declares no @Test method", mustRel(t, f))
		}
		if strings.Contains(body, "@Ignore") {
			t.Errorf("PB-TOOL-6: %s carries @Ignore; an ignored test is green and empty",
				mustRel(t, f))
		}
	}
}

// kotlinTestFiles lists *Test.kt under a root, or nil if the root is absent.
func kotlinTestFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(path, "Test.kt") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil
	}
	return out
}
