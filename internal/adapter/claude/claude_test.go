// Package claude holds the Claude Code adapter (Epic 11, E11.2/E11.6/E11.8). This
// FAILING-FIRST test suite (TDD, GG-5) freezes the adapter's contract before any
// implementation exists. It COMPILES only once the package provides the pinned
// constructor
//
//	func New() adapter.Adapter
//
// so the RED reason for every test here is "undefined: claude.New" until the
// adapter lands (the pinned entrypoint the registry + launch form call).
//
// COST NOTE (orchestrator brief): NOTHING in this file runs the real `claude`
// CLI. The version strings are the free, real `claude --version` output; every
// other behavior is asserted against the hand-authored fixture testdata/claude.json,
// which encodes Claude Code's DOCUMENTED hook format (settings-configured hooks:
// PreToolUse/PostToolUse/Notification/Stop/SubagentStop/UserPromptSubmit, each a
// JSON payload {session_id, transcript_path, hook_event_name, cwd, ...}) and a
// session UUID in the capture. The exact terminal rendering of the session id is
// marked VERIFY: a flagged live-characterization smoke (see the codex/e2e handoff)
// confirms the fixture against a real capture; this suite treats the fixture as
// the contract.
package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
)

// realVersionBanner is the EXACT output of `claude --version` (captured live —
// free, non-billable). ParseVersion must read it verbatim.
const realVersionBanner = "2.1.212 (Claude Code)"

// fixtureConversationID is the session UUID embedded in testdata/claude.json's
// capture. ExtractConversationID must recover exactly this id. VERIFY: the real
// terminal surface of the id is confirmed by the flagged live smoke.
const fixtureConversationID = "3f2a1c9e-7b4d-4e2a-9f10-abc123def456"

// claudeHookEvents are the six Claude Code hook events the adapter must declare as
// SignalSources (T-2). Configured per-invocation via settings injection, each runs
// `swarm hook <event>` and posts the documented JSON payload.
var claudeHookEvents = []string{
	"PreToolUse", "PostToolUse", "Notification", "Stop", "SubagentStop", "UserPromptSubmit",
}

// newAdapter builds the adapter under test. The single seam every test funnels
// through, so the RED "undefined: New" is reported once.
func newAdapter() adapter.Adapter { return New() }

// loadFixture loads the committed Claude characterization fixture (E11.1: fixtures
// committed BEFORE adapter code).
func loadFixture(t *testing.T) adapter.Fixture {
	t.Helper()
	fx, err := fixtureio.LoadFixture("testdata/claude.json")
	if err != nil {
		t.Fatalf("load claude fixture: %v", err)
	}
	return fx
}

// TestParseVersion_RealBanner — E11.2: ParseVersion reads the EXACT real
// `claude --version` banner, and rejects a garbage banner.
func TestParseVersion_RealBanner(t *testing.T) {
	a := newAdapter()
	v, ok := a.ParseVersion(realVersionBanner)
	if !ok || v != "2.1.212" {
		t.Fatalf("ParseVersion(%q) = (%q, %v); want (\"2.1.212\", true)", realVersionBanner, v, ok)
	}
	if v2, ok2 := a.ParseVersion("not a version"); ok2 || v2 != "" {
		t.Errorf("ParseVersion(garbage) = (%q, %v); want (\"\", false)", v2, ok2)
	}
}

// TestBinaryAndVersionArgs — E11.2: the detection descriptors match the real CLI
// (`claude --version`). These are the pure inputs the CORE adapter.Detect feeds a
// HostProber; the adapter itself opens nothing.
func TestBinaryAndVersionArgs(t *testing.T) {
	a := newAdapter()
	if a.Binary() != "claude" {
		t.Errorf("Binary() = %q; want \"claude\"", a.Binary())
	}
	if got := a.VersionArgs(); len(got) != 1 || got[0] != "--version" {
		t.Errorf("VersionArgs() = %v; want [\"--version\"]", got)
	}
}

// stubProber is an adapter.HostProber that returns canned LookPath/Run results, so
// detection is exercised without executing the real CLI (COST: no billable run).
type stubProber struct {
	path    string
	lookErr error
	out     string
	runErr  error
}

