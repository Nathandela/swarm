// Package agy holds the Antigravity CLI (agy) adapter (v1.1 CLI duo, issue
// agents-tracker-5gv, plan .claude/tmp/cli-duo-implementation-plan.md Phase D).
// This FAILING-FIRST test suite (TDD, GG-5) freezes the adapter's contract
// before any implementation exists. It COMPILES only once the package provides
// the pinned constructor
//
//	func New() adapter.Adapter
//
// so the RED reason for every test here is "undefined: agy.New" until the
// adapter lands (the pinned entrypoint the registry + launch form call).
//
// COST NOTE: NOTHING in this file runs the real `agy` CLI. The version banner
// is the free, real `agy --version` output (design doc docs/design/cli-trio-
// adapters.md section 2). Extraction and capability behavior are asserted
// against the committed characterization fixture testdata/agy.json (a
// PROMOTED, sanitized capture — see docs/verification/cli-duo-adapters-
// evidence.md, Phase B) whose exit-screen conversation id and marker byte
// offsets (7422, 10035) are frozen there. The R-B4 marker table (busy: "esc to
// cancel" + "Generating..."; idle: bare ">") is this suite's binding input
// contract for SignalSources.
package agy

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/vt"
)

// realVersionBanner is the EXACT output of `agy --version` (design doc section
// 2, captured live — free, non-billable). ParseVersion must read it verbatim.
const realVersionBanner = "1.1.4\n"

// fixtureConversationID is the session UUID testdata/agy.json's capture
// carries at its exit screen (Phase B evidence: raw offset 10035, and again at
// 7422 — a redraw of the same value). ExtractConversationID must recover
// exactly this id.
const fixtureConversationID = "fb5e3e02-e5ef-4d25-b398-aead20366441"

// agyModelSuggestions are the 8 `agy models` display names (design doc section
// 3, verified live against agy 1.1.4 during the exploration phase —
// docs/verification/cli-trio-exploration/).
var agyModelSuggestions = []string{
	"Gemini 3.5 Flash (Medium)", "Gemini 3.5 Flash (High)", "Gemini 3.5 Flash (Low)",
	"Gemini 3.1 Pro (Low)", "Gemini 3.1 Pro (High)",
	"Claude Sonnet 4.6 (Thinking)", "Claude Opus 4.6 (Thinking)", "GPT-OSS 120B (Medium)",
}

// Frozen R-B4 marker table values (docs/verification/cli-duo-adapters-
// evidence.md, Phase B) — the binding input contract SignalSources must match
// exactly.
const (
	memoBusyEscToCancel = "esc to cancel"
	memoBusyGenerating  = "Generating..."
	memoIdleValue       = ">"
)

// newAdapter builds the adapter under test. The single seam every test funnels
// through, so the RED "undefined: New" is reported once.
func newAdapter() adapter.Adapter { return New() }

// loadFixture loads the committed agy characterization fixture (Phase B:
// fixtures committed BEFORE adapter code, T-6).
func loadFixture(t *testing.T) adapter.Fixture {
	t.Helper()
	fx, err := fixtureio.LoadFixture("testdata/agy.json")
	if err != nil {
		t.Fatalf("load agy fixture: %v", err)
	}
	return fx
}

// TestParseVersion_RealBanner — R-D1: ParseVersion reads the EXACT real
// `agy --version` banner, and rejects a garbage banner.
func TestParseVersion_RealBanner(t *testing.T) {
	a := newAdapter()
	v, ok := a.ParseVersion(realVersionBanner)
	if !ok || v != "1.1.4" {
		t.Fatalf("ParseVersion(%q) = (%q, %v); want (\"1.1.4\", true)", realVersionBanner, v, ok)
	}
	if v2, ok2 := a.ParseVersion("not a version"); ok2 || v2 != "" {
		t.Errorf("ParseVersion(garbage) = (%q, %v); want (\"\", false)", v2, ok2)
	}
}

