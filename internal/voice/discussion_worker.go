package voice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func (c *Controller) discussionWorkerEvidence(ctx context.Context, worker WorkerAssociation, action Action) (*schema.WorkerTurnEvidence, error) {
	if action.ID == "" || action.ID != worker.ActionID || action.State != "settled" || worker.JobID == "" || worker.AttemptID == "" {
		return nil, ErrControllerNativeUnavailable
	}
	var intent controllerActionRequest
	if json.Unmarshal(action.Request, &intent) != nil {
		return nil, ErrCorrupt
	}
	if intent.Origin == nil {
		return nil, ErrControllerNativeUnavailable
	}
	_, parent, valid := protocol.ParseID(c.binding.SessionID)
	scope := schema.WorkerHistoryScope{Parent: parent, SessionInstance: worker.SessionInstance}
	switch intent.Tool {
	case "swarm_create":
		var receipt createdWorkerResult
		if intent.Launch == nil || intent.Launch.InitialPrompt == "" || json.Unmarshal(action.Receipt, &receipt) != nil ||
			receipt.State != "created" || receipt.Session != worker.Session || receipt.SessionInstance != worker.SessionInstance {
			return nil, ErrControllerNativeUnavailable
		}
		scope.PromptHash = hashBytes([]byte(intent.Launch.InitialPrompt))
	case "swarm_send":
		var receipt schema.SemanticMessageReceipt
		if intent.Send == nil || intent.Send.Session != worker.Session || intent.Send.SessionInstance != worker.SessionInstance ||
			json.Unmarshal(action.Receipt, &receipt) != nil {
			return nil, ErrControllerNativeUnavailable
		}
		if receipt.State == "submitted" && c.workers != nil && ctx.Err() == nil {
			var err error
			receipt, err = c.workers.SemanticOperation(worker.Session, action.OperationID)
			if err != nil {
				return nil, ErrControllerNativeUnavailable
			}
		}
		if receipt.OperationID != action.OperationID || receipt.Session != worker.Session || receipt.SessionInstance != worker.SessionInstance ||
			receipt.NativeInputID == "" || receipt.NativeTurnID == "" ||
			(receipt.State != "admitted" && receipt.State != "completed" && receipt.State != "failed" && receipt.State != "cancelled") {
			return nil, ErrControllerNativeUnavailable
		}
		scope.TurnID = receipt.NativeTurnID
		scope.PromptHash = hashBytes([]byte(intent.Send.Text))
	default:
		return nil, ErrControllerNativeUnavailable
	}
	if c.workers == nil || !valid || ctx.Err() != nil {
		return nil, ErrControllerNativeUnavailable
	}
	page, err := c.workers.NativeHistoryPage(schema.InteractionHistoryReq{Native: true, Session: worker.Session, Limit: 1, Worker: &scope})
	if err != nil || page.Scope != "authorized_worker_turn" || len(page.Items) != 0 ||
		page.Worker != nil && (page.Worker.TurnID == "" || scope.TurnID != "" && page.Worker.TurnID != scope.TurnID) {
		return nil, ErrControllerNativeUnavailable
	}
	return page.Worker, nil
}

// Reuse the controller reconciliation loop for native output outside journal retention.
func (c *Controller) reconcileDiscussionWorkerEvidence(ctx context.Context) error {
	st, err := c.store.readView()
	if err != nil {
		return err
	}
	for _, worker := range st.Workers {
		if worker.ConversationID != c.binding.ConversationID || worker.Reserved || worker.ActionID == "" || worker.JobID == "" {
			continue
		}
		id := discussionEvidenceID(worker)
		if _, exists := st.Events[key(c.binding.ConversationID, id)]; exists {
			continue
		}
		proof, err := c.discussionWorkerEvidence(ctx, worker, st.Actions[key(c.binding.ConversationID, worker.ActionID)])
		if errors.Is(err, ErrControllerNativeUnavailable) || proof == nil && err == nil {
			continue
		}
		if err != nil {
			return err
		}
		metadata, _ := json.Marshal(proof)
		event := Event{ID: id, WorkerProof: true, NativeTurnID: proof.TurnID, JobID: worker.JobID, AttemptID: worker.AttemptID, Metadata: metadata, RecoveryOnly: proof.State != "completed"}
		if _, err = c.AcceptWorkerEvent(worker.Session, worker.SessionInstance, event); err != nil && !errors.Is(err, ErrStale) {
			return err
		}
	}
	return nil
}

func discussionEvidenceID(worker WorkerAssociation) string {
	return "authorized-worker-" + hashValue(struct{ Session, Instance, Job, Attempt, Action string }{
		worker.Session, worker.SessionInstance, worker.JobID, worker.AttemptID, worker.ActionID,
	})
}

func (c *Controller) readDiscussionWorker(input ReadInput) (json.RawMessage, error) {
	if input.Cursor != "" || input.Source != "" && input.Source != "native" {
		return nil, ErrControllerNativeUnavailable
	}
	st, err := c.store.readView()
	if err != nil {
		return nil, err
	}
	workerKey := key(c.binding.ConversationID, key(input.Session, input.SessionInstance))
	worker, ok := st.Workers[workerKey]
	if !ok {
		return nil, ErrNotFound
	}
	if _, err = c.workerTarget(input.SessionInput, false); err != nil {
		return nil, err
	}
	if input.JobID != "" && input.JobID != worker.JobID || input.AttemptID != "" && input.AttemptID != worker.AttemptID {
		return nil, ErrStale
	}
	proof, err := c.discussionWorkerEvidence(context.Background(), worker, st.Actions[key(c.binding.ConversationID, worker.ActionID)])
	if err != nil {
		return nil, err
	}
	current, err := c.store.readView()
	if err != nil {
		return nil, err
	}
	if worker != current.Workers[workerKey] {
		return nil, ErrStale
	}
	return controllerResult(struct {
		Source    string                     `json:"source"`
		Session   string                     `json:"session"`
		Instance  string                     `json:"session_instance"`
		JobID     string                     `json:"job_id"`
		AttemptID string                     `json:"attempt_id"`
		Evidence  *schema.WorkerTurnEvidence `json:"evidence"`
	}{"authorized_worker_turn", worker.Session, worker.SessionInstance, worker.JobID, worker.AttemptID, proof})
}