func (s stubProber) LookPath(string) (string, error) { return s.path, s.lookErr }
func (s stubProber) Run(string, []string) (string, error) {
	return s.out, s.runErr
}

// TestDetect_VersionGreying_L2 — E11.6 / L-2: detection through the CORE
// adapter.Detect yields InRange=true for the real supported version and
// InRange=false for an out-of-supported-range version. InRange is exactly the
// signal the launch form greys an agent on (tui.AgentInfo.InRange → usable()), so
// this proves L-2 greying end-to-end at the detection boundary.
func TestDetect_VersionGreying_L2(t *testing.T) {
	a := newAdapter()

	inRange := adapter.Detect(a, stubProber{path: "/usr/local/bin/claude", out: realVersionBanner})
	if !inRange.Found || inRange.Version != "2.1.212" || !inRange.InRange {
		t.Fatalf("Detect(real version) = %+v; want Found, Version 2.1.212, InRange true", inRange)
	}

	// A clearly out-of-supported-range (too old) version must detect as found but
	// NOT in range — the greyed-with-upgrade-hint state (L-2).
	old := adapter.Detect(a, stubProber{path: "/usr/local/bin/claude", out: "1.0.0 (Claude Code)"})
	if !old.Found {
		t.Fatalf("Detect(old version) not Found; want Found (binary present, version out of range)")
	}
	if old.InRange {
		t.Errorf("Detect(old version) InRange=true; an out-of-supported-range CLI must be greyed (L-2)")
	}

	// Not installed at all → not found (picker offers it greyed as install-hint).
	missing := adapter.Detect(a, stubProber{lookErr: errNotFound})
	if missing.Found {
		t.Errorf("Detect(missing) Found=true; want not found")
	}
}

// errNotFound stands in for exec.LookPath's not-found error.
var errNotFound = &lookErr{}

type lookErr struct{}

func (*lookErr) Error() string { return "executable file not found in $PATH" }

