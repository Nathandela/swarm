package engine

// Unit tests for deriveDims — the mapping-bridge normalization that turns a raw
// hook callback's {event, payload} into status dimensions via the session's
// registered SignalSources (session.sources, previously stored but never read).

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/status"
)

func mappingSources() []adapter.SignalSource {
	return []adapter.SignalSource{
		{Kind: "hook", Descriptor: map[string]string{"event": "Stop", "turn": "idle", "interaction": "none"}},
		{Kind: "hook", Descriptor: map[string]string{"event": "PreToolUse", "turn": "active", "interaction": "none"}},
		{Kind: "hook", Descriptor: map[string]string{
			"event": "Notification", "turn": "idle", "interaction": "permission",
			"subtype_field": "notification_type", "subtype_interaction": "idle=none;permission=permission",
		}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "prompt-marker"}},
	}
}

func TestDeriveDims(t *testing.T) {
	src := mappingSources()
	cases := []struct {
		name    string
		event   string
		payload map[string]string
		want    map[string]string
	}{
		{"event maps to turn+interaction", "Stop", nil,
			map[string]string{PayloadKeyTurn: "idle", PayloadKeyInteraction: "none"}},
		{"active event", "PreToolUse", nil,
			map[string]string{PayloadKeyTurn: "active", PayloadKeyInteraction: "none"}},
		{"notification default is permission", "Notification", nil,
			map[string]string{PayloadKeyTurn: "idle", PayloadKeyInteraction: "permission"}},
		{"notification idle subtype overrides to none", "Notification", map[string]string{"notification_type": "idle"},
			map[string]string{PayloadKeyTurn: "idle", PayloadKeyInteraction: "none"}},
		{"unknown subtype falls back to default", "Notification", map[string]string{"notification_type": "weird"},
			map[string]string{PayloadKeyTurn: "idle", PayloadKeyInteraction: "permission"}},
		{"explicit dims win over the descriptor", "Stop", map[string]string{PayloadKeyTurn: "active"},
			map[string]string{PayloadKeyTurn: "active"}},
		{"unmapped event yields no dims", "TotallyUnknown", nil, nil},
		{"empty event yields no dims", "", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveDims(src, tc.event, tc.payload)
			if len(got) != len(tc.want) {
				t.Fatalf("deriveDims(%q,%v) = %v; want %v", tc.event, tc.payload, got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("deriveDims(%q) [%q] = %q; want %q", tc.event, k, got[k], v)
				}
			}
		})
	}
}

// TestDeriveDims_UnmappedEventIsBenignNoOp proves an authenticated callback naming
// an event the adapter does not map (and carrying no explicit dims) is accepted as
// a no-op, not rejected — the grid heuristic still governs.
func TestDeriveDims_UnmappedEventIsBenignNoOp(t *testing.T) {
	var emitted bool
	eng := New(Config{Emit: func(string, status.Status) { emitted = true }})
	eng.RegisterSession("s1", "tok", 0, mappingSources())
	if err := eng.HandleCallback(Callback{SessionID: "s1", Token: "tok", Sequence: 1, Event: "SomethingUnmapped"}); err != nil {
		t.Fatalf("unmapped event should be a benign no-op, got error: %v", err)
	}
	if emitted {
		t.Errorf("an unmapped event emitted a status change; it should be a no-op")
	}
}
