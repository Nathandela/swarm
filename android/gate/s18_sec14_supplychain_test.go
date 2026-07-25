package gate

// FAILING-FIRST (TDD RED, GG-5) tests for PB-SEC-14, slice S18:
//
//	"Build supply chain: dependency locking with checksum verification for Gradle/Maven and
//	 pinned gomobile/NDK."
//	Criterion: "Lockfile present and verified in the build gate."
//
// RED AT THE TIME OF WRITING, verified in the tree:
//
//   - android/gradle/ holds ONLY wrapper/. There is no verification-metadata.xml and no
//     *.lockfile anywhere under android/.
//   - Neither android/build.gradle.kts nor android/app/build.gradle.kts mentions
//     dependencyLocking.
//   - android/settings.gradle.kts resolves from google() and mavenCentral() with no
//     verification of what comes back.
//
// So today `./gradlew build` accepts whatever those repositories serve for
// androidx.appcompat:appcompat:1.7.1 and com.google.firebase:firebase-messaging:24.1.2 --
// and for the ~dozens of modules they drag in transitively -- at whatever bytes are behind
// those coordinates on the day of the build. A version number is not a checksum: Maven
// coordinates are mutable in practice (a republished artifact, a compromised mirror, a
// typosquat resolved through a repository ordering change), and the app links a native .so
// holding the user's session keys.
//
// WHY THE TWO HALVES ARE BOTH REQUIRED. Dependency LOCKING pins WHICH versions resolve, so a
// dynamic or transitive version cannot drift between builds. Dependency VERIFICATION pins
// WHAT BYTES those coordinates carry. Locking without verification pins a name to a name;
// verification without locking leaves the set of names free to change. The requirement names
// both ("dependency locking WITH checksum verification"), and each alone is a plausible-but-
// wrong configuration that reads like the requirement was met (standing defect class ii).
//
// THIS FILE NEVER SKIPS. Every assertion reads a file in the repository, so it has the same
// verdict on a machine with no Android SDK as on one with a full toolchain. That is deliberate
// -- PB-BIND-1's bind-produces-an-AAR test is already recorded in this project as a gate that
// skips when ANDROID_HOME is unset, and an auditor reading a green run over-reads it. A
// supply-chain assertion that evaporates on the machine that does not have the toolchain is
// worse than one that fails, because the machine without the toolchain is the CI runner.

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Gradle's dependency-verification metadata, read as the artifact it is.
// ---------------------------------------------------------------------------

// verificationMetadata is the subset of gradle/verification-metadata.xml this project's
// assertions are about. It is parsed rather than grepped so "checksums are verified" is a
// statement about the <verify-metadata>/<verify-signatures> CONFIGURATION and the per-artifact
// <sha256> entries, not about those words appearing somewhere in the file.
type verificationMetadata struct {
	XMLName       xml.Name `xml:"verification-metadata"`
	Configuration struct {
		VerifyMetadata   string `xml:"verify-metadata"`
		VerifySignatures string `xml:"verify-signatures"`
	} `xml:"configuration"`
	Components struct {
		Component []struct {
			Group    string `xml:"group,attr"`
			Name     string `xml:"name,attr"`
			Version  string `xml:"version,attr"`
			Artifact []struct {
				Name   string `xml:"name,attr"`
				SHA256 struct {
					Value string `xml:"value,attr"`
				} `xml:"sha256"`
				SHA512 struct {
					Value string `xml:"value,attr"`
				} `xml:"sha512"`
			} `xml:"artifact"`
		} `xml:"component"`
	} `xml:"components"`
}

func verificationMetadataPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "gradle", "verification-metadata.xml")
}

// readVerificationMetadata parses the file, failing with the requirement's own words when it
// is absent. It is exported to the package (not just this file) because PB-SEC-8's dependency
// inventory is derived from the SAME artifact: the resolved module set is the only honest
// basis for "no analytics SDK is present", and reading it from the build's declarations
// instead would miss every transitive pull.
func readVerificationMetadata(t *testing.T, requirement string) verificationMetadata {
	t.Helper()
	path := verificationMetadataPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %s does not exist. Gradle resolves every dependency from google() and "+
			"mavenCentral() with nothing checking what comes back, so the app links whatever "+
			"bytes those repositories serve on the day of the build. Generate it with "+
			"`./gradlew --write-verification-metadata sha256 help` and review the diff: %v",
			requirement, mustRel(t, path), err)
	}
	var vm verificationMetadata
	if err := xml.Unmarshal(raw, &vm); err != nil {
		t.Fatalf("%s: %s is not parseable as Gradle verification metadata: %v",
			requirement, mustRel(t, path), err)
	}
	return vm
}

