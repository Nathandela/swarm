package skeleton

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/status"
)

func ocAttentionSnapshot() opencodebridge.Snapshot {
	return opencodebridge.Snapshot{Identity: opencodebridge.Identity{Version: 1, RootID: "ses_root", BackendGeneration: "gen-1"},
		Statuses: map[string]json.RawMessage{}, Messages: []json.RawMessage{}, Permissions: []json.RawMessage{}, Questions: []json.RawMessage{}, Children: []json.RawMessage{}, Descendants: []json.RawMessage{}, HierarchyComplete: true, HierarchyScope: "current_activity_requests"}
}

func TestOpenCodeAttentionRejectsMissingAndNullNativeRequestFields(t *testing.T) {
	for _, raw := range []string{
		`{"id":"per_a","sessionID":"ses_root","permission":"bash","patterns":[null],"always":[],"metadata":{}}`,
		`{"id":"que_a","sessionID":"ses_root","questions":[{"question":"Which?","options":[]}]}`,
		`{"id":"que_a","sessionID":"ses_root","questions":[{"header":"Choose","question":"Which?","custom":null,"options":[]}]}`,
		`{"id":"que_a","sessionID":"ses_root","questions":[{"header":"Choose","question":"Which?","multiple":null,"options":[]}]}`,
		`{"id":"que_a","sessionID":"ses_root","questions":[{"header":"Choose","question":"Which?","options":[{"label":"a"}]}]}`,
	} {
		kind := "question"
		if strings.Contains(raw, `"permission"`) {
			kind = "permission"
		}
		if _, err := decodeOpenCodeRequest(kind, json.RawMessage(raw)); err == nil {
			t.Fatalf("malformed required native request field accepted: %s", raw)
		}
	}
}

func TestOpenCodeAttentionMalformedHierarchyEventWithdrawsAuthority(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	if err := s.reconcileNative(ocAttentionSnapshot(), e); err != nil {
		t.Fatal(err)
	}
	if got := s.Apply([]byte(`{"type":"session.deleted","properties":{}}`), e); got.Disposition != OCNeedsSnapshot {
		t.Fatal("unscoped hierarchy mutation silently ignored")
	}
	if s.View().BusyKnown {
		t.Fatal("malformed hierarchy event retained idle proof")
	}
}

func ocNativeChild(id, parent string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"id": id, "parentID": parent, "directory": "/project", "title": id, "time": map[string]int64{"created": 1000}})
	return raw
}

func ocPermission(id, session string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"id": id, "sessionID": session, "permission": "bash", "patterns": []string{"go test *"}, "always": []string{}, "metadata": map[string]any{}})
	return raw
}

func ocQuestion(id, session string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"id": id, "sessionID": session, "questions": []any{map[string]any{"header": "Destination", "question": "Choose targets", "multiple": true, "custom": false, "options": []any{map[string]string{"label": "local", "description": "This machine"}, map[string]string{"label": "remote", "description": "Other machine"}}}}})
	return raw
}

func TestOpenCodeAttentionCoexistsWithSiblingWork(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	snap := ocAttentionSnapshot()
	snap.Descendants = []json.RawMessage{ocNativeChild("ses_child1", "ses_root"), ocNativeChild("ses_child2", "ses_child1")}
	snap.Children = snap.Descendants[:1]
	snap.Statuses["ses_child2"] = json.RawMessage(`{"type":"busy"}`)
	snap.Permissions = []json.RawMessage{ocPermission("per_root", "ses_root"), ocPermission("per_child", "ses_child1")}
	snap.Questions = []json.RawMessage{ocQuestion("que_child", "ses_child2")}
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	v := s.View()
	if !v.BusyKnown || !v.Busy || v.RootBusy || !v.Attention || len(v.Pending) != 3 || len(v.Descendants) != 2 {
		t.Fatalf("lost simultaneous native facts: %+v", v)
	}
	st := openCodeProjectedStatus(v)
	if st.Turn != status.TurnActive || !st.Attention || status.Derive(st) != status.GroupNeedsInput {
		t.Fatalf("sibling work hid pending attention: %+v", st)
	}
	var question *OpenCodeRequest
	for i := range v.Pending {
		if v.Pending[i].Kind == "question" {
			question = &v.Pending[i]
		}
	}
	if question == nil || !question.Questions[0].Multiple || question.Questions[0].Custom == nil || *question.Questions[0].Custom {
		t.Fatal("native question semantics were flattened")
	}
	question.Raw[0] = '!'
	if !json.Valid(s.View().Pending[2].Raw) {
		t.Fatal("view mutated native request authority")
	}
}

