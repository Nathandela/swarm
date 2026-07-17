package adapter

// E9.6 / T-6 — a capability-matrix entry is emitted per characterized CLI. The
// entry is the harness's OUTPUT and the adapter's acceptance baseline: it records
// which signal styles (hooks/events/heuristics), resume, conversation-id
// extraction, and how many launch options the adapter declares — derived from
// the adapter itself, cross-checked against the recorded fixture (the id must be
// extractable from the REAL capture, not merely claimed).
//
// FROZEN (pinned):
//
//	type CapabilityEntry struct {
//	    CLI, Version string
//	    Hooks, Resume, ConversationID bool
//	    Options int
//	    Signals []string      // sorted, de-duped SignalSource kinds
//	}
//	func Capability(a Adapter, fx Fixture) CapabilityEntry
//
// Derivation (pinned): Hooks = adapter declares a "hook" signal source; Resume =
// Resume(a valid id) returns non-empty argv; ConversationID = the adapter
// extracts an id from the fixture's PTYCapture (grid + tail); Options =
// len(Options()); CLI/Version copied from the fixture.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/vt"
)

// caplessAdapter is conformant but declares no hooks, no resume, and never
// extracts an id — the all-false capability baseline.
type caplessAdapter struct{ baseAdapter }

func (caplessAdapter) SignalSources() []SignalSource {
	return []SignalSource{{Kind: "heuristic", Descriptor: map[string]string{"grid": "spinner"}}}
}
func (caplessAdapter) Resume(ResumeSpec) ([]string, error)                   { return nil, nil }
func (caplessAdapter) ExtractConversationID(*vt.Snap, []byte) (string, bool) { return "", false }

// TestCapability_FullyCapableAdapter — baseAdapter declares a hook source,
// supports resume, and its fixture capture carries an extractable id.
func TestCapability_FullyCapableAdapter(t *testing.T) {
	fx := sampleFixture() // capture contains "conv-id=abc123"
	got := Capability(baseAdapter{}, fx)

	if got.CLI != fx.CLI || got.Version != fx.Version {
		t.Errorf("identity = %q/%q, want %q/%q", got.CLI, got.Version, fx.CLI, fx.Version)
	}
	if !got.Hooks {
		t.Error("Hooks = false, want true (adapter declares a hook signal source)")
	}
	if !got.Resume {
		t.Error("Resume = false, want true")
	}
	if !got.ConversationID {
		t.Error("ConversationID = false, want true (id is extractable from the fixture capture)")
	}
	if got.Options != len(baseAdapter{}.Options()) {
		t.Errorf("Options = %d, want %d", got.Options, len(baseAdapter{}.Options()))
	}
	if !reflect.DeepEqual(got.Signals, []string{"event", "heuristic", "hook"}) {
		t.Errorf("Signals = %v, want sorted de-duped [event heuristic hook]", got.Signals)
	}
}

// TestCapability_CaplessAdapter — an adapter with no hooks/resume and no
// extractable id yields an all-false entry (with the fixture identity intact).
func TestCapability_CaplessAdapter(t *testing.T) {
	fx := sampleFixture()
	got := Capability(caplessAdapter{}, fx)

	if got.Hooks || got.Resume || got.ConversationID {
		t.Errorf("capless adapter reported a capability: %+v", got)
	}
	if got.CLI != fx.CLI {
		t.Errorf("CLI = %q, want %q", got.CLI, fx.CLI)
	}
	if !reflect.DeepEqual(got.Signals, []string{"heuristic"}) {
		t.Errorf("Signals = %v, want [heuristic]", got.Signals)
	}
}

// TestCapabilityEntry_JSONRoundTrip — the entry is serialized into the
// capability matrix; it must survive a JSON round-trip unchanged.
func TestCapabilityEntry_JSONRoundTrip(t *testing.T) {
	want := Capability(baseAdapter{}, sampleFixture())
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CapabilityEntry
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v\njson: %s", got, want, b)
	}
}
