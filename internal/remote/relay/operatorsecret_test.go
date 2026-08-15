package relay

// R2 bundle (playbook 6.5) — FAILING-FIRST (TDD RED, GG-5) tests for the
// generated relay operator secret: "generated high-entropy relay operator
// secret/instance identity for diagnostic/admin authority" (playbook 6.5),
// persisted at operator_secret_file so the doctor capability (a separate R2
// slice) can read it later. This file pins ONLY generation + 0600 persistence:
// EnsureOperatorSecret(path string) (string, error) does not exist yet
// (compile RED).

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureOperatorSecret_GeneratesAndPersistsAtFirstBoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.secret")
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("test precondition: %s already exists", path)
	}

	secret, err := EnsureOperatorSecret(path)
	if err != nil {
		t.Fatalf("EnsureOperatorSecret on a fresh path: %v", err)
	}
	if len(secret) < 32 {
		t.Fatalf("generated operator secret is %d chars, want a high-entropy value (>= 32)", len(secret))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("operator secret file was not persisted: %v", err)
	}
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("operator secret file mode = %o, want 0600 (it is a secret)", mode)
		}
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted operator secret: %v", err)
	}
	if got := string(onDisk); got != secret+"\n" && got != secret {
		t.Fatalf("persisted file content %q does not match the returned secret %q", got, secret)
	}
}

func TestEnsureOperatorSecret_ReusesAnExistingSecretRatherThanRegenerating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.secret")

	first, err := EnsureOperatorSecret(path)
	if err != nil {
		t.Fatalf("first EnsureOperatorSecret: %v", err)
	}
	second, err := EnsureOperatorSecret(path)
	if err != nil {
		t.Fatalf("second EnsureOperatorSecret: %v", err)
	}
	if first != second {
		t.Fatalf("EnsureOperatorSecret regenerated on a second boot: first=%q second=%q, want the persisted secret reused unchanged", first, second)
	}
}

func TestEnsureOperatorSecret_DistinctAcrossPaths(t *testing.T) {
	dir := t.TempDir()
	a, err := EnsureOperatorSecret(filepath.Join(dir, "a.secret"))
	if err != nil {
		t.Fatalf("EnsureOperatorSecret a: %v", err)
	}
	b, err := EnsureOperatorSecret(filepath.Join(dir, "b.secret"))
	if err != nil {
		t.Fatalf("EnsureOperatorSecret b: %v", err)
	}
	if a == b {
		t.Fatalf("two freshly generated operator secrets are identical (%q): generation is not using real entropy", a)
	}
}

func TestDefaultConfig_OperatorSecretFileEmptyByDefault(t *testing.T) {
	// Mirrors PushCredentials: unset by default, so a fresh DefaultConfig-based
	// boot (every existing test's startTestRelay included) generates no secret
	// file nobody asked for. A deployment opts in by setting operator_secret_file
	// in its config, same as push_credentials.
	if got := DefaultConfig().OperatorSecretFile; got != "" {
		t.Fatalf("DefaultConfig().OperatorSecretFile = %q, want empty (opt-in, like PushCredentials)", got)
	}
}

// TestWithOperatorSecret_InstallsSecretOnServer is the seam the doctor
// capability (a separate R2 slice) consumes: New(cfg, WithOperatorSecret(...))
// makes the secret available on the running Server, exactly the way
// WithPushSink installs the push transport.
func TestWithOperatorSecret_InstallsSecretOnServer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DBPath = dbPathForTest(t)
	secret := []byte("fixture-operator-secret")

	srv, err := New(cfg, WithOperatorSecret(secret))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	if string(srv.operatorSecret) != string(secret) {
		t.Fatalf("srv.operatorSecret = %q, want %q", srv.operatorSecret, secret)
	}
}

func TestEnsureOperatorSecret_RejectsAnEmptyExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.secret")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	if _, err := EnsureOperatorSecret(path); err == nil {
		t.Fatalf("EnsureOperatorSecret on an empty existing file returned nil error, want a refusal rather than silently handing out an empty secret")
	}
}

// TestEnsureOperatorSecret_RefusesWorldReadableExistingFile pins the reuse-path mode check:
// a secret restored or copied at 0644 is refused rather than silently accepted.
func TestEnsureOperatorSecret_RefusesWorldReadableExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operator-secret")
	if err := os.WriteFile(path, []byte("aabbcc\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := EnsureOperatorSecret(path); err == nil {
		t.Fatalf("EnsureOperatorSecret accepted a 0644 secret file; want a mode refusal")
	}
}
