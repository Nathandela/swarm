package engine

// agents-tracker-sskl FIX A: a Notification must never clobber the permission a
// dedicated PermissionRequest just committed.
//
// Live evidence (docs/verification/fixtures/spike-sb, 3/3 runs): claude fires
// PermissionRequest, then ~6s later a Notification carrying
// notification_type=permission_prompt. The descriptor's subtype table knew only
// idle/permission/prompt, so the real value fell through the old B5 rule (an
// unrecognized subtype asserts interaction=none) and the session flipped from
// needs_input to ready_for_review while the approval dialog was still on screen.
//
// Two independent guards, both pinned here: the adapter maps the real values,
// and an UNRECOGNIZED subtype now emits NO interaction dimension at all — a
// Notification the engine cannot interpret leaves the interaction it cannot
// confirm exactly as it found it. A payload with NO subtype field keeps the B5
// safe default (none): that is the absence of the whole refinement, not an
// unreadable value.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/status"
)

// claudeSignalSources are the real Claude adapter's declared sources, so these
// tests fail if the descriptor's subtype table drifts from the live values.
func claudeSignalSources(t *testing.T) []adapter.SignalSource {
	t.Helper()
	a, ok := registry.New("claude")
	if !ok {
		t.Fatal(`registry: unknown adapter "claude"`)
	}
	return a.SignalSources()
}

// The confirmed live sequence: PermissionRequest commits needs_input, then the
// trailing Notification arrives. Whatever its subtype, the session must still be
// waiting on the human.
func TestSSKL_NotificationDoesNotClobberPermissionRequest(t *testing.T) {
	cases := []struct {
		name    string
		subtype string
	}{
		{"real claude value", "permission_prompt"},
		{"a value this build has never seen", "elicitation_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			rec := &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
			e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))

			if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "PermissionRequest"}); err != nil {
				t.Fatalf("PermissionRequest: %v", err)
			}
			got, ok := rec.last()
			if !ok || status.Derive(got.s) != status.GroupNeedsInput {
				t.Fatalf("PermissionRequest derived %v (%+v); want needs_input", status.Derive(got.s), got.s)
			}

			clk.advance(6 * time.Second) // the measured PermissionRequest -> Notification gap
			if err := e.HandleCallback(Callback{
				SessionID: "s1", Token: "tok1", Sequence: 2, Event: "Notification",
				Payload: map[string]string{"notification_type": tc.subtype},
			}); err != nil {
				t.Fatalf("Notification: %v", err)
			}
			got, _ = rec.last()
			if status.Derive(got.s) != status.GroupNeedsInput {
				t.Fatalf("Notification{notification_type=%s} flipped the session to %v (%+v); want needs_input",
					tc.subtype, status.Derive(got.s), got.s)
			}
		})
	}
}

// The per-subtype dimensions the real descriptor derives, at the deriveDims seam.
func TestSSKL_NotificationSubtypeDims(t *testing.T) {
	src := claudeSignalSources(t)
	cases := []struct {
		name    string
		payload map[string]string
		want    map[string]string
	}{
		{"permission_prompt is a permission", map[string]string{"notification_type": "permission_prompt"},
			map[string]string{PayloadKeyTurn: "idle", PayloadKeyInteraction: "permission"}},
		{"idle_prompt is an idle nudge", map[string]string{"notification_type": "idle_prompt"},
			map[string]string{PayloadKeyTurn: "idle", PayloadKeyInteraction: "none"}},
		{"an unrecognized subtype emits no interaction dimension", map[string]string{"notification_type": "auth_success"},
			map[string]string{PayloadKeyTurn: "idle"}},
		{"no subtype field keeps the B5 safe default", nil,
			map[string]string{PayloadKeyTurn: "idle", PayloadKeyInteraction: "none"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveDims(src, "Notification", tc.payload)
			if len(got) != len(tc.want) {
				t.Fatalf("deriveDims(Notification, %v) = %v; want %v", tc.payload, got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("deriveDims(Notification, %v)[%q] = %q; want %q", tc.payload, k, got[k], v)
				}
			}
		})
	}
}
