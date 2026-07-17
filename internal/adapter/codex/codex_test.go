// Package codex holds the Codex adapter (Epic 11, E11.4/E11.6/E11.8). This
// FAILING-FIRST suite (TDD, GG-5) freezes the adapter's contract before any
// implementation exists. It COMPILES only once the package provides the pinned
// constructor
//
//	func New() adapter.Adapter
//
// so every RED reason here is "undefined: codex.New" until the adapter lands.
//
// COST NOTE (orchestrator brief): NOTHING here runs the real `codex` CLI. The
// version string is the free, real `codex --version` output ("codex-cli 0.144.1");
// everything else is asserted against the hand-authored fixture testdata/codex.json,
// which encodes Codex's typed app-server/exec EVENT stream (turn.started /
// turn.completed / exec_approval_request) and a session id in the capture.
//
// VERIFY: Codex's exact event field names (type == "turn.started" etc.,
// "conversation_id") and its resume flag are pinned here from the DOCUMENTED
// app-server interface but not yet confirmed against a real capture. A flagged
// live-characterization smoke (see the e2e handoff) records the real events and,
// on any drift, the fixture + these names are re-recorded (T-6). The status-mapping
// assertions below key off the MAPPED turn/interaction values, not the event
// names, so they stay valid even if a name drifts.
package codex

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
)

// realVersionBanner is the EXACT output of `codex --version` (captured live —
// free, non-billable).
const realVersionBanner = "codex-cli 0.144.1"

// fixtureConversationID is the session id embedded in testdata/codex.json's
// capture. VERIFY against a real capture via the flagged smoke.
const fixtureConversationID = "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"

func newAdapter() adapter.Adapter { return New() }

func loadFixture(t *testing.T) adapter.Fixture {
	t.Helper()
	fx, err := fixtureio.LoadFixture("testdata/codex.json")
	if err != nil {
		t.Fatalf("load codex fixture: %v", err)
	}
	return fx
}

// TestParseVersion_RealBanner — E11.4: ParseVersion reads the EXACT real
// `codex --version` banner ("codex-cli 0.144.1") and rejects garbage.
func TestParseVersion_RealBanner(t *testing.T) {
	a := newAdapter()
	v, ok := a.ParseVersion(realVersionBanner)
	if !ok || v != "0.144.1" {
		t.Fatalf("ParseVersion(%q) = (%q, %v); want (\"0.144.1\", true)", realVersionBanner, v, ok)
	}
	if v2, ok2 := a.ParseVersion("garbage line"); ok2 || v2 != "" {
		t.Errorf("ParseVersion(garbage) = (%q, %v); want (\"\", false)", v2, ok2)
	}
}

