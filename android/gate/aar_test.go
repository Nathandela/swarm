package gate

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// PB-TOOL-2 -- "One command builds the AAR for an explicit ABI set including
// arm64-v8a (v1's jni/<abi> glob allowed an x86-only AAR). Artifact inspected
// for each required ABI."
//
// Two failure modes are in scope and they are different:
//
//  1. Coverage. A glob over jni/*/libgojni.so is satisfied by ONE ABI. The
//     assertion must be per required ABI, and must also reject EXTRA ABIs --
//     otherwise "explicit ABI set" degenerates into "at least these", and the
//     artifact grows an unshippable x86 payload nobody decided to ship.
//  2. Reproducibility. The S8 reviewer found 48 absolute builder paths
//     (/Users/Nathan/go/pkg/mod/...) inside the shipped libgojni.so because the
//     bind ran without -trimpath. PB-TOOL-2 owns build reproducibility, so the
//     flag is pinned here and the artifact is scanned for the strings it removes.

// gomobileABIs is the complete set gomobile can emit for -target=android. A
// declared ABI outside this set is a typo that would silently produce nothing.
var gomobileABIs = map[string]string{
	"armeabi-v7a": "android/arm",
	"arm64-v8a":   "android/arm64",
	"x86":         "android/386",
	"x86_64":      "android/amd64",
}

func buildAARScript(t *testing.T) string { return filepath.Join(androidRoot(t), "build-aar.sh") }

// TestPBTOOL2_OneCommandBuildsTheAAR asserts the single command exists and is
// executable. "One command" is the requirement; a README paragraph is not one.
func TestPBTOOL2_OneCommandBuildsTheAAR(t *testing.T) {
	path := buildAARScript(t)
	if !exists(path) {
		t.Fatalf("PB-TOOL-2: no single AAR build command at %s. S8 produced its AAR by "+
			"hand; PB-TOOL-2 requires one checked-in command that anyone can run",
			mustRel(t, path))
	}
	info, err := statFile(path)
	if err != nil {
		t.Fatalf("PB-TOOL-2: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("PB-TOOL-2: %s is not executable", mustRel(t, path))
	}
}

// TestPBTOOL2_BuildCommandDeclaresAnExplicitABISet reads the build command's
// target list out of the pin, not out of the script, so the ABI set is a
// reviewable decision rather than a flag buried in a shell line.
func TestPBTOOL2_BuildCommandDeclaresAnExplicitABISet(t *testing.T) {
	env := sourcePin(t, repoRoot(t))
	abis := declaredABIs(t, env)

	if len(abis) == 0 {
		t.Fatalf("PB-TOOL-2: SWARM_AAR_ABIS is empty. An empty ABI set makes every " +
			"per-ABI assertion below pass vacuously, which is the v1 defect in its " +
			"purest form")
	}
	var hasArm64 bool
	for _, abi := range abis {
		if _, ok := gomobileABIs[abi]; !ok {
			t.Errorf("PB-TOOL-2: SWARM_AAR_ABIS names %q, which gomobile cannot emit. "+
				"Legal values: %v", abi, sortedKeys(gomobileABIs))
		}
		if abi == "arm64-v8a" {
			hasArm64 = true
		}
	}
	if !hasArm64 {
		t.Errorf("PB-TOOL-2: SWARM_AAR_ABIS=%v does not include arm64-v8a. That is the "+
			"named requirement and it is also the only ABI the swarmtest AVD "+
			"(google_apis/arm64-v8a) and every current handset can run", abis)
	}
}

// TestPBTOOL2_BuildCommandPassesTrimpathAndTheAndroidAPIFloor pins the two flags
// whose absence produced real defects: -trimpath (48 leaked builder paths) and
// -androidapi (gomobile defaults to 16 and NDK 27 refuses it).
func TestPBTOOL2_BuildCommandPassesTrimpathAndTheAndroidAPIFloor(t *testing.T) {
	src := readFileOrFail(t, buildAARScript(t), "PB-TOOL-2")

	if !regexp.MustCompile(`(^|\s)-trimpath(\s|$)`).MatchString(src) {
		t.Errorf("PB-TOOL-2: the AAR build command does not pass -trimpath. Without it " +
			"libgojni.so carries absolute builder paths -- S8's artifact carried 48 of " +
			"them, rooted at /Users/Nathan/go/pkg/mod -- and the build is not reproducible")
	}
	if !strings.Contains(src, "-androidapi") {
		t.Errorf("PB-TOOL-2: the AAR build command does not pass -androidapi. gomobile " +
			"defaults to API 16 and NDK 27 fails on it (§2)")
	}
	// The flag must take its value from the pin, not from a second literal that
	// can drift away from SWARM_ANDROID_API.
	if regexp.MustCompile(`-androidapi[= ]\d`).MatchString(src) {
		t.Errorf("PB-TOOL-2/PB-TOOL-1: -androidapi is given a literal in the build command. " +
			"It must read SWARM_ANDROID_API from the pin so the two cannot diverge")
	}
	// Same for the target list: a hard-coded -target here would make
	// SWARM_AAR_ABIS decorative.
	if regexp.MustCompile(`-target[= ]android/`).MatchString(src) {
		t.Errorf("PB-TOOL-2: -target is hard-coded in the build command. It must be " +
			"derived from SWARM_AAR_ABIS so the declared ABI set is the one that builds")
	}
}

// ---------------------------------------------------------------------------
// Artifact assertions. Tagged: these run the real cross-compile.
// ---------------------------------------------------------------------------

func declaredABIs(t *testing.T, env map[string]string) []string {
	t.Helper()
	raw := strings.TrimSpace(env["SWARM_AAR_ABIS"])
	if raw == "" {
		return nil
	}
	var out []string
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
