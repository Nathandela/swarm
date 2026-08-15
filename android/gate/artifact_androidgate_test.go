//go:build androidgate

package gate

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// The lane that runs the real toolchain. Behind the `androidgate` build tag so
// `go test ./...` stays fast on runners with no Android SDK; PB-TOOL-7's
// untagged test asserts CI actually invokes this tag, so the tag cannot become
// a place where assertions go to stop running.
//
// Nothing here calls t.Skip. A skipped assertion in the one lane that inspects
// real artifacts is the failure mode this package exists to prevent.

// runInPinnedShell executes a command in a scrubbed shell that has sourced the
// toolchain pin. /usr/local/bin is NOT on PATH: it holds both the system Gradle
// (9.6.1) and the Go toolchain on this host, so anything that works here works
// because the pin made it work.
func runInPinnedShell(t *testing.T, timeout time.Duration, script string) (string, error) {
	t.Helper()
	root := repoRoot(t)
	full := ". " + shellQuote(filepath.Join(root, "android", "toolchain.env")) + " && " + script

	cmd := exec.Command("/bin/sh", "-c", full)
	cmd.Env = scrubbedEnv()
	cmd.Dir = root

	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		t.Fatalf("timed out after %v running: %s", timeout, script)
	}
	return string(out), err
}

