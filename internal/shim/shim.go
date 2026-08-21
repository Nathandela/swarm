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
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
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

	// HookSocketPath is the per-session hook UDS (playbook §6.1): a second listener,
	// independent of SocketPath's PTY/control plane. Empty disables it entirely --
	// no listener is bound and no HookSpoolFile is ever created, so an old-shim
	// launch config that never sets this field runs exactly as it does today.
	HookSocketPath string
	// HookSpoolMaxBytes bounds the hook spool; 0 means hookSpoolDefaultMaxBytes.
	HookSpoolMaxBytes int
	// HookToken, when non-empty, gates the hook socket's DRAIN verb (a connection
	// must present the same value in HookDrainRequest.Token). "" disables the check
	// entirely -- the compat default, matching HookSocketPath's own "unset means
	// disabled" convention. POST needs no token of its own; see hooksocket.go.
	HookToken string

	// Backend, when non-nil, is the SESSION BACKEND this shim owns beside the agent -- the
	// `codex app-server` of Mirror M4.1 (backend.go). nil is the PRE-R7 SESSION and the
	// session of every other CLI: no file written, no timeout waited, no handshake expected,
	// and the spawn path below is byte-for-byte what it has always been.
	Backend *BackendConfig
}

// Metrics holds test-observable counters. All fields are safe for concurrent
// use.
type Metrics struct {
	FramesDropped  atomic.Int64
	VTParserFaults atomic.Int64
	vtLastLog      atomic.Int64
}

// ExitInfo is the decoded exit.json side-file.
type ExitInfo struct {
	ExitCode   int       `json:"exit_code"`
	ExitSignal string    `json:"exit_signal"`
	FinishedAt time.Time `json:"finished_at"`
	// BackendExit is the session backend's own exit code, present ONLY when this session had
	// one (Wave R7, ADR-013 §R7.6). Its whole purpose is that "the Codex backend died" is
	// never reported as an unexplained agent exit: with no PTY and no streams, a dead
	// app-server leaves no other trace anywhere in the system.
	BackendExit *int `json:"backend_exit,omitempty"`
}

// testHookAfterSignalArm, when non-nil, is invoked inside Run once the
// self-containment signal handler is armed (signal.Notify) and before the agent
// is spawned (pty.StartWithSize). It exists ONLY to make the arm->spawn ordering
// window observable to a test; it is nil in production and changes no behavior.
// See shim_signal_order_test.go.
var testHookAfterSignalArm func()

