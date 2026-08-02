package gate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// PB-TOOL-1 -- "Toolchain pinned in-repo (JDK, SDK, build-tools, NDK, gomobile,
// -androidapi). Criterion: a fresh shell sourcing it can build."
//
// Why this requirement is not paperwork on this host: nothing Android is on
// PATH and no ANDROID_* variable is exported by default. `which adb`,
// `java -version` and `ls ~/Library/Android/sdk` all report that there is no
// Android toolchain at all, while a complete one sits in
// /usr/local/share/android-commandlinetools with a Homebrew JDK 17 that
// /usr/libexec/java_home does not know about. The pin is what stops the next
// reader concluding the toolchain is absent.
//
// The pin is android/toolchain.env: a POSIX-sh file that is SOURCED, not parsed.
// Sourcing is the criterion, so sourcing is the test.

// pinKeys are the values the pin must export. Each entry names why it exists, so
// a future reader can tell a pin from a wish list.
var pinKeys = []struct {
	key string
	why string
}{
	{"JAVA_HOME", "the JDK is Homebrew-installed and NOT registered with /usr/libexec/java_home"},
	{"ANDROID_HOME", "the SDK is in a non-standard location"},
	{"ANDROID_SDK_ROOT", "gomobile and AGP read different variables for the same thing"},
	{"ANDROID_NDK_HOME", "gomobile bind needs the NDK explicitly; there is no default"},
	{"SWARM_JDK_MAJOR", "PB-TOOL-1: the pinned JDK feature release"},
	{"SWARM_GO_VERSION", "the AAR is a Go cross-compile; a fresh shell must find a Go toolchain"},
	{"SWARM_ANDROID_COMPILE_SDK", "PB-RUN-1: the API the app compiles against"},
	{"SWARM_ANDROID_MIN_SDK", "PB-RUN-1: the app's floor. NOT the NDK's -androidapi"},
	{"SWARM_ANDROID_TARGET_SDK", "PB-RUN-1: the API whose behaviour changes the app opts into"},
	{"SWARM_ANDROID_BUILD_TOOLS", "PB-TOOL-1"},
	{"SWARM_ANDROID_NDK", "PB-TOOL-1; NDK 27 supports API 21..35"},
	{"SWARM_ANDROID_API", "gomobile -androidapi: the NDK's floor, a different number from minSdk"},
	{"SWARM_GOMOBILE_VERSION", "PB-TOOL-1: pinned golang.org/x/mobile; must equal go.mod"},
	{"SWARM_GRADLE_VERSION", "PB-TOOL-4: must equal the wrapper's distributionUrl"},
	{"SWARM_GRADLE_DISTRIBUTION_SHA256", "PB-TOOL-4: an unpinned distribution is not a pin"},
	{"SWARM_AAR_ABIS", "PB-TOOL-2: the explicit ABI set, not a glob"},
}

// TestPBTOOL1_PinFileIsCheckedIn is the coarsest RED: the file does not exist.
func TestPBTOOL1_PinFileIsCheckedIn(t *testing.T) {
	path := pinPath(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("PB-TOOL-1: no toolchain pin at %s. A fresh shell has no ANDROID_HOME, "+
			"no JAVA_HOME and nothing Android on PATH on this host, so without this file "+
			"the toolchain is undiscoverable. Required: a POSIX-sh file that can be "+
			"sourced, exporting %v.", mustRel(t, path), pinKeyNames())
	}
	if info.IsDir() {
		t.Fatalf("PB-TOOL-1: %s is a directory; the pin must be a sourceable file", mustRel(t, path))
	}

	// The pin must be tracked. A pin that only exists on the author's disk pins
	// nothing, and this is the exact mutation that would otherwise pass: create
	// the file, never `git add` it.
	rel := mustRel(t, path)
	for _, f := range trackedFiles(t, repoRoot(t)) {
		if f == rel {
			return
		}
	}
	t.Fatalf("PB-TOOL-1: %s exists on disk but is not tracked by git; an untracked pin "+
		"pins nothing for anyone else", rel)
}

