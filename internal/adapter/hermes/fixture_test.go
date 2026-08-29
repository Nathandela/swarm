package hermes

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/vt"
)

func TestCharacterizationFixturesLoadAndRetainIntegrity(t *testing.T) {
	tests := []struct {
		file     string
		scenario string
		bytes    int
		sha256   string
		id       string
	}{
		{
			file: "normal.json", scenario: "mock-turn-clean-exit", bytes: 25999,
			sha256: "94f4649e1e7e8856f4a04b5a4c409a08bb014c1c848fffeadb1b381fe9bf987c",
			id:     fixtureConversationID,
		},
		{
			file: "approval.json", scenario: "approval-dialog", bytes: 29692,
			sha256: "ecf92ab0a228b6d4da82c2ac4a2288165a95306f9c18cdaaec3ab61993da558d",
			id:     "20260829_103623_b993a9",
		},
		{
			file: "clarify.json", scenario: "clarify-dialog", bytes: 28382,
			sha256: "4dcf08d6e6dd810de678e3ab8f920e79cc466c1520701128d68082871446af42",
			id:     secondConversationID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			fixture, err := fixtureio.LoadFixture("testdata/" + tt.file)
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			if fixture.CLI != "hermes" || fixture.Version != "0.20.6" || fixture.Scenario != tt.scenario {
				t.Fatalf("fixture identity = %q/%q/%q; want hermes/0.20.6/%s", fixture.CLI, fixture.Version, fixture.Scenario, tt.scenario)
			}
			if len(fixture.PTYCapture) != tt.bytes {
				t.Fatalf("PTY capture length = %d; want %d (corpus changed without re-characterization)", len(fixture.PTYCapture), tt.bytes)
			}
			sum := sha256.Sum256(fixture.PTYCapture)
			if got := fmt.Sprintf("%x", sum); got != tt.sha256 {
				t.Fatalf("PTY capture SHA-256 = %s; want %s (corpus changed without re-characterization)", got, tt.sha256)
			}
			if len(fixture.HookPayloads) != 0 {
				t.Fatalf("fixture has %d hook payloads; classic Hermes is heuristic-only", len(fixture.HookPayloads))
			}

			grid := renderFixtureGrid(t, fixture.PTYCapture)
			id, ok := newAdapter().ExtractConversationID(grid, fixture.PTYCapture)
			if !ok || id != tt.id {
				t.Fatalf("ExtractConversationID(fixture) = (%q,%v); want (%q,true)", id, ok, tt.id)
			}
		})
	}
}

func TestCapabilityFromCharacterizedFixture(t *testing.T) {
	fixture, err := fixtureio.LoadFixture("testdata/normal.json")
	if err != nil {
		t.Fatalf("load normal fixture: %v", err)
	}
	entry := adapter.Capability(newAdapter(), fixture, renderFixtureGrid(t, fixture.PTYCapture))
	if entry.Hooks {
		t.Errorf("capability Hooks=true; Hermes v1 is heuristic-only")
	}
	if !entry.Resume || !entry.ConversationID {
		t.Errorf("capability lacks resume or conversation identity: %+v", entry)
	}
	if entry.Options != 7 {
		t.Errorf("capability Options=%d; want 7", entry.Options)
	}
	if len(entry.Signals) != 1 || entry.Signals[0] != "heuristic" {
		t.Errorf("capability Signals=%v; want [heuristic]", entry.Signals)
	}
}

func renderFixtureGrid(t *testing.T, capture []byte) *vt.Snap {
	t.Helper()
	emulator := vt.NewEmulator(100, 30)
	defer func() { _ = emulator.Close() }()
	emulator.Feed(capture)
	encoded, err := emulator.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap, err := vt.DecodeSnapshot(encoded)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snap
}