// TestCommand_ComposesArgvWithHookSettingsAndPrompt — E11.2: Command composes the
// launch argv as `claude` + a per-invocation settings injection that installs the
// hooks (T-2: per-invocation, never a global-config mutation) + the initial prompt.
// The settings value MUST be inline JSON (the adapter owns no fds, so it cannot
// write a settings file) that wires the hook events to `swarm hook <event>`.
func TestCommand_ComposesArgvWithHookSettingsAndPrompt(t *testing.T) {
	a := newAdapter()
	argv, err := a.Command(adapter.LaunchSpec{
		Cwd:           "/work/proj",
		Options:       map[string]string{},
		InitialPrompt: "refactor the parser",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if len(argv) == 0 || argv[0] != "claude" {
		t.Fatalf("Command argv[0] = %q; want \"claude\" (argv = %v)", first(argv), argv)
	}

	settings, ok := settingsValue(argv)
	if !ok {
		t.Fatalf("Command argv carries no --settings hook injection (T-2 per-invocation install); argv = %v", argv)
	}
	if !json.Valid([]byte(settings)) {
		t.Fatalf("--settings value is not inline JSON (the adapter owns no fds and cannot write a settings file): %q", settings)
	}
	// The injected settings must wire the hooks to `swarm hook`, per-invocation.
	for _, want := range []string{"swarm", "hook", "Notification", "Stop"} {
		if !strings.Contains(settings, want) {
			t.Errorf("--settings JSON does not reference %q; it must install the swarm hooks per-invocation:\n%s", want, settings)
		}
	}
	// The initial prompt must reach the argv so the session starts with it.
	if !containsArg(argv, "refactor the parser") {
		t.Errorf("Command argv omits the initial prompt; argv = %v", argv)
	}
}

// TestSignalSources_DeclaresSixHooksWithStatusMapping — E11.2 / T-2: the adapter
// declares each of Claude Code's six hook events as a hook SignalSource carrying
// the documented status mapping (the engine payload keys "turn"/"interaction"):
//   - UserPromptSubmit, PreToolUse  → turn active
//   - Stop                          → turn idle
//   - Notification                  → turn idle, interaction permission|prompt
//
// and at least one heuristic fallback source (T-3).
func TestSignalSources_DeclaresSixHooksWithStatusMapping(t *testing.T) {
	a := newAdapter()
	byEvent := map[string]adapter.SignalSource{}
	heuristics := 0
	for _, s := range a.SignalSources() {
		switch s.Kind {
		case "hook":
			ev := s.Descriptor["event"]
			if ev == "" {
				t.Errorf("hook SignalSource has no \"event\" in its descriptor: %+v", s)
				continue
			}
			byEvent[ev] = s
		case "heuristic":
			heuristics++
		case "event":
			// allowed by the contract, but Claude uses hooks
		default:
			t.Errorf("SignalSource has invalid Kind %q", s.Kind)
		}
	}

	for _, ev := range claudeHookEvents {
		if _, ok := byEvent[ev]; !ok {
			t.Errorf("SignalSources missing the Claude hook event %q (all six must be declared)", ev)
		}
	}
	if heuristics == 0 {
		t.Errorf("SignalSources declares no heuristic fallback source (T-3)")
	}

	assertTurn := func(ev, wantTurn string) {
		s, ok := byEvent[ev]
		if !ok {
			return // already reported missing above
		}
		if got := s.Descriptor["turn"]; got != wantTurn {
			t.Errorf("%s hook maps turn=%q; want %q", ev, got, wantTurn)
		}
	}
	assertTurn("UserPromptSubmit", string(status.TurnActive))
	assertTurn("PreToolUse", string(status.TurnActive))
	assertTurn("Stop", string(status.TurnIdle))
	assertTurn("Notification", string(status.TurnIdle))

	// Notification is the permission/idle-prompt signal: it must map interaction to
	// a real waiting-on-user value.
	if n, ok := byEvent["Notification"]; ok {
		inter := n.Descriptor["interaction"]
		if inter != string(status.InteractionPermission) && inter != string(status.InteractionPrompt) {
			t.Errorf("Notification hook maps interaction=%q; want permission or prompt", inter)
		}
	}
}

// TestResume_CarriesConversationID — E11.2 / R-2: Resume composes `claude --resume
// <id>`; an empty id resumes nothing.
func TestResume_CarriesConversationID(t *testing.T) {
	a := newAdapter()

	none, err := a.Resume(adapter.ResumeSpec{Cwd: "/work"})
	if err != nil {
		t.Fatalf("Resume(no id): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("Resume with no ConversationID returned argv %v; want empty", none)
	}

	argv, err := a.Resume(adapter.ResumeSpec{Cwd: "/work", ConversationID: fixtureConversationID})
	if err != nil {
		t.Fatalf("Resume(id): %v", err)
	}
	if len(argv) == 0 || argv[0] != "claude" {
		t.Fatalf("Resume argv[0] = %q; want \"claude\" (argv = %v)", first(argv), argv)
	}
	if !containsArg(argv, "--resume") {
		t.Errorf("Resume argv missing --resume flag; argv = %v", argv)
	}
	if !containsArg(argv, fixtureConversationID) {
		t.Errorf("Resume argv missing the conversation id %q; argv = %v", fixtureConversationID, argv)
	}
}

// TestExtractConversationID_FromFixture — E11.2: the adapter recovers the session
// id its own characterization fixture recorded, and returns nothing on unrelated
// input. The id is read from the rendered grid + raw capture, exactly as the
// engine drives extraction at runtime.
func TestExtractConversationID_FromFixture(t *testing.T) {
	a := newAdapter()
	fx := loadFixture(t)
	grid := renderGrid(t, fx.PTYCapture)

	id, ok := a.ExtractConversationID(grid, fx.PTYCapture)
	if !ok || id != fixtureConversationID {
		t.Fatalf("ExtractConversationID(fixture) = (%q, %v); want (%q, true)", id, ok, fixtureConversationID)
	}

	if gid, gok := a.ExtractConversationID(nil, []byte("no id here at all")); gok || gid != "" {
		t.Errorf("ExtractConversationID(garbage) = (%q, %v); want (\"\", false)", gid, gok)
	}
}

// TestConformance — E11.2: the adapter passes the frozen T-1 conformance suite
// (CheckConformance zero violations + the *testing.T wrapper with its goroutine /
// race checks).
func TestConformance(t *testing.T) {
	a := newAdapter()
	if errs := adapter.CheckConformance(a); len(errs) != 0 {
		t.Fatalf("claude adapter is not conformant: %v", errs)
	}
	adapter.Conformance(t, a)
}

// TestCapability_FullSurface — E11.1 / E9.6: the adapter's capability entry, derived
// against the REAL grid rendered from the fixture, records hooks + resume +
// conversation-id and stamps the fixture identity.
func TestCapability_FullSurface(t *testing.T) {
	a := newAdapter()
	fx := loadFixture(t)
	grid := renderGrid(t, fx.PTYCapture)

	entry := adapter.Capability(a, fx, grid)
	if !entry.Hooks {
		t.Errorf("capability Hooks=false; Claude Code drives status via hooks (T-2)")
	}
	if !entry.Resume {
		t.Errorf("capability Resume=false; Claude Code supports --resume")
	}
	if !entry.ConversationID {
		t.Errorf("capability ConversationID=false; the fixture capture carries an extractable session id")
	}
	if entry.CLI != fx.CLI || entry.Version != fx.Version {
		t.Errorf("capability identity %q/%q != fixture %q/%q", entry.CLI, entry.Version, fx.CLI, fx.Version)
	}
}

// TestGridHeuristicFallback_ClassifiesIdlePrompt — E11.6 / T-3: with NO fresh typed
// signal, the generic grid heuristic (run through the real engine's OnOutput tap)
// classifies Claude's idle-at-prompt fixture grid as turn=idle. This is the
// per-CLI fallback behavior: the CLI's real rendered grid, fed to the engine,
// yields the right turn. StalenessThreshold=0 means no typed signal suppresses the
// read, so the heuristic always applies.
func TestGridHeuristicFallback_ClassifiesIdlePrompt(t *testing.T) {
	a := newAdapter()
	fx := loadFixture(t)
	grid := renderGrid(t, fx.PTYCapture)

	var got status.Status
	var emitted bool
	eng := engine.New(engine.Config{
		StalenessThreshold: 0, // no typed-signal freshness window: heuristic always applies
		Emit: func(_ string, s status.Status) {
			got = s
			emitted = true
		},
	})
	eng.RegisterSession("s1", "tok", 0, a.SignalSources())
	eng.OnOutput("s1", grid)

	if !emitted {
		t.Fatalf("OnOutput on the idle-prompt grid emitted no status change; the fallback did not classify it")
	}
	if got.Turn != status.TurnIdle {
		t.Errorf("grid heuristic classified turn=%q on Claude's idle-prompt fixture; want idle", got.Turn)
	}
}

// TestImportBoundary_T5 — E11.8 / T-5: the claude package's transitive NON-TEST
// dependencies within this module are ONLY the adapter contract and internal/vt.
// Any dependency on daemon/shim/wire/protocol/persist/status/engine/TUI/cmd would
// force core edits to add an adapter — the exact coupling T-5 forbids. (This test's
// own engine/status imports are test-only and excluded from `go list -deps`.)
func TestImportBoundary_T5(t *testing.T) {
	allowed := map[string]bool{
		"github.com/Nathandela/swarm/internal/adapter/claude": true,
		"github.com/Nathandela/swarm/internal/adapter":        true,
		"github.com/Nathandela/swarm/internal/vt":             true,
	}
	for _, dep := range moduleInternalDeps(t, "github.com/Nathandela/swarm/internal/adapter/claude") {
		if !allowed[dep] {
			t.Errorf("claude adapter imports forbidden package %q (T-5: adapters depend on the contract + vt only)", dep)
		}
	}
}

// TestStateless_NoIOInSource — E9.2 (applied to the claude adapter, per E11.8's
// reuse of the E9.5 check): no non-test source file may name an fd/disk/socket/exec
// primitive. Core owns all lifecycle; the adapter owns no fds.
func TestStateless_NoIOInSource(t *testing.T) {
	scanBannedIO(t, ".")
}
