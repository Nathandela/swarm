package codex

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

func TestSessionNameSyncBuildsThreadNameSetRequest(t *testing.T) {
	syncer, ok := adapter.AsSessionNameSync(New())
	if !ok {
		t.Fatal("Codex adapter does not expose session-name sync")
	}
	req, ok := syncer.SetSessionName("01a00339-a80e-72a0-966f-116427b6b9ce", "parser cleanup")
	if !ok {
		t.Fatal("SetSessionName refused a canonical Codex thread id")
	}
	if req.Method != "thread/name/set" {
		t.Fatalf("method = %q, want thread/name/set", req.Method)
	}
	if req.Params["threadId"] != "01a00339-a80e-72a0-966f-116427b6b9ce" || req.Params["name"] != "parser cleanup" {
		t.Fatalf("params = %#v, want threadId and name", req.Params)
	}
}

func TestSessionNameSyncReadsThreadNameUpdatedNotification(t *testing.T) {
	syncer, ok := adapter.AsSessionNameSync(New())
	if !ok {
		t.Fatal("Codex adapter does not expose session-name sync")
	}
	p := adapter.HookPayload{
		Event: "thread/name/updated",
		Raw:   json.RawMessage(`{"method":"thread/name/updated","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","threadName":"native rename"}}`),
	}
	threadID, name, ok := syncer.SessionNameFromEvent(p)
	if !ok || threadID != "01a00339-a80e-72a0-966f-116427b6b9ce" || name != "native rename" {
		t.Fatalf("SessionNameFromEvent = (%q, %q, %v), want native update", threadID, name, ok)
	}
}

func TestSessionNameSyncRejectsNotificationWithoutName(t *testing.T) {
	syncer, _ := adapter.AsSessionNameSync(New())
	p := adapter.HookPayload{
		Event: "thread/name/updated",
		Raw:   json.RawMessage(`{"method":"thread/name/updated","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce"}}`),
	}
	if threadID, name, ok := syncer.SessionNameFromEvent(p); ok {
		t.Fatalf("SessionNameFromEvent = (%q, %q, true) for a notification with no threadName", threadID, name)
	}
}
