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
// is non-nil for a shim-level failure — either a setup failure (before the
// agent runs) or a side-file persistence failure at exit; in the latter case
// agentExit still carries the agent's real exit code. Any agent outcome (clean
// exit, non-zero, or signal death) on its own returns err == nil.
func Run(cfg Config) (agentExit int, err error) {
	if cfg.Metrics == nil {
		cfg.Metrics = &Metrics{}
	}
	if len(cfg.Argv) == 0 {
		return 0, errors.New("shim: empty Argv (no program to exec)")
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

	srv := newServer(listener, cfg.SocketPath, emu, tr, ptmx, cmd.Process.Pid, cfg.GraceTimeout, cfg.Metrics)
	// Route emulator query replies (DSR/DA/...) back into the PTY master so the
	// agent receives them on stdin. A bounded async pump does the actual writes,
	// so a query-flooding agent that never reads stdin can never block the
	// emulator's reply drain (and thus the PTY drain) on a full PTY (S9).
	replies := newReplyPump(srv.ptyIn)
	emu.SetReplyWriter(replies)

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

	// Block until the agent (group leader) is reaped.
	waitErr := cmd.Wait()

	// Bound the wait for the PTY to reach EOF: a descendant can hold the slave
	// open after the leader is reaped. We do not kill here — finishEscalation
	// issues the single containment KILL below, and closing the master then
	// guarantees the drain terminates — so this wait only gives in-flight output
	// a bounded chance to drain before the group is reaped, and Run never blocks.
	waitClosed(drainDone, cfg.GraceTimeout)

	// Cancel and join the TERM escalation worker, then issue exactly one final
	// synchronous group KILL: containment is guaranteed here (a descendant that
	// ignored TERM — whether or not it held the PTY — is reaped now, not by a
	// timer) and no escalation goroutine survives Run to fire a stray signal.
	srv.finishEscalation()

	// Release the master: closing it unblocks the drain's Read and any in-flight
	// reply write (freeing the ptyWriter lock), so neither the reply pump nor the
	// drain can be stuck when we tear them down — even if a pathological
	// out-of-group holder kept the slave open past the KILL.
	ptmx.Close()
	srv.ptyIn.close()
	replies.close()
	<-drainDone

	exitCode, exitSignal := interpretExit(waitErr)

	// Transcript: flush the tail durable, then close — both under a timeout so a
	// wedged disk cannot hang the shim's exit path (Epic 3 binding, S9).
	flushTranscript(tr)
	closeTranscript(tr)

	// Persist the side-files (snapshot first, then exit.json, then fsync the
	// dir). A persistence failure is surfaced as Run's err while the agent's
	// exit code is still returned.
	persistErr := persistSideFiles(cfg.SessionDir, emu, exitCode, exitSignal)

	// Flush buffered DataOut, emit exit_report to any connected client, then
	// tear the socket down.
	srv.shutdown(exitReport(exitCode, exitSignal))
	<-acceptDone

	return exitCode, persistErr
}

// waitClosed reports whether ch is closed within d.
func waitClosed(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// persistSideFiles writes the G3 side-files: the final grid snapshot (fsync'd),
// then exit.json, then an fsync of the session dir so the temp+rename of both
// files is durable. If the snapshot cannot be produced or written, exit.json is
// NOT written, preserving the invariant that exit.json's presence implies a
// complete snapshot. Any failure is returned so Run can surface it.
func persistSideFiles(dir string, emu *vt.Emulator, exitCode int, exitSignal string) error {
	snap, err := emu.Snapshot()
	if err != nil {
		return fmt.Errorf("shim: snapshot: %w", err)
	}
	if err := writeFileAtomic(dir, SnapshotFile, snap); err != nil {
		return fmt.Errorf("shim: write snapshot: %w", err)
	}
	data, err := json.Marshal(ExitInfo{
		ExitCode:   exitCode,
		ExitSignal: exitSignal,
		FinishedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("shim: marshal exit info: %w", err)
	}
	if err := writeFileAtomic(dir, ExitFile, data); err != nil {
		return fmt.Errorf("shim: write exit info: %w", err)
	}
	if err := fsyncDir(dir); err != nil {
		return fmt.Errorf("shim: fsync session dir: %w", err)
	}
	return nil
}

// fsyncDir fsyncs a directory so a prior temp+rename of a file within it is
// durable across a crash.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
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

// finalizeStepTimeout bounds each disk-facing finalization step (transcript
// Flush and Close) so a stalled or wedged disk cannot hang the shim's exit path
// (S9 carry-forward from Epic 3). It is a var so tests can shorten it.
var finalizeStepTimeout = 5 * time.Second

// flushTranscript flushes the transcript tail under a timeout so a wedged disk
// cannot hang finalization before the timeout-protected Close ever runs (S9).
func flushTranscript(tr *transcript.Writer) {
	done := make(chan struct{})
	go func() {
		_ = tr.Flush()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(finalizeStepTimeout):
	}
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
	case <-time.After(finalizeStepTimeout):
	}
}
