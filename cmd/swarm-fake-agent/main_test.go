package main

// FIX 8 — exec-level smoke tests for the swarm-fake-agent binary. They build the
// binary and run it as a real process so the stdin/stdout/exit-code contract is
// exercised end-to-end, not just the library.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestParseArgsRejectsMalformedStdinLogFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "flag only", args: []string{"--stdin-log"}},
		{name: "missing script", args: []string{"--stdin-log", "/tmp/stdin.bin"}},
		{name: "empty path", args: []string{"--stdin-log", "", "script.txt"}},
		{name: "empty script", args: []string{"--stdin-log", "/tmp/stdin.bin", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if scriptPath, stdinLog, ok := parseArgs(tt.args); ok {
				t.Fatalf("parseArgs(%q) = script %q log %q ok; want malformed invocation rejected",
					tt.args, scriptPath, stdinLog)
			}
		})
	}
}

// The opt-in audit is an observation of the fake process's stdin, not its
// rendered PTY echo. It must therefore preserve terminators and every other byte
// exactly, and the resulting input transcript is always private.
func TestExec_StdinLogIsByteExactAndPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are the contract of the swarm test fixture")
	}
	bin := buildFakeAgent(t)
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.txt")
	logPath := filepath.Join(dir, "stdin.bin")
	if err := os.WriteFile(scriptPath, []byte("ask first?\nask second?\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	// An existing, overly broad file must be truncated and tightened too; the
	// create mode alone would not establish the 0600 contract in that case.
	if err := os.WriteFile(logPath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed stdin log: %v", err)
	}

	want := []byte{'a', 0, 'b', '\r', '\n', 0xff, 'z', '\n'}
	cmd := exec.Command(bin, "--stdin-log", logPath, scriptPath)
	cmd.Stdin = bytes.NewReader(want)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run audited fake agent: %v\n%s", err, out)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read stdin log: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("stdin log = %q, want byte-exact %q", got, want)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat stdin log: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Errorf("stdin log mode = %04o, want 0600", gotMode)
	}
}

// The ordinary one-argument invocation is unchanged: no audit path is guessed
// and no side-channel file is created unless the explicit flag is present.
func TestExec_DefaultDoesNotCreateAStdinLog(t *testing.T) {
	bin := buildFakeAgent(t)
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.txt")
	if err := os.WriteFile(scriptPath, []byte("ask ?\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command(bin, scriptPath)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("ordinary input\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run default fake agent: %v", err)
	}
	if want := "?got: ordinary input\n"; string(out) != want {
		t.Errorf("stdout = %q, want unchanged %q", out, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read working directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "script.txt" {
		t.Errorf("default invocation created files %v, want only script.txt", entryNames(entries))
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
