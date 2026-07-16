package fakeagent

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Kind identifies which directive a Step represents.
type Kind int

const (
	KindPrint Kind = iota
	KindAsk
	KindIdle
	KindExit
)

// Step is one parsed script directive.
type Step struct {
	Kind     Kind
	Text     string        // print, ask
	Duration time.Duration // idle
	Code     int           // exit
}

// Parse reads a fakeagent script and returns its steps. Comments (lines
// whose first non-whitespace character is '#') and blank lines are
// skipped. Any other error stops parsing immediately and names the
// offending 1-indexed line number.
func Parse(r io.Reader) ([]Step, error) {
	var steps []Step
	scanner := bufio.NewScanner(r)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		directive, rest := splitDirective(line)
		switch directive {
		case "print":
			if rest == "" {
				return nil, fmt.Errorf("line %d: print requires text", lineNum)
			}
			steps = append(steps, Step{Kind: KindPrint, Text: rest})
		case "ask":
			if rest == "" {
				return nil, fmt.Errorf("line %d: ask requires text", lineNum)
			}
			steps = append(steps, Step{Kind: KindAsk, Text: rest})
		case "idle":
			if rest == "" {
				return nil, fmt.Errorf("line %d: idle requires a duration", lineNum)
			}
			d, err := time.ParseDuration(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: invalid idle duration %q: %v", lineNum, rest, err)
			}
			steps = append(steps, Step{Kind: KindIdle, Duration: d})
		case "exit":
			if rest == "" {
				return nil, fmt.Errorf("line %d: exit requires a code", lineNum)
			}
			code, err := strconv.Atoi(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: invalid exit code %q: %v", lineNum, rest, err)
			}
			steps = append(steps, Step{Kind: KindExit, Code: code})
		default:
			return nil, fmt.Errorf("line %d: unknown directive %q", lineNum, directive)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return steps, nil
}

// splitDirective splits a trimmed script line into its leading directive
// word and the (trimmed) remainder of the line.
func splitDirective(line string) (directive, rest string) {
	idx := strings.IndexAny(line, " \t")
	if idx == -1 {
		return line, ""
	}
	return line[:idx], strings.TrimSpace(line[idx+1:])
}

// Run executes steps in order against stdin/stdout, using sleep in place
// of real time for idle steps. It returns the process exit code and any
// execution error (e.g. stdin EOF during an ask).
func Run(steps []Step, stdin io.Reader, stdout io.Writer, sleep func(time.Duration)) (int, error) {
	in := bufio.NewReader(stdin)
	for _, step := range steps {
		switch step.Kind {
		case KindPrint:
			if _, err := fmt.Fprintf(stdout, "%s\n", step.Text); err != nil {
				return 0, err
			}
		case KindAsk:
			if _, err := fmt.Fprint(stdout, step.Text); err != nil {
				return 0, err
			}
			line, err := in.ReadString('\n')
			if err != nil && line == "" {
				return 0, fmt.Errorf("ask: reading stdin: %w", err)
			}
			line = strings.TrimRight(line, "\r\n")
			if _, err := fmt.Fprintf(stdout, "got: %s\n", line); err != nil {
				return 0, err
			}
		case KindIdle:
			sleep(step.Duration)
		case KindExit:
			return step.Code, nil
		}
	}
	// Orchestrator-pinned contract: fall-off-the-end = success (exit 0).
	return 0, nil
}
