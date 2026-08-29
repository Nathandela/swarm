package main

// Main-level tests for the swarm-publish binary: argv parsing and the checks that must
// happen BEFORE anything touches the network or reads a credential. Nothing here contacts
// Google -- the flow itself is covered in-package by internal/play.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testFirebaseAppID = "1:733314021126:android:ff6e016cffe98782535087"

type provenanceFixture struct {
	Schema         int    `json:"schema"`
	ProjectID      string `json:"project_id"`
	PackageName    string `json:"package_name"`
	MobileSDKAppID string `json:"mobilesdk_app_id"`
	AABSHA256      string `json:"aab_sha256"`
}

func writeAABWithProvenance(t *testing.T, content []byte, projectID string) string {
	t.Helper()
	dir := t.TempDir()
	aab := filepath.Join(dir, "app-release.aab")
	if err := os.WriteFile(aab, content, 0o600); err != nil {
		t.Fatalf("write AAB fixture: %v", err)
	}
	sum := sha256.Sum256(content)
	doc, err := json.Marshal(provenanceFixture{
		Schema:         1,
		ProjectID:      projectID,
		PackageName:    "dev.swarm.phone",
		MobileSDKAppID: testFirebaseAppID,
		AABSHA256:      hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatalf("marshal provenance fixture: %v", err)
	}
	if err := os.WriteFile(aab+".swarm-firebase-provenance.json", doc, 0o600); err != nil {
		t.Fatalf("write provenance fixture: %v", err)
	}
	return aab
}

func verifyProductionFirebaseProvenanceForTest(aab, packageName string) error {
	bundle, err := openVerifiedProductionFirebaseBundle(aab, packageName)
	if bundle != nil {
		_ = bundle.Close()
	}
	return err
}

// TestProductionFirebaseProvenanceAcceptsTheExactBundle is the positive control:
// the sidecar emitted by bundleRelease names the production Firebase application
// and binds those facts to the exact AAB bytes through SHA-256.
func TestProductionFirebaseProvenanceAcceptsTheExactBundle(t *testing.T) {
	aab := writeAABWithProvenance(t, []byte("fresh production bundle"), "swarm-8404f")
	if err := verifyProductionFirebaseProvenanceForTest(aab, "dev.swarm.phone"); err != nil {
		t.Fatalf("openVerifiedProductionFirebaseBundle: %v", err)
	}
}

// TestVerifiedBundleDescriptorSurvivesAPathReplacement fences the handoff to
// internal/play. The path may be replaced while a credential is parsed; the
// publisher must still receive the exact descriptor whose bytes matched the
// sidecar, never reopen whatever now occupies the pathname.
func TestVerifiedBundleDescriptorSurvivesAPathReplacement(t *testing.T) {
	const original = "fresh production bundle"
	aab := writeAABWithProvenance(t, []byte(original), "swarm-8404f")
	bundle, err := openVerifiedProductionFirebaseBundle(aab, "dev.swarm.phone")
	if err != nil {
		t.Fatalf("openVerifiedProductionFirebaseBundle: %v", err)
	}
	defer func() {
		if err := bundle.Close(); err != nil {
			t.Errorf("close verified bundle: %v", err)
		}
	}()

	if err := os.Rename(aab, aab+".verified"); err != nil {
		t.Fatalf("rename verified AAB: %v", err)
	}
	if err := os.WriteFile(aab, []byte("replacement bundle"), 0o600); err != nil {
		t.Fatalf("replace AAB path: %v", err)
	}
	got, err := io.ReadAll(bundle)
	if err != nil {
		t.Fatalf("read verified descriptor: %v", err)
	}
	if string(got) != original {
		t.Fatalf("verified descriptor bytes = %q, want %q", got, original)
	}
}

// TestProductionFirebaseProvenanceRejectsAnArbitraryBundle makes the current
// failure mode explicit: an otherwise uploadable AAB with no build provenance
// must be refused before a Play credential is read.
func TestProductionFirebaseProvenanceRejectsAnArbitraryBundle(t *testing.T) {
	aab := filepath.Join(t.TempDir(), "app-release.aab")
	if err := os.WriteFile(aab, []byte("arbitrary bundle"), 0o600); err != nil {
		t.Fatalf("write AAB fixture: %v", err)
	}
	err := verifyProductionFirebaseProvenanceForTest(aab, "dev.swarm.phone")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "provenance") {
		t.Fatalf("missing provenance error = %v, want an actionable provenance refusal", err)
	}
}