// TestPBTOOL1_FreshShellSourcingExportsEveryPin is the criterion itself, minus
// running the compilers: a scrubbed shell sources the pin and every pinned value
// is exported and non-empty.
func TestPBTOOL1_FreshShellSourcingExportsEveryPin(t *testing.T) {
	env := sourcePin(t, repoRoot(t))
	var missing []string
	for _, k := range pinKeys {
		if strings.TrimSpace(env[k.key]) == "" {
			missing = append(missing, k.key+" ("+k.why+")")
		}
	}
	if len(missing) > 0 {
		t.Fatalf("PB-TOOL-1: sourcing the pin in a fresh shell left these unset:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// TestPBTOOL1_AndroidAPIFloorIsTheNDKFloor pins the -androidapi value against the
// NDK's real floor. gomobile defaults to API 16 and NDK 27 refuses it, so every
// bind must pass -androidapi >= 21.
func TestPBTOOL1_AndroidAPIFloorIsTheNDKFloor(t *testing.T) {
	env := sourcePin(t, repoRoot(t))
	const ndk27Floor = 21
	if got := pinInt(t, env, "SWARM_ANDROID_API"); got < ndk27Floor {
		t.Fatalf("PB-TOOL-1: SWARM_ANDROID_API=%d is below the NDK 27 floor of %d; "+
			"gomobile's own default (16) fails outright", got, ndk27Floor)
	}
}

// TestPBTOOL1_AndroidAPIIsNotConflatedWithMinSdk guards the trap PB-RUN-1 names
// explicitly: "the gomobile -androidapi floor is the NDK's, not the app's".
//
// The check is deliberately NOT "they are equal" -- asserting equality would be
// asserting the bug. What is actually true is an ordering: the native library is
// built to run on SWARM_ANDROID_API and up, so an app whose minSdk is BELOW that
// ships a .so its own floor devices cannot load. minSdk >= androidapi is the real
// invariant, and the two must be separately declared so a later edit to one does
// not silently move the other.
func TestPBTOOL1_AndroidAPIIsNotConflatedWithMinSdk(t *testing.T) {
	root := repoRoot(t)
	env := sourcePin(t, root)
	androidAPI := pinInt(t, env, "SWARM_ANDROID_API")
	minSdk := pinInt(t, env, "SWARM_ANDROID_MIN_SDK")

	if minSdk < androidAPI {
		t.Fatalf("PB-TOOL-1/PB-RUN-1: minSdk=%d is below the native floor -androidapi=%d. "+
			"Devices at API %d..%d would install the app and fail to load libgojni.so",
			minSdk, androidAPI, minSdk, androidAPI-1)
	}

	// Separately declared, not aliased. `SWARM_ANDROID_MIN_SDK="$SWARM_ANDROID_API"`
	// would satisfy the ordering above while re-creating exactly the conflation
	// PB-RUN-1 warns about.
	src := readFileOrFail(t, pinPath(t), "PB-TOOL-1")
	alias := regexp.MustCompile(`SWARM_ANDROID_MIN_SDK=[^\n]*\$\{?SWARM_ANDROID_API`)
	if alias.MatchString(src) {
		t.Fatalf("PB-TOOL-1/PB-RUN-1: SWARM_ANDROID_MIN_SDK is defined in terms of " +
			"SWARM_ANDROID_API. They are different decisions -- the app's supported floor " +
			"versus the NDK's native floor -- and must be pinned independently")
	}
	_ = root
}

// TestPBTOOL1_GomobileVersionMatchesGoMod makes the pin drift-detecting rather
// than decorative: golang.org/x/mobile is in the module graph via the `tool`
// directive, and the pinned version must be that version.
func TestPBTOOL1_GomobileVersionMatchesGoMod(t *testing.T) {
	root := repoRoot(t)
	env := sourcePin(t, root)
	pinned := strings.TrimSpace(env["SWARM_GOMOBILE_VERSION"])

	gomod := readFileOrFail(t, filepath.Join(root, "go.mod"), "PB-TOOL-1")
	re := regexp.MustCompile(`(?m)^\s*golang\.org/x/mobile\s+(v\S+)`)
	m := re.FindStringSubmatch(gomod)
	if m == nil {
		t.Fatalf("PB-TOOL-1: go.mod does not require golang.org/x/mobile, so `gomobile bind` " +
			"cannot resolve gobind. §2 of the requirements: it must be in the module " +
			"dependency graph via `go get -tool golang.org/x/mobile/cmd/gobind`")
	}
	if pinned != m[1] {
		t.Fatalf("PB-TOOL-1: SWARM_GOMOBILE_VERSION=%q but go.mod pins golang.org/x/mobile %s. "+
			"A pin that disagrees with the build is worse than no pin", pinned, m[1])
	}

	if !strings.Contains(gomod, "tool golang.org/x/mobile/cmd/gobind") {
		t.Fatalf("PB-TOOL-1: go.mod has no `tool golang.org/x/mobile/cmd/gobind` directive. " +
			"Without it gobind is not reproducibly available and `gomobile bind` fails with " +
			"the misleading \"gobind was not found. Please run gomobile init\"")
	}
}

// TestPBTOOL5_GomobileToolDoesNotEnterTheDaemonBinaries is the other half of the
// tool directive: it must not link x/mobile into the shipped binaries.
//
// This is PB-TOOL-5 ("no Go regression") in its only mechanically checkable
// form. `go test -race ./...` cannot be asserted from inside itself, so what is
// asserted here is the regression this slice is actually capable of causing:
// dragging a mobile toolchain dependency into cmd/swarm.
func TestPBTOOL5_GomobileToolDoesNotEnterTheDaemonBinaries(t *testing.T) {
	root := repoRoot(t)
	for _, pkg := range []string{"./cmd/swarm", "./cmd/swarm-remote"} {
		if !exists(filepath.Join(root, strings.TrimPrefix(pkg, "./"))) {
			continue
		}
		deps := goListDeps(t, root, pkg)
		for _, d := range deps {
			if strings.HasPrefix(d, "golang.org/x/mobile") {
				t.Errorf("PB-TOOL-5: %s depends on %s; the gomobile tool directive must stay "+
					"a build-time tool and never enter a shipped binary", pkg, d)
			}
		}
	}
}

func pinKeyNames() []string {
	out := make([]string, 0, len(pinKeys))
	for _, k := range pinKeys {
		out = append(out, k.key)
	}
	return out
}
