package gate

import (
	"path/filepath"
	"regexp"
	"testing"
)

// TestPlayReleaseIdentityIsV01326Code38 pins the two values Google Play uses to
// distinguish this upload. Every source release advances both together so a new
// release never reuses a previous candidate's Android identity (v0.13.25 was
// versionCode 37).
func TestPlayReleaseIdentityIsV01326Code38(t *testing.T) {
	build := readFileOrFail(t, filepath.Join(appModule(t), "build.gradle.kts"), "Play release identity")

	assertSingleGradleAssignment(t, build, "versionCode", `38`)
	assertSingleGradleAssignment(t, build, "versionName", `"0.13.26"`)
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
