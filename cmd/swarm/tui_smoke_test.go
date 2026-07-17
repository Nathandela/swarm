package main

// F1 (Epic 8 milestone) — the PTY smoke of the assembled no-argument binary: build
// `swarm`, start a real daemon, then run bare `swarm` attached to a pseudo-terminal
// and prove it opens the REAL TUI (the general view header "swarm" is painted), then
// exits cleanly on SIGINT with the terminal handed back. This is the end-to-end
// proof that runTUI assembles skeleton + protocol client + internal/tui into the
// bare binary, complementing the no-tty guard asserted in TestDispatch and the
// termios-restore guarantee proven in internal/attach/pty_test.go.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/Nathandela/swarm/internal/protocol"
)

// lockedBuf is a threadsafe sink for the PTY master, drained in a goroutine while
// the test polls its accumulated contents.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startSmokeDaemon spawns a real `swarm daemon` and waits until it serves the full
// client protocol, returning the SWARM_DAEMON_* env the client role reads. The
// daemon is killed on cleanup.
func startSmokeDaemon(t *testing.T, swarmBin, fakeAgentBin string) []string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	env := []string{
		"SWARM_DAEMON_STATE=" + dir,
		"SWARM_DAEMON_SOCK=" + sock,
		"SWARM_DAEMON_LOCK=" + filepath.Join(dir, "d.lock"),
		"SWARM_DAEMON_LOG=" + filepath.Join(dir, "d.log"),
		envFakeAgentBin + "=" + fakeAgentBin,
	}

	cmd := exec.Command(swarmBin, "daemon")
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start swarm daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, derr := protocol.Dial(sock, nil); derr == nil {
			_ = c.Close()
			return env
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("smoke daemon never served the client protocol within 10s")
	return nil
}

// TestTUI_OpensAndRestoresOverPTY drives the bare binary over a real PTY: it must
// paint the general view and exit 0 (clean) on SIGINT, which Bubble Tea catches and
// turns into a graceful, terminal-restoring quit.
func TestTUI_OpensAndRestoresOverPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("PTY smoke spawns subprocesses")
	}
	swarmBin, fakeAgentBin := buildRoleBinaries(t)
	daemonEnv := startSmokeDaemon(t, swarmBin, fakeAgentBin)

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = ptmx.Close(); _ = tty.Close() }()
	// A fresh PTY has a 0x0 size; give it real dimensions so Bubble Tea's first
	// WindowSizeMsg has width to paint the board into (a 0-column board is blank).
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatalf("pty.Setsize: %v", err)
	}

	cmd := exec.Command(swarmBin) // no args -> the TUI role
	cmd.Env = append(os.Environ(), daemonEnv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start swarm (tui): %v", err)
	}
	_ = tty.Close() // the child holds its own dup; keep only the master here
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	out := &lockedBuf{}
	go func() { _, _ = copyUntilClosed(ptmx, out) }()

	// The general view header "swarm" must appear — proof the real TUI opened.
	deadline := time.Now().Add(10 * time.Second)
	painted := false
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(out.String()), []byte("swarm")) {
			painted = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !painted {
		t.Fatalf("TUI never painted the general view header within 10s; got:\n%q", out.String())
	}

	// SIGINT -> Bubble Tea restores the terminal and quits; runTUI maps the resulting
	// ErrInterrupted to a clean (exit 0) return.
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signal SIGINT: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		if werr != nil {
			t.Fatalf("swarm TUI did not exit cleanly after SIGINT: %v\noutput:\n%q", werr, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("swarm TUI did not exit within 10s of SIGINT")
	}
}

// TestTUI_RejectsRedirectedStdin covers the codex stdin-guard gap: a TTY stdout is
// not enough — a redirected/non-TTY stdin (Bubble Tea + attach both read it) must be
// rejected too. Here stdout is a real PTY but stdin is /dev/null, so the guard must
// fire before any daemon dial (no daemon is started) with the clear error.
func TestTUI_RejectsRedirectedStdin(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	swarmBin, _ := buildRoleBinaries(t)

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = ptmx.Close(); _ = tty.Close() }()
	go func() { _, _ = copyUntilClosed(ptmx, &lockedBuf{}) }() // drain the master

	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer devnull.Close()

	var stderr bytes.Buffer
	cmd := exec.Command(swarmBin) // no args -> the TUI role
	cmd.Stdin = devnull           // non-TTY stdin
	cmd.Stdout = tty              // TTY stdout
	cmd.Stderr = &stderr
	err = cmd.Run()

	if err == nil {
		t.Fatal("swarm with a redirected (non-TTY) stdin must exit non-zero")
	}
	if !strings.Contains(stderr.String(), "not a terminal") {
		t.Fatalf("expected a not-a-terminal error for redirected stdin; stderr=%q", stderr.String())
	}
}

// copyUntilClosed copies src into dst until src closes (the child exits and the PTY
// master returns EIO/EOF). It never fails the test; it only feeds the output buffer.
func copyUntilClosed(src *os.File, dst *lockedBuf) (int64, error) {
	var total int64
	b := make([]byte, 4096)
	for {
		n, err := src.Read(b)
		if n > 0 {
			_, _ = dst.Write(b[:n])
			total += int64(n)
		}
		if err != nil {
			return total, err
		}
	}
}
