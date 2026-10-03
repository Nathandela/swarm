package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

func TestManagedFailureClosesOnlyParentAndNeverRepeatsProviderDetails(t *testing.T) {
	managed := ManagedObservations()
	for _, code := range []string{"rate_limit", "authentication_failed"} {
		raw, _ := json.Marshal(map[string]string{"error": code, "error_details": "private-bearer-that-must-not-be-published"})
		items := managed.Interactions(adapter.HookPayload{Event: "StopFailure", Raw: raw})
		if len(items) != 1 || items[0].Kind != adapter.KindAgentMessage || items[0].Status != adapter.StatusFailed {
			t.Fatal("failure did not close parent turn")
		}
		if err := items[0].Validate(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(items[0].Text, "private-bearer") {
			t.Fatal("provider detail escaped")
		}
	}
	if got := managed.Interactions(adapter.HookPayload{Event: "StopFailure", Raw: json.RawMessage(`{"error":"rate_limit","agent_id":"child"}`)}); len(got) != 0 {
		t.Fatal("child failure closed parent")
	}
	if got := managed.Interactions(adapter.HookPayload{Event: "StopFailure", Raw: json.RawMessage(`{"error":{"type":"rate_limit"}}`)}); len(got) != 0 {
		t.Fatal("invented failure schema accepted")
	}
	if got := New().Interactions(adapter.HookPayload{Event: "StopFailure", Raw: json.RawMessage(`{"error":"rate_limit"}`)}); len(got) != 0 {
		t.Fatal("managed events enabled on ordinary adapter")
	}
	argv, err := managed.Command(adapter.LaunchSpec{})
	if err != nil || strings.Contains(strings.Join(argv, " "), "StopFailure") {
		t.Fatal("unversioned command installed managed hooks")
	}
	rows := adapter.CaptureEvents(managed)
	if !strings.Contains(strings.Join(rows, ","), "StopFailure") {
		t.Fatal("failure capture not declared")
	}
}
