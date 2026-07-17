// Command swarm is the single distributed binary for the swarm system.
// Role is selected by the first argument: daemon, shim, hook, or no
// argument at all (opens the TUI).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/transcript"
)

// defaultMaxSessions caps concurrent sessions for a production daemon.
const defaultMaxSessions = 128

// shimSessionEnv guards the setsid re-exec against an infinite loop: it is set
// on the re-exec'd child so a shim that still cannot become a session leader
// fails loudly instead of re-exec'ing again.
const shimSessionEnv = "SWARM_SHIM_SESSION"

const usage = `usage: swarm [daemon|shim|hook]

  swarm          open the TUI
  swarm daemon   run the session daemon
  swarm shim     run the PTY-owning shim process
  swarm hook     post a hook event to the daemon
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
		return runHook(stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}

func runTUI(_, stderr io.Writer) int {
	fmt.Fprintln(stderr, "tui: not implemented")
	return 1
}

// runDaemon runs the `swarm daemon` role. `swarm daemon restart` performs the
// D-8 safe restart. A plain `swarm daemon` opens the daemon from its
// SWARM_DAEMON_* environment (set by the client's detached auto-start, D-1) and
// serves until signalled; with no such configuration it is a no-op stub, since
// the daemon is never started bare by a user — the client auto-starts it.
func runDaemon(args []string, _, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "restart" {
		return runDaemonRestart(stderr)
	}
	cfg, ok := daemonConfigFromEnv()
	if !ok {
		fmt.Fprintln(stderr, "daemon: not implemented")
		return 1
	}
	d, err := daemon.Open(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "daemon: open: %v\n", err)
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

// daemonConfigFromEnv builds a daemon Config from the SWARM_DAEMON_* environment.
// It reports false when no state dir is configured (the bare-invocation stub).
func daemonConfigFromEnv() (daemon.Config, bool) {
	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		return daemon.Config{}, false
	}
	exe, _ := os.Executable() // the daemon spawns `swarm shim` from its own binary
	return daemon.Config{
		StateDir:    stateDir,
		SocketPath:  os.Getenv(daemon.EnvSocket),
		LockPath:    os.Getenv(daemon.EnvLock),
		LogPath:     os.Getenv(daemon.EnvLog),
		ShimBinary:  exe,
		MaxSessions: defaultMaxSessions,
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
	if sid, gerr := syscall.Getsid(0); gerr == nil && sid == os.Getpid() {
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

func runHook(_, stderr io.Writer) int {
	fmt.Fprintln(stderr, "hook: not implemented")
	return 1
}
