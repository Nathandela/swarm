package claudeinput

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This uses the real installed TUI and a synthetic local model, with isolated HOME/auth.
func TestNativeClaudeModalAdmission(t *testing.T) {
	if os.Getenv("SWARM_NATIVE_ADMISSION_FIXTURE") != "1" {
		t.Skip("set SWARM_NATIVE_ADMISSION_FIXTURE=1 for installed-Claude native qualification")
	}
	binary, version, dir := privateNativeClaude(t)
	broker, err := Start(dir, "native-probe", version, os.Getpid(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broker.Close() }()
	plugin, err := Install(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	harness, err := filepath.Abs("../../scripts/verification/claude_native_admission.py")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "python3", harness, binary, filepath.Join(dir, DescriptorFile), plugin)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native modal admission: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func privateNativeClaude(t *testing.T) (binary, version, dir string) {
	t.Helper()
	var err error
	binary, err = exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	banner, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	version = strings.Fields(string(banner))[0]
	dir = shortDir(t)
	original, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(dir, "claude")
	target, err := os.OpenFile(private, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		_ = original.Close()
		t.Fatal(err)
	}
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(target, digest), original)
	closeErr := target.Close()
	_ = original.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("private provider copy: %v %v", copyErr, closeErr)
	}
	binary = private
	t.Logf("installed native CLI version=%s sha256=%x; private copied executable", version, digest.Sum(nil))
	return
}

// This sends only a synthetic greeting using the existing native Keychain account.
func TestNativeClaudeAccount(t *testing.T) {
	if os.Getenv("SWARM_NATIVE_ACCOUNT_FIXTURE") != "1" {
		t.Skip("set SWARM_NATIVE_ACCOUNT_FIXTURE=1 for an authorized real native-account turn")
	}
	binary, _, dir := privateNativeClaude(t)
	harness, err := filepath.Abs("../../scripts/verification/claude_native_account.py")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", harness, binary, dir)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated native account: %v\n%s", err, output)
	}
	t.Log(string(output))
}