// TestPBTOOL1_FreshShellCanResolveTheWholeToolchain is PB-TOOL-1's criterion in
// full: "a fresh shell sourcing it can build".
//
// gobind is listed explicitly. `gomobile bind` spawns gobind as a CHILD process
// and, when gobind exists but is not on the child's PATH, reports "gomobile:
// gobind was not found. Please run gomobile init" -- an error that names the
// wrong cause and sent the S8 reviewer down a dead end.
func TestPBTOOL1_FreshShellCanResolveTheWholeToolchain(t *testing.T) {
	for _, tool := range []string{"java", "javac", "go", "gomobile", "gobind"} {
		out, err := runInPinnedShell(t, 60*time.Second, "command -v "+tool)
		if err != nil {
			t.Errorf("PB-TOOL-1: after sourcing the pin, a fresh shell cannot find %q: %v\n%s",
				tool, err, out)
			continue
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("PB-TOOL-1: `command -v %s` produced no path", tool)
		}
	}

	// The pinned JDK must be the one that answers, not whatever the host has.
	env := sourcePin(t, repoRoot(t))
	wantMajor := strings.TrimSpace(env["SWARM_JDK_MAJOR"])
	out, err := runInPinnedShell(t, 60*time.Second, "java -version 2>&1")
	if err != nil {
		t.Fatalf("PB-TOOL-1: `java -version` failed in a pinned fresh shell: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`version "(\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("PB-TOOL-1: cannot read a version from:\n%s", out)
	}
	if m[1] != wantMajor {
		t.Errorf("PB-TOOL-1: the pinned shell resolves JDK %s, pin says SWARM_JDK_MAJOR=%s",
			m[1], wantMajor)
	}
}

// TestPBTOOL1_GomobileFindsGobindFromThePinnedShell reproduces the exact defect
// the S8 harness hit, rather than proxying it with `command -v gobind`.
//
// `gomobile bind` spawns gobind as a CHILD process. When gobind exists but is
// not on the child's PATH, gomobile reports:
//
//	gobind was not found. Please run gomobile init before trying again
//
// which names the wrong cause -- gobind is installed and `gomobile init` does
// not fix it -- and cost the S8 reviewer real time to decode. PB-TOOL-1's pin is
// what should make it impossible.
//
// The probe deliberately names a package that does not exist, so gomobile
// resolves gobind and then fails on the package. Both outcomes are ~1 s
// (measured: 0.37 s for the trap, 1.24 s for the package error), so this is a
// cheap check of the real thing:
//
//   - gobind unreachable  -> "gobind was not found"        <- the defect
//   - gobind reachable    -> "no exported names in the package"  <- expected
//
// A test that merely asserted the command failed would pass in both cases.
func TestPBTOOL1_GomobileFindsGobindFromThePinnedShell(t *testing.T) {
	out, err := runInPinnedShell(t, 5*time.Minute,
		`gomobile bind -androidapi "$SWARM_ANDROID_API" -target android/arm64 `+
			`-o "$TMPDIR/pb-tool-1-probe.aar" ./no/such/package`)
	if err == nil {
		t.Fatalf("PB-TOOL-1: the gobind probe was expected to fail on a non-existent "+
			"package but succeeded; it no longer discriminates anything:\n%s", out)
	}
	if strings.Contains(out, "gobind was not found") {
		t.Fatalf("PB-TOOL-1: `gomobile bind` cannot reach gobind from a shell that has "+
			"sourced the pin. The message below names the wrong cause -- gobind IS "+
			"installed; it is absent from the child process's PATH, and `gomobile init` "+
			"does not fix that. The pin must put $(go env GOPATH)/bin on PATH:\n%s", out)
	}
	if !strings.Contains(out, "no exported names in the package") {
		t.Fatalf("PB-TOOL-1: the gobind probe failed for an unrecognised reason, so it "+
			"proves nothing about gobind's reachability:\n%s", out)
	}
}

// TestPBTOOL1_PinnedSDKComponentsExistOnDisk turns the pinned version strings
// into claims about reality. A pin naming an NDK the host does not have reads
// exactly like a correct pin until someone runs a build.
func TestPBTOOL1_PinnedSDKComponentsExistOnDisk(t *testing.T) {
	env := sourcePin(t, repoRoot(t))
	sdk := env["ANDROID_HOME"]
	if sdk == "" {
		t.Fatalf("PB-TOOL-1: the pin exports no ANDROID_HOME")
	}
	checks := []struct{ what, path string }{
		{"NDK " + env["SWARM_ANDROID_NDK"], filepath.Join(sdk, "ndk", env["SWARM_ANDROID_NDK"])},
		{"build-tools " + env["SWARM_ANDROID_BUILD_TOOLS"], filepath.Join(sdk, "build-tools", env["SWARM_ANDROID_BUILD_TOOLS"])},
		{"platform android-" + env["SWARM_ANDROID_COMPILE_SDK"], filepath.Join(sdk, "platforms", "android-"+env["SWARM_ANDROID_COMPILE_SDK"])},
	}
	for _, c := range checks {
		if !exists(c.path) {
			t.Errorf("PB-TOOL-1: the pin names %s but %s does not exist", c.what, c.path)
		}
	}
	if ndk := env["ANDROID_NDK_HOME"]; ndk == "" || !exists(ndk) {
		t.Errorf("PB-TOOL-1: ANDROID_NDK_HOME=%q does not exist; `gomobile bind` cannot "+
			"cross-compile libgojni.so", ndk)
	}
}

// TestPBTOOL2_OneCommandProducesAnAARWithEveryDeclaredABI is the artifact half.
func TestPBTOOL2_OneCommandProducesAnAARWithEveryDeclaredABI(t *testing.T) {
	out, err := runInPinnedShell(t, 20*time.Minute, "./android/build-aar.sh")
	if err != nil {
		t.Fatalf("PB-TOOL-2: the AAR build command failed: %v\n%s", err, out)
	}

	aar := findAAR(t)
	env := sourcePin(t, repoRoot(t))
	assertAARContents(t, aar, declaredABIs(t, env))
}

// findAAR locates the artifact the build command produced. It insists on exactly
// one: two AARs from different runs would let the inspection assert against a
// stale artifact that predates the change under test.
func findAAR(t *testing.T) string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(androidRoot(t), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(path, ".aar") {
			found = append(found, path)
		}
		return nil
	})
	switch len(found) {
	case 0:
		t.Fatalf("PB-TOOL-2: the build command exited 0 but produced no .aar under %s. "+
			"An exit-status check is vacuous here: gobind exits 0 while silently dropping "+
			"bind-illegal exports", mustRel(t, androidRoot(t)))
	case 1:
		return found[0]
	default:
		t.Fatalf("PB-TOOL-2: %d AARs under android/: %v. The inspection cannot tell which "+
			"one the build just produced", len(found), found)
	}
	return ""
}

