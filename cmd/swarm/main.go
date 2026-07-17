// Command swarm is the single distributed binary for the swarm system.
// Role is selected by the first argument: daemon, shim, hook, or no
// argument at all (opens the TUI).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/detect"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/skeleton"
	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/tui"
	"github.com/Nathandela/swarm/internal/version"
	"golang.org/x/sys/unix"
)

// defaultMaxSessions caps concurrent sessions for a production daemon.
const defaultMaxSessions = 128

// Engine tuning for the assembled daemon: a low-frequency fallback poll and the
// staleness window that bounds a stale typed signal / an active-but-silent turn.
const (
	daemonPollInterval       = time.Second
	daemonStalenessThreshold = 30 * time.Second
)

// envFakeAgentBin is the dev/test-only knob naming the swarm-fake-agent binary the
// walking-skeleton assembly execs for the reserved agent "fake". It is unset in a
// real install, so "fake" simply does not resolve there.
const envFakeAgentBin = "SWARM_FAKE_AGENT_BIN"

// shimSessionEnv guards the setsid re-exec against an infinite loop: it is set
// on the re-exec'd child so a shim that still cannot become a session leader
// fails loudly instead of re-exec'ing again.
const shimSessionEnv = "SWARM_SHIM_SESSION"

const usage = `usage: swarm [daemon|shim|hook|version]

  swarm            open the TUI
  swarm daemon     run the session daemon
  swarm shim       run the PTY-owning shim process
  swarm hook       post a hook event to the daemon
  swarm version    print the build version
`

const shimUsage = `usage: swarm shim --config <path>

  --config <path>   JSON launch config: session_id, argv, cwd, env,
                    socket_path, session_dir, cols, rows, grace_ms
`

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch routes args to the appropriate role and returns the process
// exit code. It performs no I/O beyond stdout/stderr so it is testable
// without exec.
func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runTUI(stdout, stderr)
	}

	switch args[0] {
	case "daemon":
		return runDaemon(args[1:], stdout, stderr)
	case "shim":
		return runShim(args[1:], stdout, stderr)
	case "hook":
		return runHook(args[1:], stdout, stderr)
	case "version", "--version":
		return runVersion(stdout)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}

// runVersion is the `swarm version` (and `--version`) role (E13.2): it prints
// the build-time stamped version (internal/version.Version, "dev" unless
// overridden via -ldflags at release build time — see .goreleaser.yaml) plus
// the Go toolchain version. This is also the value the D-8 hello handshake
// reports to a connecting client (internal/protocol's Control.BuildVersion),
// so a client can tell it is talking to a different-build daemon.
func runVersion(stdout io.Writer) int {
	fmt.Fprintf(stdout, "swarm %s (%s)\n", version.Version, runtime.Version())
	return 0
}

// runTUI is the no-argument role: it opens the client TUI on the real terminal
// (F1 — the Epic 8 milestone that assembles skeleton + attach + tui into the bare
// binary). It ensures a daemon is running (auto-start, D-1), dials a protocol
// client, builds the agent-detect and attach-runner seams, and runs the Bubble Tea
// program over the controlling terminal, handing the terminal to internal/attach on
// Enter and taking it back on detach. Without an interactive terminal (a pipe / CI)
// it fails with a clear message and a non-zero exit — never a panic or a half-drawn
// screen. A user-initiated quit (Esc, or SIGINT that Bubble Tea catches and turns
// into ErrInterrupted after restoring the terminal) is a clean exit.
func runTUI(stdout, stderr io.Writer) int {
	out, ok := interactiveTTY(stdout, os.Stdin)
	if !ok {
		fmt.Fprintln(stderr, "swarm: not a terminal; the TUI needs an interactive terminal")
		return 1
	}

	client, err := dialClient()
	if err != nil {
		fmt.Fprintf(stderr, "swarm: %v\n", err)
		return 1
	}
	defer client.Close()

	// prog is captured by the attach runner's terminal handoff; it is assigned just
	// before Run, so the closures see the live program when an attach fires.
	var prog *tea.Program
	dialAttach := func(id string) (attach.Session, error) {
		att, aerr := client.Attach(id)
		if aerr != nil {
			return nil, aerr
		}
		return att, nil
	}
	runner := tui.NewAttachRunner(dialAttach, tui.TerminalHandoff{
		Release: func() error { return prog.ReleaseTerminal() },
		Restore: func() error { return prog.RestoreTerminal() },
	})
	model := tui.New(client, detectAgents(os.Getenv(envFakeAgentBin)), tui.WithAttachRunner(runner))

	prog = tea.NewProgram(model, tea.WithInput(os.Stdin), tea.WithOutput(out))
	if _, err := prog.Run(); err != nil && !errors.Is(err, tea.ErrInterrupted) {
		fmt.Fprintf(stderr, "swarm: tui: %v\n", err)
		return 1
	}
	return 0
}