// TestBinaryAndVersionArgs — E11.4: detection descriptors match the real CLI.
func TestBinaryAndVersionArgs(t *testing.T) {
	a := newAdapter()
	if a.Binary() != "codex" {
		t.Errorf("Binary() = %q; want \"codex\"", a.Binary())
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

// TestDetect_VersionGreying_L2 — E11.6 / L-2: detection yields InRange=true for the
// real version and InRange=false for a version below the supported floor. InRange
// is the exact signal the launch form greys on, so this proves L-2 end-to-end at
// the detection boundary. The supported floor sits well above the ancient 0.1 era
// (fixtures pin the characterized version; drift = re-characterize, T-6).
func TestDetect_VersionGreying_L2(t *testing.T) {
	a := newAdapter()

	ok := adapter.Detect(a, stubProber{path: "/usr/local/bin/codex", out: realVersionBanner})
	if !ok.Found || ok.Version != "0.144.1" || !ok.InRange {
		t.Fatalf("Detect(real version) = %+v; want Found, Version 0.144.1, InRange true", ok)
	}

	old := adapter.Detect(a, stubProber{path: "/usr/local/bin/codex", out: "codex-cli 0.1.0"})
	if !old.Found {
		t.Fatalf("Detect(old version) not Found; want Found (binary present, version out of range)")
	}
	if old.InRange {
		t.Errorf("Detect(old 0.1.0) InRange=true; an out-of-supported-range CLI must be greyed (L-2)")
	}
}

// TestCommand_ComposesArgv — E11.4: Command composes an argv starting with the
// codex program and carrying the initial prompt. The exact subcommand/flags of
// Codex's launch are VERIFY (its app-server/exec interface); the load-bearing
// contract here is a bare `codex` argv[0] and that the prompt reaches the argv.
func TestCommand_ComposesArgv(t *testing.T) {
	a := newAdapter()
	argv, err := a.Command(adapter.LaunchSpec{
		Cwd:           "/work/proj",
		Options:       map[string]string{},
		InitialPrompt: "add a benchmark",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if len(argv) == 0 || argv[0] != "codex" {
		t.Fatalf("Command argv[0] = %q; want \"codex\" (argv = %v)", first(argv), argv)
	}
	if !containsArg(argv, "add a benchmark") {
		t.Errorf("Command argv omits the initial prompt; argv = %v", argv)
	}
}

// TestSignalSources_DeclaresTypedEventsWithStatusMapping — E11.4 / T-2: Codex drives
// status from TYPED events (Kind "event"), not hooks. The adapter declares event
// sources whose status mapping is:
//   - turn.started        → turn active
//   - turn.completed      → turn idle
//   - approval request    → interaction permission
//
// plus a heuristic fallback (T-3). The assertions key off the MAPPED status values
// (robust to event-name drift, VERIFY); every event source must still name a
// non-empty Codex event.
func TestSignalSources_DeclaresTypedEventsWithStatusMapping(t *testing.T) {
	a := newAdapter()

	var hasActive, hasIdle, hasPermission, hasHeuristic, hasEventKind bool
	for _, s := range a.SignalSources() {
		switch s.Kind {
		case "event":
			hasEventKind = true
			if s.Descriptor["event"] == "" {
				t.Errorf("event SignalSource has no \"event\" name in its descriptor: %+v", s)
			}
			if s.Descriptor["turn"] == string(status.TurnActive) {
				hasActive = true
			}
			if s.Descriptor["turn"] == string(status.TurnIdle) {
				hasIdle = true
			}
			if s.Descriptor["interaction"] == string(status.InteractionPermission) {
				hasPermission = true
			}
		case "heuristic":
			hasHeuristic = true
		case "hook":
			t.Errorf("Codex declared a hook SignalSource; Codex uses typed events, not settings hooks")
		default:
			t.Errorf("SignalSource has invalid Kind %q", s.Kind)
		}
	}

	if !hasEventKind {
		t.Errorf("SignalSources declares no typed \"event\" source; Codex status is event-driven (T-2)")
	}
	if !hasActive {
		t.Errorf("no event source maps turn=active (turn.started)")
	}
	if !hasIdle {
		t.Errorf("no event source maps turn=idle (turn.completed)")
	}
	if !hasPermission {
		t.Errorf("no event source maps interaction=permission (approval request)")
	}
	if !hasHeuristic {
		t.Errorf("SignalSources declares no heuristic fallback source (T-3)")
	}
}

// TestResume_CarriesConversationID — E11.4 / R-2: Resume composes a `codex` argv
// carrying the conversation id; an empty id resumes nothing. The exact resume flag
// is VERIFY; the contract is that the id travels in the argv.
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
	if len(argv) == 0 || argv[0] != "codex" {
		t.Fatalf("Resume argv[0] = %q; want \"codex\" (argv = %v)", first(argv), argv)
	}
	if !containsArg(argv, fixtureConversationID) {
		t.Errorf("Resume argv missing the conversation id %q; argv = %v", fixtureConversationID, argv)
	}
}

// TestExtractConversationID_FromFixture — E11.4: the adapter recovers the session
// id its own fixture recorded, and returns nothing on unrelated input.
func TestExtractConversationID_FromFixture(t *testing.T) {
	a := newAdapter()
	fx := loadFixture(t)
	grid := renderGrid(t, fx.PTYCapture)

	id, ok := a.ExtractConversationID(grid, fx.PTYCapture)
	if !ok || id != fixtureConversationID {
		t.Fatalf("ExtractConversationID(fixture) = (%q, %v); want (%q, true)", id, ok, fixtureConversationID)
	}
	if gid, gok := a.ExtractConversationID(nil, []byte("nothing")); gok || gid != "" {
		t.Errorf("ExtractConversationID(garbage) = (%q, %v); want (\"\", false)", gid, gok)
	}
}

// TestConformance — E11.4: the adapter passes the frozen T-1 conformance suite.
func TestConformance(t *testing.T) {
	a := newAdapter()
	if errs := adapter.CheckConformance(a); len(errs) != 0 {
		t.Fatalf("codex adapter is not conformant: %v", errs)
	}
	adapter.Conformance(t, a)
}

// TestCapability_EventStyle — E11.1 / E9.6: Codex's capability entry records the
// EVENT signal style (not hooks) plus resume + conversation-id. This is the second
// signal style Epic 11 proves against the one frozen interface (claude = hooks,
// codex = events).
func TestCapability_EventStyle(t *testing.T) {
	a := newAdapter()
	fx := loadFixture(t)
	grid := renderGrid(t, fx.PTYCapture)

	entry := adapter.Capability(a, fx, grid)
	if !entry.Resume {
		t.Errorf("capability Resume=false; Codex supports resume")
	}
	if !entry.ConversationID {
		t.Errorf("capability ConversationID=false; the fixture capture carries an extractable session id")
	}
	if !containsStr(entry.Signals, "event") {
		t.Errorf("capability Signals %v does not include \"event\"; Codex is event-driven", entry.Signals)
	}
	if containsStr(entry.Signals, "hook") {
		t.Errorf("capability Signals %v includes \"hook\"; Codex uses typed events, not hooks", entry.Signals)
	}
	if entry.CLI != fx.CLI || entry.Version != fx.Version {
		t.Errorf("capability identity %q/%q != fixture %q/%q", entry.CLI, entry.Version, fx.CLI, fx.Version)
	}
}

// TestGridHeuristicFallback_ClassifiesIdlePrompt — E11.6 / T-3: with no fresh typed
// signal, the generic grid heuristic (through the real engine's OnOutput tap)
// classifies Codex's idle fixture grid as turn=idle. Codex prefers typed events;
// this proves the heuristic FALLBACK still reads its real grid correctly.
func TestGridHeuristicFallback_ClassifiesIdlePrompt(t *testing.T) {
	a := newAdapter()
	fx := loadFixture(t)
	grid := renderGrid(t, fx.PTYCapture)

	var got status.Status
	var emitted bool
	eng := engine.New(engine.Config{
		StalenessThreshold: 0,
		Emit: func(_ string, s status.Status) {
			got = s
			emitted = true
		},
	})
	eng.RegisterSession("s1", "tok", 0, a.SignalSources())
	eng.OnOutput("s1", grid)

	if !emitted {
		t.Fatalf("OnOutput on the idle grid emitted no status change; the fallback did not classify it")
	}
	if got.Turn != status.TurnIdle {
		t.Errorf("grid heuristic classified turn=%q on Codex's idle fixture; want idle", got.Turn)
	}
}

// TestImportBoundary_T5 — E11.8 / T-5: the codex package's transitive NON-TEST deps
// within this module are ONLY the adapter contract and internal/vt.
func TestImportBoundary_T5(t *testing.T) {
	allowed := map[string]bool{
		"github.com/Nathandela/swarm/internal/adapter/codex": true,
		"github.com/Nathandela/swarm/internal/adapter":       true,
		"github.com/Nathandela/swarm/internal/vt":            true,
	}
	for _, dep := range moduleInternalDeps(t, "github.com/Nathandela/swarm/internal/adapter/codex") {
		if !allowed[dep] {
			t.Errorf("codex adapter imports forbidden package %q (T-5: adapters depend on the contract + vt only)", dep)
		}
	}
}

// TestStateless_NoIOInSource — E9.2 (applied to codex per E11.8): no non-test source
// file names any fd/disk/socket/exec primitive.
func TestStateless_NoIOInSource(t *testing.T) {
	scanBannedIO(t, ".")
}

// containsStr reports whether xs contains s.
func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
