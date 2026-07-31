package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// PB-TOOL-4 -- "Gradle wrapper checked in with a pinned distribution.
// `./gradlew --version` works without system gradle."
//
// The trap: this host HAS Gradle 9.6.1 at /usr/local/bin/gradle. A test that
// runs `gradle --version`, or that runs `./gradlew` with the ambient PATH,
// proves nothing -- it can pass with no wrapper checked in at all, because
// the wrapper script would simply not be the thing that ran. So:
//
//   - the wrapper script, its jar, and its properties must be TRACKED files;
//   - distributionUrl must name an exact version and distributionSha256Sum must
//     be set (an unverified download is not a pin, it is a URL);
//   - the wrapper jar's own SHA-256 is pinned in toolchain.env, because the jar
//     is opaque committed bytecode that runs on every build and is the classic
//     supply-chain insertion point;
//   - and the execution test (tagged) runs ./gradlew with /usr/local/bin removed
//     from PATH and asserts the reported version equals the pinned one.

func wrapperPropsPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "gradle", "wrapper", "gradle-wrapper.properties")
}

func wrapperJarPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "gradle", "wrapper", "gradle-wrapper.jar")
}

// TestPBTOOL4_WrapperIsCheckedIn asserts all four wrapper files exist AND are
// tracked. The jar is the one people gitignore by accident (".jar" rules), and a
// missing jar turns ./gradlew into a script that cannot bootstrap.
func TestPBTOOL4_WrapperIsCheckedIn(t *testing.T) {
	root := repoRoot(t)
	tracked := map[string]bool{}
	for _, f := range trackedFiles(t, root) {
		tracked[f] = true
	}

	want := []string{
		"android/gradlew",
		"android/gradlew.bat",
		"android/gradle/wrapper/gradle-wrapper.properties",
		"android/gradle/wrapper/gradle-wrapper.jar",
	}
	for _, rel := range want {
		if !exists(filepath.Join(root, rel)) {
			t.Errorf("PB-TOOL-4: %s does not exist", rel)
			continue
		}
		if !tracked[rel] {
			t.Errorf("PB-TOOL-4: %s exists but is not tracked by git; a wrapper that is "+
				"not committed is not a wrapper", rel)
		}
	}

	if info, err := os.Stat(filepath.Join(root, "android/gradlew")); err == nil {
		if info.Mode()&0o111 == 0 {
			t.Errorf("PB-TOOL-4: android/gradlew is not executable")
		}
	}
}

// TestPBTOOL4_DistributionIsPinnedAndVerified rejects the two ways a wrapper can
// look pinned and not be: a floating URL, and a pinned URL with no checksum.
func TestPBTOOL4_DistributionIsPinnedAndVerified(t *testing.T) {
	props := readFileOrFail(t, wrapperPropsPath(t), "PB-TOOL-4")

	url := propValue(props, "distributionUrl")
	if url == "" {
		t.Fatalf("PB-TOOL-4: gradle-wrapper.properties has no distributionUrl")
	}
	// Escaped colons are normal in a .properties file.
	url = strings.ReplaceAll(url, `\:`, ":")

	verRe := regexp.MustCompile(`gradle-(\d+\.\d+(?:\.\d+)?)-(bin|all)\.zip$`)
	m := verRe.FindStringSubmatch(url)
	if m == nil {
		t.Fatalf("PB-TOOL-4: distributionUrl=%q does not name an exact Gradle version. "+
			"A distribution that can change under the build is not pinned", url)
	}
	if !strings.HasPrefix(url, "https://") {
		t.Errorf("PB-TOOL-4: distributionUrl is not https: %q", url)
	}

	if sum := propValue(props, "distributionSha256Sum"); sum == "" {
		t.Errorf("PB-TOOL-4/PB-SEC-14: gradle-wrapper.properties sets no " +
			"distributionSha256Sum. Without it the wrapper downloads and executes " +
			"whatever the URL serves, which pins the name and not the bytes")
	} else if len(sum) != 64 {
		t.Errorf("PB-TOOL-4: distributionSha256Sum=%q is not a 64-hex-character SHA-256", sum)
	}

	// The pin file and the wrapper must agree, so `SWARM_GRADLE_VERSION` is the
	// single readable source and cannot drift from what actually runs.
	env := sourcePin(t, repoRoot(t))
	if pinned := strings.TrimSpace(env["SWARM_GRADLE_VERSION"]); pinned != m[1] {
		t.Errorf("PB-TOOL-4/PB-TOOL-1: SWARM_GRADLE_VERSION=%q but the wrapper resolves "+
			"Gradle %s", pinned, m[1])
	}
}