// TestProductionFirebaseProvenanceRejectsAStaleOrReplacedBundle proves a
// sidecar cannot be copied from an earlier successful build onto different AAB
// bytes. That is the stale-artifact hole this preflight exists to close.
func TestProductionFirebaseProvenanceRejectsAStaleOrReplacedBundle(t *testing.T) {
	aab := writeAABWithProvenance(t, []byte("original production bundle"), "swarm-8404f")
	if err := os.WriteFile(aab, []byte("stale or replaced bundle"), 0o600); err != nil {
		t.Fatalf("replace AAB fixture: %v", err)
	}
	err := verifyProductionFirebaseProvenanceForTest(aab, "dev.swarm.phone")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "sha-256") {
		t.Fatalf("replaced bundle error = %v, want an AAB SHA-256 mismatch", err)
	}
}

func TestProductionFirebaseProvenanceRejectsADevelopmentProject(t *testing.T) {
	aab := writeAABWithProvenance(t, []byte("development bundle"), "swarm-development")
	err := verifyProductionFirebaseProvenanceForTest(aab, "dev.swarm.phone")
	if err == nil || !strings.Contains(err.Error(), "project_id") {
		t.Fatalf("development-project error = %v, want the production project_id refusal", err)
	}
}

func TestRunRejectsMissingProvenanceBeforeReadingTheCredential(t *testing.T) {
	aab := filepath.Join(t.TempDir(), "app-release.aab")
	if err := os.WriteFile(aab, []byte("unprovenanced bundle"), 0o600); err != nil {
		t.Fatalf("write AAB fixture: %v", err)
	}
	err := run(context.Background(), []string{
		"--aab", aab,
		"--key", filepath.Join(t.TempDir(), "also-missing.json"),
		"--package", "dev.swarm.phone",
		"--track", "internal",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "provenance") {
		t.Fatalf("run error = %v, want provenance refusal before credential read", err)
	}
}

// TestRunRequiresEveryFlag asserts the binary refuses to run on a partial invocation
// rather than defaulting its way to the wrong app, the wrong track, or a nil credential.
// Publishing is irreversible from this side, so every target must be stated explicitly.
func TestRunRequiresEveryFlag(t *testing.T) {
	dir := t.TempDir()
	aab := filepath.Join(dir, "app.aab")
	key := filepath.Join(dir, "key.json")
	for _, f := range []string{aab, key} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}

	for name, args := range map[string][]string{
		"no flags at all": nil,
		"no aab":          {"--key", key, "--package", "dev.swarm.phone", "--track", "internal"},
		"no key":          {"--aab", aab, "--package", "dev.swarm.phone", "--track", "internal"},
		"no package":      {"--aab", aab, "--key", key, "--track", "internal"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(context.Background(), args); err == nil {
				t.Fatal("run returned nil for an incomplete invocation")
			}
		})
	}
}

// TestRunRejectsAnUnknownTrack pins that a mistyped track fails locally with the valid
// values named. Google's own rejection for a bad track arrives four API calls later, after
// an edit has been opened and a bundle uploaded.
func TestRunRejectsAnUnknownTrack(t *testing.T) {
	dir := t.TempDir()
	aab := filepath.Join(dir, "app.aab")
	key := filepath.Join(dir, "key.json")
	for _, f := range []string{aab, key} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}

	err := run(context.Background(), []string{
		"--aab", aab, "--key", key, "--package", "dev.swarm.phone", "--track", "prodcution",
	})
	if err == nil {
		t.Fatal("run accepted the track \"prodcution\"")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("error does not name the valid tracks: %v", err)
	}
}

// TestRunRejectsABrokenCredentialWithoutLeakingIt covers the two things that matter about
// a bad --key: it fails, and its failure says nothing about the file's contents. A
// credential quoted into an error lands in the terminal transcript and in CI logs.
func TestRunRejectsABrokenCredentialWithoutLeakingIt(t *testing.T) {
	dir := t.TempDir()
	aab := writeAABWithProvenance(t, []byte("production bundle"), "swarm-8404f")
	const secret = "SUPER-SECRET-KEY-MATERIAL"
	key := filepath.Join(dir, "key.json")
	if err := os.WriteFile(key, []byte(`{"private_key":"`+secret+`"`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	err := run(context.Background(), []string{
		"--aab", aab, "--key", key, "--package", "dev.swarm.phone", "--track", "internal",
	})
	if err == nil {
		t.Fatal("run accepted an unparseable credential")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error quotes the credential file's contents: %v", err)
	}
}

// TestRunRejectsAMissingCredentialFile pins the common operator mistake -- a wrong path --
// as a clean error rather than a nil-pointer panic further down.
func TestRunRejectsAMissingCredentialFile(t *testing.T) {
	dir := t.TempDir()
	aab := writeAABWithProvenance(t, []byte("production bundle"), "swarm-8404f")

	err := run(context.Background(), []string{
		"--aab", aab, "--key", filepath.Join(dir, "absent.json"),
		"--package", "dev.swarm.phone", "--track", "internal",
	})
	if err == nil {
		t.Fatal("run returned nil for a missing credential file")
	}
	if !strings.Contains(err.Error(), "read credential") {
		t.Fatalf("error = %v, want the missing-credential path after provenance passed", err)
	}
}
