package attach

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// PTY smoke tests: the real termios restore (E8.2/A-1). A real NewTermControl over
// a PTY slave is driven by a helper subprocess; the parent (holding another fd to
// the same tty device) observes the slave's termios flip to raw and back. These
// prove what the injected-seam unit tests cannot: that the production MakeRaw's
// restore actually returns the kernel terminal to a sane state — including when the
// client is killed hard by a signal (every signal except SIGKILL, which is the
// documented, un-restorable limitation).
//
// The helper reuses this package's fakeSession (an open, never-closing Frames
// stream keeps Run blocked in raw mode) and the frozen attach.NewTermControl +
// attach.Run. It runs only when SWARM_ATTACH_PTY_HELPER is set in its environment.

const ptyHelperEnv = "SWARM_ATTACH_PTY_HELPER"

// TestPTYHelperProcess is the re-exec'd child. As a normal test run it is a no-op
// (the env guard returns immediately); the parent tests below invoke it with the
// env set and a mode selecting how the attach ends.
func TestPTYHelperProcess(t *testing.T) {
	mode := os.Getenv(ptyHelperEnv)
	if mode == "" {
		return // ordinary test run: not the helper
	}

	tc, err := NewTermControl(os.Stdin, os.Stdout)
	if err != nil {
		os.Exit(11)
	}
	sess := newFakeSession([]byte("PTY-SNAPSHOT"))
	// Frames stays open, so Run blocks in raw mode until a detach key or a signal.
	reason, _ := Run(Config{Term: tc, Session: sess, DetachKey: DefaultDetachKey})
	// On darwin the kernel revokes the controlling terminal the instant this
	// session-leader process exits, which invalidates the parent's slave fd before
	// it can read the restored termios. So after Run has restored, linger long
	// enough for the parent to verify the restore WHILE we are still alive; the
	// parent kills us once it has read. Linux does not revoke, so it reads
	// post-exit and no linger is needed.
	if runtime.GOOS == "darwin" {
		time.Sleep(10 * time.Second)
	}
	if reason == ReasonDetached {
		os.Exit(0)
	}
	os.Exit(0)
}

// waitCooked polls the tty termios until it is back in cooked mode (ICANON+ECHO on)
// while the helper is still alive, or fails. Used on darwin, where reading the
// restored termios after the session-leader helper exits is impossible (the
// controlling terminal is revoked on its exit).
func waitCooked(t *testing.T, fd uintptr) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := isRaw(fd); err == nil && !raw {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("terminal not restored to cooked within 3s (while-alive)")
}

// spawnPTYHelper opens a PTY, starts the helper with the slave as its controlling
// terminal, drains the master, and returns the master fd, the slave fd (for termios
// reads), and the running command.
func spawnPTYHelper(t *testing.T, mode string) (ptmx, tty *os.File, cmd *exec.Cmd) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })

	// Sanity: a fresh PTY slave starts in canonical mode with ECHO on.
	if raw, err := isRaw(tty.Fd()); err != nil {
		t.Fatalf("read initial termios: %v", err)
	} else if raw {
		t.Fatal("fresh PTY slave is unexpectedly in raw mode")
	}

	cmd = exec.Command(os.Args[0], "-test.run=TestPTYHelperProcess")
	cmd.Env = append(os.Environ(), ptyHelperEnv+"="+mode)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	// Discard the slave's writes so they never block; the copy error (e.g. EIO on
	// PTY close during t.Cleanup) is expected and not asserted on.
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()

	// Wait until the helper has put the terminal into raw mode.
	waitRaw(t, tty.Fd(), true)
	return ptmx, tty, cmd
}

// waitRaw polls the tty termios until its raw-ness equals want, or fails.
func waitRaw(t *testing.T, fd uintptr, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := isRaw(fd); err == nil && raw == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("terminal raw=%v not reached within 3s", want)
}