// interactiveTTY verifies BOTH stdout and stdin are interactive terminals — the TUI
// needs both: Bubble Tea renders to stdout while the attach passthrough reads
// keystrokes from stdin, so a piped/redirected either end (a non-TTY) must be
// rejected up front rather than half-drawing a screen or blocking on dead input.
// Checking only stdout would let `swarm < /dev/null` (a redirected stdin) slip past.
// It returns the stdout file to render into when both are terminals.
func interactiveTTY(stdout io.Writer, stdin *os.File) (out *os.File, ok bool) {
	f, isFile := stdout.(*os.File)
	if !isFile || !term.IsTerminal(f.Fd()) {
		return nil, false
	}
	if stdin == nil || !term.IsTerminal(stdin.Fd()) {
		return nil, false
	}
	return f, true
}

// dialClient ensures a daemon is running (auto-start, D-1) and returns a connected
// protocol client to it. The SWARM_DAEMON_* environment overrides the default home
// (the same knobs `swarm daemon` reads), so a test can point the client at a
// controlled daemon; EnsureDaemon only spawns one when the socket does not answer.
func dialClient() (*protocol.Client, error) {
	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		var err error
		if stateDir, err = persist.DefaultDir(); err != nil {
			return nil, err
		}
	}
	exe, _ := os.Executable()
	cc := daemon.ClientConfig{
		StateDir:   stateDir,
		SocketPath: envOr(daemon.EnvSocket, filepath.Join(stateDir, "daemon.sock")),
		LockPath:   envOr(daemon.EnvLock, filepath.Join(stateDir, "daemon.lock")),
		LogPath:    envOr(daemon.EnvLog, filepath.Join(stateDir, "daemon.log")),
		DaemonBin:  exe,
	}
	conn, err := daemon.EnsureDaemon(cc)
	if err != nil {
		return nil, err
	}
	_ = conn.Close() // EnsureDaemon proved the daemon is live; the TUI speaks the full client protocol on its own dial
	return protocol.Dial(cc.SocketPath, []string{"attach", "subscribe"})
}

// detectAgents builds the launch-form agent detector. It probes the host for every
// registered adapter (claude, codex) through the CORE adapter.Detect + the real
// exec-based detect.Host, so the picker greys an agent that is missing or
// out-of-supported-range (L-2). The reserved dev/test "fake" agent is appended when
// SWARM_FAKE_AGENT_BIN is set (unset in a real install). Detection runs the free
// `--version` probe only — never a billable agent run.
func detectAgents(fakeBin string) tui.DetectFunc {
	return func() []tui.AgentInfo {
		var agents []tui.AgentInfo
		host := detect.Host{}
		for _, name := range registry.Names() {
			if name == "reference" {
				continue // the reference adapter is a test harness, not an installable CLI
			}
			ad, ok := registry.New(name)
			if !ok {
				continue
			}
			det := adapter.Detect(ad, host)
			agents = append(agents, tui.AgentInfo{
				Name:      name,
				Installed: det.Found,
				InRange:   det.InRange,
				Options:   ad.Options(),
			})
		}
		if fakeBin != "" {
			agents = append(agents, tui.AgentInfo{
				Name:      "fake",
				Installed: true,
				InRange:   true,
				Options:   []adapter.OptionSpec{{Key: "script", Label: "Script path", Type: "string", Required: true}},
			})
		}
		return agents
	}
}

// runDaemon runs the `swarm daemon` role. `swarm daemon restart` performs the
// D-8 safe restart. A plain `swarm daemon` stands up the FULL assembly
// (internal/skeleton) from its SWARM_DAEMON_* environment (set by the client's
// detached auto-start, D-1) and serves until signalled; with no such configuration
// it is a no-op stub, since the daemon is never started bare by a user — the
// client auto-starts it.
func runDaemon(args []string, _, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "restart" {
		return runDaemonRestart(stderr)
	}
	cfg, ok := skeletonConfigFromEnv()
	if !ok {
		fmt.Fprintln(stderr, "daemon: not implemented")
		return 1
	}
	d, err := skeleton.Serve(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "daemon: serve: %v\n", err)
		return 1
	}
	// Serve until a termination signal, then Close cleanly (running shims are
	// independent and survive; the singleton lock is released).
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	_ = d.Close()
	return 0
}

