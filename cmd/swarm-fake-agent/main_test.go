package main

// FIX 8 — exec-level smoke tests for the swarm-fake-agent binary. They build the
// binary and run it as a real process so the stdin/stdout/exit-code contract is
// exercised end-to-end, not just the library.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildFakeAgent compiles the binary into a temp dir and returns its path.
func buildFakeAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "swarm-fake-agent")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

// runExit runs cmd and returns the process exit code, failing on any non-exit
// execution error.
func runExit(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	t.Fatalf("run error: %v", err)
	return -1
}

// (a) A script FILE exercising all four directives, including ask with a fed
// stdin answer: full stdout and the exit code must match exactly.
func TestExec_ScriptFileAllDirectives(t *testing.T) {
	bin := buildFakeAgent(t)
	scriptPath := filepath.Join(t.TempDir(), "script.txt")
	script := "print starting\nask name?\nidle 10ms\nprint done\nexit 5\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command(bin, scriptPath)
	cmd.Stdin = strings.NewReader("Bob\n")
	out, err := cmd.Output()
	code := runExit(t, err)

	if code != 5 {
		t.Errorf("exit code = %d, want 5", code)
	}
	want := "starting\nname?got: Bob\ndone\n"
	if string(out) != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
}

// (b) A script read from stdin that contains an ask must be rejected before any
// execution: exit 2 with the explanatory message, since stdin is already the
// script and cannot also answer the prompt.
func TestExec_StdinScriptWithAskRejected(t *testing.T) {
	bin := buildFakeAgent(t)
	cmd := exec.Command(bin, "-")
	cmd.Stdin = strings.NewReader("print hi\nask name?\n")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	code := runExit(t, err)

	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if want := "ask requires a script file (stdin is consumed by the script)"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
	}
	if len(out) != 0 {
		t.Errorf("stdout = %q, want empty: the script must not run", out)
	}
}
