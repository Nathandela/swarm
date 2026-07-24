// Package opencode holds the opencode adapter (Phase E, R-E1..R-E8). This
// FAILING-FIRST test suite (TDD, GG-5) freezes the adapter's contract before any
// implementation exists. It COMPILES only once the package provides the pinned
// constructor
//
//	func New() adapter.Adapter
//
// so the RED reason for every test here is "undefined: opencode.New" until the
// adapter lands (the pinned entrypoint the registry + launch form call).
//
// COST NOTE: nothing here runs the real `opencode` CLI. The version banner is
// the free, real `opencode --version` output (bare "1.17.9", no CLI-name
// prefix); everything else is asserted against the committed characterization
// fixture testdata/opencode.json — a REAL recorded capture (Phase B, not
// hand-authored) whose marker table and byte offsets are frozen in
// docs/verification/cli-duo-adapters-evidence.md.
package opencode

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
)

// realVersionBanner is the EXACT output of `opencode --version` (captured live —
// free, non-billable): a bare dotted-numeric line, unlike claude/codex which
// prefix or suffix the version with the CLI name.
const realVersionBanner = "1.17.9\n"

// fixtureConversationID is the session id embedded in testdata/opencode.json's
// capture (Phase B memo: raw offset 76243 / grid char offset 798).
const fixtureConversationID = "ses_08b642915ffeYL3T6ea1DnJZDd"

// newAdapter builds the adapter under test. The single seam every test funnels
// through, so the RED "undefined: New" is reported once.
func newAdapter() adapter.Adapter { return New() }

// loadFixture loads the committed opencode characterization fixture (Phase B:
// fixtures committed BEFORE adapter code).
func loadFixture(t *testing.T) adapter.Fixture {
	t.Helper()
	fx, err := fixtureio.LoadFixture("testdata/opencode.json")
	if err != nil {
		t.Fatalf("load opencode fixture: %v", err)
	}
	return fx
}

// TestParseVersion_RealBanner — R-E1: ParseVersion reads the EXACT real, bare
// `opencode --version` banner, and rejects garbage.
func TestParseVersion_RealBanner(t *testing.T) {
	a := newAdapter()
	v, ok := a.ParseVersion(realVersionBanner)
	if !ok || v != "1.17.9" {
		t.Fatalf("ParseVersion(%q) = (%q, %v); want (\"1.17.9\", true)", realVersionBanner, v, ok)
	}
	if v2, ok2 := a.ParseVersion("garbage line"); ok2 || v2 != "" {
		t.Errorf("ParseVersion(garbage) = (%q, %v); want (\"\", false)", v2, ok2)
	}
}

// TestBinaryAndVersionArgs — R-E1: the detection descriptors match the real CLI.
func TestBinaryAndVersionArgs(t *testing.T) {
	a := newAdapter()
	if a.Binary() != "opencode" {
		t.Errorf("Binary() = %q; want \"opencode\"", a.Binary())
	}
	if got := a.VersionArgs(); len(got) != 1 || got[0] != "--version" {
		t.Errorf("VersionArgs() = %v; want [\"--version\"]", got)
	}
}

// stubProber is an adapter.HostProber with canned results (no billable run).
type stubProber struct {
	path    string
	lookErr error
	out     string
	runErr  error
}

func (s stubProber) LookPath(string) (string, error)      { return s.path, s.lookErr }
func (s stubProber) Run(string, []string) (string, error) { return s.out, s.runErr }

// TestDetect_VersionGreying_L2 — R-E1 / L-2: detection yields InRange=true for
// the real version and InRange=false for a version below the supported floor.
func TestDetect_VersionGreying_L2(t *testing.T) {
	a := newAdapter()

	ok := adapter.Detect(a, stubProber{path: "/usr/local/bin/opencode", out: realVersionBanner})
	if !ok.Found || ok.Version != "1.17.9" || !ok.InRange {
		t.Fatalf("Detect(real version) = %+v; want Found, Version 1.17.9, InRange true", ok)
	}

	old := adapter.Detect(a, stubProber{path: "/usr/local/bin/opencode", out: "0.5.0\n"})
	if !old.Found {
		t.Fatalf("Detect(old version) not Found; want Found (binary present, version out of range)")
	}
	if old.InRange {
		t.Errorf("Detect(old 0.5.0) InRange=true; an out-of-supported-range CLI must be greyed (L-2)")
	}

	missing := adapter.Detect(a, stubProber{lookErr: errNotFound})
	if missing.Found {
		t.Errorf("Detect(missing) Found=true; want not found")
	}
}

