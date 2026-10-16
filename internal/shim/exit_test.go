package shim

// On agent exit the shim flushes its bounded identity tail before persisting
// exit.json. Screen snapshots remain available only from a running shim.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// A clean exit reports its outcome and flushes the final output into the tail.
func TestExit_CleanZero(t *testing.T) {
	cfg := fakeAgentConfig(t, "print first-line\nprint FINAL-LINE\nexit 0\n")
	r := waitRun(t, runShimAsync(cfg), 15*time.Second)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if r.exit != 0 {
		t.Errorf("Run agentExit = %d, want 0", r.exit)
	}

	ei := readExitInfo(t, cfg.SessionDir)
	if ei.ExitCode != 0 || ei.ExitSignal != "" {
		t.Errorf("exit.json = {code:%d signal:%q}, want {0 \"\"}", ei.ExitCode, ei.ExitSignal)
	}
	if ei.FinishedAt.IsZero() || time.Since(ei.FinishedAt) > time.Minute {
		t.Errorf("exit.json finished_at = %v, want a recent RFC3339 time", ei.FinishedAt)
	}

	// Transcript flushed before close: non-empty, ending with the final line.
	tr := readTranscript(t, cfg.SessionDir)
	if strings.TrimSpace(tr) == "" {
		t.Fatalf("transcript is empty; Flush-before-Close not honored")
	}
	if !strings.Contains(tr, "FINAL-LINE") {
		t.Errorf("transcript does not contain the agent's final output %q:\n%s", "FINAL-LINE", tr)
	}
	if lastNonEmptyLine(tr) != "FINAL-LINE" {
		t.Errorf("transcript's last line = %q, want %q (Flush must capture the tail)", lastNonEmptyLine(tr), "FINAL-LINE")
	}
}

// E4.5 — a non-zero exit code is reported faithfully in both Run's return value
// and exit.json.
func TestExit_NonZeroCode(t *testing.T) {
	cfg := fakeAgentConfig(t, "print bye\nexit 7\n")
	r := waitRun(t, runShimAsync(cfg), 15*time.Second)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if r.exit != 7 {
		t.Errorf("Run agentExit = %d, want 7", r.exit)
	}
	ei := readExitInfo(t, cfg.SessionDir)
	if ei.ExitCode != 7 || ei.ExitSignal != "" {
		t.Errorf("exit.json = {code:%d signal:%q}, want {7 \"\"}", ei.ExitCode, ei.ExitSignal)
	}
}

// E4.5 — a signal death is reported with the terminating signal and the
// 128+signum code convention.
func TestExit_KilledBySignalRecorded(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)
	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	c.waitObserved("IDLING", 5*time.Second)
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	r := waitRun(t, ch, 10*time.Second)

	if r.exit != 128+9 {
		t.Errorf("Run agentExit = %d, want 137 (128+SIGKILL)", r.exit)
	}
	ei := readExitInfo(t, cfg.SessionDir)
	if ei.ExitSignal != "SIGKILL" {
		t.Errorf("exit.json exit_signal = %q, want SIGKILL", ei.ExitSignal)
	}
	if tail := readTranscript(t, cfg.SessionDir); !strings.Contains(tail, "IDLING") {
		t.Errorf("identity tail missing pre-kill output:\n%s", tail)
	}
}

// Tail completeness and file ordering still hold without persisting a screen archive.
func TestExit_SideFilesCompleteAndOrdered(t *testing.T) {
	cfg := fakeAgentConfig(t, "print done\nexit 0\n")
	if r := waitRun(t, runShimAsync(cfg), 15*time.Second); r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}

	tailPath := filepath.Join(cfg.SessionDir, IdentityTailFile)
	exitPath := filepath.Join(cfg.SessionDir, ExitFile)

	tailInfo, err := os.Stat(tailPath)
	if err != nil {
		t.Fatalf("stat %s: %v", IdentityTailFile, err)
	}
	exitInfo, err := os.Stat(exitPath)
	if err != nil {
		t.Fatalf("stat %s: %v", ExitFile, err)
	}
	if tail := readTranscript(t, cfg.SessionDir); lastNonEmptyLine(tail) != "done" {
		t.Fatalf("identity tail is incomplete after exit: %q", tail)
	}
	_ = readExitInfo(t, cfg.SessionDir)

	if tailInfo.ModTime().After(exitInfo.ModTime()) {
		t.Errorf("tail mtime %v is after exit.json mtime %v — tail must be flushed first",
			tailInfo.ModTime(), exitInfo.ModTime())
	}
	assertNoArchivedCapture(t, cfg.SessionDir)
}

// lastNonEmptyLine returns the last non-blank line of s (CR/LF trimmed).
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimRight(lines[i], "\r"); strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}
