// Package shim is the Epic 4 shim engine: the per-session process that owns the
// PTY, execs the agent in its own process group from an argv array + captured
// env, serves the per-session UDS (G2 message set), pipes PTY bytes into the VT
// emulator + transcript, and — surviving the daemon indefinitely — always
// drains the PTY, then on agent exit writes the final snapshot + exit side-file
// (G3). This is the security-critical heart of ADR-001.
package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/creack/pty"
)

// Side-file names written into the session dir on agent exit (G3).
const (
	SnapshotFile   = "final-snapshot.bin"
	ExitFile       = "exit.json"
	TranscriptFile = "transcript.log"
)

// defaultTerm is injected into the agent env when the caller supplies no TERM.
const defaultTerm = "TERM=xterm-256color"

// Config is the frozen launch contract for a single shim-managed session.
type Config struct {
	SessionID     string
	Argv          []string // argv[0] = program; exec'd directly, never via a shell
	Cwd           string   // agent working directory
	Env           []string // pre-filtered by caller; used verbatim (+ TERM if absent)
	SocketPath    string   // per-session UDS
	SessionDir    string   // side-files: final-snapshot.bin, exit.json, transcript.log
	Cols, Rows    int      // initial PTY + emulator dimensions
	TranscriptCfg transcript.Config
	GraceTimeout  time.Duration // TERM->KILL grace on the signal op
	Metrics       *Metrics      // optional, test-observable counters
}

// Metrics holds test-observable counters. All fields are safe for concurrent
// use.
type Metrics struct {
	FramesDropped atomic.Int64
}

// ExitInfo is the decoded exit.json side-file.
type ExitInfo struct {
	ExitCode   int       `json:"exit_code"`
	ExitSignal string    `json:"exit_signal"`
	FinishedAt time.Time `json:"finished_at"`
}

// Run execs the agent under a fresh PTY, serves the per-session socket, and
// blocks until the agent exits. It always drains the PTY to completion, writes
// the final snapshot + exit side-files, and reports the agent's exit code. err
// is non-nil only for a shim-level setup failure; any agent outcome (clean
// exit, non-zero, or signal death) returns err == nil.
func Run(cfg Config) (agentExit int, err error) {
	if cfg.Metrics == nil {
		cfg.Metrics = &Metrics{}
	}

	emu := vt.NewEmulator(cfg.Cols, cfg.Rows)
	defer emu.Close()

	tr, err := transcript.New(filepath.Join(cfg.SessionDir, TranscriptFile), cfg.TranscriptCfg)
	if err != nil {
		return 0, fmt.Errorf("shim: open transcript: %w", err)
	}

	// Bind the socket first so a fast daemon client can dial (kernel backlog)
	// and attach during the agent's startup window — before the agent produces
	// output, keeping the attach snapshot/stream boundary crisp.
	listener, err := listen(cfg.SocketPath)
	if err != nil {
		closeTranscript(tr)
		return 0, err
	}

	cmd := &exec.Cmd{
		Path: cfg.Argv[0],
		Args: cfg.Argv,
		Env:  buildEnv(cfg.Env),
		Dir:  cfg.Cwd,
	}
	ws := &pty.Winsize{Rows: uint16(cfg.Rows), Cols: uint16(cfg.Cols)}
	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		listener.Close()
		closeTranscript(tr)
		return 0, fmt.Errorf("shim: start agent: %w", err)
	}

	srv := newServer(listener, emu, tr, ptmx, cmd.Process.Pid, cfg.GraceTimeout, cfg.Metrics)
	// Route emulator query replies (DSR/DA/...) back into the PTY master so the
	// agent receives them on stdin. The writer discards after PTY close.
	emu.SetReplyWriter(srv.ptyIn)

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		srv.acceptLoop()
	}()
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		srv.drain()
	}()

	// Block until the agent exits, then finish draining whatever the PTY still
	// holds before deciding the final grid/transcript state.
	waitErr := cmd.Wait()
	close(srv.exited)
	<-drainDone

	// PTY fully drained: stop reply writes and release the master.
	srv.ptyIn.close()
	ptmx.Close()

	exitCode, exitSignal := interpretExit(waitErr)

	// Transcript: flush the tail durable, then close under a timeout so a wedged
	// disk cannot hang the shim (Epic 3 binding).
	_ = tr.Flush()
	closeTranscript(tr)

	// Side-files: snapshot first (fsync'd), then exit.json, so exit.json's
	// presence implies a complete snapshot (G3 ordering).
	if snap, serr := emu.Snapshot(); serr == nil {
		_ = writeFileAtomic(cfg.SessionDir, SnapshotFile, snap)
	}
	if data, jerr := json.Marshal(ExitInfo{
		ExitCode:   exitCode,
		ExitSignal: exitSignal,
		FinishedAt: time.Now(),
	}); jerr == nil {
		_ = writeFileAtomic(cfg.SessionDir, ExitFile, data)
	}

	// Flush buffered DataOut, emit exit_report to any connected client, then
	// tear the socket down.
	code := exitCode
	srv.shutdown(exitReport(exitCode, exitSignal))
	<-acceptDone

	return code, nil
}

// buildEnv returns env unchanged except that a TERM is injected when the caller
// supplied none (agents assume a terminfo-known TERM).
func buildEnv(env []string) []string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			return env
		}
	}
	out := make([]string, len(env), len(env)+1)
	copy(out, env)
	return append(out, defaultTerm)
}

// interpretExit maps a cmd.Wait() error to (exit code, signal name). A signal
// death yields the 128+signum convention and the signal's name; a clean or
// non-zero exit yields that code and an empty signal.
func interpretExit(waitErr error) (code int, signal string) {
	if waitErr == nil {
		return 0, ""
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				sig := ws.Signal()
				return 128 + int(sig), signalName(sig)
			}
			return ws.ExitStatus(), ""
		}
		return ee.ExitCode(), ""
	}
	return -1, ""
}

// signalNames maps the signals a shim itself deals in (plus a few common
// neighbors) to their canonical names. macOS + Linux have no /proc-independent
// stdlib name lookup that returns "SIGKILL"-style strings, so a small explicit
// map is the portable choice.
var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGILL:  "SIGILL",
	syscall.SIGABRT: "SIGABRT",
	syscall.SIGFPE:  "SIGFPE",
	syscall.SIGKILL: "SIGKILL",
	syscall.SIGSEGV: "SIGSEGV",
	syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGALRM: "SIGALRM",
	syscall.SIGTERM: "SIGTERM",
	syscall.SIGBUS:  "SIGBUS",
}

func signalName(sig syscall.Signal) string {
	if n, ok := signalNames[sig]; ok {
		return n
	}
	return fmt.Sprintf("SIG%d", int(sig))
}

// writeFileAtomic writes data to dir/name via a temp file + fsync + rename, so a
// reader never observes a partial side-file (persist.go pattern).
func writeFileAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, name+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}

// closeTranscript closes tr under a timeout so a stalled disk cannot hang the
// shim's exit path (S9 carry-forward from Epic 3).
func closeTranscript(tr *transcript.Writer) {
	done := make(chan struct{})
	go func() {
		_ = tr.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}