// errNotFound stands in for exec.LookPath's not-found error.
var errNotFound = &lookErr{}

type lookErr struct{}

func (*lookErr) Error() string { return "executable file not found in $PATH" }

// TestCommand_NoOptions — R-E2: with no options and no prompt, Command composes
// the bare `opencode` argv.
func TestCommand_NoOptions(t *testing.T) {
	a := newAdapter()
	argv, err := a.Command(adapter.LaunchSpec{Cwd: "/work/proj", Options: map[string]string{}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !equalArgv(argv, []string{"opencode"}) {
		t.Fatalf("Command(no options) = %v; want exactly [\"opencode\"]", argv)
	}
}

// TestCommand_AllOptions — R-E2: model then agent, in that fixed order.
func TestCommand_AllOptions(t *testing.T) {
	a := newAdapter()
	argv, err := a.Command(adapter.LaunchSpec{
		Cwd:     "/work/proj",
		Options: map[string]string{"model": "anthropic/claude-sonnet-5", "agent": "build"},
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := []string{"opencode", "--model", "anthropic/claude-sonnet-5", "--agent", "build"}
	if !equalArgv(argv, want) {
		t.Fatalf("Command(all options) = %v; want %v (fixed order: model then agent)", argv, want)
	}
}

// TestCommand_InitialPrompt — R-E2: the initial prompt reaches the argv behind
// its own --prompt flag.
func TestCommand_InitialPrompt(t *testing.T) {
	a := newAdapter()
	argv, err := a.Command(adapter.LaunchSpec{
		Cwd:           "/work/proj",
		Options:       map[string]string{},
		InitialPrompt: "Say OK and nothing else.",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !equalArgv(argv, []string{"opencode", "--prompt", "Say OK and nothing else."}) {
		t.Fatalf("Command(initial prompt) = %v; want [\"opencode\", \"--prompt\", \"Say OK and nothing else.\"]", argv)
	}
}

// TestOptions_Schema — R-E3: exactly the two declared options, both free-text
// strings; the model option's label documents the provider/model format.
func TestOptions_Schema(t *testing.T) {
	a := newAdapter()
	opts := a.Options()
	if len(opts) != 2 {
		t.Fatalf("Options() has %d entries; want 2 (model, agent)", len(opts))
	}
	byKey := map[string]adapter.OptionSpec{}
	for _, o := range opts {
		byKey[o.Key] = o
	}

	model, ok := byKey["model"]
	if !ok {
		t.Fatalf("Options() missing \"model\"")
	}
	if model.Type != "string" {
		t.Errorf("model option Type = %q; want \"string\"", model.Type)
	}
	if !strings.Contains(model.Label, "provider") || !strings.Contains(model.Label, "model") {
		t.Errorf("model option Label %q does not mention the provider/model format", model.Label)
	}

	agentOpt, ok := byKey["agent"]
	if !ok {
		t.Fatalf("Options() missing \"agent\"")
	}
	if agentOpt.Type != "string" {
		t.Errorf("agent option Type = %q; want \"string\"", agentOpt.Type)
	}
}

// TestSignalSources_MatchMemoTable_HeuristicsOnly — R-E4: exactly the two
// heuristic sources the R-B4 memo pins (prompt-marker + busy-contains "esc
// interrupt"), zero hook/event kinds, and NO idle rule (the memo's honest T-4
// determination: no reliable idle marker was found for opencode).
func TestSignalSources_MatchMemoTable_HeuristicsOnly(t *testing.T) {
	a := newAdapter()
	sources := a.SignalSources()
	if len(sources) != 2 {
		t.Fatalf("SignalSources() has %d entries; want exactly 2 (prompt-marker + busy-contains)", len(sources))
	}

	var hasPromptMarker, hasBusyContains bool
	for _, s := range sources {
		if s.Kind != "heuristic" {
			t.Errorf("SignalSources() declares Kind %q; opencode is heuristics-only (no hook/event declarations — its real SSE schema, a single session.status event, cannot be expressed by the engine's exact-event-name mapping today)", s.Kind)
		}
		switch s.Descriptor["grid"] {
		case "prompt-marker":
			hasPromptMarker = true
		case "busy-contains":
			hasBusyContains = true
			if s.Descriptor["value"] != "esc interrupt" {
				t.Errorf("busy-contains source has value %q; want \"esc interrupt\" (R-B4 memo)", s.Descriptor["value"])
			}
		case "idle-line-equals":
			t.Errorf("SignalSources() declares an idle-line-equals rule; the R-B4 memo determination is NO idle rule for opencode")
		}
	}
	if !hasPromptMarker {
		t.Errorf("SignalSources() missing the generic {grid: prompt-marker} source")
	}
	if !hasBusyContains {
		t.Errorf("SignalSources() missing the {grid: busy-contains, value: \"esc interrupt\"} source")
	}
}

// TestResume_CarriesConversationID — R-E5: Resume composes `opencode --session
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
	want := []string{"opencode", "--session", fixtureConversationID}
	if !equalArgv(argv, want) {
		t.Errorf("Resume(id) = %v; want %v", argv, want)
	}
}

// TestExtractConversationID_FromFixture — R-E6: the adapter recovers the
// session id its own real recorded fixture carries, and returns nothing on
// unrelated input.
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

// TestConformance — the adapter passes the frozen T-1 conformance suite.
func TestConformance(t *testing.T) {
	a := newAdapter()
	if errs := adapter.CheckConformance(a); len(errs) != 0 {
		t.Fatalf("opencode adapter is not conformant: %v", errs)
	}
	adapter.Conformance(t, a)
}

// TestCapability_FromFixture — R-E7: the capability entry derived against the
// REAL grid rendered from the fixture records no hooks, resume + conversation
// id, exactly 2 options, and Signals == ["heuristic"].
func TestCapability_FromFixture(t *testing.T) {
	a := newAdapter()
	fx := loadFixture(t)
	grid := renderGrid(t, fx.PTYCapture)

	entry := adapter.Capability(a, fx, grid)
	if entry.Hooks {
		t.Errorf("capability Hooks=true; opencode declares no hook sources")
	}
	if !entry.Resume {
		t.Errorf("capability Resume=false; opencode supports --session resume")
	}
	if !entry.ConversationID {
		t.Errorf("capability ConversationID=false; the fixture capture carries an extractable session id")
	}
	if entry.Options != 2 {
		t.Errorf("capability Options=%d; want 2 (model, agent)", entry.Options)
	}
	if !equalArgv(entry.Signals, []string{"heuristic"}) {
		t.Errorf("capability Signals=%v; want [\"heuristic\"]", entry.Signals)
	}
	if entry.CLI != fx.CLI || entry.Version != fx.Version {
		t.Errorf("capability identity %q/%q != fixture %q/%q", entry.CLI, entry.Version, fx.CLI, fx.Version)
	}
}

// TestImportBoundary_T5 — the opencode package's transitive NON-TEST
// dependencies within this module are ONLY the adapter contract and internal/vt.
func TestImportBoundary_T5(t *testing.T) {
	allowed := map[string]bool{
		"github.com/Nathandela/swarm/internal/adapter/opencode": true,
		"github.com/Nathandela/swarm/internal/adapter":          true,
		"github.com/Nathandela/swarm/internal/vt":               true,
	}
	for _, dep := range moduleInternalDeps(t, "github.com/Nathandela/swarm/internal/adapter/opencode") {
		if !allowed[dep] {
			t.Errorf("opencode adapter imports forbidden package %q (T-5: adapters depend on the contract + vt only)", dep)
		}
	}
}

// TestStateless_NoIOInSource — no non-test source file names any fd/disk/socket/
// exec primitive.
func TestStateless_NoIOInSource(t *testing.T) {
	scanBannedIO(t, ".")
}
