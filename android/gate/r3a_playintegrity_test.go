package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR3A_PlayIntegrityProductionProvenanceIsPinned(t *testing.T) {
	root := repoRoot(t)
	build := kotlinCodeOnly(readPlayFile(t, filepath.Join(root, "android", "app", "build.gradle.kts")))
	for _, want := range []string{
		`com.google.android.play:integrity:1.6.0`,
		`733314021126`,
		`cloud_project_number`,
		`push_gateway_url`,
		`play_signing_certificate_sha256`,
		`hz8YTGhTTgpYccjMiQDrhx5HcddqRsTu1HRcmhhknmU`,
		`requireProductionPushConfig`,
	} {
		if !strings.Contains(build, want) {
			t.Errorf("app build has no production Play Integrity provenance %q", want)
		}
	}
	for _, tracked := range []string{"app/gradle.lockfile", "gradle/verification-metadata.xml", "dependency-inventory.tsv"} {
		raw := readPlayFile(t, filepath.Join(root, "android", tracked))
		if !strings.Contains(raw, "com.google.android.play") {
			t.Errorf("%s does not account for the Play Integrity SDK", tracked)
		}
	}
}

// TestR2_ReleasePushOriginRequiresTheConfiguredCloudRunOrigin keeps the direct v2
// provider URL a release-time, operator-controlled coordinate. Pairing is not allowed to
// select it, and a generic HTTPS URL is not enough: Cloud Run has a bare HTTPS origin with
// no credentials, route, query, fragment, or explicit port.
func TestR2_ReleasePushOriginRequiresTheConfiguredCloudRunOrigin(t *testing.T) {
	root := repoRoot(t)
	build := kotlinCodeOnly(readPlayFile(t, filepath.Join(root, "android", "app", "build.gradle.kts")))
	for _, want := range []string{
		`SWARM_PUSH_GATEWAY_URL`,
		`findProperty("SWARM_PUSH_GATEWAY_URL")`,
		`fun validatedProductionPushGatewayURL(raw: String)`,
		`URI(`,
		`u.scheme == "https"`,
		`host?.endsWith(".run.app") == true`,
		`host.matches(cloudRunDNSHostname)`,
		`host.length <= 253`,
		`u.userInfo == null`,
		`u.port == -1`,
		`u.rawAuthority == host`,
		`u.rawPath.isNullOrEmpty()`,
		`u.rawQuery == null`,
		`u.rawFragment == null`,
		`tasks.register("verifyProductionPushOriginContract")`,
		`runCatching { validatedProductionPushGatewayURL(origin) }.exceptionOrNull()`,
		`check(failure != null)`,
		`generateSequence(failure) { it.cause }`,
		`"push_gateway_url": "$productionPushGatewayURL"`,
	} {
		if !strings.Contains(build, want) {
			t.Errorf("app build has no v2 Cloud Run release-origin guard %q", want)
		}
	}
	if strings.Contains(build, "push-swarm.dsfactory.org") {
		t.Error("app build still pins the retired push-swarm.dsfactory.org release origin")
	}
}

func TestR3A_AndroidKeystoreSignerHasNoPrivateExportSurface(t *testing.T) {
	root := repoRoot(t)
	signer := readPlayFile(t, filepath.Join(root, "android", "app", "src", "main", "kotlin", "dev", "swarm", "phone", "push", "AndroidInstallationSigner.kt"))
	for _, want := range []string{
		`AndroidKeyStore`, `secp256r1`, `PURPOSE_SIGN`, `SHA256withECDSA`,
		`derToLowSP1363`,
	} {
		if !strings.Contains(signer, want) {
			t.Errorf("Android signer lacks %q", want)
		}
	}
	for _, forbidden := range []string{"privateKey.encoded", "privateKey.getEncoded", "PKCS8EncodedKeySpec"} {
		if strings.Contains(signer, forbidden) {
			t.Errorf("Android signer exposes private bytes through %q", forbidden)
		}
	}
	runtime := readPlayFile(t, filepath.Join(root, "android", "app", "src", "main", "kotlin", "dev", "swarm", "phone", "PhoneRuntime.kt"))
	if !strings.Contains(runtime, "configurePushRegistration") ||
		!strings.Contains(runtime, "PlayIntegrityAttestor") ||
		!strings.Contains(runtime, "AndroidInstallationSigner") {
		t.Fatal("PhoneRuntime does not install both reverse-bound production authorities")
	}
}

func TestR3A_PushAuthorityConstructionFailureCannotContainTokenOrPrivateMaterial(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "android", "app", "src", "main", "kotlin", "dev", "swarm", "phone", "PhoneRuntime.kt")
	runtime := readPlayFile(t, path)
	body := d0b8FunctionBody(t, runtime, "installPushRegistration", path)
	for _, forbidden := range []string{
		".attest(", ".sign(", ".ensurePushRegistration(", ".registerPushToken(",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("construction-only failure scope reaches secret-bearing operation %q", forbidden)
		}
	}
	for _, want := range []string{
		"PlayIntegrityAttestor(context)", "AndroidInstallationSigner()", "configurePushRegistration(",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("construction-only failure scope no longer contains %q", want)
		}
	}
}

func readPlayFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
