// Command swarm-char is the characterization harness (E9.3 / T-6): it drives a
// real agent CLI in a PTY and records a versioned fixture plus a capability
// matrix entry. It is a dev/test tool; the engine lives in char.go and the CLI
// wiring in run(), so both are testable out of func main.
//
// Usage:
//
//	swarm-char -cli claude-code -version 1.2.3 -scenario idle \
//	    -geometry 100x40 -adapter claude \
//	    -input drive.txt -hook-sink /tmp/hooks.sock \
//	    -cwd /work -out fixture.json -- claude --some-flag
//
// Everything after "--" is the argv exec'd directly (never through a shell).
// -input is a file (or inline text) of `<delay> <keystrokes>` lines fed to the
// CLI's stdin; -hook-sink is a unix socket the CLI's hooks post JSON to;
// -adapter selects which registered Adapter derives the capability entry.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/refadapter"
)

// adapterRegistry maps an -adapter name to the constructor that builds it from a
// recorded fixture. The reference adapter is the default (the worked example for
// the fake agent); Epic 11 registers the real claude/codex adapters here, and
// that is the ONLY change needed to characterize a new CLI (T-5). It is a var so
// tests can register a distinctive adapter and prove selection is honored.
var adapterRegistry = map[string]func(adapter.Fixture) adapter.Adapter{
	"refadapter": refadapter.New,
	"reference":  refadapter.New,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses args, characterizes the CLI, writes the fixture, and emits the
// capability entry from the selected adapter. It returns a process exit code and
// writes all output to stdout/stderr, so tests can drive the whole CLI in-process.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("swarm-char", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cli := fs.String("cli", "", "CLI identifier (required)")
	version := fs.String("version", "", "CLI version (required)")
	scenario := fs.String("scenario", "", "scenario name (required)")
	cwd := fs.String("cwd", "", "working directory for the CLI")
	cols := fs.Int("cols", 80, "PTY columns (overridden by -geometry)")
	rows := fs.Int("rows", 24, "PTY rows (overridden by -geometry)")
	geometry := fs.String("geometry", "", "PTY geometry as COLSxROWS (e.g. 100x40); overrides -cols/-rows")
	timeout := fs.Duration("timeout", defaultTimeout, "wall-clock bound for the run")
	out := fs.String("out", "", "write the fixture JSON here (default: stdout)")
	input := fs.String("input", "", "scripted stdin keystrokes: a file path, or inline `<delay> <data>` lines")
	hookSink := fs.String("hook-sink", "", "unix socket path the CLI's hooks post JSON payloads to")
	adapterName := fs.String("adapter", "refadapter", "adapter used to derive the capability entry (see registry)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	argv := fs.Args()
	if *cli == "" || *version == "" || *scenario == "" || len(argv) == 0 {
		fmt.Fprintln(stderr, "usage: swarm-char -cli C -version V -scenario S [flags] -- program [args...]")
		return 2
	}

	c, r, err := resolveGeometry(*geometry, *cols, *rows)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	inputs, err := loadScriptedInput(*input)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	ctor, ok := adapterRegistry[*adapterName]
	if !ok {
		fmt.Fprintf(stderr, "swarm-char: unknown -adapter %q (known: %s)\n", *adapterName, strings.Join(knownAdapters(), ", "))
		return 2
	}

	fx, err := characterize(charSpec{
		CLI:      *cli,
		Version:  *version,
		Scenario: *scenario,
		Argv:     argv,
		Cwd:      *cwd,
		Cols:     c,
		Rows:     r,
		Timeout:  *timeout,
		Input:    inputs,
		HookSink: *hookSink,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if err := writeFixture(*out, fx, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// Emit the capability entry from the SELECTED adapter, rendering the grid at
	// the characterization geometry (never a hardcoded refadapter or a fixed size).
	entry, err := deriveCapability(ctor(fx), fx, c, r)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	capJSON, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "swarm-char: marshal capability:", err)
		return 1
	}
	fmt.Fprintln(stderr, string(capJSON))
	return 0
}

// knownAdapters returns the registered adapter names, sorted.
func knownAdapters() []string {
	names := make([]string, 0, len(adapterRegistry))
	for n := range adapterRegistry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// resolveGeometry resolves the PTY geometry: -geometry "COLSxROWS" wins when
// set, else the -cols/-rows values. It rejects a malformed or non-positive
// geometry.
func resolveGeometry(geometry string, cols, rows int) (int, int, error) {
	if geometry == "" {
		return cols, rows, nil
	}
	cs, rs, ok := strings.Cut(geometry, "x")
	if !ok {
		return 0, 0, fmt.Errorf("swarm-char: invalid -geometry %q (want COLSxROWS)", geometry)
	}
	c, err := strconv.Atoi(strings.TrimSpace(cs))
	if err != nil || c <= 0 {
		return 0, 0, fmt.Errorf("swarm-char: invalid -geometry columns %q", cs)
	}
	r, err := strconv.Atoi(strings.TrimSpace(rs))
	if err != nil || r <= 0 {
		return 0, 0, fmt.Errorf("swarm-char: invalid -geometry rows %q", rs)
	}
	return c, r, nil
}

// loadScriptedInput reads the -input value: a readable file path yields its
// contents, otherwise the value is treated as inline scripted-input text. Empty
// means no scripted input.
func loadScriptedInput(v string) ([]ScriptedInput, error) {
	if v == "" {
		return nil, nil
	}
	content := v
	if b, err := os.ReadFile(v); err == nil {
		content = string(b)
	}
	return parseScriptedInput(content)
}

// parseScriptedInput parses `<delay> <data>` lines into timed keystrokes. Blank
// lines and `#` comments are skipped; delay is a Go duration; data may use \n,
// \t, \r escapes. e.g. "300ms hello\n" waits 300ms then sends "hello\n".
func parseScriptedInput(content string) ([]ScriptedInput, error) {
	var inputs []ScriptedInput
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		delayStr, data, found := strings.Cut(line, " ")
		if !found {
			return nil, fmt.Errorf("swarm-char: -input line %d: expected `<delay> <data>`", i+1)
		}
		d, err := time.ParseDuration(delayStr)
		if err != nil {
			return nil, fmt.Errorf("swarm-char: -input line %d: invalid delay %q: %v", i+1, delayStr, err)
		}
		inputs = append(inputs, ScriptedInput{Delay: d, Data: unescapeKeystrokes(data)})
	}
	return inputs, nil
}

// unescapeKeystrokes decodes the common C escapes a scripted-input line may carry
// so control characters can be sent as keystrokes.
func unescapeKeystrokes(s string) string {
	r := strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\r`, "\r")
	return r.Replace(s)
}

// writeFixture serializes fx as indented JSON to path, or to stdout when path is
// empty.
func writeFixture(path string, fx adapter.Fixture, stdout io.Writer) error {
	b, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return fmt.Errorf("swarm-char: marshal fixture: %w", err)
	}
	if path == "" {
		_, err := stdout.Write(append(b, '\n'))
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