// TestPBTOOL4_GradlewRunsWithoutSystemGradle. The scrubbed PATH is the whole
// point: this host has Gradle 9.6.1 at /usr/local/bin/gradle, so a run with the
// ambient PATH would pass with no wrapper checked in at all.
func TestPBTOOL4_GradlewRunsWithoutSystemGradle(t *testing.T) {
	out, err := runInPinnedShell(t, 15*time.Minute, "cd android && ./gradlew --version")
	if err != nil {
		t.Fatalf("PB-TOOL-4: `./gradlew --version` failed with /usr/local/bin off PATH: %v\n%s",
			err, out)
	}
	env := sourcePin(t, repoRoot(t))
	want := strings.TrimSpace(env["SWARM_GRADLE_VERSION"])
	m := regexp.MustCompile(`(?m)^Gradle\s+(\S+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("PB-TOOL-4: no Gradle version line in:\n%s", out)
	}
	if m[1] != want {
		t.Errorf("PB-TOOL-4: the wrapper ran Gradle %s, the pin says %s", m[1], want)
	}
}

// TestPBTOOL6_GradleGateIsGreen runs the gate the Definition of Done names.
func TestPBTOOL6_GradleGateIsGreen(t *testing.T) {
	out, err := runInPinnedShell(t, 30*time.Minute, "cd android && ./gradlew --no-daemon lint test")
	if err != nil {
		t.Fatalf("PB-TOOL-6: `./gradlew lint test` failed: %v\n%s", err, out)
	}
}

// TestPBTOOL6_EveryCheckedInKotlinTestActuallyRan reads the JUnit XML the gate
// produced. This is the assertion that a *Test.kt outside the compiled source
// set cannot survive: the file exists, the gate is green, and its report is
// simply absent.
func TestPBTOOL6_EveryCheckedInKotlinTestActuallyRan(t *testing.T) {
	results := filepath.Join(appModule(t), "build", "test-results")
	var reports []string
	_ = filepath.WalkDir(results, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasPrefix(filepath.Base(path), "TEST-") &&
			strings.HasSuffix(path, ".xml") {
			reports = append(reports, path)
		}
		return nil
	})
	if len(reports) == 0 {
		t.Fatalf("PB-TOOL-6: no JUnit reports under %s after the gate ran. `./gradlew test` "+
			"is green when it runs nothing", mustRel(t, results))
	}

	// Keyed by FULLY-QUALIFIED class name, which is what the XML's name= attribute
	// carries. Simple names happen to be unique across this module today, but nothing
	// enforces that: two packages may legally hold same-named suites, and under a
	// simple-name map the report from one would vouch for the other, which never ran.
	// Keying on the qualified name costs nothing and removes that failure mode.
	reported := map[string]int{}
	for _, r := range reports {
		body, err := os.ReadFile(r)
		if err != nil {
			continue
		}
		m := regexp.MustCompile(`name="([^"]+)"\s+tests="(\d+)"`).FindStringSubmatch(string(body))
		if m == nil {
			continue
		}
		n := 0
		for _, c := range m[2] {
			n = n*10 + int(c-'0')
		}
		reported[m[1]] = n
	}

	for _, f := range kotlinTestFiles(t, filepath.Join(appModule(t), "src", "test", "kotlin")) {
		classes := kotlinTestClasses(t, f)
		if len(classes) == 0 {
			t.Errorf("PB-TOOL-6: %s is named like a suite but declares no top-level class "+
				"holding an @Test, so there is nothing for Gradle to run", mustRel(t, f))
			continue
		}
		for _, cls := range classes {
			n, ok := reported[cls]
			if !ok {
				t.Errorf("PB-TOOL-6: %s declares %s but it produced no JUnit report; it is "+
					"not in a source set Gradle compiles, so it never ran", mustRel(t, f), cls)
				continue
			}
			if n == 0 {
				t.Errorf("PB-TOOL-6: %s: %s ran 0 tests", mustRel(t, f), cls)
			}
		}
	}
}