// skeletonConfigFromEnv builds the assembly Config from the SWARM_DAEMON_*
// environment (plus the dev/test-only fake-agent knob). It reports false when no
// state dir is configured (the bare-invocation stub).
func skeletonConfigFromEnv() (skeleton.Config, bool) {
	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		return skeleton.Config{}, false
	}
	exe, _ := os.Executable() // the daemon spawns `swarm shim` from its own binary
	return skeleton.Config{
		StateDir:           stateDir,
		SocketPath:         os.Getenv(daemon.EnvSocket),
		LockPath:           os.Getenv(daemon.EnvLock),
		LogPath:            os.Getenv(daemon.EnvLog),
		ShimBinary:         exe,
		MaxSessions:        defaultMaxSessions,
		PollInterval:       daemonPollInterval,
		StalenessThreshold: daemonStalenessThreshold,
		FakeAgentBin:       os.Getenv(envFakeAgentBin),
	}, true
}

// runDaemonRestart stops the running daemon and spawns a fresh one (D-8). Its
// shims survive the handoff and are reconnected by the replacement.
func runDaemonRestart(stderr io.Writer) int {
	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		var err error
		if stateDir, err = persist.DefaultDir(); err != nil {
			fmt.Fprintf(stderr, "daemon restart: %v\n", err)
			return 1
		}
	}
	exe, _ := os.Executable()
	cc := daemon.ClientConfig{
		StateDir:   stateDir,
		SocketPath: envOr(daemon.EnvSocket, filepath.Join(stateDir, "daemon.sock")),
		LockPath:   envOr(daemon.EnvLock, filepath.Join(stateDir, "daemon.lock")),
		LogPath:    envOr(daemon.EnvLog, filepath.Join(stateDir, "daemon.log")),
		DaemonBin:  exe,
	}
	if err := daemon.Restart(cc); err != nil {
		fmt.Fprintf(stderr, "daemon restart: %v\n", err)
		return 1
	}
	return 0
}

// envOr returns the environment value for key, or fallback when it is unset.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// shimLaunchConfig is the JSON launch contract for `swarm shim --config`,
// decoded into a shim.Config.
type shimLaunchConfig struct {
	SessionID  string   `json:"session_id"`
	Argv       []string `json:"argv"`
	Cwd        string   `json:"cwd"`
	Env        []string `json:"env"`
	SocketPath string   `json:"socket_path"`
	SessionDir string   `json:"session_dir"`
	Cols       int      `json:"cols"`
	Rows       int      `json:"rows"`
	GraceMS    int      `json:"grace_ms"`
}

// runShim parses --config, detaches from any controlling terminal, and runs the
// shim engine, exiting with the agent's exit code. A missing --config is a usage
// error (exit 2).
func runShim(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("shim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to the JSON launch config")
	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, shimUsage)
		return 2
	}
	if *configPath == "" {
		fmt.Fprint(stderr, shimUsage)
		return 2
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "shim: read config: %v\n", err)
		return 2
	}
	var lc shimLaunchConfig
	if err := json.Unmarshal(data, &lc); err != nil {
		fmt.Fprintf(stderr, "shim: parse config: %v\n", err)
		return 2
	}

	// Guarantee the shim leads its own session so it outlives the launching
	// terminal (E4.1 "Shim setsids", D-3). On success we proceed; if a re-exec
	// was needed to acquire the session, we return its child's exit code; any
	// unexpected failure is fatal.
	code, reexeced, err := ensureSession()
	if err != nil {
		fmt.Fprintf(stderr, "shim: %v\n", err)
		return 1
	}
	if reexeced {
		return code
	}

	cfg := shim.Config{
		SessionID:     lc.SessionID,
		Argv:          lc.Argv,
		Cwd:           lc.Cwd,
		Env:           lc.Env,
		SocketPath:    lc.SocketPath,
		SessionDir:    lc.SessionDir,
		Cols:          lc.Cols,
		Rows:          lc.Rows,
		TranscriptCfg: transcript.Config{MaxBytes: 8 << 20, MaxFiles: 3},
		GraceTimeout:  time.Duration(lc.GraceMS) * time.Millisecond,
	}
	exit, err := shim.Run(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "shim: %v\n", err)
		if exit == 0 {
			return 1 // a setup failure with no agent exit code to report
		}
	}
	return exit
}

