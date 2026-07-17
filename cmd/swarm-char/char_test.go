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
// DELIBERATE: this test never imports internal/adapter. It uses the returned
// adapter.Fixture via field/method access only (fx.Validate(), fx.PTYCapture,
// ...), which Go permits without importing the type's package — so the pinned
// RED command `go test ./internal/adapter/ ./cmd/swarm-char/` fails with
// UNDEFINED SYMBOLS ONLY (characterize/charSpec), not an import error against
// the not-yet-existent contract package.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAgentBin is swarm-fake-agent, built once in TestMain (the same strategy
// internal/shim's tests use).
var fakeAgentBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "swarm-char-fake")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	fakeAgentBin = filepath.Join(dir, "swarm-fake-agent")
	build := exec.Command("go", "build", "-o", fakeAgentBin, "github.com/Nathandela/swarm/cmd/swarm-fake-agent")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build fake agent:", err)
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