// kotlinTestClasses returns the fully-qualified name of every top-level class in
// one Kotlin file that holds at least one test annotation.
//
// WHY THIS EXISTS RATHER THAN A FILENAME. The check above used to derive the class
// from the file's base name, which is a JAVA rule that Kotlin does not have: a .kt
// file may declare any number of top-level classes under any names. Four checked-in
// suites -- ConnectionAndErrorTest.kt, MachineAndLaunchTest.kt, PairingFlowTest.kt
// and SessionScreensTest.kt -- group two or three suites each (ConnectionBannerTest,
// ErrorRoutingTest, ...), so no report is ever emitted under the file's own name and
// the gate reported four compiled, running, passing files as never having run.
//
// It is STRICTER than the rule it replaces, not looser. The filename rule checked at
// most one class per file and was blind to the other nine in those four files; this
// requires a report, with a non-zero test count, for EVERY top-level class a file
// declares that carries at least one test annotation. A file whose second class
// silently stops running now fails, where before only the first one was ever looked
// for. A class carrying no test annotation at all is not demanded, because JUnit
// would not run it either.
//
// The annotation is matched by regexp, not by a "@Test" substring: Kotlin permits the
// fully-qualified form, and PairingFlowTest.kt writes `@org.junit.Test`. A substring
// rule silently dropped that suite -- the demanded set came to 142 against 143 emitted
// reports, and the one suite the gate stopped watching was the one it could least
// afford to lose, since a file whose OTHER classes still report keeps the gate green.
// The pattern accepts @Test and any qualified prefix while the trailing \b rejects
// @ParameterizedTest, @TestFactory and @Testable.
//
// Top-level only, by the `^class` anchor: a nested class is compiled and run as part
// of its enclosing suite, and JUnit 4 reports it under the enclosing name unless the
// file opts into a runner like Enclosed -- which nothing in this module does, and
// which TestPBTOOL6_GradleGateIsGreen would surface as a failure if it did.
func kotlinTestClasses(t *testing.T, file string) []string {
	t.Helper()
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("PB-TOOL-6: cannot read %s: %v", mustRel(t, file), err)
	}
	src := string(body)

	pkg := ""
	if m := regexp.MustCompile(`(?m)^package\s+([\w.]+)`).FindStringSubmatch(src); m != nil {
		pkg = m[1] + "."
	}

	decl := regexp.MustCompile(`(?m)^(?:(?:public|internal|private|open|final)\s+)*class\s+(\w+)`)
	testAnno := regexp.MustCompile(`@(?:[\w.]+\.)?Test\b`)
	locs := decl.FindAllStringSubmatchIndex(src, -1)
	var out []string
	for i, loc := range locs {
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		// The class body runs to the next top-level declaration. An abstract or
		// sealed base is excluded by the modifier list above; JUnit does not run one.
		if !testAnno.MatchString(src[loc[0]:end]) {
			continue
		}
		out = append(out, pkg+src[loc[2]:loc[3]])
	}
	return out
}

// TestPBTOOL3_DebugAPKBuildsAndIsSigned.
func TestPBTOOL3_DebugAPKBuildsAndIsSigned(t *testing.T) {
	out, err := runInPinnedShell(t, 30*time.Minute, "cd android && ./gradlew --no-daemon :app:assembleDebug")
	if err != nil {
		t.Fatalf("PB-TOOL-3: the debug APK build failed: %v\n%s", err, out)
	}
	apk := findDebugAPK(t)

	entries := readArchive(t, apk)
	need := map[string]bool{"AndroidManifest.xml": false, "classes.dex": false}
	sawNativeLib := false
	for _, e := range entries {
		if _, ok := need[e.name]; ok {
			need[e.name] = true
		}
		if strings.HasPrefix(e.name, "lib/") && strings.HasSuffix(e.name, "libgojni.so") {
			sawNativeLib = true
		}
	}
	for name, got := range need {
		if !got {
			t.Errorf("PB-TOOL-3: the debug APK contains no %s; it is not installable", name)
		}
	}
	if !sawNativeLib {
		t.Errorf("PB-TOOL-3/PB-TOOL-2: the debug APK contains no lib/*/libgojni.so, so the " +
			"AAR this whole slice exists to consume is not actually in the app")
	}

	out, err = runInPinnedShell(t, 5*time.Minute,
		"\"$ANDROID_HOME/build-tools/$SWARM_ANDROID_BUILD_TOOLS/apksigner\" verify --verbose "+
			shellQuote(apk))
	if err != nil {
		t.Errorf("PB-TOOL-3: apksigner cannot verify the debug APK: %v\n%s", err, out)
	}
}

