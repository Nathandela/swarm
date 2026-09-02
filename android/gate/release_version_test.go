package gate

import (
	"path/filepath"
	"regexp"
	"testing"
)

// TestPlayReleaseIdentityIsV01320Code32 pins the two values Google Play uses to
// distinguish this upload. The v0.13.19 tag and container release identity were
// consumed before its durable GitHub release completed. Play did not publish
// versionCode 31, but the replacement still advances both values together so a
// new source release never reuses the previous candidate's Android identity.
func TestPlayReleaseIdentityIsV01320Code32(t *testing.T) {
	build := readFileOrFail(t, filepath.Join(appModule(t), "build.gradle.kts"), "Play release identity")

	assertSingleGradleAssignment(t, build, "versionCode", `32`)
	assertSingleGradleAssignment(t, build, "versionName", `"0.13.20"`)
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
