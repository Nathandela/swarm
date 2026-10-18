package skeleton

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/status"
	"testing"
)

func ocUnconfirmedToolRow(t *testing.T, id, parent string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"info": map[string]any{"id": id, "sessionID": "ses_root", "role": "assistant", "parentID": parent, "time": map[string]int64{"created": 3, "completed": 4}, "finish": "stop"}, "parts": []any{map[string]any{"id": "prt_denied", "sessionID": "ses_root", "messageID": id, "type": "tool", "tool": "bash", "callID": "call_denied", "state": map[string]any{"status": "error", "input": map[string]any{}, "error": "Permission rejected", "time": map[string]int64{"start": 3, "end": 4}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestOpenCodeRejectedToolIdleIsUnconfirmedAttentionNotCompletion(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	if err := s.reconcileNative(ocAttentionSnapshot(), e); err != nil {
		t.Fatal(err)
	}
	s.Apply(ocUserMessage("ses_root", "msg_requested", 1), e)
	s.Apply(ocUserTextPart("ses_root", "msg_requested", "prt_user", "run command"), e)
	if s.View().Attention {
		t.Fatal("new input before execution was presented as a rejected tool")
	}
	busy := ocAttentionSnapshot()
	busy.Statuses["ses_root"] = json.RawMessage(`{"type":"busy"}`)
	if err := s.reconcileNative(busy, e); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileMessage("msg_tool", ocUnconfirmedToolRow(t, "msg_tool", "msg_requested"), e); err != nil {
		t.Fatal(err)
	}
	if s.View().Attention {
		t.Fatal("ordinary continuing tool work was presented as idle uncertainty")
	}
	if err := s.reconcileNative(ocAttentionSnapshot(), e); err != nil {
		t.Fatal(err)
	}
	view := s.View()
	if !view.Attention || ocInput(view, "msg_requested").Outcome != OpenCodeOutcomeNone || status.Derive(openCodeProjectedStatus(view)) == status.GroupReadyForReview {
		t.Fatalf("tool rejection idle falsely became ready/completed: %+v", view)
	}
	// A later complete request is a different input; the old unresolved receipt stays old.
	s.Apply(ocUserMessage("ses_root", "msg_later", 10), e)
	s.Apply(ocUserTextPart("ses_root", "msg_later", "prt_later", "next request"), e)
	ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", "msg_later_done", "msg_later", 11, 12, "stop"))
	if s.View().Attention {
		t.Fatal("past unresolved tool outcome blocked later completed input")
	}
}
func TestOpenCodeLiveAPIErrorWithoutSessionErrorNeedsAttention(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	snap := ocAttentionSnapshot()
	snap.Questions = []json.RawMessage{ocQuestion("que_other", "ses_root")}
	if err := s.reconcileNative(snap, e); err != nil {
		t.Fatal(err)
	}
	s.Apply(ocUserMessage("ses_root", "msg_failed", 1), e)
	s.Apply(ocUserTextPart("ses_root", "msg_failed", "prt_user", "request"), e)
	s.Apply(ocAssistantError("ses_root", "msg_error", "msg_failed", 2, 0, "APIError"), e)
	view := s.View()
	if !view.Attention || view.Error == nil || view.Error.Name != "APIError" || len(view.Pending) != 1 || ocInput(view, "msg_failed").Outcome != OpenCodeOutcomeFailed {
		t.Fatalf("native failed input lost actionable attention or unrelated request: %+v", view)
	}
	if openCodeProjectedStatus(view).Interaction != status.InteractionPrompt {
		t.Fatal("provider error hid unrelated native question")
	}
}
func TestOpenCodeHistoricalFailureDoesNotBecomeFreshAttention(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	rows := []json.RawMessage{json.RawMessage(`{"info":{"id":"msg_old","sessionID":"ses_root","role":"user","time":{"created":1}},"parts":[{"id":"prt_old","sessionID":"ses_root","messageID":"msg_old","type":"text","text":"old"}]}`), json.RawMessage(`{"info":{"id":"msg_old_error","sessionID":"ses_root","role":"assistant","parentID":"msg_old","time":{"created":2,"completed":3},"error":{"name":"APIError","data":{"message":"old failure"}}},"parts":[]}`)}
	if err := s.ImportHistory(rows, e); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileNative(ocAttentionSnapshot(), e); err != nil {
		t.Fatal(err)
	}
	if s.View().Attention || s.View().Error != nil {
		t.Fatal("old imported provider failure became fresh attention")
	}
}