// TestPBTOOL3_ReleaseBuildRefusesWithNoOperatorKeystore is the negative control
// for the signing requirement: with no operator material in the environment, the
// release build must FAIL. If it succeeds it has produced an unsigned release
// APK, which is the quiet form of the defect.
func TestPBTOOL3_ReleaseBuildRefusesWithNoOperatorKeystore(t *testing.T) {
	out, err := runInPinnedShell(t, 30*time.Minute,
		"unset SWARM_RELEASE_KEYSTORE SWARM_RELEASE_KEYSTORE_PASSWORD "+
			"SWARM_RELEASE_KEY_ALIAS SWARM_RELEASE_KEY_PASSWORD; "+
			"cd android && ./gradlew --no-daemon :app:assembleRelease")
	if err == nil {
		t.Fatalf("PB-TOOL-3: the release build SUCCEEDED with no operator keystore. It has "+
			"produced an unsigned or debug-signed release artifact:\n%s", out)
	}
}

// TestPBRUN1_BuiltAPKReportsThePinnedSdkLevels is PB-RUN-1's "build enforces
// it", asserted on the artifact rather than on the build script.
func TestPBRUN1_BuiltAPKReportsThePinnedSdkLevels(t *testing.T) {
	apk := findDebugAPK(t)
	out, err := runInPinnedShell(t, 5*time.Minute,
		"\"$ANDROID_HOME/build-tools/$SWARM_ANDROID_BUILD_TOOLS/aapt2\" dump badging "+shellQuote(apk))
	if err != nil {
		t.Fatalf("PB-RUN-1: aapt2 dump badging failed: %v\n%s", err, out)
	}
	env := sourcePin(t, repoRoot(t))
	// minSdkVersion, not sdkVersion. `sdkVersion:'` is AAPT1's label; aapt2 renamed it, and
	// this test invokes aapt2. Verified on the built APK with build-tools 35.0.0:
	//   aapt  dump badging -> sdkVersion:'33'        targetSdkVersion:'35'
	//   aapt2 dump badging -> minSdkVersion:'33'     targetSdkVersion:'35'
	//   aapt2 ... | grep -c "[^a-zA-Z]sdkVersion:'"  -> 0
	// The old key was unsatisfiable by ANY APK, forever, while the targetSdkVersion row of
	// this same table passed because that label is identical in both tools -- so half the
	// assertion worked and masked the other half. The `[^a-zA-Z]` anchor below keeps
	// minSdkVersion from being matched by a bare sdkVersion pattern in the other direction.
	for _, c := range []struct{ label, key, want string }{
		{"minSdkVersion", "minSdkVersion", env["SWARM_ANDROID_MIN_SDK"]},
		{"targetSdkVersion", "targetSdkVersion", env["SWARM_ANDROID_TARGET_SDK"]},
	} {
		re := regexp.MustCompile(`[^a-zA-Z]` + c.key + `:'(\d+)'`)
		m := re.FindStringSubmatch(out)
		if m == nil {
			t.Errorf("PB-RUN-1: aapt2 reports no %s", c.label)
			continue
		}
		if m[1] != c.want {
			t.Errorf("PB-RUN-1: the built APK declares %s=%s, the pin says %s",
				c.label, m[1], c.want)
		}
	}
}

func findDebugAPK(t *testing.T) string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(filepath.Join(appModule(t), "build", "outputs", "apk", "debug"),
		func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(path, ".apk") {
				found = append(found, path)
			}
			return nil
		})
	if len(found) == 0 {
		t.Fatalf("PB-TOOL-3: no debug APK under android/app/build/outputs/apk/debug")
	}
	return found[0]
}

