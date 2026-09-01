package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

type contextGuardViewStub struct {
	*stubDaemon
	view    ContextGuardView
	present bool
}

func (s *contextGuardViewStub) ContextGuardView(string) (ContextGuardView, bool) {
	return s.view, s.present
}

func TestContextGuardViewOwnerOnlyAndOptional(t *testing.T) {
	stub := &contextGuardViewStub{
		stubDaemon: newStubDaemon(),
		present:    true,
		view: ContextGuardView{
			UsagePercent: 84,
			Support:      "observe_only",
			Phase:        "pending_idle",
			LastResult:   "compacted",
			ErrorCode:    "action_unverified",
		},
	}
	m := persist.Meta{ID: "s1", AgentType: "codex"}
	owner := &Server{d: stub}
	got := owner.stampView("ep", m, status.GroupWorking, false, false, time.Time{})
	if got.ContextGuard == nil || *got.ContextGuard != stub.view {
		t.Fatalf("owner context guard = %#v, want %#v", got.ContextGuard, stub.view)
	}

	remote := &Server{d: stub, remoteTier: true}
	if got := remote.stampView("ep", m, status.GroupWorking, false, false, time.Time{}); got.ContextGuard != nil {
		t.Fatalf("remote roster leaked context guard: %#v", got.ContextGuard)
	}

	stub.present = false
	if got := owner.stampView("ep", m, status.GroupWorking, false, false, time.Time{}); got.ContextGuard != nil {
		t.Fatalf("unsupported session carried context guard: %#v", got.ContextGuard)
	}
}

func TestContextGuardViewOmitsAbsentAndCarriesNoRawFields(t *testing.T) {
	view := stampView("ep", persist.Meta{ID: "s1"}, status.GroupWorking, false, false, time.Time{})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["context_guard"]; ok {
		t.Fatalf("absent context guard serialized: %s", raw)
	}

	guardRaw, err := json.Marshal(ContextGuardView{UsagePercent: 80, Support: "observe_only", Phase: "armed", LastResult: "compacted"})
	if err != nil {
		t.Fatal(err)
	}
	var guardBody map[string]json.RawMessage
	if err := json.Unmarshal(guardRaw, &guardBody); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"raw", "payload", "provider_error", "transcript_path"} {
		if _, ok := guardBody[forbidden]; ok {
			t.Fatalf("sanitized guard unexpectedly carries %q", forbidden)
		}
	}
}
