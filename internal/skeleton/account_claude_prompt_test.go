package skeleton

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
	"testing"
)

func TestNativeDelayedClaudeFailureCannotClaimNewPrompt(t *testing.T) {
	m, source, transcript, diagnostic := nativeClaudeFailureFixture(t)
	accountTestPut(t, transcript, []byte(nativeFailureTranscript(source.ConversationID, nativePromptID, "req_current")))
	accountTestPut(t, diagnostic, []byte(`{"event":"cli_api_error","data":{"model":"claude-sonnet-4-6","query_source":"repl_main_thread","error_kind":"rate_limit","api_error_type":"rate_limit_error","response":{"status":429,"request_id":"req_current"}}}`+"\n"))
	failure, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "error": "rate_limit", "prompt_id": nativePromptID})
	if e := m.NoteClaudeFailure(engine.Callback{SessionID: source.ID, Event: "StopFailure", Sequence: 7, Raw: failure}); e != nil {
		t.Fatal(e)
	}
	newer, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "prompt_id": nativeErrorID, "prompt": "new owner input"})
	if e := m.NoteClaudeTurn(engine.Callback{SessionID: source.ID, Event: "UserPromptSubmit", Sequence: 8, Raw: newer}); e != nil {
		t.Fatal(e)
	}
	_ = m.drainInbox()
	if _, exists := m.w.state.AccountRotations[source.ID]; exists {
		t.Fatal("old failure claimed despite later authenticated UserPromptSubmit awaiting transcript flush")
	}
}

func TestNativeClaudeExpiredFailureNeverRefillsFromLaterTurn(t *testing.T) {
	m, source, _, _ := nativeClaudeFailureFixture(t)
	input, _ := m.inboxRecord(source.ID, "failure", source.ConversationID, 7)
	input.Class = "quota"
	if e := m.applyInbox(input); e != nil {
		t.Fatal(e)
	}
	if e := m.noteModel(source, "claude-opus-4-6", 9); e != nil {
		t.Fatal(e)
	}
	m.stepRecord(m.w.state.AccountRotations[source.ID])
	if rec := m.w.state.AccountRotations[source.ID]; !rec.NativeClaudeFailure || rec.Incident.Model != "" {
		t.Fatal("native proof hold was refilled by a later model")
	}
}

func TestNativeClaudePromptFenceRejectsLaterAdmissionBeforeStop(t *testing.T) {
	m, source, _, _ := nativeClaudeFailureFixture(t)
	rec := accountRotationRecord{SourceID: source.ID, SourceBinding: *source.AccountBinding, NativeClaudeFailure: true, FailedPromptID: nativePromptID, FailedPromptSequence: 7}
	stopped := false
	if e := m.withClaudeFailureFence(rec, func() error { stopped = true; return nil }); e != nil || !stopped {
		t.Fatal("exact current turn was refused")
	}
	newer, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "prompt_id": nativeErrorID, "prompt": "new owner input"})
	if e := m.NoteClaudeTurn(engine.Callback{SessionID: source.ID, Event: "UserPromptSubmit", Sequence: 8, Raw: newer}); e != nil {
		t.Fatal(e)
	}
	stopped = false
	if e := m.withClaudeFailureFence(rec, func() error { stopped = true; return nil }); e == nil || stopped {
		t.Fatal("new admission did not fence old kill")
	}
	if e := m.drainInbox(); e != nil {
		t.Fatal(e)
	}
	source.Status.Turn = status.TurnIdle
	if m.claimableAccountSource(source, rec) {
		t.Fatal("retiring the newer inbox fact restored old claim authority")
	}
	loaded, e := loadAuthWatchStateChecked(m.w.stateDir)
	if e != nil {
		t.Fatal(e)
	}
	m.w.state = loaded
	m.inboxPrompts = nil
	if m.currentClaudeFailure(rec) {
		t.Fatal("restart restored stale failed prompt authority")
	}
}
func TestNativeMissingClaudePromptCannotBorrowModel(t *testing.T) {
	m, source, _, _ := nativeClaudeFailureFixture(t)
	if e := m.noteModel(source, "claude-sonnet-4-6", 7); e != nil {
		t.Fatal(e)
	}
	if m.effectiveModel(source) != "claude-sonnet-4-6" {
		t.Fatal("cached-model positive control missing")
	}
	failure, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "error": "rate_limit"})
	if e := m.NoteClaudeFailure(engine.Callback{SessionID: source.ID, Event: "StopFailure", Sequence: 9, Raw: failure}); e != nil {
		t.Fatal(e)
	}
	_ = m.drainInbox()
	if rec, exists := m.w.state.AccountRotations[source.ID]; exists && rec.Incident.Model != "" {
		t.Fatal("missing prompt ID borrowed previous model authority")
	}
}
