// Command swarm-char is the characterization harness (E9.3 / T-6): it drives a
// real agent CLI in a PTY and records a versioned fixture plus a capability
// matrix entry. It is a dev/test tool; the engine lives in char.go so it is
// testable out of func main.
//
// Usage:
//
//	swarm-char -cli claude-code -version 1.2.3 -scenario idle \
//	    -cwd /work -out fixture.json -- claude --some-flag
//
// Everything after "--" is the argv exec'd directly (never through a shell).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/refadapter"
)

func main() {
	cli := flag.String("cli", "", "CLI identifier (required)")
	version := flag.String("version", "", "CLI version (required)")
	scenario := flag.String("scenario", "", "scenario name (required)")
	cwd := flag.String("cwd", "", "working directory for the CLI")
	cols := flag.Int("cols", 80, "PTY columns")
	rows := flag.Int("rows", 24, "PTY rows")
	timeout := flag.Duration("timeout", defaultTimeout, "wall-clock bound for the run")
	out := flag.String("out", "", "write the fixture JSON here (default: stdout)")
	flag.Parse()

	argv := flag.Args()
	if *cli == "" || *version == "" || *scenario == "" || len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: swarm-char -cli C -version V -scenario S [flags] -- program [args...]")
		os.Exit(2)
	}

	fx, err := characterize(charSpec{
		CLI:      *cli,
		Version:  *version,
		Scenario: *scenario,
		Argv:     argv,
		Cwd:      *cwd,
		Cols:     *cols,
		Rows:     *rows,
		Timeout:  *timeout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := writeFixture(*out, fx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Emit the capability matrix entry derived from the recorded fixture. With no
	// real adapter wired for an arbitrary CLI, the reference adapter is the worked
	// example: it reads the same fixture and yields an E9.6 CapabilityEntry.
	entry := adapter.Capability(refadapter.New(fx), fx)
	capJSON, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "swarm-char: marshal capability:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, string(capJSON))
}

// writeFixture serializes fx as indented JSON to path, or to stdout when path is
// empty.
func writeFixture(path string, fx adapter.Fixture) error {
	b, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return fmt.Errorf("swarm-char: marshal fixture: %w", err)
	}
	if path == "" {
		_, err := os.Stdout.Write(append(b, '\n'))
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
