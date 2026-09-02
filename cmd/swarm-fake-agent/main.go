// Command swarm-fake-agent is a scripted stand-in for a real agent CLI,
// driven by internal/fakeagent. It is a dev/test binary only, never
// shipped (E1.9).
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Nathandela/swarm/internal/fakeagent"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main's testable body. --stdin-log is deliberately an explicit dev/test
// flag rather than a launch environment variable: supervised agent environments
// are security-allowlisted, and a fixture must not widen that production contract.
// When enabled, every byte the scripted agent consumes from stdin is copied to a
// private file before Run interprets it.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	scriptPath, stdinLog, ok := parseArgs(args)
	if !ok {
		_, _ = fmt.Fprintln(stderr, "usage: swarm-fake-agent [--stdin-log PATH] <script-path|->")
		return 2
	}

	var script io.Reader
	fromStdin := scriptPath == "-"
	if fromStdin {
		script = stdin
	} else {
		f, err := os.Open(scriptPath)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		defer func() { _ = f.Close() }()
		script = f
	}

	steps, err := fakeagent.Parse(script)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	// When the script itself came from stdin, stdin is already consumed, so an
	// ask step has no channel left to read its answer from. Reject before running.
	if fromStdin {
		for _, s := range steps {
			if s.Kind == fakeagent.KindAsk {
				_, _ = fmt.Fprintln(stderr, "ask requires a script file (stdin is consumed by the script)")
				return 2
			}
		}
	}

	if stdinLog != "" {
		logFile, openErr := os.OpenFile(stdinLog, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if openErr != nil {
			_, _ = fmt.Fprintln(stderr, openErr)
			return 2
		}
		defer func() { _ = logFile.Close() }()
		// OpenFile's mode only governs creation. Pin an existing opt-in log back to
		// the same private mode too, so the side channel never leaves readable input.
		if chmodErr := logFile.Chmod(0o600); chmodErr != nil {
			_, _ = fmt.Fprintln(stderr, chmodErr)
			return 2
		}
		stdin = io.TeeReader(stdin, logFile)
	}

	code, err := fakeagent.Run(steps, stdin, stdout, time.Sleep)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 3
	}
	return code
}

// parseArgs preserves the original one-positional-argument interface and adds
// one opt-in prefix. As before, trailing arguments are ignored by the fixture.
func parseArgs(args []string) (scriptPath, stdinLog string, ok bool) {
	if len(args) == 0 {
		return "", "", false
	}
	if args[0] != "--stdin-log" {
		return args[0], "", true
	}
	if len(args) < 3 || args[1] == "" || args[2] == "" {
		return "", "", false
	}
	return args[2], args[1], true
}
