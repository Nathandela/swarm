package claude

// FIX 3 mapping-bridge proof (Epic 11 seam c): a real-format Claude hook payload,
// extracted as `swarm hook` extracts a stdin payload and posted to the engine,
// drives the correct status PURELY through the adapter's declared SignalSources
// (the engine reads session.sources to normalize event -> status). This is the
// behavior audit-009 finding #3 required and that the frozen suite did not cover:
// the status table was dead data until the engine read it.

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
)

// TestHookMapping_DrivesStatusViaSignalSources posts hook events through the engine
// with the Claude adapter's SignalSources and asserts the derived status:
//   - Stop (from the fixture)      -> turn idle, interaction none
//   - PermissionRequest            -> turn idle, interaction permission (dedicated)
//   - Notification, idle subtype   -> turn idle, interaction NONE (not a false permission)
//   - Notification (from fixture)  -> turn idle, interaction permission (default, real permission nudge)
func TestHookMapping_DrivesStatusViaSignalSources(t *testing.T) {
	sources := New().SignalSources()
	fx := loadFixture(t)

	// Extract each fixture hook payload's string fields exactly as `swarm hook`
	// extracts a Claude stdin payload, keyed by event.
	fixturePayload := map[string]map[string]string{}
	for _, hp := range fx.HookPayloads {
		fixturePayload[hp.Event] = extractStringFields(t, hp.Raw)
	}

	cases := []struct {
		name      string
		event     string
		payload   map[string]string
		wantTurn  status.Turn
		wantInter status.Interaction
	}{
		{"Stop from fixture -> idle", "Stop", fixturePayload["Stop"], status.TurnIdle, status.InteractionNone},
		{"PermissionRequest is the dedicated permission event", "PermissionRequest", nil, status.TurnIdle, status.InteractionPermission},
		// B5: a bare Notification (no confirmed subtype — the fixture's is such) must
		// NOT be assumed a permission prompt; it degrades to none. A permission signal
		// comes from the dedicated PermissionRequest event, or an explicit subtype.
		{"Notification with no subtype degrades to none (B5)", "Notification", fixturePayload["Notification"], status.TurnIdle, status.InteractionNone},
		{"Notification idle subtype -> none", "Notification", map[string]string{"notification_type": "idle"}, status.TurnIdle, status.InteractionNone},
		{"Notification explicit permission subtype -> permission", "Notification", map[string]string{"notification_type": "permission"}, status.TurnIdle, status.InteractionPermission},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, emitted := deriveViaEngine(t, sources, tc.event, tc.payload)
			if !emitted {
				t.Fatalf("event %q drove no status change; the engine did not derive a status via SignalSources", tc.event)
			}
			if got.Turn != tc.wantTurn {
				t.Errorf("event %q derived turn=%q; want %q", tc.event, got.Turn, tc.wantTurn)
			}
			if got.Interaction != tc.wantInter {
				t.Errorf("event %q derived interaction=%q; want %q", tc.event, got.Interaction, tc.wantInter)
			}
		})
	}
}

// deriveViaEngine registers a fresh session with sources and posts a single
// authenticated callback (sequence 1) for event+payload, returning the status the
// engine derived and emitted. A fresh engine per call keeps the anti-replay
// high-water clean so each case is independent.
func deriveViaEngine(t *testing.T, sources []adapter.SignalSource, event string, payload map[string]string) (status.Status, bool) {
	t.Helper()
	var got status.Status
	emitted := false
	eng := engine.New(engine.Config{
		StalenessThreshold: 0,
		Emit:               func(_ string, s status.Status) { got = s; emitted = true },
	})
	eng.RegisterSession("s1", "tok", 0, sources)
	if err := eng.HandleCallback(engine.Callback{
		SessionID: "s1", Token: "tok", Sequence: 1, Event: event, Payload: payload,
	}); err != nil {
		t.Fatalf("HandleCallback(%s): %v", event, err)
	}
	return got, emitted
}

// extractStringFields mirrors `swarm hook`'s stdin extraction: top-level string
// fields of a raw JSON object (skipping the reserved dimension keys).
func extractStringFields(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal fixture raw: %v", err)
	}
	out := map[string]string{}
	for k, v := range obj {
		if k == "turn" || k == "interaction" {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			out[k] = s
		}
	}
	return out
}
