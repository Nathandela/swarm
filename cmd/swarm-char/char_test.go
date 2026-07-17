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
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
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
		Input:    []ScriptedInput{{Delay: 100 * time.Millisecond, Data: "go\n"}}, // unblock hookprobe's stdin read
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
		Input:    []ScriptedInput{{Delay: 100 * time.Millisecond, Data: "go\n"}}, // unblock hookprobe's stdin read
	})
	if err != nil {
		t.Fatalf("characterize: %v", err)
	}
	if len(fx.HookPayloads) != 0 {
		t.Errorf("expected no hook payloads without a sink, got %d", len(fx.HookPayloads))
	}
}

// TestBuildGrid_UsesCharacterizationGeometry — the capability grid is rendered
// at the ACTUAL characterization geometry, not a fixed 80x24. A capture rendered
// at each size must yield a Snap of exactly that size (an adapter reading the
// grid must see the same wrapping/cursor the real CLI drew).
func TestBuildGrid_UsesCharacterizationGeometry(t *testing.T) {
	capture := []byte("hello\r\nconv-id=xyz\r\n> \r\n")
	for _, tc := range []struct{ cols, rows int }{{80, 24}, {100, 40}, {120, 50}} {
		grid, err := buildGrid(capture, tc.cols, tc.rows)
		if err != nil {
			t.Fatalf("buildGrid(%dx%d): %v", tc.cols, tc.rows, err)
		}
		if grid.Cols != tc.cols || grid.Rows != tc.rows {
			t.Errorf("grid geometry = %dx%d, want %dx%d (capability grid must match the run geometry)", grid.Cols, grid.Rows, tc.cols, tc.rows)
		}
	}
}

