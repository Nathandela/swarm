package gate

import (
	"path/filepath"
	"regexp"
	"testing"
)

// TestPlayReleaseIdentityIsV0163Code56 pins the two values Google Play uses to
// distinguish this upload. Every source release advances both together so a new
// release never reuses the preceding Android identity (v0.16.1/code 54).
func TestPlayReleaseIdentityIsV0163Code56(t *testing.T) {
	build := readFileOrFail(t, filepath.Join(appModule(t), "build.gradle.kts"), "Play release identity")

	assertSingleGradleAssignment(t, build, "versionCode", `56`)
	assertSingleGradleAssignment(t, build, "versionName", `"0.16.3"`)
}

func assertSingleGradleAssignment(t *testing.T, build, name, want string) {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*=\s*([^\s]+)\s*$`)
	matches := re.FindAllStringSubmatch(build, -1)
	if len(matches) != 1 {
		t.Fatalf("android/app/build.gradle.kts has %d %s assignments, want exactly one", len(matches), name)
	}
	if got := matches[0][1]; got != want {
		t.Fatalf("android/app/build.gradle.kts %s = %s, want %s", name, got, want)
	}
}