// TestPBTOOL4_DistributionChecksumMatchesThePin is the sibling of the jar test below, and it
// was MISSING -- derived 2026-07-31, ADR-007 B127 finding C.
//
// Two copies of this hash exist: the one Gradle actually enforces, in
// gradle-wrapper.properties, and SWARM_GRADLE_DISTRIBUTION_SHA256 in the toolchain pin. The
// checks above establish only that the enforced one is 64 hex characters, so ANY 64-hex value
// passed -- measured, by replacing it with `deadbeef` repeated eight times and watching the
// whole package stay green. And the pin's copy had NO CONSUMER ANYWHERE IN THE REPOSITORY, so
// it could not disagree with anything: a dead duplicate of a supply-chain-critical value.
//
// The receiver's irreversible act is what makes this the serious one. The wrapper does not
// merely fetch the distribution, it EXECUTES it, and this hash is the only thing that decides
// whether the bytes it fetched are the bytes anybody chose. The jar forty lines down was
// already hashed against reality; one of the two hashes was verified and the other against
// nothing.
//
// RESIDUAL, recorded rather than invented (PB-APP-11's form). This test binds the two copies
// to each other. It does NOT bind either to the REAL Gradle distribution, and it does not
// constrain distributionUrl's HOST -- repointing it at another host, keeping the version and
// the https scheme, survives every fence in this package. Deciding which hosts a distribution
// may be fetched from is a section 6.0-shaped policy nobody has stated, so it is named here
// and left to whoever owns that budget.
func TestPBTOOL4_DistributionChecksumMatchesThePin(t *testing.T) {
	props := readFileOrFail(t, wrapperPropsPath(t), "PB-TOOL-4")
	got := strings.TrimSpace(propValue(props, "distributionSha256Sum"))

	env := sourcePin(t, repoRoot(t))
	want := strings.TrimSpace(env["SWARM_GRADLE_DISTRIBUTION_SHA256"])
	if want == "" {
		t.Fatalf("PB-TOOL-4: the toolchain pin does not export "+
			"SWARM_GRADLE_DISTRIBUTION_SHA256. gradle-wrapper.properties enforces %q and "+
			"nothing readable records what it should be", got)
	}
	if !strings.EqualFold(got, want) {
		t.Fatalf("PB-TOOL-4/PB-SEC-14: gradle-wrapper.properties enforces distribution "+
			"checksum %s, the toolchain pin records %s. The wrapper DOWNLOADS AND EXECUTES "+
			"whatever matches the enforced value, so the two copies disagreeing means the "+
			"reviewable one is not the one that runs", got, want)
	}
}

// TestPBTOOL4_WrapperJarChecksumMatchesThePin pins the committed jar's bytes.
// gradle-wrapper.jar executes on every single build, is not human-readable, and
// is routinely regenerated by IDEs -- a silent swap is invisible in review.
func TestPBTOOL4_WrapperJarChecksumMatchesThePin(t *testing.T) {
	body, err := os.ReadFile(wrapperJarPath(t))
	if err != nil {
		t.Fatalf("PB-TOOL-4: cannot read %s: %v", mustRel(t, wrapperJarPath(t)), err)
	}
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])

	env := sourcePin(t, repoRoot(t))
	want := strings.TrimSpace(env["SWARM_GRADLE_WRAPPER_JAR_SHA256"])
	if want == "" {
		t.Fatalf("PB-TOOL-4: the toolchain pin does not export "+
			"SWARM_GRADLE_WRAPPER_JAR_SHA256. The committed wrapper jar hashes to %s", got)
	}
	if !strings.EqualFold(got, want) {
		t.Fatalf("PB-TOOL-4: gradle-wrapper.jar hashes to %s, pin says %s. Either the jar "+
			"was replaced or the pin was not updated; both are review-visible only through "+
			"this test", got, want)
	}
}

// propValue reads one key out of a .properties file.
func propValue(body, key string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