// testHookBeforeBackendSpawn, when non-nil, is invoked on a backend session after the
// control planes are up (startPlanes) and immediately BEFORE the backend is spawned. It
// exists ONLY to let a test deliver a termination request into the window where NEITHER
// contained process group exists yet (r7r3_startupkill_test.go); it is nil in production
// and changes no behavior.
var testHookBeforeBackendSpawn func(s *server)

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
	defer func() { _ = emu.Close() }()

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

	// Arm the self-containment signal handler BEFORE spawning the agent, so that from
	// the instant the agent exists, ANY catchable termination of the shim
	// (SIGTERM/SIGINT/SIGHUP) first runs the agent's process-group TERM->grace->KILL.
	// This makes shim-exited ⇒ agent-group-killed with no socket round-trip and no
	// startup/acceptLoop-window race: the daemon can SIGTERM the shim at any moment to
	// reliably contain the agent (audit-004 N2 containment primitive). Signals are
	// buffered here and acted on once the agent's pgid is known (below). A SIGKILL of
	// the shim itself remains uncatchable — the documented last-resort residual.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	sigStop := func() { signal.Stop(sigCh) } // signal.Stop is idempotent

	// Test-only seam (nil in production): runs inside the arm->spawn window —
	// after the handler is armed, before the agent is spawned — so a test can
	// deliver a signal into that exact window and prove it is buffered, not lost.
	// It adds no production behavior.
	if testHookAfterSignalArm != nil {
		testHookAfterSignalArm()
	}

	ws := &pty.Winsize{Rows: uint16(cfg.Rows), Cols: uint16(cfg.Cols)}

	// THE BACKEND PATH (Wave R7, ADR-013 §R7.2e) and THE PRE-R7 PATH are deliberately
	// separate, and the second is byte-for-byte what it has always been. Every session
	// launched before R7, and every session of every other CLI forever, has Backend == nil:
	// no file written, no timeout waited, no handshake expected (HookSocketPath's own
	// unset-means-disabled convention).
	var (
		cmd     *exec.Cmd
		ptmx    *os.File
		srv     *server
		replies *replyPump
		backend *backendProc
		// backendWatch is `backend` ONLY once its socket became servable. See §R7.6 below.
		backendWatch *backendProc
		// preAgent holds the goroutines the backend path starts BEFORE the agent exists, so
		// they are started exactly once on either path.
		started bool
	)
	acceptDone := make(chan struct{})
	drainDone := make(chan struct{})
	// sigDone releases the signal handler on a clean agent exit; it is JOINED at
	// finalization, so after that join no onSignal can run to signal a possibly-reused pgid.
	sigDone := make(chan struct{})
	var sigWG sync.WaitGroup
	// startPlanes brings up everything that needs the server: the signal handler, the
	// control-socket accept loop and the PTY drain. On a BACKEND session it runs BEFORE the
	// backend is even spawned, which is what lets the daemon hello/attach and send its
	// go-ahead while no agent exists yet -- and what keeps a TERM arriving in that window
	// from waiting on the go-ahead bound before anything is contained.
	startPlanes := func() {
		if started {
			return
		}
		started = true
		sigWG.Add(1)
		go func() {
			defer sigWG.Done()
			select {
			case <-sigCh:
				srv.onSignal(shimwire.SigTerm)
			case <-sigDone:
			}
		}()
		go func() {
			defer close(acceptDone)
			srv.acceptLoop()
		}()
		go func() {
			defer close(drainDone)
			srv.drain()
		}()
	}

	if cfg.Backend == nil {
		cmd = &exec.Cmd{
			Path: cfg.Argv[0],
			Args: cfg.Argv,
			Env:  buildEnv(cfg.Env),
			Dir:  cfg.Cwd,
		}
		var startErr error
		ptmx, startErr = pty.StartWithSize(cmd, ws)
		if startErr != nil {
			sigStop()
			_ = listener.Close()
			closeTranscript(tr)
			return 0, fmt.Errorf("shim: start agent: %w", startErr)
		}
		srv = newServer(listener, cfg.SocketPath, emu, tr, ptmx, cmd.Process.Pid, cfg.GraceTimeout, cfg.Metrics)
		replies = wireReplies(emu, srv)
		startPlanes()
	} else {
		// The PTY pair is opened FIRST so the control plane can serve -- hello, attach, and
		// the go-ahead itself -- before the agent process exists. That ordering is the whole
		// point: the daemon becomes a connected JSON-RPC client of the backend BEFORE the
		// agent can start a turn, which is what makes it impossible to miss a
		// `thread/started` and removes the cold-start rollout race rather than retrying
		// around it.
		var tty *os.File
		var openErr error
		ptmx, tty, openErr = pty.Open()
		if openErr != nil {
			sigStop()
			_ = listener.Close()
			closeTranscript(tr)
			return 0, fmt.Errorf("shim: open pty: %w", openErr)
		}
		_ = setWinsize(ptmx, ws)
		srv = newServer(listener, cfg.SocketPath, emu, tr, ptmx, 0, cfg.GraceTimeout, cfg.Metrics)
		replies = wireReplies(emu, srv)
		startPlanes()

		if testHookBeforeBackendSpawn != nil {
			testHookBeforeBackendSpawn(srv)
		}
		var berr error
		backend, berr = startBackend(cfg.Backend, cfg.SessionDir)
		if backend != nil {
			// Recorded BEFORE anything can be signalled at it, so a TERM arriving in this
			// window still reaps it.
			srv.setBackendPgid(backend.pgid)
		}
		if berr != nil {
			log.Printf("shim: session backend unavailable, launching the agent without it: %v", berr)
			if backend != nil {
				// It is ALIVE (a readiness timeout does not kill it) but was never
				// recorded -- writeBackendInfo runs only after servable -- so it must
				// not be left to finalization: an unrecorded live backend plus an
				// uncatchable SIGKILL of this shim is an orphan the daemon can never
				// find (containBackendFailure says why). setBackendPgid(0) afterwards:
				// the group is fully reaped, and re-KILLing that pgid at finalization
				// -- possibly hours later -- could hit a recycled group.
				containBackendFailure(backend, cfg.SessionDir, cfg.GraceTimeout)
				srv.setBackendPgid(0)
			}
		} else if werr := writeBackendInfo(cfg.SessionDir, backend, cfg.Backend.SocketPath); werr != nil {
			// THE RECORD IS THE CONTAINMENT: backend.json is the daemon's only means of
			// identifying an orphan backend, so a failure to write it (disk full, a
			// permission fault) makes this live, account-authenticated app-server
			// unreapable the moment the shim takes a SIGKILL. A write failure is
			// therefore a failure of the BACKEND CHANNEL: kill the group now, surface
			// why, and launch the agent degraded -- exactly the never-servable path.
			// backendWatch stays nil: a deliberately-killed backend must not fire the
			// backend-died-first edge and take the agent down with it.
			log.Printf("shim: record the session backend: %v; killing the unrecorded backend "+
				"and launching the agent without it (a backend the daemon cannot identify "+
				"must not survive this shim)", werr)
			containBackendFailure(backend, cfg.SessionDir, cfg.GraceTimeout)
			srv.setBackendPgid(0)
		} else {
			// SERVING AND RECORDED, so the session HAS a structured plane and its death
			// is a real lifecycle event (below). A backend that never served, or was
			// never recorded, is not watched: this session is running degraded without
			// one, exactly as a pre-R7 Codex session does, and ending it on the death of
			// something it never used would take the agent down for a channel it is not
			// using.
			backendWatch = backend
		}

		goAheadTimeout := cfg.Backend.GoAheadTimeout
		if goAheadTimeout <= 0 {
			goAheadTimeout = defaultBackendGoAheadTimeout
		}
		agentArgs, ok := srv.waitBackendGoAhead(goAheadTimeout)
		if !ok {
			log.Printf("shim: no backend_attach arrived within %s; launching the agent DEGRADED "+
				"(no backend arguments appended)", goAheadTimeout)
		}
		argv := append(append([]string(nil), cfg.Argv...), agentArgs...)
		cmd = &exec.Cmd{
			Path:        argv[0],
			Args:        argv,
			Env:         buildEnv(cfg.Env),
			Dir:         cfg.Cwd,
			Stdin:       tty,
			Stdout:      tty,
			Stderr:      tty,
			SysProcAttr: &syscall.SysProcAttr{Setsid: true, Setctty: true},
		}
		startErr := cmd.Start()
		_ = tty.Close() // the child holds its own dup; the master is ours
		if startErr != nil {
			sigStop()
			_ = ptmx.Close()
			_ = listener.Close()
			closeTranscript(tr)
			<-acceptDone
			<-drainDone
			// THE BACKEND IS ALREADY RUNNING ON THIS PATH, and this is the only exit from
			// Run that does not pass through finalization. Review BLOCKING 5, PROBED: with
			// cfg.Argv naming a nonexistent program the backend was still alive 5 s after
			// Run returned, still holding the session socket -- a process authenticated to
			// a real ChatGPT account, with no PTY to HUP it and no stream anyone watches,
			// which is the exact sentence backend.go opens with. finishEscalation issues the
			// group KILL to both recorded groups (the agent's is still 0, and killGroups
			// never signals 0), and the join is what proves it is gone rather than merely
			// signalled.
			if backend != nil {
				srv.finishEscalation()
				<-backend.dead
			}
			return 0, fmt.Errorf("shim: start agent: %w", startErr)
		}
		srv.setAgentPgid(cmd.Process.Pid)
	}

	// The hook socket (playbook §6.1): a second, independent listener over its own
	// durable spool. Bound only after the agent is spawned -- unlike the control
	// socket, nothing needs to attach to it before the agent can produce its first
	// hook event, and binding it here keeps this path off the PTY-spawn critical
	// window entirely (ADR-013's sacred rule: the PTY plane is untouched by any of
	// this). HookSocketPath=="" disables it entirely -- no listener, no spool file
	// (requirement 7's compat). A setup failure here (disk full, an unbindable
	// path) degrades to no hook socket for this session rather than aborting the
	// run: playbook 6.1's "disk-full ... lets the agent continue locally" applies
	// exactly as much to spool OPEN as to a later spool WRITE -- the PTY plane is
	// never at this channel's mercy either way.
	var hookSrv *hookServer
	if cfg.HookSocketPath != "" {
		// A dedicated local error, deliberately never assigned to the named return
		// `err` above: every path below this point sets Run's own return values
		// explicitly, so this could never actually leak -- but using Run's own named
		// `err` as scratch space here was a latent trap for the next edit that adds
		// an early `return` relying on it.
		var hsErr error
		hookSrv, hsErr = newSessionHookServer(cfg)
		if hsErr != nil {
			log.Printf("shim: hook socket unavailable for this session: %v", hsErr)
			hookSrv = nil
		}
	}

	var hookAcceptDone chan struct{}
	if hookSrv != nil {
		hookAcceptDone = make(chan struct{})
		go func() {
			defer close(hookAcceptDone)
			hookSrv.acceptLoop()
		}()
	}

	// Block until the agent (group leader) is reaped -- or, on a backend session, until the
	// BACKEND dies first.
	//
	// THE BACKEND'S OWN CRASH IS A REAL LIFECYCLE EVENT and it kills the terminal, not just
	// the mirror: under `codex --remote unix://SOCK` the TUI is a CLIENT of the app-server
	// (R1 leg 2 recorded its whole boot handshake going through it), so a dead backend leaves
	// the owner wedged. R7 does NOT restart it -- a restarted server has no thread state and
	// the TUI's connection is already broken -- it fires the agent's existing
	// TERM->grace->KILL from this new edge so the owner gets a clean end, and RECORDS WHY in
	// exit.json so it is never reported as an unexplained agent exit.
	var backendExit *int
	waitErr := waitAgentOrBackend(cmd, backendWatch, srv, &backendExit)

	// Agent reaped: stop catching signals, release the handler, drain any signal that
	// was buffered before the stop, and JOIN the handler goroutine. After this it is
	// provably quiescent — no onSignal can run during or after finalization to signal
	// a possibly-reused pgid (finishEscalation, below, issues the single final group
	// KILL). This mirrors the escalation worker's cancel-and-join discipline.
	sigStop()
	close(sigDone)
	sigWG.Wait()
	select {
	case <-sigCh: // discard a signal buffered before sigStop; nothing acts on it now
	default:
	}

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

	// JOIN THE BACKEND, AND ONLY NOW. The backend's Wait goroutine is joined AFTER
	// finishEscalation has issued the final group KILL, which is what guarantees the join
	// RETURNS: a backend that ignores TERM is already reaped by that KILL. Joining it before
	// would park Run behind a process that will never die on its own -- the deadlock
	// TestR7ShimBackend_TheJoinDoesNotBlockRun exists to fence.
	if backend != nil {
		<-backend.dead
		if backendExit == nil {
			code := backend.exitCode
			backendExit = &code
		}
	}

	// Release the master: closing it unblocks the drain's Read and any in-flight
	// reply write (freeing the ptyWriter lock), so neither the reply pump nor the
	// drain can be stuck when we tear them down — even if a pathological
	// out-of-group holder kept the slave open past the KILL.
	_ = ptmx.Close()
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
	persistErr := persistSideFiles(cfg.SessionDir, emu, exitCode, exitSignal, backendExit)

	// Flush buffered DataOut, emit exit_report to any connected client, then
	// tear the socket down.
	srv.shutdown(exitReport(exitCode, exitSignal))
	<-acceptDone
	if hookSrv != nil {
		hookSrv.shutdown()
		<-hookAcceptDone
	}

	return exitCode, persistErr
}

