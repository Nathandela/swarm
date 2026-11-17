package voice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

func advanceControllerFixtureTurn(t *testing.T, c *Controller, r *controllerRuntimeFixture, id string) {
	t.Helper()
	r.status.Turn.State = "completed"
	r.status.ReadyForInput = true
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SubmitText(Utterance{ID: id, DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "next task"); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	nonce := r.sent[len(r.sent)-1].ManagerAdmission.Nonce
	r.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: id}
	r.status.ReadyForInput = false
	r.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: id, NativeCallID: "call-" + id}
}
func TestControllerBusinessIDsScopeToAdmissionAndOldServiceRefsRemainReadOnly(t *testing.T) {
	c, _, r, _ := provenControllerFixture(t)
	worker := &controllerWorkerFixture{}
	c.workers = worker
	call := ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"task1","agent":"codex","cwd":"/tmp","prompt":"Work"}`)}
	raw, err := c.ManagerCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		ActionID string `json:"action_id"`
	}
	if err = json.Unmarshal(raw, &first); err != nil || first.ActionID == "" {
		t.Fatal("missing stable service action reference", err)
	}
	if _, err = c.ManagerCall(context.Background(), call); err != nil || worker.launches != 1 {
		t.Fatal("exact retry did not reuse action", err)
	}
	advanceControllerFixtureTurn(t, c, r, "next-turn")
	raw, err = c.ManagerCall(context.Background(), call)
	if err != nil {
		t.Fatal("ordinary raw business ID reuse conflicted across inputs", err)
	}
	var second struct {
		ActionID string `json:"action_id"`
	}
	if err = json.Unmarshal(raw, &second); err != nil || second.ActionID == first.ActionID || worker.launches != 2 {
		t.Fatal("business identity was not admission-scoped", err)
	}
	statusArgs, _ := json.Marshal(StatusInput{ActionID: first.ActionID})
	raw, err = c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_status", Arguments: statusArgs})
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		ActionID string `json:"action_id"`
	}
	if err = json.Unmarshal(raw, &old); err != nil || old.ActionID != first.ActionID || worker.launches != 2 {
		t.Fatal("old service reference remapped to current action", err)
	}
	mutateArgs, _ := json.Marshal(CreateInput{ActionID: first.ActionID, Agent: "codex", Cwd: "/tmp", Prompt: "Work"})
	if _, err = c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: mutateArgs}); !errors.Is(err, ErrExpired) {
		t.Fatalf("old scope mutation was relabeled into a new action: %v", err)
	}
	if worker.launches != 2 {
		t.Fatal("old service reference launched another worker")
	}
}
func TestControllerPublicationIDsAndSpeechEpochsAdvanceWithoutRenewingRevokedProducer(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	call := ManagerToolCall{Name: "publish", Arguments: json.RawMessage(`{"output_id":"reply","sequence":1,"text":"Hello","final":false}`)}
	raw, err := c.ManagerCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var first Publication
	if err = json.Unmarshal(raw, &first); err != nil || first.SpeechEpoch == 0 {
		t.Fatal("missing durable speech epoch", err)
	}
	if err = c.StopSpeech("main-turn"); err != nil {
		t.Fatal(err)
	}
	call.Arguments = json.RawMessage(`{"output_id":"final","sequence":2,"text":"Text only","final":true}`)
	raw, err = c.ManagerCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var stopped Publication
	if err = json.Unmarshal(raw, &stopped); err != nil || stopped.Speak || stopped.SpeechEpoch != first.SpeechEpoch {
		t.Fatal("revoked producer acquired another speech epoch", err)
	}
	advanceControllerFixtureTurn(t, c, r, "new-turn")
	call.Arguments = json.RawMessage(`{"output_id":"reply","sequence":1,"text":"Hello again","final":true}`)
	raw, err = c.ManagerCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var second Publication
	if err = json.Unmarshal(raw, &second); err != nil || second.ID == first.ID || second.SpeechEpoch <= first.SpeechEpoch || !second.Speak {
		t.Fatal("new native input reused publication identity or playback epoch", err)
	}
	repeat, err := c.ManagerCall(context.Background(), call)
	if err != nil || string(repeat) != string(raw) {
		t.Fatal("exact publication retry changed epoch/identity", err)
	}
	r.status.Turn.State = "completed"
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.Compact(b, time.Now().Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if st.Conversations[b.ConversationID].LastSpeechEpoch != second.SpeechEpoch {
		t.Fatal("compaction reset durable speech epoch")
	}
}
