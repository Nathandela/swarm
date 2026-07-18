package shim

// E4.5 / G3 — on agent exit the shim writes a final grid snapshot and an exit
// side-file into the session dir, then exits. Run returns the agent's exit code.
// The transcript is flushed before close, so it ends with the agent's final
// output (binding carry-forward from Epic 3).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// E4.5 — a clean exit 0: side-files present and decodable, code reported, grid
// captured, transcript non-empty and ending with the last output.
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

	// Final snapshot decodes and shows the agent's output.
	snap := decodeFinalSnapshot(t, cfg.SessionDir)
	if !strings.Contains(gridText(snap), "FINAL-LINE") {
		t.Errorf("final snapshot grid missing last output:\n%s", gridText(snap))
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
	// The final snapshot is still written on a signal death.
	snap := decodeFinalSnapshot(t, cfg.SessionDir)
	if !strings.Contains(gridText(snap), "IDLING") {
		t.Errorf("final snapshot missing pre-kill output:\n%s", gridText(snap))
	}
}

// E4.5 — ordering/completeness: after Run returns, BOTH side-files exist and are
// fully readable. The shim writes the snapshot (and fsyncs) before exit.json, so
// exit.json's presence implies a complete snapshot; the mtime ordering is
// asserted as a supporting signal (equal timestamps allowed).
func TestExit_SideFilesCompleteAndOrdered(t *testing.T) {
	cfg := fakeAgentConfig(t, "print done\nexit 0\n")
	if r := waitRun(t, runShimAsync(cfg), 15*time.Second); r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}

	snapPath := filepath.Join(cfg.SessionDir, SnapshotFile)
	exitPath := filepath.Join(cfg.SessionDir, ExitFile)

	snapInfo, err := os.Stat(snapPath)
	if err != nil {
		t.Fatalf("stat %s: %v", SnapshotFile, err)
	}
	exitInfo, err := os.Stat(exitPath)
	if err != nil {
		t.Fatalf("stat %s: %v", ExitFile, err)
	}
	// Both decode completely (content-completeness is the binding ordering proxy).
	_ = decodeFinalSnapshot(t, cfg.SessionDir)
	_ = readExitInfo(t, cfg.SessionDir)

	if snapInfo.ModTime().After(exitInfo.ModTime()) {
		t.Errorf("snapshot mtime %v is after exit.json mtime %v — snapshot must be written first",
			snapInfo.ModTime(), exitInfo.ModTime())
	}
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
