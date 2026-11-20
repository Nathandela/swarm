package voice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type discussionSendWorkers struct {
	controllerWorkerFixture
	sent []string
}

func (w *discussionSendWorkers) SemanticSend(operation string, _ protocol.ComposerSendReq) error {
	w.sent = append(w.sent, operation)
	return nil
}

func (w *discussionSendWorkers) SemanticOperation(session, operation string) (protocol.SemanticMessageReceipt, error) {
	return protocol.SemanticMessageReceipt{Session: session, SessionInstance: "worker-instance", OperationID: operation, State: "admitted"}, nil
}

func TestDiscussionSendWaitsForExactIdleWorker(t *testing.T) {
	c, st, _, binding := newControllerFixture(t)
	origin := schema.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "author", RequesterID: "author",
		DiscussionID: "discussion", Revision: "revision", GuildID: "guild", ChannelID: "thread", MessageID: "message"}
	grants := 0
	c.discussion = &DiscussionScope{
		DiscussionID: "discussion", InstallationID: "install", GuildID: "guild", ThreadID: "thread",
		VerifyOrigin: func(context.Context, schema.MessageOrigin) error {
			t.Fatal("queued send rechecked a pre-cutover source")
			return nil
		},
		VerifyGrant: func(_ context.Context, selected schema.MessageOrigin) error {
			if selected != origin {
				t.Fatalf("wrong retained grant: %+v", selected)
			}
			grants++
			return nil
		},
	}
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worker := WorkerAssociation{Project: project, Session: "machine/worker", SessionInstance: "worker-instance", JobID: "job", AttemptID: "attempt"}
	to := key(worker.Session, worker.SessionInstance)
	workers := &discussionSendWorkers{controllerWorkerFixture: controllerWorkerFixture{rows: []protocol.SessionView{{ID: worker.Session, Agent: "codex", Capabilities: &schema.SessionCapabilities{SessionInstance: worker.SessionInstance, NativeOwnerInput: true}, Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnActive}}}}}
	c.workers = workers
	operation, _ := protocol.NewSemanticOperationID()
	req, _ := json.Marshal(controllerActionRequest{Tool: "swarm_send", Origin: &origin, Project: project, WorkerJobID: worker.JobID, WorkerAttemptID: worker.AttemptID, Send: &protocol.ComposerSendReq{Session: worker.Session, SessionInstance: worker.SessionInstance, Text: "correction"}})
	action := Action{ID: "correction", OperationID: operation, ConversationID: binding.ConversationID, State: discussionQueuedSend, Target: to, Request: req}
	if err := st.update(func(s *State) error {
		a, err := allocateAdmission(s, binding, "discussion:worker-correction")
		if err != nil {
			return err
		}
		action.Admission = a.Sequence
		putRecord(s, "actions", &s.Actions, key(binding.ConversationID, action.ID), action)
		putRecord(s, "workers", &s.Workers, key(binding.ConversationID, to), worker)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.advanceDiscussionSend(ctx, action); err != nil || len(workers.sent) != 0 || grants != 0 {
		t.Fatalf("busy worker: writes=%v, err=%v", workers.sent, err)
	}
	workers.rows[0].Status.Turn = status.TurnIdle
	workers.rows[0].Capabilities = nil
	if err := c.advanceDiscussionSend(ctx, action); err != nil || len(workers.sent) != 1 || workers.sent[0] != operation || grants != 1 {
		t.Fatalf("verified idle worker: writes=%v, err=%v", workers.sent, err)
	}
	if err := c.advanceDiscussionSend(ctx, action); err != nil || len(workers.sent) != 1 {
		t.Fatalf("duplicate write: %v, %v", workers.sent, err)
	}
	snapshot, err := st.Snapshot()
	if err != nil || snapshot.Actions[key(binding.ConversationID, action.ID)].State != "settled" {
		t.Fatalf("send did not settle: %v", err)
	}
}
