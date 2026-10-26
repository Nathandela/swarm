package claudeinput

import (
	"context"
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
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	banner, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	version := strings.Fields(string(banner))[0]
	dir := shortDir(t)
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
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native modal admission: %v\n%s", err, output)
	}
	t.Log(string(output))
}