// ensureSession makes the shim a session leader (E4.1). It returns:
//   - reexeced=false, err=nil: this process is now (or already was) a session
//     leader — proceed to run the shim here.
//   - reexeced=true: we could not setsid in place, so we re-exec'd ourselves
//     with SysProcAttr{Setsid:true} and ran the shim in that child; exitCode is
//     the child's exit code, which the caller must return.
//   - err!=nil: an unexpected, fatal failure — never silently proceed.
func ensureSession() (exitCode int, reexeced bool, err error) {
	if _, serr := syscall.Setsid(); serr == nil {
		return 0, false, nil // we are now a session leader
	} else if !errors.Is(serr, syscall.EPERM) {
		return 0, false, fmt.Errorf("setsid: %w", serr)
	}
	// EPERM: we are already a process-group leader. If we already lead the
	// session, that is fine; otherwise we must re-exec to acquire one.
	if sid, gerr := unix.Getsid(0); gerr == nil && sid == os.Getpid() {
		return 0, false, nil
	}
	if os.Getenv(shimSessionEnv) == "1" {
		return 0, false, errors.New("setsid: not a session leader even after re-exec")
	}
	code, rerr := reexecWithSetsid()
	if rerr != nil {
		return 0, false, rerr
	}
	return code, true, nil
}

// reexecWithSetsid re-launches this binary with the same args in a fresh session
// (SysProcAttr.Setsid), guarded by shimSessionEnv to prevent re-exec loops, and
// returns the child's exit code.
func reexecWithSetsid() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate self for setsid re-exec: %w", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), shimSessionEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, fmt.Errorf("setsid re-exec: %w", err)
	}
	return 0, nil
}

// runHook runs the `swarm hook <event>` role (E10.1 / G4, Epic 11 mapping bridge):
// it composes an authenticated status callback from the per-session environment
// injected at spawn (session id, live token, daemon socket, monotonic sequence) and
// posts it to the daemon socket. The hook CLI (e.g. Claude Code) posts its JSON
// payload on STDIN, whose top-level fields are extracted into the callback payload;
// the engine then NORMALIZES {event, payload} into status dimensions via the
// session's registered SignalSources (the adapter's event->status table). Explicit
// `key=value` args still work (and override a stdin field of the same name), so
// `swarm hook Stop` and `swarm hook Notification notification_type=idle` both work.
// A bare `swarm hook` with no event has nothing to post.
func runHook(args []string, _, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "hook: not implemented")
		return 1
	}
	payload := parseHookStdin(os.Stdin)
	for k, v := range parseHookPayload(args[1:]) {
		payload[k] = v // explicit args override a stdin field of the same name
	}
	cb, err := hookclient.FromEnv(os.Getenv, args[0], payload)
	if err != nil {
		fmt.Fprintf(stderr, "hook: %v\n", err)
		return 1
	}
	if err := hookclient.Post(os.Getenv(hookclient.EnvSocket), cb); err != nil {
		fmt.Fprintf(stderr, "hook: %v\n", err)
		return 1
	}
	return 0
}

// hookStdinLimit bounds how much of a hook's stdin payload we read. Claude posts a
// small JSON object; the cap guards against an unbounded or garbage stream.
const hookStdinLimit = 1 << 20

// parseHookStdin reads a hook's JSON payload from r (Claude Code posts it on stdin)
// and extracts its top-level STRING fields into a status payload the engine
// normalizes via the session's SignalSources. It is best-effort and total: nil,
// empty, non-JSON, or a non-object stream yields an empty (never nil) map. The
// reserved dimension keys "turn"/"interaction" are skipped, so a crafted payload
// cannot inject a status dimension directly — deriving those from the event is the
// engine's job.
func parseHookStdin(r io.Reader) map[string]string {
	out := map[string]string{}
	if r == nil {
		return out
	}
	data, err := io.ReadAll(io.LimitReader(r, hookStdinLimit))
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return out
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return out
	}
	for k, raw := range obj {
		if k == "turn" || k == "interaction" { // engine.PayloadKey* — never client-injected
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			out[k] = s
		}
	}
	return out
}

// parseHookPayload turns `key=value` args into a status-dimension payload,
// ignoring any arg without '='. Returns nil when there is nothing to carry.
func parseHookPayload(args []string) map[string]string {
	if len(args) == 0 {
		return nil
	}
	m := make(map[string]string, len(args))
	for _, a := range args {
		if k, v, ok := strings.Cut(a, "="); ok {
			m[k] = v
		}
	}
	return m
}