// wireReplies routes emulator query replies (DSR/DA/...) back into the PTY master so the
// agent receives them on stdin. A bounded async pump does the actual writes, so a
// query-flooding agent that never reads stdin can never block the emulator's reply drain
// (and thus the PTY drain) on a full PTY (S9).
func wireReplies(emu *vt.Emulator, srv *server) *replyPump {
	replies := newReplyPump(srv.ptyIn)
	emu.SetReplyWriter(replies)
	return replies
}

// waitAgentOrBackend blocks until the agent is reaped and returns its wait error. On a
// session with a backend it ALSO watches the backend: if the backend dies first, its exit is
// recorded through backendExit, the agent's own TERM->grace->KILL is fired from that new
// edge, and the agent is then waited for as usual.
//
// It is a separate function rather than an inline select so the no-backend path is literally
// `cmd.Wait()`, unchanged.
func waitAgentOrBackend(cmd *exec.Cmd, backend *backendProc, srv *server, backendExit **int) error {
	if backend == nil {
		return cmd.Wait()
	}
	agentDone := make(chan error, 1)
	go func() { agentDone <- cmd.Wait() }()
	select {
	case err := <-agentDone:
		return err
	case <-backend.dead:
		code := backend.exitCode
		*backendExit = &code
		log.Printf("shim: the session backend exited with %d; ending the session (its TUI is a "+
			"client of that server, so it is wedged rather than merely unmirrored)", code)
		srv.onSignal(shimwire.SigTerm)
		return <-agentDone
	}
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
func persistSideFiles(dir string, emu *vt.Emulator, exitCode int, exitSignal string, backendExit *int) error {
	snap, err := emu.Snapshot()
	if err != nil {
		return fmt.Errorf("shim: snapshot: %w", err)
	}
	if err := writeFileAtomic(dir, SnapshotFile, snap); err != nil {
		return fmt.Errorf("shim: write snapshot: %w", err)
	}
	data, err := json.Marshal(ExitInfo{
		ExitCode:    exitCode,
		ExitSignal:  exitSignal,
		FinishedAt:  time.Now(),
		BackendExit: backendExit,
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
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// buildEnv returns the agent environment: the caller's env minus the
// launch-environment denylist (persist.ScrubRemoteControl — a supervised session
// must never inherit ambient Remote Control, agents-tracker-n047), plus a TERM
// injected when the caller supplied none (agents assume a terminfo-known TERM).
// This is the last gate before exec, so the denylist holds whatever composed
// cfg.Env — a widened daemon allowlist, a post-filter injection, or a
// hand-written shim-launch.json.
func buildEnv(env []string) []string {
	env = persist.ScrubRemoteControl(env)
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
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
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
