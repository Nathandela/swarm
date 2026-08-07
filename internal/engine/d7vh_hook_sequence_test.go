package engine

// agents-tracker-d7vh (spike-SD F3/F4): the two live permission waits, replayed
// at engine level in the hook ORDER the capture recorded.
//
// BORN GREEN, deliberately. Fix A (529758e) already stops the trailing
// Notification from clobbering the interaction its own PermissionRequest set,
// and spike-SD confirms on live 2.1.224 that PermissionRequest fires for BOTH
// AskUserQuestion (F3) and ExitPlanMode (F4) — the shapes the adapter's hook
// mapping was never exercised against before. These are regression pins of that
// freshly-confirmed behavior: they were run before the grid changes in this set
// and passed unmodified. A red here would falsify the F3/F4 reading, not the
// grid work.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	askUserWaitFixture = "../../docs/verification/fixtures/spike-sd/askuser-wait.json"
	planWaitFixture    = "../../docs/verification/fixtures/spike-sd/plan-wait.json"
)

func TestD7VH_CapturedPermissionHookOrderHoldsNeedsInput(t *testing.T) {
	cases := []struct{ name, fixture, tool string }{
		{"AskUserQuestion", askUserWaitFixture, "AskUserQuestion"},
		{"ExitPlanMode", planWaitFixture, "ExitPlanMode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx, err := fixtureio.LoadFixture(tc.fixture)
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			hooks := fx.HookPayloads
			fields := make([]map[string]string, len(hooks))
			for i, h := range hooks {
				fields[i] = hookFields(t, h.Raw)
			}

			// The captured order itself is evidence: the tool's PreToolUse, then its
			// dedicated PermissionRequest, then the permission_prompt Notification
			// that trails it by ~6s and once wiped the permission (spike-SD F3/F4).
			pre := indexOfHook(hooks, fields, "PreToolUse", "tool_name", tc.tool)
			perm := indexOfHook(hooks, fields, "PermissionRequest", "tool_name", tc.tool)
			notif := indexOfHook(hooks, fields, "Notification", "notification_type", "permission_prompt")
			if pre < 0 || perm < 0 || notif < 0 || pre >= perm || perm >= notif {
				t.Fatalf("captured hook order is not PreToolUse{%s}(%d) -> PermissionRequest(%d) -> Notification{permission_prompt}(%d)",
					tc.tool, pre, perm, notif)
			}
			if notif != len(hooks)-1 {
				t.Fatalf("the permission_prompt Notification is hook %d of %d; expected it to close the capture", notif, len(hooks))
			}

			clk := newClock()
			rec := &emitRecorder{}
			e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
			e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))

			var groups []status.Group
			for i, h := range hooks {
				if i > 0 {
					clk.advance(time.Duration(h.ReceivedAtMs-hooks[i-1].ReceivedAtMs) * time.Millisecond)
				}
				cb := Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: h.Event, Payload: fields[i]}
				if err := e.HandleCallback(cb); err != nil {
					t.Fatalf("hook %d (%s): %v", i, h.Event, err)
				}
				// SessionStart maps to no status dimension, so the first hooks may emit
				// nothing at all; "" records that nothing has been committed yet.
				group := status.Group("")
				if got, ok := rec.last(); ok {
					group = status.Derive(got.s)
				}
				groups = append(groups, group)
			}

			if groups[perm] != status.GroupNeedsInput {
				t.Fatalf("PermissionRequest{%s} derived %v; want needs_input", tc.tool, groups[perm])
			}
			if groups[notif] != status.GroupNeedsInput {
				t.Fatalf("the trailing Notification{permission_prompt} left the session %v; want needs_input held", groups[notif])
			}
		})
	}
}

// hookFields pulls the payload fields this replay needs out of a captured raw
// hook body: the Notification subtype the claude descriptor maps by, and the
// tool name that identifies which wait the hook belongs to.
func hookFields(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var f struct {
		ToolName         string `json:"tool_name"`
		NotificationType string `json:"notification_type"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode hook payload: %v", err)
	}
	p := make(map[string]string, 2)
	if f.ToolName != "" {
		p["tool_name"] = f.ToolName
	}
	if f.NotificationType != "" {
		p["notification_type"] = f.NotificationType
	}
	return p
}

// indexOfHook returns the position of the first hook with the given event whose
// field carries want, or -1.
func indexOfHook(hooks []adapter.HookPayload, fields []map[string]string, event, field, want string) int {
	for i, h := range hooks {
		if h.Event == event && fields[i][field] == want {
			return i
		}
	}
	return -1
}
