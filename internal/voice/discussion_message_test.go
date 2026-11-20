package voice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestDiscussionWorkerMessageIsScopedAndRetrySafe(t *testing.T) {
	c, store, _, binding := newControllerFixture(t)
	origin := discussionFixtureOrigin("initial")
	origin.DiscussionID, origin.Revision = "discussion", "revision"
	var changed atomic.Bool
	c.discussion = &DiscussionScope{DiscussionID: origin.DiscussionID, InstallationID: origin.InstallationID, GuildID: origin.GuildID, ThreadID: origin.ChannelID,
		VerifyGrant: func(_ context.Context, actual schema.MessageOrigin) error {
			if changed.Load() || actual != origin {
				return ErrDiscussionSourceChanged
			}
			return nil
		}}
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, local, _ := protocol.ParseID(binding.SessionID)
	endpoint, _, _ := protocol.ParseID(binding.SessionID)
	token := strings.Repeat("ab", 32)
	worker := WorkerAssociation{Project: project, ConversationID: binding.ConversationID, Session: endpoint + "/worker", SessionInstance: "exact", JobID: "job", AttemptID: "launch"}
	raw, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Origin: &origin, Launch: &protocol.LaunchReq{Agent: "claude", WorkerBridge: "/tmp/bridge.sock:" + token}})
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
	if got, err := ReadDiscussionWorkerReply(ctx, bridge, "worker", operation); err != nil || got != "" {
		t.Fatalf("pending worker reply = %q, %v", got, err)
	}
	for _, wake := range st.Wakes {
		event := st.Events[key(binding.ConversationID, wake.EventID)]
		if !event.WorkerMessage || event.WorkerProof || event.AttemptID != worker.AttemptID ||
			!strings.Contains(string(wake.Request), "I need clarification") {
			t.Fatalf("message custody: %+v %+v", event, wake)
		}
		a := st.Admissions[admissionKey(binding.ConversationID, wake.Admission)]
		a.Phase, a.NativeInputID, a.NativeTurnID = "running", "input", "turn"
		if err := store.update(func(st *State) error {
			putRecord(st, "admissions", &st.Admissions, admissionKey(binding.ConversationID, a.Sequence), a)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		changed.Store(true)
		if err := c.replyToDiscussionWorker(ctx, a, "Use the second fixture."); err != ErrDiscussionSourceChanged {
			t.Fatalf("edited source reply = %v", err)
		}
		changed.Store(false)
		if err := c.replyToDiscussionWorker(ctx, a, "Use the second fixture."); err != nil {
			t.Fatal(err)
		}
		if err := c.replyToDiscussionWorker(ctx, a, "Use the first fixture."); err == nil {
			t.Fatal("changed reply overwrote the confirmed response")
		}
		got, err := ReadDiscussionWorkerReply(ctx, bridge, "worker", operation)
		if err != nil || got != "Use the second fixture." {
			t.Fatalf("worker reply = %q, %v", got, err)
		}
		if err := SendDiscussionWorkerMessage(ctx, bridge, "worker", operation, "I need clarification"); err != nil {
			t.Fatalf("reply changed the message retry: %v", err)
		}
		if _, err := ReadDiscussionWorkerReply(ctx, bridge, "different", operation); err == nil {
			t.Fatal("reply visible to an unrelated worker")
		}
		changed.Store(true)
		if got, err := ReadDiscussionWorkerReply(ctx, bridge, "worker", operation); got != "" || err == nil || !strings.Contains(err.Error(), "discussion_source_changed") {
			t.Fatalf("stored reply exposed after source edit: %q, %v", got, err)
		}
		changed.Store(false)
		if err := store.update(func(st *State) error {
			retired := worker
			retired.Ended = true
			putRecord(st, "workers", &st.Workers, key(binding.ConversationID, key(worker.Session, worker.SessionInstance)), retired)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadDiscussionWorkerReply(ctx, bridge, "worker", operation); got != "" || err == nil || !strings.Contains(err.Error(), "stale") {
			t.Fatalf("stored reply visible to retired worker: %q, %v", got, err)
		}
	}
}