func TestOpenCodeAttentionReconcilesEachNativeRequestIndependently(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	snap := ocAttentionSnapshot()
	snap.Permissions = []json.RawMessage{ocPermission("per_a", "ses_root"), ocPermission("per_b", "ses_root")}
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	snap.Permissions = snap.Permissions[1:]
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	if v := s.View(); len(v.Pending) != 1 || v.Pending[0].ID != "per_b" || !v.Attention {
		t.Fatalf("resolution removed another request: %+v", v)
	}
	snap.Permissions = nil
	if err := s.reconcileNative(snap, e); err == nil {
		t.Fatal("missing request snapshot became empty authority")
	}
	if v := s.View(); len(v.Pending) != 1 || !v.Attention || !v.Degraded {
		t.Fatalf("failed read erased native attention: %+v", v)
	}
	snap.Permissions = []json.RawMessage{}
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	if v := s.View(); len(v.Pending) != 0 || v.Attention || v.Degraded {
		t.Fatalf("successful current read did not resolve attention: %+v", v)
	}
}

func TestOpenCodeAttentionRequiresCompleteVerifiedCurrentAncestry(t *testing.T) {
	for _, kind := range []string{"partial", "foreign-parent", "cycle", "duplicate", "wrong-scope"} {
		t.Run(kind, func(t *testing.T) {
			s, e := newOCState(t, "ses_root")
			snap := ocAttentionSnapshot()
			snap.Descendants = []json.RawMessage{ocNativeChild("ses_child1", "ses_root")}
			switch kind {
			case "partial":
				snap.HierarchyComplete = false
			case "foreign-parent":
				snap.Descendants[0] = ocNativeChild("ses_child1", "ses_foreign")
			case "cycle":
				snap.Descendants[0] = ocNativeChild("ses_child1", "ses_child1")
			case "duplicate":
				snap.Descendants = append(snap.Descendants, snap.Descendants[0])
			case "wrong-scope":
				snap.HierarchyScope = "all_history"
			}
			if err := s.reconcileNative(snap, e); err == nil {
				t.Fatal("unverified hierarchy admitted")
			}
			if v := s.View(); v.BusyKnown || !v.Degraded {
				t.Fatal("invalid scope granted idle authority")
			}
		})
	}
}

func TestOpenCodeAttentionForeignRequestsDoNotBecomeWorkerAuthority(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	snap := ocAttentionSnapshot()
	snap.Permissions = []json.RawMessage{ocPermission("per_foreign", "ses_foreign")}
	snap.Statuses["ses_foreign"] = json.RawMessage(`{"type":"busy"}`)
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	if v := s.View(); v.Busy || v.Attention || len(v.Pending) != 0 || !v.BusyKnown {
		t.Fatalf("foreign root changed owned state: %+v", v)
	}
}

func TestOpenCodeAttentionEpochRetainsRequestsWithoutTheirAuthority(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	snap := ocAttentionSnapshot()
	snap.Permissions = []json.RawMessage{ocPermission("per_a", "ses_root")}
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	e = s.BeginEpoch()
	if v := s.View(); len(v.Pending) != 1 || !v.Attention || v.BusyKnown || !v.Degraded {
		t.Fatalf("reconnect silently cleared pending request: %+v", v)
	}
	snap.Permissions = []json.RawMessage{}
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	if v := s.View(); v.Attention || v.Degraded || !v.BusyKnown {
		t.Fatalf("qualified current scope did not recover: %+v", v)
	}
}

func TestOpenCodeAttentionEventsRequireFreshCurrentSet(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	snap := ocAttentionSnapshot()
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(opencodebridge.Event{Type: "permission.asked", Properties: ocPermission("per_old", "ses_root")})
	if got := s.Apply(raw, e); got.Disposition != OCNeedsSnapshot {
		t.Fatalf("request event bypassed current-set proof: %+v", got)
	}
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	if v := s.View(); v.Attention || len(v.Pending) != 0 {
		t.Fatal("late already-resolved request was resurrected")
	}
}

func TestOpenCodeAttentionUnknownChildActivityWithdrawsIdle(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	snap := ocAttentionSnapshot()
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	if got := s.Apply([]byte(`{"type":"session.status","properties":{"sessionID":"ses_new_child","status":{"type":"busy"}}}`), e); got.Disposition != OCNeedsSnapshot {
		t.Fatal("unknown active native scope was silently ignored")
	}
	if s.View().BusyKnown {
		t.Fatal("unknown child activity retained idle authority")
	}
	snap.Descendants = []json.RawMessage{ocNativeChild("ses_new_child", "ses_root")}
	snap.Children = snap.Descendants
	snap.Statuses["ses_new_child"] = json.RawMessage(`{"type":"busy"}`)
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	if v := s.View(); !v.Busy || !v.BusyKnown {
		t.Fatal("verified child activity was not adopted")
	}
}
