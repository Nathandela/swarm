package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
)

const nativePromptID = "01234567-89ab-4cde-8abc-012345678901"
const nativeUserID = "01234567-89ab-4cde-8abc-012345678902"
const nativeErrorID = "01234567-89ab-4cde-8abc-012345678903"

func nativeFailureTranscript(conversation, prompt, request string) string {
	user, _ := json.Marshal(map[string]any{"type": "user", "uuid": nativeUserID, "parentUuid": nil, "sessionId": conversation, "promptId": prompt})
	failure, _ := json.Marshal(map[string]any{"type": "assistant", "uuid": nativeErrorID, "parentUuid": nativeUserID, "sessionId": conversation, "isApiErrorMessage": true, "error": "rate_limit", "apiErrorStatus": 429, "requestId": request, "message": map[string]string{"model": "<synthetic>"}})
	return string(user) + "\n" + string(failure) + "\n"
}

func TestNativeClaudeFailureModelRequiresExactCurrentTurnAncestry(t *testing.T) {
	conversation := migratedConversationID
	valid := nativeFailureTranscript(conversation, nativePromptID, "req_current")
	request, stale, err := readClaudeTerminalFailure(strings.NewReader(valid), conversation, nativePromptID, "rate_limit")
	if err != nil || stale || request != "req_current" {
		t.Fatalf("current native error not proved: %v", err)
	}
	for _, input := range []string{
		strings.Replace(valid, `"requestId":"req_current"`, `"requestId":"req_current","RequestId":"req_other"`, 1),
		strings.Replace(valid, `"promptId":"`+nativePromptID+`"`, `"promptId":"`+nativePromptID+`","PromptId":"`+nativeErrorID+`"`, 1),
		strings.Replace(valid, `"type":"assistant"`, `"type":"assistant","isSidechain":false,"IsSidechain":true`, 1),
		strings.Replace(valid, nativeUserID+`"`, nativeErrorID+`"`, 1),
		strings.Replace(valid, `"requestId":"req_current"`, `"requestId":""`, 1),
		strings.Replace(valid, `"parentUuid":"`+nativeUserID+`"`, `"parentUuid":"`+nativePromptID+`"`, 1),
		strings.Replace(valid, `"isApiErrorMessage":true`, `"isApiErrorMessage":false`, 1),
		valid[:len(valid)-10],
	} {
		if _, stale, err := readClaudeTerminalFailure(strings.NewReader(input), conversation, nativePromptID, "rate_limit"); err == nil && !stale {
			t.Fatal("ambiguous/stale native error became model authority")
		}
	}
	newPrompt, _ := json.Marshal(map[string]string{"type": "user", "uuid": nativePromptID, "parentUuid": nativeErrorID, "sessionId": conversation, "promptId": nativeErrorID})
	if _, stale, err := readClaudeTerminalFailure(strings.NewReader(valid+string(newPrompt)+"\n"), conversation, nativePromptID, "rate_limit"); err != nil || !stale {
		t.Fatal("new owner input did not invalidate the old failure")
	}
}

func nativeClaudeFailureFixture(t *testing.T) (*accountRotationManager, persist.Meta, string, string) {
	t.Helper()
	m, fake, source := inboxClaudeFixture(t)
	profile, err := m.store.ProfilePath(*source.AccountBinding)
	if err != nil {
		t.Fatal(err)
	}
	p, err := accountconfig.PrepareNative(m.w.stateDir, "claude", profile, source.Cwd, source.Env, []string{"claude"}, "", "", func(_ string, install func() error) error { return install() }, true)
	if err != nil {
		t.Fatal(err)
	}
	binding := *source.AccountBinding
	binding.ConfigurationGeneration = p.Generation
	source.AccountBinding = &binding
	source.AccountProjectionRef, source.CLIIdentity.Version, source.Env = p.Ref, "2.1.289", p.HarmlessEnv
	fake.add(source)
	original, err := accountconfig.NativeHistoryAuthority(m.w.stateDir, p.Ref, "claude", source.Cwd, p.Generation)
	if err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(original, "projects", claude.New().ProjectDirName(source.Cwd), source.ConversationID+".jsonl")
	diagnostic := filepath.Join(m.w.stateDir, source.ID, claudeDiagnosticsFile)
	if err := os.MkdirAll(filepath.Dir(diagnostic), 0700); err != nil {
		t.Fatal(err)
	}
	started, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "prompt_id": nativePromptID, "prompt": "synthetic owner input"})
	if err := m.NoteClaudeTurn(engine.Callback{SessionID: source.ID, Event: "UserPromptSubmit", Sequence: 6, Raw: started}); err != nil {
		t.Fatal(err)
	}
	if err := m.drainInbox(); err != nil {
		t.Fatal(err)
	}
	return m, source, transcript, diagnostic
}

func TestNativeClaudeFailureWaitsForBothRecordsAcrossRestart(t *testing.T) {
	m, source, transcript, diagnostic := nativeClaudeFailureFixture(t)
	callback, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "error": "rate_limit", "prompt_id": nativePromptID, "error_details": "synthetic-secret-never-persist"})
	if err := m.NoteClaudeFailure(engine.Callback{SessionID: source.ID, Event: "StopFailure", Sequence: 7, Raw: callback}); err != nil {
		t.Fatal(err)
	}
	if err := m.drainInbox(); err == nil {
		t.Fatal("unflushed evidence authorized recovery")
	}
	if _, exists := m.w.state.AccountRotations[source.ID]; exists {
		t.Fatal("missing proof initiated recovery")
	}
	accountTestPut(t, transcript, []byte(nativeFailureTranscript(source.ConversationID, nativePromptID, "req_current")))
	if err := m.drainInbox(); err == nil {
		t.Fatal("transcript without diagnostic authorized recovery")
	}
	accountTestPut(t, diagnostic, []byte(`{"event":"cli_api_error","data":{"model":"claude-sonnet-4-6","query_source":"repl_main_thread","error_kind":"rate_limit","api_error_type":"rate_limit_error","response":{"status":429,"request_id":"req_current"}}}`+"\n"))
	fresh, _ := rotationTestManager(t, m.store, m.w.stateDir, source)
	loaded, err := loadAuthWatchStateChecked(m.w.stateDir)
	fresh.w.state = loaded
	fresh.w.state.AccountRotations = make(map[string]accountRotationRecord)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.drainInbox(); err != nil {
		t.Fatal(err)
	}
	rec := fresh.w.state.AccountRotations[source.ID]
	if rec.Incident.Model != "claude-sonnet-4-6" {
		t.Fatal("recovery inherited stale/synthetic model")
	}
	raw, err := os.ReadFile(filepath.Join(m.w.stateDir, "auth-watch-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic-secret") || strings.Contains(string(raw), "req_current") {
		t.Fatal("native raw error or request ID entered recovery journal")
	}
}

func TestNativeClaudeFailureExpiresToModelHold(t *testing.T) {
	m, source, _, _ := nativeClaudeFailureFixture(t)
	input, ok := m.inboxRecord(source.ID, "failure", source.ConversationID, 8)
	if !ok {
		t.Fatal("missing source")
	}
	input.Class, input.TurnID, input.ReceivedAt = "quota", nativePromptID, time.Now().Add(-31*time.Second)
	if err := m.applyInbox(input); err != nil {
		t.Fatal(err)
	}
	if rec := m.w.state.AccountRotations[source.ID]; rec.Incident.Model != "" {
		t.Fatal("expired proof borrowed a launch default")
	}
}