// ---------------------------------------------------------------------------
// Archive inspection, shared by the AAR (PB-TOOL-2) and APK (PB-TOOL-3) tests.
// ---------------------------------------------------------------------------

type archiveEntry struct {
	name string
	body []byte
}

func readArchive(t *testing.T, path string) []archiveEntry {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("cannot open %s as a zip archive: %v", path, err)
	}
	defer func() { _ = zr.Close() }()
	var entries []archiveEntry
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("cannot open %s inside %s: %v", f.Name, path, err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("cannot read %s inside %s: %v", f.Name, path, err)
		}
		entries = append(entries, archiveEntry{name: f.Name, body: body})
	}
	if len(entries) == 0 {
		t.Fatalf("%s contains no entries; every assertion over it would pass vacuously", path)
	}
	return entries
}

// findAbsoluteBuilderPaths scans a binary for absolute host paths. This is the
// PB-TOOL-2 reproducibility check: the S8 reviewer found 48 such strings in the
// shipped libgojni.so because the AAR was not built with -trimpath.
func findAbsoluteBuilderPaths(body []byte) []string {
	needles := [][]byte{
		[]byte("/Users/"),
		[]byte("/home/"),
		[]byte("/private/var/folders/"), // macOS TMPDIR, where gomobile stages its work tree
	}
	seen := map[string]bool{}
	var found []string
	for _, needle := range needles {
		rest := body
		for {
			i := bytes.Index(rest, needle)
			if i < 0 {
				break
			}
			// Take the printable run around the hit so the failure message
			// names a real path rather than an offset.
			start := i
			end := i
			for end < len(rest) && isPathByte(rest[end]) {
				end++
			}
			s := string(rest[start:end])
			if !seen[s] {
				seen[s] = true
				found = append(found, s)
			}
			rest = rest[i+len(needle):]
		}
	}
	return found
}

func isPathByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("/._-+@", b) >= 0
}

// assertAARContents is the artifact half of PB-TOOL-2, called by the tagged test
// in aar_artifact_test.go after the build command has actually run.
func assertAARContents(t *testing.T, aarPath string, wantABIs []string) {
	t.Helper()
	entries := readArchive(t, aarPath)

	got := map[string][]byte{}
	for _, e := range entries {
		if strings.HasPrefix(e.name, "jni/") && strings.HasSuffix(e.name, "/libgojni.so") {
			abi := strings.TrimSuffix(strings.TrimPrefix(e.name, "jni/"), "/libgojni.so")
			got[abi] = e.body
		}
	}

	// Per required ABI, not "at least one".
	for _, abi := range wantABIs {
		if _, ok := got[abi]; !ok {
			t.Errorf("PB-TOOL-2: %s contains no jni/%s/libgojni.so. Present: %v",
				filepath.Base(aarPath), abi, sortedKeys(got))
		}
	}
	// And no extras: "explicit ABI set" is exact.
	want := map[string]bool{}
	for _, a := range wantABIs {
		want[a] = true
	}
	for abi := range got {
		if !want[abi] {
			t.Errorf("PB-TOOL-2: %s ships jni/%s/libgojni.so, which SWARM_AAR_ABIS does not "+
				"declare. The ABI set is a decision, not a floor", filepath.Base(aarPath), abi)
		}
	}
	if len(got) == 0 {
		t.Fatalf("PB-TOOL-2: %s contains no jni/*/libgojni.so at all", aarPath)
	}

	// Reproducibility, per shipped .so.
	for abi, body := range got {
		leaks := findAbsoluteBuilderPaths(body)
		if len(leaks) > 0 {
			sort.Strings(leaks)
			shown := leaks
			if len(shown) > 8 {
				shown = shown[:8]
			}
			t.Errorf("PB-TOOL-2: jni/%s/libgojni.so leaks %d absolute builder paths "+
				"(build without -trimpath). First: %v", abi, len(leaks), shown)
		}
	}
}
