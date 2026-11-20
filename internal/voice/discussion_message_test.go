package voice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestDiscussionWorkerMessageIsScopedAndRetrySafe(t *testing.T) {
	c, store, _, binding := newControllerFixture(t)
	c.discussion = &DiscussionScope{}
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, local, _ := protocol.ParseID(binding.SessionID)
	endpoint, _, _ := protocol.ParseID(binding.SessionID)
	token := strings.Repeat("ab", 32)
	worker := WorkerAssociation{Project: project, ConversationID: binding.ConversationID, Session: endpoint + "/worker", SessionInstance: "exact", JobID: "job", AttemptID: "launch"}
	raw, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Launch: &protocol.LaunchReq{WorkerBridge: "/tmp/bridge.sock:" + token}})
	if err = store.update(func(st *State) error {
		putRecord(st, "workers", &st.Workers, key(binding.ConversationID, key(worker.Session, worker.SessionInstance)), worker)
		putRecord(st, "actions", &st.Actions, key(binding.ConversationID, worker.AttemptID), Action{State: "settled", Request: raw})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	operation, _ := protocol.NewSemanticOperationID()
	ctx := context.Background()
	handler := newManagerHandler(binding.ConversationID, strings.Repeat("ef", 32))
	handler.bindWorker(c.acceptDiscussionWorkerMessage)
	server, err := startPrivateHTTP(ctx, privateServerDir(t), handler)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	bridge := server.socket + ":" + token
	for _, request := range []struct{ bridge, session string }{
		{bridge, local},
		{server.socket + ":" + strings.Repeat("cd", 32), "worker"},
		{server.socket + ":" + strings.Repeat("ef", 32), "worker"},
	} {
		if err := SendDiscussionWorkerMessage(ctx, request.bridge, request.session, operation, "wrong identity"); err == nil || !strings.Contains(err.Error(), "not_found") {
			t.Fatalf("wrong worker identity got %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := SendDiscussionWorkerMessage(ctx, bridge, "worker", operation, "I need clarification"); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if err := SendDiscussionWorkerMessage(ctx, bridge, "worker", operation, "changed"); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("changed payload got %v", err)
	}
	st, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Wakes) != 1 || len(st.Admissions) != 1 || len(st.Journal) != 0 {
		t.Fatalf("wake was duplicated or changed daemon cursor: %+v", st)
	}
	for _, wake := range st.Wakes {
		event := st.Events[key(binding.ConversationID, wake.EventID)]
		if !event.WorkerMessage || event.WorkerProof || event.AttemptID != worker.AttemptID ||
			!strings.Contains(string(wake.Request), "I need clarification") {
			t.Fatalf("message custody: %+v %+v", event, wake)
		}
	}
}