// TestCharacterize_HookSinkDoesNotHangOnOpenClient — a hook client that connects
// and holds its connection open (posting nothing, never closing) must NOT wedge
// the harness. collect() bounds its drain wait, so characterize returns within
// the run + hookDrainGrace rather than blocking forever.
func TestCharacterize_HookSinkDoesNotHangOnOpenClient(t *testing.T) {
	dir, err := os.MkdirTemp("", "sc")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "h.sock")

	// Misbehaving client: once the sink is up, connect and hold the connection
	// open until the test tears down.
	clientStop := make(chan struct{})
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		for i := 0; i < 1000; i++ {
			if c, err := net.Dial("unix", sock); err == nil {
				<-clientStop
				_ = c.Close()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	defer func() { close(clientStop); <-clientDone }()

	// The CLI idles long enough for the client to be accepted, then exits; collect
	// then faces a still-open client connection.
	script := writeScript(t, "print starting\nidle 500ms\nexit 0\n")

	errCh := make(chan error, 1)
	go func() {
		_, err := characterize(charSpec{
			CLI: "fake-agent", Version: "0.0.1", Scenario: "open-client",
			Argv: []string{fakeAgentBin, script}, Cwd: t.TempDir(),
			Cols: 80, Rows: 24, Timeout: 10 * time.Second, HookSink: sock,
		})
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("characterize: %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("characterize hung on an open hook client (collect() did not bound its wait)")
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
	entry, err := deriveCapability(a, fx, 80, 24)
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

// noHookAdapter wraps the reference adapter but declares NO hook signal source.
// Registered under a test-only -adapter name, it makes capability selection
// observable: refadapter yields Hooks=true, this yields Hooks=false.
type noHookAdapter struct{ adapter.Adapter }

func (noHookAdapter) SignalSources() []adapter.SignalSource {
	return []adapter.SignalSource{{Kind: "heuristic", Descriptor: map[string]string{"grid": "spinner"}}}
}

// TestRun_CLIWiresInputHooksAndSelectedAdapter — the R1 end-to-end: the swarm-char
// CLI (run) wires -scenario, -input, -hook-sink, -geometry, and -adapter. Driving
// hookprobe: -input answers its interactive read, -hook-sink records its posts,
// and -adapter selects a distinctive adapter (no hooks) so the capability entry
// is provably from the SELECTED adapter, not a hardcoded refadapter.
func TestRun_CLIWiresInputHooksAndSelectedAdapter(t *testing.T) {
	// Register a distinctive adapter to prove -adapter selection is honored.
	adapterRegistry["testcap"] = func(fx adapter.Fixture) adapter.Adapter {
		return noHookAdapter{refadapter.New(fx)}
	}
	defer delete(adapterRegistry, "testcap")

	dir, err := os.MkdirTemp("", "sc")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "h.sock")
	fixturePath := filepath.Join(t.TempDir(), "fx.json")
	inputPath := writeScript(t, `200ms cli-driven-reply\n`) // <delay> <data>, \n escaped

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-cli", "hookprobe-cli",
		"-version", "9.9.9",
		"-scenario", "cli-e2e",
		"-geometry", "90x30",
		"-adapter", "testcap",
		"-input", inputPath,
		"-hook-sink", sock,
		"-out", fixturePath,
		"-cwd", t.TempDir(),
		"-timeout", "20s",
		"--", hookprobeBin,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit=%d, stderr:\n%s", code, stderr.String())
	}

	// Fixture written to -out: scenario stamped, hooks recorded, -input drove it.
	fx, err := fixtureio.LoadFixture(fixturePath)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if fx.Scenario != "cli-e2e" {
		t.Errorf("Scenario = %q, want cli-e2e", fx.Scenario)
	}
	if len(fx.HookPayloads) == 0 {
		t.Error("HookPayloads empty; -hook-sink did not receive payloads")
	}
	if !strings.Contains(string(fx.PTYCapture), "got: cli-driven-reply") {
		t.Errorf("capture missing the interactive echo; -input did not drive the CLI:\n%s", fx.PTYCapture)
	}

	// Capability emitted to stderr, from the SELECTED adapter (testcap => no hooks),
	// not the default refadapter (which declares a hook).
	var entry adapter.CapabilityEntry
	if err := json.Unmarshal(stderr.Bytes(), &entry); err != nil {
		t.Fatalf("parse capability JSON from stderr (%q): %v", stderr.String(), err)
	}
	if entry.Hooks {
		t.Error("capability Hooks=true — the default refadapter was used, not the selected -adapter testcap")
	}
	if entry.CLI != fx.CLI || entry.Version != fx.Version {
		t.Errorf("capability identity %q/%q != fixture %q/%q", entry.CLI, entry.Version, fx.CLI, fx.Version)
	}
}

// TestResolveGeometry and TestParseScriptedInput cover the -geometry / -input
// parsers directly (unit-level, no PTY).
func TestResolveGeometry(t *testing.T) {
	if c, r, err := resolveGeometry("100x40", 80, 24); err != nil || c != 100 || r != 40 {
		t.Errorf("resolveGeometry(100x40) = %d,%d,%v; want 100,40,nil", c, r, err)
	}
	if c, r, err := resolveGeometry("", 80, 24); err != nil || c != 80 || r != 24 {
		t.Errorf("resolveGeometry(\"\") = %d,%d,%v; want 80,24,nil (fallback)", c, r, err)
	}
	for _, bad := range []string{"80", "80xzz", "0x24", "80x0", "axb"} {
		if _, _, err := resolveGeometry(bad, 80, 24); err == nil {
			t.Errorf("resolveGeometry(%q) accepted a malformed geometry", bad)
		}
	}
}

func TestParseScriptedInput(t *testing.T) {
	in, err := parseScriptedInput("# comment\n\n300ms hello\\n\n1s /help\\n")
	if err != nil {
		t.Fatalf("parseScriptedInput: %v", err)
	}
	if len(in) != 2 {
		t.Fatalf("parsed %d inputs, want 2", len(in))
	}
	if in[0].Delay != 300*time.Millisecond || in[0].Data != "hello\n" {
		t.Errorf("input[0] = %+v, want {300ms, \"hello\\n\"}", in[0])
	}
	if in[1].Delay != time.Second || in[1].Data != "/help\n" {
		t.Errorf("input[1] = %+v, want {1s, \"/help\\n\"}", in[1])
	}
	if _, err := parseScriptedInput("nodelaydata"); err == nil {
		t.Error("parseScriptedInput accepted a line without a delay")
	}
	if _, err := parseScriptedInput("notaduration data"); err == nil {
		t.Error("parseScriptedInput accepted an invalid delay")
	}
}