// TestBinaryAndVersionArgs — R-D1: the detection descriptors match the real
// CLI (`agy --version`).
func TestBinaryAndVersionArgs(t *testing.T) {
	a := newAdapter()
	if a.Binary() != "agy" {
		t.Errorf("Binary() = %q; want \"agy\"", a.Binary())
	}
	if got := a.VersionArgs(); len(got) != 1 || got[0] != "--version" {
		t.Errorf("VersionArgs() = %v; want [\"--version\"]", got)
	}
}

// stubProber is an adapter.HostProber that returns canned LookPath/Run results,
// so detection is exercised without executing the real CLI (COST: no billable
// run).
type stubProber struct {
	path    string
	lookErr error
	out     string
	runErr  error
}

func (s stubProber) LookPath(string) (string, error)      { return s.path, s.lookErr }
func (s stubProber) Run(string, []string) (string, error) { return s.out, s.runErr }

// errNotFound stands in for exec.LookPath's not-found error.
var errNotFound = &lookErr{}

type lookErr struct{}

func (*lookErr) Error() string { return "executable file not found in $PATH" }

// TestDetect_VersionGreying_L2 — R-D1 / L-2: detection through the CORE
// adapter.Detect yields InRange=true for the real supported version and
// InRange=false for a version below the SupportedVersions floor (1.1.0).
func TestDetect_VersionGreying_L2(t *testing.T) {
	a := newAdapter()

	inRange := adapter.Detect(a, stubProber{path: "/usr/local/bin/agy", out: realVersionBanner})
	if !inRange.Found || inRange.Version != "1.1.4" || !inRange.InRange {
		t.Fatalf("Detect(real version) = %+v; want Found, Version 1.1.4, InRange true", inRange)
	}

	old := adapter.Detect(a, stubProber{path: "/usr/local/bin/agy", out: "1.0.0\n"})
	if !old.Found {
		t.Fatalf("Detect(old version) not Found; want Found (binary present, version out of range)")
	}
	if old.InRange {
		t.Errorf("Detect(old 1.0.0) InRange=true; an out-of-supported-range CLI must be greyed (L-2)")
	}

	missing := adapter.Detect(a, stubProber{lookErr: errNotFound})
	if missing.Found {
		t.Errorf("Detect(missing) Found=true; want not found")
	}
}

