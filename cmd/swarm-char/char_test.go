package main

// E9.3 / T-6 — the `swarm-char` characterization harness drives a REAL CLI in a
// PTY and records a versioned fixture {cli, version, scenario, pty_capture,
// hook_payloads[]}. In CI no real CLI exists, so the harness is driven against
// swarm-fake-agent (the Epic 1 scripted stand-in) as the "CLI": the test proves
// the harness produces a SCHEMA-VALID fixture with a non-empty PTY capture that
// contains the agent's real output.
//
// PINNED HARNESS API (package main, so the harness logic is testable out of
// func main):
//
//	type charSpec struct {
//	    CLI, Version, Scenario string
//	    Argv    []string   // argv[0] = program; exec'd directly, never via a shell
//	    Cwd     string
//	    Cols, Rows int
//	    Timeout time.Duration
//	}
//	func characterize(spec charSpec) (adapter.Fixture, error)
//
// The harness reuses internal/shim's PTY spawn pattern (creack/pty
// StartWithSize, see internal/shim/shim.go) + the internal/vt emulator as
// LIBRARIES; it does not add a shim API.
//
// The core E9.3 tests (fixture capture, empty-argv, no-hooks) use the returned
// adapter.Fixture via field/method access only, so they need no adapter import.
// The E9.6 capability test DOES import internal/adapter + refadapter, since it
// exercises deriveCapability against a real adapter and the rendered grid — the
// contract package is implemented now, so the historical RED-cleanliness reason
// to avoid the import no longer applies.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/refadapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// fakeAgentBin is swarm-fake-agent and hookprobeBin is the test-only hook
// poster, both built once in TestMain (the same strategy internal/shim's tests
// use).
var (
	fakeAgentBin string
	hookprobeBin string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "swarm-char-fake")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	build := func(bin, pkg string) bool {
		path := filepath.Join(dir, bin)
		cmd := exec.Command("go", "build", "-o", path, pkg)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build "+bin+":", err)
			return false
		}
		return true
	}
	fakeAgentBin = filepath.Join(dir, "swarm-fake-agent")
	hookprobeBin = filepath.Join(dir, "hookprobe")
	if !build("swarm-fake-agent", "github.com/Nathandela/swarm/cmd/swarm-fake-agent") ||
		!build("hookprobe", "github.com/Nathandela/swarm/cmd/swarm-char/hookprobe") {
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// writeScript writes a fakeagent script to a temp file and returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return p
}

// TestCharacterize_ProducesSchemaValidFixture — the core E9.3 contract: driving
// a CLI to completion yields a schema-valid fixture whose PTY capture holds the
// agent's real output and whose identity fields are stamped from the spec.
func TestCharacterize_ProducesSchemaValidFixture(t *testing.T) {
	const marker = "characterization-marker-42"
	script := writeScript(t, "print "+marker+"\nprint second-line\nexit 0\n")

	fx, err := characterize(charSpec{
		CLI:      "fake-agent",
		Version:  "0.0.1",
		Scenario: "print-then-exit",
		Argv:     []string{fakeAgentBin, script},
		Cwd:      t.TempDir(),
		Cols:     80,
		Rows:     24,
		Timeout:  20 * time.Second,
	})
	if err != nil {
		t.Fatalf("characterize: %v", err)
	}

	// Schema-valid: the fixture passes its own Validate (which checks the schema
	// version and required fields). Accessed via method only — no adapter import.
	if err := fx.Validate(); err != nil {
		t.Fatalf("characterize produced an invalid fixture: %v", err)
	}
	if len(fx.PTYCapture) == 0 {
		t.Fatal("PTYCapture is empty; the harness recorded nothing")
	}
	if !strings.Contains(string(fx.PTYCapture), marker) {
		t.Errorf("PTYCapture does not contain the agent's output %q; got:\n%s", marker, fx.PTYCapture)
	}
	if fx.CLI != "fake-agent" || fx.Version != "0.0.1" || fx.Scenario != "print-then-exit" {
		t.Errorf("identity fields not stamped from spec: cli=%q version=%q scenario=%q", fx.CLI, fx.Version, fx.Scenario)
	}
}