// resolvedModules returns "group:name:version" for every component the metadata pins.
func (vm verificationMetadata) resolvedModules() []string {
	var out []string
	for _, c := range vm.Components.Component {
		out = append(out, c.Group+":"+c.Name+":"+c.Version)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// PB-SEC-14: locking.
// ---------------------------------------------------------------------------

// TestPBSEC14_DependenciesAreLocked asserts the Gradle build declares dependency locking AND
// carries the lockfiles it produces.
//
// Both halves are needed and neither implies the other. `dependencyLocking { ... }` with no
// lockfile checked in is a build that WRITES a lock nobody reviews and that CI regenerates on
// every run; a lockfile with no declaration is a stale file the build never reads. Only the
// pair gives the requirement's "lockfile present AND verified in the build gate".
func TestPBSEC14_DependenciesAreLocked(t *testing.T) {
	root := androidRoot(t)

	var declares []string
	for _, rel := range []string{"build.gradle.kts", filepath.Join("app", "build.gradle.kts")} {
		src := readFileOrFail(t, filepath.Join(root, rel), "PB-SEC-14")
		if strings.Contains(stripKotlinComments(src), "dependencyLocking") {
			declares = append(declares, rel)
		}
	}
	if len(declares) == 0 {
		t.Errorf("PB-SEC-14: no Gradle build file declares dependencyLocking. Every dependency " +
			"the app links -- including the transitive closure of firebase-messaging, which is " +
			"the largest part of it -- is free to resolve to a different module on the next " +
			"build, with nothing recording that it changed")
	}

	locks := lockfilesUnder(t, root)
	if len(locks) == 0 {
		t.Errorf("PB-SEC-14: no Gradle lockfile is checked in under %s. The criterion is "+
			"\"lockfile PRESENT and verified in the build gate\"; a locking block that has "+
			"never been written out locks nothing", mustRel(t, root))
	}
}

// lockfilesUnder finds Gradle's lockfiles. Gradle writes them as gradle.lockfile per project
// (and gradle/dependency-locks/*.lockfile in the older layout), so both shapes count -- a
// narrow match would fail a correct implementation that used the other one.
func lockfilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+"build"+string(filepath.Separator)) {
			return nil // Gradle's own output tree, not a tracked artifact
		}
		base := filepath.Base(path)
		if base == "gradle.lockfile" || strings.HasSuffix(base, ".lockfile") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// PB-SEC-14: checksum verification.
// ---------------------------------------------------------------------------

// TestPBSEC14_EveryResolvedModuleCarriesAChecksum is the "with checksum verification" half.
//
// It asserts every pinned component has at least one strong digest on at least one artifact.
// A verification-metadata.xml that lists components with NO checksum is the shape Gradle
// writes when asked for metadata verification alone, and it is exactly the plausible-but-wrong
// artifact that makes this requirement look satisfied: the file exists, it names every module,
// and it verifies nothing about their contents.
func TestPBSEC14_EveryResolvedModuleCarriesAChecksum(t *testing.T) {
	vm := readVerificationMetadata(t, "PB-SEC-14")

	if len(vm.Components.Component) == 0 {
		t.Fatalf("PB-SEC-14: %s pins ZERO components. An empty verification file verifies "+
			"nothing while looking exactly like one that does -- every assertion in this test "+
			"would pass vacuously", mustRel(t, verificationMetadataPath(t)))
	}

	var unchecked []string
	for _, c := range vm.Components.Component {
		checksummed := false
		for _, a := range c.Artifact {
			if a.SHA256.Value != "" || a.SHA512.Value != "" {
				checksummed = true
				break
			}
		}
		if !checksummed {
			unchecked = append(unchecked, c.Group+":"+c.Name+":"+c.Version)
		}
	}
	sort.Strings(unchecked)
	if len(unchecked) > 0 {
		t.Errorf("PB-SEC-14: %d of %d pinned components carry no sha256/sha512 on any artifact, "+
			"so their BYTES are unverified and only their names are pinned:\n\t%s",
			len(unchecked), len(vm.Components.Component), strings.Join(unchecked, "\n\t"))
	}
}

// TestPBSEC14_VerificationIsNotDisabledInTheFile guards the switch Gradle puts at the top of
// the same file. `<verify-metadata>false</verify-metadata>` turns the whole apparatus off
// while leaving every checksum in place to be read by a reviewer -- a file that documents a
// control it has disabled, which is this project's standing "requirement satisfiable while
// the defect ships" class in its purest form.
func TestPBSEC14_VerificationIsNotDisabledInTheFile(t *testing.T) {
	vm := readVerificationMetadata(t, "PB-SEC-14")
	// Non-vacuity FIRST. The vacuous-pass probe run at RED time caught this test passing
	// against a hand-written metadata file with <components/> empty and the flag set to true:
	// it read a switch that was on and reported the control healthy, over a file that pins
	// nothing. TestPBSEC14_EveryResolvedModuleCarriesAChecksum also covers the emptiness, but
	// a reader seeing THIS test green would still over-read it, which is the failure mode this
	// slice was warned about twice.
	if len(vm.Components.Component) == 0 {
		t.Fatalf("PB-SEC-14: %s pins zero components, so \"verification is not disabled\" is a "+
			"statement about a file that verifies nothing", mustRel(t, verificationMetadataPath(t)))
	}
	if strings.EqualFold(strings.TrimSpace(vm.Configuration.VerifyMetadata), "false") {
		t.Errorf("PB-SEC-14: %s sets <verify-metadata>false</verify-metadata>. Every checksum "+
			"below it is decoration: the build does not check any of them",
			mustRel(t, verificationMetadataPath(t)))
	}
}

// ---------------------------------------------------------------------------
// PB-SEC-14: the native half -- pinned gomobile and NDK.
// ---------------------------------------------------------------------------

// TestPBSEC14_GomobileAndNDKArePinned covers the requirement's second clause.
//
// LEGITIMATE PASSERS TODAY: android/toolchain.env already pins SWARM_GOMOBILE_VERSION to an
// exact pseudo-version and SWARM_ANDROID_NDK to an exact revision, and PB-TOOL-1's own tests
// already assert the pin is sourceable. This test is here because PB-SEC-14 names them
// explicitly and because the two pins are the ones that decide what goes INTO the .so holding
// the user's keys -- so a later edit that loosened either to a range must fail an assertion
// carrying THIS requirement's name, not only PB-TOOL-1's.
func TestPBSEC14_GomobileAndNDKArePinned(t *testing.T) {
	env := sourcePin(t, repoRoot(t))

	// gomobile must match go.mod's require exactly, or the AAR is built by a different
	// binding generator than the one the module resolves.
	gomobile := strings.TrimSpace(env["SWARM_GOMOBILE_VERSION"])
	if gomobile == "" {
		t.Fatalf("PB-SEC-14: the toolchain pin exports no SWARM_GOMOBILE_VERSION")
	}
	if strings.ContainsAny(gomobile, "+*") || strings.Contains(gomobile, "latest") {
		t.Errorf("PB-SEC-14: SWARM_GOMOBILE_VERSION=%q is not an exact version", gomobile)
	}
	gomod := readFileOrFail(t, filepath.Join(repoRoot(t), "go.mod"), "PB-SEC-14")
	if !strings.Contains(gomod, gomobile) {
		t.Errorf("PB-SEC-14: go.mod does not require golang.org/x/mobile %s, which the pin "+
			"names. The AAR would be generated by a gobind the module does not resolve", gomobile)
	}

	ndk := strings.TrimSpace(env["SWARM_ANDROID_NDK"])
	if ndk == "" {
		t.Fatalf("PB-SEC-14: the toolchain pin exports no SWARM_ANDROID_NDK")
	}
	// An NDK revision is major.minor.build; anything shorter is a floating major.
	if strings.Count(ndk, ".") < 2 {
		t.Errorf("PB-SEC-14: SWARM_ANDROID_NDK=%q is not a full NDK revision, so the native "+
			"toolchain that compiles the key-holding .so floats", ndk)
	}
}
