// Command swarm is the single distributed binary for the swarm system.
// Role is selected by the first argument: daemon, shim, hook, or no
// argument at all (opens the TUI).
package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `usage: swarm [daemon|shim|hook]

  swarm          open the TUI
  swarm daemon   run the session daemon
  swarm shim     run the PTY-owning shim process
  swarm hook     post a hook event to the daemon
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
		return runShim(stdout, stderr)
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

func runShim(_, stderr io.Writer) int {
	fmt.Fprintln(stderr, "shim: not implemented")
	return 1
}

func runHook(_, stderr io.Writer) int {
	fmt.Fprintln(stderr, "hook: not implemented")
	return 1
}