// TestCommand_NoOptions — R-D2: with no options and no initial prompt, Command
// composes the bare `agy` argv.
func TestCommand_NoOptions(t *testing.T) {
	a := newAdapter()
	argv, err := a.Command(adapter.LaunchSpec{Cwd: "/work/proj", Options: map[string]string{}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := []string{"agy"}
	if !equalArgv(argv, want) {
		t.Errorf("Command(no options) = %v; want %v", argv, want)
	}
}

// TestCommand_AllOptions — R-D2: Command composes the FIXED flag order
// --model, --mode, --dangerously-skip-permissions, --prompt-interactive
// regardless of map iteration order (pure/deterministic).
func TestCommand_AllOptions(t *testing.T) {
	a := newAdapter()
	argv, err := a.Command(adapter.LaunchSpec{
		Cwd: "/work/proj",
		Options: map[string]string{
			"model":                        "Gemini 3.5 Flash (Low)",
			"mode":                         "accept-edits",
			"dangerously-skip-permissions": "true",
		},
		InitialPrompt: "Say OK and nothing else.",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := []string{
		"agy",
		"--model", "Gemini 3.5 Flash (Low)",
		"--mode", "accept-edits",
		"--dangerously-skip-permissions",
		"--prompt-interactive", "Say OK and nothing else.",
	}
	if !equalArgv(argv, want) {
		t.Errorf("Command(all options) = %v; want %v", argv, want)
	}
}

// TestCommand_InitialPrompt — R-D2: an initial prompt with no options composes
// `agy --prompt-interactive <prompt>`.
func TestCommand_InitialPrompt(t *testing.T) {
	a := newAdapter()
	argv, err := a.Command(adapter.LaunchSpec{
		Cwd:           "/work/proj",
		Options:       map[string]string{},
		InitialPrompt: "refactor the parser",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := []string{"agy", "--prompt-interactive", "refactor the parser"}
	if !equalArgv(argv, want) {
		t.Errorf("Command(initial prompt) = %v; want %v", argv, want)
	}
	if len(argv) == 0 || argv[0] != "agy" {
		t.Fatalf("Command argv[0] = %q; want \"agy\" (argv = %v)", first(argv), argv)
	}
	if !containsArg(argv, "refactor the parser") {
		t.Errorf("Command argv omits the initial prompt; argv = %v", argv)
	}
}

// TestOptions_Schema — R-D3: model (string, Suggest = the 8 display names),
// mode (choice accept-edits|plan, no default), dangerously-skip-permissions
// (bool, default "false").
func TestOptions_Schema(t *testing.T) {
	a := newAdapter()
	opts := a.Options()
	if len(opts) != 3 {
		t.Fatalf("Options() has %d entries; want 3 (model, mode, dangerously-skip-permissions)", len(opts))
	}
	byKey := make(map[string]adapter.OptionSpec, len(opts))
	for _, o := range opts {
		byKey[o.Key] = o
	}

	model, ok := byKey["model"]
	if !ok {
		t.Fatal("Options() missing \"model\"")
	}
	if model.Type != "string" {
		t.Errorf("model.Type = %q; want \"string\"", model.Type)
	}
	if !equalArgv(model.Suggest, agyModelSuggestions) {
		t.Errorf("model.Suggest = %v; want %v", model.Suggest, agyModelSuggestions)
	}

	mode, ok := byKey["mode"]
	if !ok {
		t.Fatal("Options() missing \"mode\"")
	}
	if mode.Type != "choice" {
		t.Errorf("mode.Type = %q; want \"choice\"", mode.Type)
	}
	if !equalArgv(mode.Choices, []string{"accept-edits", "plan"}) {
		t.Errorf("mode.Choices = %v; want [accept-edits plan]", mode.Choices)
	}
	if mode.Default != "" {
		t.Errorf("mode.Default = %q; want \"\" (R-D3: no default)", mode.Default)
	}

	skip, ok := byKey["dangerously-skip-permissions"]
	if !ok {
		t.Fatal("Options() missing \"dangerously-skip-permissions\"")
	}
	if skip.Type != "bool" || skip.Default != "false" {
		t.Errorf("dangerously-skip-permissions = %+v; want Type \"bool\", Default \"false\"", skip)
	}
}

// TestSignalSources_MatchMemoTable — R-D4: agy is heuristic-only (v1, design
// doc section 4) and declares EXACTLY the frozen R-B4 marker table: the
// generic prompt-marker, the busy-contains UNION ("esc to cancel" load-bearing
// + "Generating..." reinforcement — Phase B's 72-byte spinner-overwrite
// transient proves neither alone suffices), and idle-line-equals ">".
func TestSignalSources_MatchMemoTable(t *testing.T) {
	a := newAdapter()
	sources := a.SignalSources()

	type key struct{ grid, value string }
	got := make(map[key]bool, len(sources))
	for _, s := range sources {
		if s.Kind != "heuristic" {
			t.Errorf("SignalSources(): non-heuristic Kind %q; agy is heuristic-only v1 (design doc section 4)", s.Kind)
		}
		got[key{s.Descriptor["grid"], s.Descriptor["value"]}] = true
	}

	want := []key{
		{"prompt-marker", ""},
		{"busy-contains", memoBusyEscToCancel},
		{"busy-contains", memoBusyGenerating},
		{"idle-line-equals", memoIdleValue},
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("SignalSources() missing {grid:%q value:%q} (R-B4 memo table)", w.grid, w.value)
		}
	}
	if len(sources) != len(want) {
		t.Errorf("SignalSources() has %d entries; want exactly %d (R-D4)", len(sources), len(want))
	}
}

// TestResume_CarriesConversationID — R-D5: Resume composes
// `agy --conversation <id>`; an empty id resumes nothing.
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
	want := []string{"agy", "--conversation", fixtureConversationID}
	if !equalArgv(argv, want) {
		t.Errorf("Resume(id) = %v; want %v", argv, want)
	}
}

// TestExtractConversationID_Adversarial — R-D6: the hardened extraction
// contract. Each subtest is one adversarial condition the plan calls out.
func TestExtractConversationID_Adversarial(t *testing.T) {
	a := newAdapter()

	t.Run("prose_marker_earlier_then_real_exit_marker_last_wins", func(t *testing.T) {
		tail := []byte("the assistant's transcript explained: type \"agy --conversation=notarealuuidatallxxxxxxxxxxxxxxxx\" to resume\n" +
			"...\nResume with -c (or command below):\nagy --conversation=" + fixtureConversationID + "\r\n")
		id, ok := a.ExtractConversationID(nil, tail)
		if !ok || id != fixtureConversationID {
			t.Fatalf("ExtractConversationID = (%q, %v); want (%q, true)", id, ok, fixtureConversationID)
		}
	})

	t.Run("malformed_uuid_then_valid", func(t *testing.T) {
		// The decoy is UUID-SHAPED (36 chars, hyphens in the right places) but
		// uppercase — the shape check must be case-sensitive, not merely a
		// length/hyphen check.
		tail := []byte("agy --conversation=FB5E3E02-E5EF-4D25-B398-AEAD20366441\r\n" +
			"agy --conversation=" + fixtureConversationID + "\r\n")
		id, ok := a.ExtractConversationID(nil, tail)
		if !ok || id != fixtureConversationID {
			t.Fatalf("ExtractConversationID = (%q, %v); want (%q, true)", id, ok, fixtureConversationID)
		}
	})

	t.Run("multiple_exit_screen_redraws", func(t *testing.T) {
		one := "agy --conversation=" + fixtureConversationID + "\x1b[K\r\n"
		tail := []byte(one + one + one)
		id, ok := a.ExtractConversationID(nil, tail)
		if !ok || id != fixtureConversationID {
			t.Fatalf("ExtractConversationID = (%q, %v); want (%q, true)", id, ok, fixtureConversationID)
		}
	})

	t.Run("esc_bracket_K_butted_against_id", func(t *testing.T) {
		tail := []byte("agy --conversation=" + fixtureConversationID + "\x1b[K")
		id, ok := a.ExtractConversationID(nil, tail)
		if !ok || id != fixtureConversationID {
			t.Fatalf("ExtractConversationID = (%q, %v); want (%q, true)", id, ok, fixtureConversationID)
		}
	})

	t.Run("utf8_C1_control_butted_against_id", func(t *testing.T) {
		// 0xC2 0x9B is the UTF-8 encoding of U+009B (CSI, a C1 control). The
		// terminator rule's ">= 0x80" clause must stop on the lead byte 0xC2
		// without needing to decode the sequence (cf. commit a817cfd).
		tail := append([]byte("agy --conversation="+fixtureConversationID), 0xC2, 0x9B)
		id, ok := a.ExtractConversationID(nil, tail)
		if !ok || id != fixtureConversationID {
			t.Fatalf("ExtractConversationID = (%q, %v); want (%q, true)", id, ok, fixtureConversationID)
		}
	})

	t.Run("malformed_uuid_only_candidate_rejected", func(t *testing.T) {
		// R-D6 MUST: the token has to match the UUID shape, not merely be
		// present and terminated. A properly-terminated but wrong-case token
		// with no other, valid candidate anywhere must be rejected outright
		// (not returned as-is).
		tail := []byte("agy --conversation=FB5E3E02-E5EF-4D25-B398-AEAD20366441\r\n")
		if id, ok := a.ExtractConversationID(nil, tail); ok {
			t.Fatalf("ExtractConversationID(malformed shape) = (%q, true); want (_, false)", id)
		}
	})

	t.Run("truncated_EOF_token_rejected", func(t *testing.T) {
		// The capture ends immediately after the marker, mid-uuid — a
		// mid-write read, not a complete token (C3).
		tail := []byte("agy --conversation=fb5e3e02-e5ef-4d25-b398-aead2036")
		if id, ok := a.ExtractConversationID(nil, tail); ok {
			t.Fatalf("ExtractConversationID(truncated) = (%q, true); want (_, false)", id)
		}
	})

	t.Run("tail_invalid_grid_valid_fallback", func(t *testing.T) {
		// The tail's only candidate is EOF-truncated (invalid); the rendered
		// grid carries a complete, valid marker+id — extraction must fall
		// back to it.
		tail := []byte("agy --conversation=fb5e3e02-e5ef-4d25-b398-aead2036")
		grid := &vt.Snap{Lines: []vt.Line{
			{Runs: []vt.Run{{Text: "agy --conversation=" + fixtureConversationID, Width: 1}}},
		}}
		id, ok := a.ExtractConversationID(grid, tail)
		if !ok || id != fixtureConversationID {
			t.Fatalf("ExtractConversationID(tail-invalid, grid-valid) = (%q, %v); want (%q, true)", id, ok, fixtureConversationID)
		}
	})
}

// TestExtractConversationID_FromFixture — R-D7: the adapter recovers the
// session id its own characterization fixture recorded, and returns nothing on
// unrelated input. The id is read from the raw capture, exactly as the engine
// drives extraction at runtime (Phase B evidence: marker at raw offset 10035).
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

// TestCapability_FromFixture — R-D7: the T-6 acceptance baseline derived from
// the REAL adapter + REAL fixture: Resume:true, ConversationID:true,
// Hooks:false (agy has no argv-injectable hook mechanism, design doc section
// 4), Options:3, Signals:["heuristic"] (agy is heuristic-only, v1).
func TestCapability_FromFixture(t *testing.T) {
	a := newAdapter()
	fx := loadFixture(t)
	grid := renderGrid(t, fx.PTYCapture)

	entry := adapter.Capability(a, fx, grid)
	if entry.Hooks {
		t.Errorf("capability Hooks=true; agy declares no hook SignalSource")
	}
	if !entry.Resume {
		t.Errorf("capability Resume=false; agy supports --conversation resume")
	}
	if !entry.ConversationID {
		t.Errorf("capability ConversationID=false; the fixture capture carries an extractable conversation id")
	}
	if entry.Options != 3 {
		t.Errorf("capability Options=%d; want 3", entry.Options)
	}
	if !equalArgv(entry.Signals, []string{"heuristic"}) {
		t.Errorf("capability Signals=%v; want [\"heuristic\"]", entry.Signals)
	}
	if entry.CLI != fx.CLI || entry.Version != fx.Version {
		t.Errorf("capability identity %q/%q != fixture %q/%q", entry.CLI, entry.Version, fx.CLI, fx.Version)
	}
}

// TestConformance — R-D8: the adapter passes the frozen T-1 conformance suite
// (CheckConformance zero violations + the *testing.T wrapper with its
// goroutine / race checks).
func TestConformance(t *testing.T) {
	a := newAdapter()
	if errs := adapter.CheckConformance(a); len(errs) != 0 {
		t.Fatalf("agy adapter is not conformant: %v", errs)
	}
	adapter.Conformance(t, a)
}

// TestImportBoundary_T5 — R-D8 / T-5: the agy package's transitive NON-TEST
// dependencies within this module are ONLY the adapter contract and
// internal/vt. Any dependency on daemon/shim/wire/protocol/persist/status/
// engine/TUI/cmd would force core edits to add an adapter — the exact coupling
// T-5 forbids. (This test's own imports are test-only and excluded from
// `go list -deps`.)
func TestImportBoundary_T5(t *testing.T) {
	allowed := map[string]bool{
		"github.com/Nathandela/swarm/internal/adapter/agy": true,
		"github.com/Nathandela/swarm/internal/adapter":     true,
		"github.com/Nathandela/swarm/internal/vt":          true,
	}
	for _, dep := range moduleInternalDeps(t, "github.com/Nathandela/swarm/internal/adapter/agy") {
		if !allowed[dep] {
			t.Errorf("agy adapter imports forbidden package %q (T-5: adapters depend on the contract + vt only)", dep)
		}
	}
}

// TestStateless_NoIOInSource — R-D8 / E9.2: no non-test source file may name
// an fd/disk/socket/exec primitive. Core owns all lifecycle; the adapter owns
// no fds.
func TestStateless_NoIOInSource(t *testing.T) {
	scanBannedIO(t, ".")
}
