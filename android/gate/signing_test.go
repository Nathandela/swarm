package gate

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// PB-TOOL-3 -- "One command builds the debug APK; release signing reads an
// operator keystore from config/env, never the repo. Installable APK; no
// keystore or password in git."
//
// The interesting half is the negative one, and it has three distinct holes:
//
//   - a keystore committed to the tree,
//   - a password committed to the tree (including in gradle.properties, which is
//     exactly where the Android documentation's own example puts it),
//   - a signingConfig whose storeFile RESOLVES inside the repository even though
//     the literal is a variable -- `storeFile file(project.rootDir.path +
//     "/release.jks")` reads as configuration and is a committed keystore.
//
// The first two are string scans over the git index. The third needs the
// resolved path, which is why the tagged half evaluates the signing config
// through Gradle rather than by reading it.

var forbiddenKeystoreExts = []string{".jks", ".keystore", ".p12", ".pfx", ".bks"}

// forbiddenSecretKeys are the Gradle/Android property names whose presence with
// a literal value in a tracked file is a committed secret.
var forbiddenSecretKeys = []string{
	"storePassword",
	"keyPassword",
	"RELEASE_STORE_PASSWORD",
	"RELEASE_KEY_PASSWORD",
	"signingPassword",
}

// TestPBTOOL3_NoKeystoreIsTracked scans the git index, not the working tree: an
// ignored keystore on disk is the intended workflow, a tracked one is the defect.
func TestPBTOOL3_NoKeystoreIsTracked(t *testing.T) {
	root := repoRoot(t)
	for _, f := range trackedFiles(t, root) {
		lower := strings.ToLower(f)
		for _, ext := range forbiddenKeystoreExts {
			if strings.HasSuffix(lower, ext) {
				t.Errorf("PB-TOOL-3: %s is a tracked keystore. Release signing material "+
					"must come from operator config or the environment and never from git", f)
			}
		}
	}
}

// TestPBTOOL3_NoSigningPasswordIsTracked scans the CONTENT of the tracked files
// most likely to carry one. Restricting the scan keeps it fast and keeps it from
// matching this test file's own vocabulary.
func TestPBTOOL3_NoSigningPasswordIsTracked(t *testing.T) {
	root := repoRoot(t)
	scanned := 0
	for _, f := range trackedFiles(t, root) {
		if !isBuildConfigFile(f) {
			continue
		}
		scanned++
		body := readFileOrFail(t, filepath.Join(root, f), "PB-TOOL-3")
		for _, key := range forbiddenSecretKeys {
			// A literal assignment: storePassword "hunter2" / storePassword = "hunter2".
			re := regexp.MustCompile(`(?m)` + regexp.QuoteMeta(key) + `\s*=?\s*"[^"]+"`)
			if m := re.FindString(body); m != "" {
				t.Errorf("PB-TOOL-3: %s assigns a literal signing secret: %s", f, m)
			}
		}
	}
	if scanned == 0 {
		t.Fatalf("PB-TOOL-3: no Gradle build configuration is tracked, so this scan " +
			"passed without examining anything. The Android module does not exist yet")
	}
}

// TestPBTOOL3_GitignoreExcludesSigningMaterial makes the workflow safe rather
// than merely currently-clean: without these rules the first operator to drop a
// keystore next to the module commits it.
func TestPBTOOL3_GitignoreExcludesSigningMaterial(t *testing.T) {
	body := readFileOrFail(t, filepath.Join(repoRoot(t), ".gitignore"), "PB-TOOL-3")
	for _, ext := range []string{".jks", ".keystore"} {
		if !strings.Contains(body, "*"+ext) {
			t.Errorf("PB-TOOL-3: .gitignore has no `*%s` rule; nothing stops a keystore "+
				"being committed by accident", ext)
		}
	}
	if !strings.Contains(body, "local.properties") {
		t.Errorf("PB-TOOL-3: .gitignore does not exclude local.properties, the file " +
			"Android tooling writes the local SDK path and operators write secrets into")
	}
}

// TestPBTOOL3_ReleaseSigningReadsConfigOrEnvironment asserts the positive half:
// the release signingConfig must source every field from a project property or
// an environment variable.
func TestPBTOOL3_ReleaseSigningReadsConfigOrEnvironment(t *testing.T) {
	build := moduleBuildFile(t)
	body := readFileOrFail(t, build, "PB-TOOL-3")

	if !strings.Contains(body, "signingConfigs") {
		t.Fatalf("PB-TOOL-3: %s declares no signingConfigs, so a release build would be "+
			"silently unsigned rather than loudly refused", mustRel(t, build))
	}

	// Every storeFile must come from a variable. A quoted literal path is a
	// committed location by definition.
	literal := regexp.MustCompile(`storeFile\s*=?\s*file\(\s*"`)
	if m := literal.FindString(body); m != "" {
		t.Errorf("PB-TOOL-3: %s sets storeFile from a string literal (%q). It must resolve "+
			"from an operator-supplied property or environment variable", mustRel(t, build), m)
	}

	// And the source must be named, so the runbook has something to document.
	if !regexp.MustCompile(`(findProperty|providers\.environmentVariable|System\.getenv|gradleProperty)`).MatchString(body) {
		t.Errorf("PB-TOOL-3: %s never reads a project property or environment variable; "+
			"there is no operator-supplied path for the release keystore", mustRel(t, build))
	}
}

// TestPBTOOL3_ReleaseBuildRefusesWithoutOperatorMaterial closes the quiet-failure
// hole. The usual Android idiom
//
//	signingConfig = if (keystorePath != null) release else null
//
// produces an UNSIGNED release APK when the operator forgets the variable -- the
// build goes green and the artifact is not installable. The module must declare
// that missing signing material fails the release build.
func TestPBTOOL3_ReleaseBuildRefusesWithoutOperatorMaterial(t *testing.T) {
	build := moduleBuildFile(t)
	body := readFileOrFail(t, build, "PB-TOOL-3")
	if !regexp.MustCompile(`(?i)(error\(|throw\s+GradleException|checkNotNull|requireNotNull)`).MatchString(body) {
		t.Errorf("PB-TOOL-3: %s has no failure path for missing release signing material. "+
			"A release build with no keystore must fail, not produce an unsigned APK",
			mustRel(t, build))
	}
}

func isBuildConfigFile(rel string) bool {
	base := filepath.Base(rel)
	switch base {
	case "gradle.properties", "local.properties", "toolchain.env":
		return true
	}
	return strings.HasSuffix(base, ".gradle") || strings.HasSuffix(base, ".gradle.kts")
}

func moduleBuildFile(t *testing.T) string {
	t.Helper()
	kts := filepath.Join(appModule(t), "build.gradle.kts")
	if exists(kts) {
		return kts
	}
	groovy := filepath.Join(appModule(t), "build.gradle")
	if exists(groovy) {
		return groovy
	}
	t.Fatalf("no Android application module: neither %s nor %s exists",
		mustRel(t, kts), mustRel(t, groovy))
	return ""
}