// TestCharacterize_EmptyArgvRejected — the harness has nothing to exec without a
// program and must error rather than spawn a shell or panic.
func TestCharacterize_EmptyArgvRejected(t *testing.T) {
	if _, err := characterize(charSpec{CLI: "x", Version: "1", Scenario: "s", Argv: nil, Cols: 80, Rows: 24, Timeout: 5 * time.Second}); err == nil {
		t.Error("characterize accepted empty Argv")
	}
}

// TestCharacterize_NoHooksIsValid — a CLI that emits no hook callbacks yields a
// fixture with empty HookPayloads that is still schema-valid (hooks are
// optional; the fake agent has none).
func TestCharacterize_NoHooksIsValid(t *testing.T) {
	script := writeScript(t, "print only-output\nexit 0\n")
	fx, err := characterize(charSpec{
		CLI:      "fake-agent",
		Version:  "0.0.1",
		Scenario: "no-hooks",
		Argv:     []string{fakeAgentBin, script},
		Cwd:      t.TempDir(),
		Cols:     80,
		Rows:     24,
		Timeout:  20 * time.Second,
	})
	if err != nil {
		t.Fatalf("characterize: %v", err)
	}
	if len(fx.HookPayloads) != 0 {
		t.Errorf("expected no hook payloads from the fake agent, got %d", len(fx.HookPayloads))
	}
	if err := fx.Validate(); err != nil {
		t.Errorf("fixture with no hooks is invalid: %v", err)
	}
}

// TestCharacterize_ScenarioInputDrivesInteractiveState — the harness feeds a
// scripted, timed stdin sequence so the CLI advances through an INTERACTIVE
// state, not just idle output. The fake agent's `ask` prints a prompt and blocks
// reading a line; the scripted input answers it, and the agent echoes "got:
// <answer>". Seeing that echo proves the harness actually drove the CLI.
func TestCharacterize_ScenarioInputDrivesInteractiveState(t *testing.T) {
	script := writeScript(t, "ask question?\nprint after-answer\nexit 0\n")

	fx, err := characterize(charSpec{
		CLI:      "fake-agent",
		Version:  "0.0.1",
		Scenario: "interactive-ask",
		Argv:     []string{fakeAgentBin, script},
		Cwd:      t.TempDir(),
		Cols:     80,
		Rows:     24,
		Timeout:  20 * time.Second,
		Input:    []ScriptedInput{{Delay: 300 * time.Millisecond, Data: "scripted-reply\n"}},
	})
	if err != nil {
		t.Fatalf("characterize: %v", err)
	}
	if !strings.Contains(string(fx.PTYCapture), "got: scripted-reply") {
		t.Errorf("capture does not show the interactive answer echo; the scripted stdin did not drive the CLI. got:\n%s", fx.PTYCapture)
	}
}

// TestCharacterize_HookSinkRecordsPayloads — the hook-collection sink records the
// JSON payloads a CLI posts during the run into Fixture.HookPayloads. hookprobe
// (a test-only hook stand-in) dials $SWARM_CHAR_HOOK_SINK and posts two payloads;
// the recorded fixture must carry both, with their event names, and stay valid.
func TestCharacterize_HookSinkRecordsPayloads(t *testing.T) {
	// A SHORT socket path: unix sockets are length-capped (~104 bytes on darwin),
	// and t.TempDir()'s test-named path can overflow it.
	dir, err := os.MkdirTemp("", "sc")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "h.sock")

	fx, err := characterize(charSpec{
		CLI:      "hookprobe",
		Version:  "0.0.1",
		Scenario: "posts-hooks",
		Argv:     []string{hookprobeBin},
		Cwd:      t.TempDir(),
		Cols:     80,
		Rows:     24,
		Timeout:  20 * time.Second,
		HookSink: sock,
	})
	if err != nil {
		t.Fatalf("characterize: %v", err)
	}
	if len(fx.HookPayloads) != 2 {
		t.Fatalf("recorded %d hook payloads, want 2: %+v", len(fx.HookPayloads), fx.HookPayloads)
	}
	events := map[string]bool{}
	for _, hp := range fx.HookPayloads {
		events[hp.Event] = true
		if !json.Valid(hp.Raw) {
			t.Errorf("recorded hook payload has invalid raw JSON: %s", hp.Raw)
		}
		if hp.ReceivedAtMs == 0 {
			t.Errorf("recorded hook payload has no arrival time: %+v", hp)
		}
	}
	if !events["SessionStart"] || !events["Stop"] {
		t.Errorf("recorded events %v, want SessionStart and Stop", events)
	}
	if err := fx.Validate(); err != nil {
		t.Errorf("fixture with recorded hooks is invalid: %v", err)
	}
}

