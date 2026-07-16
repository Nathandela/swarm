// Package fakeagent implements the fakeagent engine (E1.9): a scripted
// print/ask/idle/exit interpreter that stands in for a real agent CLI in
// every later epic's tests. The script format and the Parse/Run signatures
// are a frozen data contract. These are the tests only: no implementation
// exists yet, so this package currently fails to build.
package fakeagent

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- Parse: happy path ---

func TestParse_AllDirectivesWithCommentsAndBlanks(t *testing.T) {
	script := `# leading comment

print hello world

# a comment between steps
ask what is your name?
idle 200ms
exit 3
`
	steps, err := Parse(strings.NewReader(script))
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}

	want := []Step{
		{Kind: KindPrint, Text: "hello world"},
		{Kind: KindAsk, Text: "what is your name?"},
		{Kind: KindIdle, Duration: 200 * time.Millisecond},
		{Kind: KindExit, Code: 3},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("Parse() = %+v, want %+v", steps, want)
	}
}

// --- Parse: error path ---

func TestParse_Errors(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		wantLine int
	}{
		{
			name:     "unknown directive",
			script:   "foo bar\n",
			wantLine: 1,
		},
		{
			name:     "unknown directive on a later line after comments and blanks",
			script:   "# c\n\nprint ok\nbogus\n",
			wantLine: 4,
		},
		{
			name:     "malformed idle duration",
			script:   "idle notaduration\n",
			wantLine: 1,
		},
		{
			name:     "malformed exit code",
			script:   "exit notanumber\n",
			wantLine: 1,
		},
		{
			name:     "missing args on print",
			script:   "print\n",
			wantLine: 1,
		},
		{
			name:     "missing args on ask",
			script:   "ask\n",
			wantLine: 1,
		},
		{
			name:     "missing args on idle",
			script:   "idle\n",
			wantLine: 1,
		},
		{
			name:     "missing args on exit",
			script:   "exit\n",
			wantLine: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps, err := Parse(strings.NewReader(tt.script))
			if err == nil {
				t.Fatalf("Parse(%q) error = nil, want error", tt.script)
			}
			if len(steps) != 0 {
				t.Errorf("Parse(%q) steps = %+v, want none on error", tt.script, steps)
			}
			wantLineStr := strconv.Itoa(tt.wantLine)
			if !strings.Contains(err.Error(), wantLineStr) {
				t.Errorf("Parse(%q) error = %q, want it to mention line %d", tt.script, err.Error(), tt.wantLine)
			}
		})
	}
}

// --- Run: one directive at a time ---

func TestRun_PrintOutputsExactly(t *testing.T) {
	steps := []Step{{Kind: KindPrint, Text: "hello"}}
	var stdout bytes.Buffer

	code, err := Run(steps, strings.NewReader(""), &stdout, func(time.Duration) {})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if code != 0 {
		t.Errorf("Run() exit code = %d, want 0", code)
	}
	if stdout.String() != "hello\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "hello\n")
	}
}

func TestRun_AskEchoesPromptThenGotLine(t *testing.T) {
	steps := []Step{{Kind: KindAsk, Text: "name?"}}
	stdin := strings.NewReader("Alice\n")
	var stdout bytes.Buffer

	code, err := Run(steps, stdin, &stdout, func(time.Duration) {})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if code != 0 {
		t.Errorf("Run() exit code = %d, want 0", code)
	}
	want := "name?got: Alice\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRun_AskEOFOnStdinReturnsErrorNotHang(t *testing.T) {
	steps := []Step{{Kind: KindAsk, Text: "name?"}}
	var stdout bytes.Buffer

	_, err := Run(steps, strings.NewReader(""), &stdout, func(time.Duration) {})
	if err == nil {
		t.Fatalf("Run() error = nil, want error on stdin EOF during ask")
	}
}

func TestRun_IdleCallsInjectedSleepWithExactDuration(t *testing.T) {
	steps := []Step{
		{Kind: KindIdle, Duration: 200 * time.Millisecond},
		{Kind: KindIdle, Duration: 2 * time.Second},
	}
	var calls []time.Duration
	sleep := func(d time.Duration) { calls = append(calls, d) }
	var stdout bytes.Buffer

	start := time.Now()
	code, err := Run(steps, strings.NewReader(""), &stdout, sleep)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if code != 0 {
		t.Errorf("Run() exit code = %d, want 0", code)
	}
	want := []time.Duration{200 * time.Millisecond, 2 * time.Second}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("sleep calls = %v, want %v", calls, want)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("Run() took %v with an injected no-op sleep, want no real sleeping", elapsed)
	}
}

func TestRun_ExitReturnsCodeAndStopsExecution(t *testing.T) {
	steps := []Step{
		{Kind: KindExit, Code: 7},
		{Kind: KindPrint, Text: "should not run"},
	}
	var stdout bytes.Buffer

	code, err := Run(steps, strings.NewReader(""), &stdout, func(time.Duration) {})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if code != 7 {
		t.Errorf("Run() exit code = %d, want 7", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty: steps after exit must never run", stdout.String())
	}
}

// --- E1.9 smoke test: all four directives, end-to-end, through Parse+Run ---

func TestSmoke_AllFourDirectivesEndToEnd(t *testing.T) {
	script := "print starting\n" +
		"ask name?\n" +
		"idle 50ms\n" +
		"print done\n" +
		"exit 5\n"

	steps, err := Parse(strings.NewReader(script))
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}

	var stdin bytes.Buffer
	stdin.WriteString("Bob\n")
	var stdout bytes.Buffer
	var slept []time.Duration
	sleep := func(d time.Duration) { slept = append(slept, d) }

	code, err := Run(steps, &stdin, &stdout, sleep)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if code != 5 {
		t.Errorf("Run() exit code = %d, want 5", code)
	}

	wantStdout := "starting\nname?got: Bob\ndone\n"
	if stdout.String() != wantStdout {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantStdout)
	}

	wantSlept := []time.Duration{50 * time.Millisecond}
	if !reflect.DeepEqual(slept, wantSlept) {
		t.Errorf("sleep calls = %v, want %v", slept, wantSlept)
	}
}

// Orchestrator-pinned contract: fall-off-the-end = success (exit 0).
func TestRun_NoExitDirectiveFallsOffTheEndReturnsZero(t *testing.T) {
	steps := []Step{{Kind: KindPrint, Text: "hi"}}
	var stdout bytes.Buffer

	code, err := Run(steps, strings.NewReader(""), &stdout, func(time.Duration) {})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if code != 0 {
		t.Errorf("Run() exit code = %d, want 0", code)
	}
}