// E8.2 — a normal detach (the user presses Ctrl+\) restores the terminal.
func TestPTY_RestoreOnNormalDetach(t *testing.T) {
	ptmx, tty, cmd := spawnPTYHelper(t, "detach")

	if _, err := ptmx.Write([]byte{DefaultDetachKey}); err != nil {
		t.Fatalf("write detach key: %v", err)
	}

	if runtime.GOOS == "darwin" {
		// macOS revokes the controlling terminal the instant the session-leader
		// helper exits, invalidating this slave fd before a post-exit read. The
		// helper lingers after restore (see TestPTYHelperProcess), so verify the
		// restore WHILE it is still alive. Linux reads post-exit below, unchanged.
		waitCooked(t, tty.Fd())
		return
	}
	waitHelperExit(t, cmd)
	if raw, err := isRaw(tty.Fd()); err != nil || raw {
		t.Fatalf("terminal not restored after normal detach (raw=%v err=%v)", raw, err)
	}
}

// E8.2 — a SIGINT / SIGTERM / SIGHUP that kills the attached client restores the
// terminal (the raw-mode client never leaves a wrecked terminal behind).
func TestPTY_RestoreOnSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			_, tty, cmd := spawnPTYHelper(t, "signal")
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatalf("signal helper: %v", err)
			}
			if runtime.GOOS == "darwin" {
				// See TestPTY_RestoreOnNormalDetach: read the restored termios
				// while the helper is still alive, before the controlling-terminal
				// revoke on its exit invalidates this slave fd.
				waitCooked(t, tty.Fd())
				return
			}
			waitHelperExit(t, cmd)
			if raw, err := isRaw(tty.Fd()); err != nil || raw {
				t.Fatalf("terminal not restored after %v (raw=%v err=%v)", sig, raw, err)
			}
		})
	}
}

// E8.1 / A-1 — IXON (software flow control, XON/XOFF) is OFF while attached, so
// Ctrl+S / Ctrl+Q reach the agent instead of freezing the local terminal.
func TestPTY_IXONOffWhileRaw(t *testing.T) {
	_, tty, cmd := spawnPTYHelper(t, "signal")
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	ixon, err := ixonSet(tty.Fd())
	if err != nil {
		t.Fatalf("read termios: %v", err)
	}
	if ixon {
		t.Fatal("IXON must be cleared in attach raw mode (A-1)")
	}
}

// E8.2 — SIGKILL restoration is NOT claimed (impossible without a wrapper). This
// test pins the honest limitation: after kill -9 the terminal is left raw. It is
// the documented boundary of the restore guarantee, not a bug.
func TestPTY_SIGKILLLeavesRawUnrestored_DocumentedLimitation(t *testing.T) {
	if runtime.GOOS == "darwin" {
		// This limitation is inherently a POST-death observation (no handler runs
		// under SIGKILL, so the terminal is left raw). On darwin the controlling
		// terminal is revoked when the session-leader helper dies, so the post-death
		// slave read returns ENOTTY instead of the raw termios — the state cannot be
		// observed here. It is verified on the Linux CI runner (E8.5), which does not
		// revoke. The restore-does-run paths are still covered on darwin by the
		// while-alive reads in TestPTY_RestoreOnNormalDetach / _RestoreOnSignal.
		t.Skip("darwin: controlling-terminal revoke on session-leader death precludes the post-death termios read")
	}
	_, tty, cmd := spawnPTYHelper(t, "signal")

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL helper: %v", err)
	}
	_, _ = cmd.Process.Wait()

	// The terminal stays raw — no handler can run under SIGKILL. If a future build
	// ever DID restore here, that would be a surprising (if welcome) change worth
	// revisiting this documented limitation.
	if raw, err := isRaw(tty.Fd()); err != nil || !raw {
		t.Fatalf("expected terminal left raw after SIGKILL (documented limitation); raw=%v err=%v", raw, err)
	}
}

func waitHelperExit(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("helper did not exit within 3s")
	}
}

// isRaw reports whether the tty at fd is in raw mode (ICANON and ECHO both off).
func isRaw(fd uintptr) (bool, error) {
	tio, err := getTermios(fd)
	if err != nil {
		return false, err
	}
	canonical := uint64(tio.Lflag)&uint64(unix.ICANON) != 0
	echo := uint64(tio.Lflag)&uint64(unix.ECHO) != 0
	return !canonical && !echo, nil
}

// ixonSet reports whether IXON is set in the tty's input flags.
func ixonSet(fd uintptr) (bool, error) {
	tio, err := getTermios(fd)
	if err != nil {
		return false, err
	}
	return uint64(tio.Iflag)&uint64(unix.IXON) != 0, nil
}
