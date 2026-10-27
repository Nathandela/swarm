// Command swarm-fake-agent is a scripted stand-in for a real agent CLI,
// driven by internal/fakeagent. It is a dev/test binary only, never
// shipped (E1.9).
package main

import (
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/adapter"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/fakeagent"
	"github.com/charmbracelet/x/term"
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
	if len(args) > 0 && args[0] == "--claude-primary-fixture" {
		return runClaudePrimaryFixture(args, stdin, stdout, stderr)
	}
	scriptPath, stdinLog, raw, ok := parseArgs(args)
	if !ok {
		_, _ = fmt.Fprintln(stderr, "usage: swarm-fake-agent [--stdin-log PATH|--raw-stdin-log PATH] <script-path|->")
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
	if raw {
		state, rawErr := term.MakeRaw(os.Stdin.Fd())
		if rawErr != nil {
			_, _ = fmt.Fprintln(stderr, rawErr)
			return 2
		}
		defer func() { _ = term.Restore(os.Stdin.Fd(), state) }()
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
func parseArgs(args []string) (scriptPath, stdinLog string, raw, ok bool) {
	if len(args) == 0 {
		return "", "", false, false
	}
	if args[0] != "--stdin-log" && args[0] != "--raw-stdin-log" {
		return args[0], "", false, true
	}
	if len(args) < 3 || args[1] == "" || args[2] == "" {
		return "", "", false, false
	}
	return args[2], args[1], args[0] == "--raw-stdin-log", true
}

// A finite dev-only stdio primary; it sends no automatic user message and
// accepts no chat commands. Process termination owns its blocked input reader.
func runClaudePrimaryFixture(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 || !strings.HasPrefix(args[1], "--session-id=") {
		return 2
	}
	id := strings.TrimPrefix(args[1], "--session-id=")
	if !adapter.IsCanonicalConversationID(id) {
		return 2
	}
	tty := func(v any) bool { f, ok := v.(*os.File); return ok && term.IsTerminal(f.Fd()) }
	type fixtureStdio struct {
		StdinTTY  bool `json:"stdin_tty"`
		StdoutTTY bool `json:"stdout_tty"`
		StderrTTY bool `json:"stderr_tty"`
	}
	stdio := fixtureStdio{tty(stdin), tty(stdout), tty(stderr)}
	pgid, err := syscall.Getpgid(0)
	if err != nil {
		return 3
	}
	init := struct {
		Type         string       `json:"type"`
		Subtype      string       `json:"subtype"`
		SessionID    string       `json:"session_id"`
		FixturePID   int          `json:"fixture_pid"`
		FixturePGID  int          `json:"fixture_pgid"`
		FixtureStdio fixtureStdio `json:"fixture_stdio"`
	}{"system", "init", id, os.Getpid(), pgid, stdio}
	if json.NewEncoder(stdout).Encode(init) != nil {
		return 3
	}
	telemetry := struct {
		Fixture     string `json:"fixture"`
		StdinTTY    bool   `json:"stdin_tty"`
		StdoutTTY   bool   `json:"stdout_tty"`
		StderrTTY   bool   `json:"stderr_tty"`
		FixturePID  int    `json:"fixture_pid"`
		FixturePGID int    `json:"fixture_pgid"`
	}{"claude-primary", tty(stdin), tty(stdout), tty(stderr), os.Getpid(), pgid}
	if json.NewEncoder(stderr).Encode(telemetry) != nil {
		return 3
	}
	input := make(chan int, 1)
	go func() {
		var b [1]byte
		n, err := stdin.Read(b[:])
		if n != 0 || err != io.EOF {
			input <- 3
		} else {
			input <- 0
		}
	}()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	select {
	case code := <-input:
		return code
	case <-timer.C:
		return 0
	}
}