// TestCharacterize_NoHookSinkStillValid — with no sink configured the child
// cannot post hooks and the fixture simply carries none (still schema-valid).
func TestCharacterize_NoHookSinkStillValid(t *testing.T) {
	fx, err := characterize(charSpec{
		CLI:      "hookprobe",
		Version:  "0.0.1",
		Scenario: "no-sink",
		Argv:     []string{hookprobeBin},
		Cwd:      t.TempDir(),
		Cols:     80,
		Rows:     24,
		Timeout:  20 * time.Second,
	})
	if err != nil {
		t.Fatalf("characterize: %v", err)
	}
	if len(fx.HookPayloads) != 0 {
		t.Errorf("expected no hook payloads without a sink, got %d", len(fx.HookPayloads))
	}
}

// gridReader wraps the reference adapter but reads the conversation id ONLY from
// the rendered grid, ignoring the tail. It proves deriveCapability feeds the REAL
// grid (rendered from the capture), never nil: with a nil grid it finds nothing.
type gridReader struct{ adapter.Adapter }

func (gridReader) ExtractConversationID(grid *vt.Snap, _ []byte) (string, bool) {
	if grid == nil {
		return "", false
	}
	const marker = "conv-id="
	for _, line := range grid.Lines {
		var b strings.Builder
		for _, r := range line.Runs {
			b.WriteString(r.Text)
		}
		text := b.String()
		if i := strings.Index(text, marker); i >= 0 {
			id := strings.TrimSpace(text[i+len(marker):])
			if j := strings.IndexAny(id, " \t"); j >= 0 {
				id = id[:j]
			}
			if id != "" {
				return id, true
			}
		}
	}
	return "", false
}

// TestDeriveCapability_FromActualAdapterAndRealGrid — the capability entry is
// derived from the ACTUAL adapter passed in (not a hardcoded reference) and fed
// the REAL grid rendered from the capture. A grid-reading adapter reports
// ConversationID only because deriveCapability rendered the "conv-id=..." line
// onto a non-nil grid.
func TestDeriveCapability_FromActualAdapterAndRealGrid(t *testing.T) {
	script := writeScript(t, "print conv-id=grid-session-777\nexit 0\n")
	fx, err := characterize(charSpec{
		CLI:      "fake-agent",
		Version:  "0.0.1",
		Scenario: "prints-conv-id",
		Argv:     []string{fakeAgentBin, script},
		Cwd:      t.TempDir(),
		Cols:     80,
		Rows:     24,
		Timeout:  20 * time.Second,
	})
	if err != nil {
		t.Fatalf("characterize: %v", err)
	}

	a := gridReader{refadapter.New(fx)}
	entry, err := deriveCapability(a, fx)
	if err != nil {
		t.Fatalf("deriveCapability: %v", err)
	}
	if !entry.ConversationID {
		t.Error("ConversationID=false: deriveCapability did not feed the real grid to the actual adapter")
	}
	if entry.CLI != fx.CLI || entry.Version != fx.Version {
		t.Errorf("capability identity %q/%q != fixture %q/%q", entry.CLI, entry.Version, fx.CLI, fx.Version)
	}
}
