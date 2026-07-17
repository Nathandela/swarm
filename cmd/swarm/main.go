// Command swarm is the single distributed binary for the swarm system.
// Role is selected by the first argument: daemon, shim, hook, or no
// argument at all (opens the TUI).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/transcript"
)

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
		return runDaemon(stdout, stderr)
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

func runDaemon(_, stderr io.Writer) int {
	fmt.Fprintln(stderr, "daemon: not implemented")
	return 1
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

	// Detach from any controlling terminal so the shim outlives the launching
	// session (E4.1 "Shim setsids", D-3). Best-effort: an EPERM (we already lead
	// a process group) is expected and fine.
	_, _ = syscall.Setsid()

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

func runHook(_, stderr io.Writer) int {
	fmt.Fprintln(stderr, "hook: not implemented")
	return 1
}
