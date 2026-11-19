package voice

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const discussionQueuedSend = "queued"

func queuedDiscussionSendReceipt(action Action) (json.RawMessage, error) {
	return controllerActionResult(action.ID, json.RawMessage(`{"state":"queued","reason":"waiting for verified native worker readiness"}`))
}

func (c *Controller) reconcileDiscussionSends(ctx context.Context) error {
	c.discussionEffects.Lock()
	defer c.discussionEffects.Unlock()
	st, err := c.store.readView()
	if err != nil {
		return err
	}
	for _, action := range st.Actions {
		if action.ConversationID != c.binding.ConversationID {
			continue
		}
		switch action.State {
		case discussionQueuedSend:
			if err := c.advanceDiscussionSend(ctx, action); err != nil {
				return err
			}
		case "executing":
			var request controllerActionRequest
			if json.Unmarshal(action.Request, &request) != nil {
				return ErrCorrupt
			}
			if request.Send == nil || request.WorkerJobID == "" {
				continue
			}
			if retry, err := c.requeuePreparedDiscussionSend(action, *request.Send); err != nil {
				return err
			} else if retry {
				continue
			}
			if _, err := c.reconcileAction(ctx, action); err != nil && !errors.Is(err, ErrControllerUncertain) {
				return err
			}
		}
	}
	return nil
}

// The manager turn authorizes the exact intention once. The scheduler dispatches it
// only when the same worker and attempt can accept a fresh semantic input safely.
func (c *Controller) advanceDiscussionSend(ctx context.Context, action Action) error {
	st, err := c.store.readView()
	if err != nil {
		return err
	}
	k := key(c.binding.ConversationID, action.ID)
	if st.Actions[k].State != discussionQueuedSend {
		return nil
	}
	var request controllerActionRequest
	if json.Unmarshal(action.Request, &request) != nil || request.Tool != "swarm_send" || request.Send == nil {
		return ErrCorrupt
	}
	reject := func(reason string) error {
		raw, _ := controllerResult(struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		}{"rejected", reason})
		return c.store.SettleAction(c.binding, action.ID, raw)
	}
	if !protocol.SemanticOperationReplayable(action.OperationID, time.Now()) {
		return reject("queued worker send expired before native submission")
	}
	worker := st.Workers[key(c.binding.ConversationID, action.Target)]
	if worker.Ended || worker.RecoveryRequired || worker.Session != request.Send.Session || worker.SessionInstance != request.Send.SessionInstance ||
		worker.JobID != request.WorkerJobID || worker.AttemptID != request.WorkerAttemptID ||
		worker.Project != request.Project || !c.projectPath(request.Project, request.Project) {
		return reject("queued worker or job binding changed before native submission")
	}
	row, err := c.workerTarget(SessionInput{worker.Session, worker.SessionInstance}, false)
	if errors.Is(err, ErrStale) || errors.Is(err, ErrNotFound) {
		return reject("queued worker instance no longer exists")
	}
	if err == nil && row.Status.Process == status.ProcessExited {
		return reject("queued worker exited before native submission")
	}
	if err != nil || row.Status.Process != status.ProcessRunning || row.Status.Turn != status.TurnIdle || !row.Capabilities.NativeOwnerInput {
		return nil
	}
	if c.discussion.VerifyAudience != nil {
		if err := c.discussion.VerifyAudience(ctx); err != nil {
			return nil
		}
	}
	if c.discussion.VerifyGrant != nil {
		if request.Origin == nil || !c.discussion.validateOrigin(*request.Origin) {
			return reject("queued worker send has no current Discord source")
		}
		if err := c.discussion.VerifyGrant(ctx, *request.Origin); err != nil {
			if errors.Is(err, ErrDiscussionSourceChanged) {
				return reject("queued worker send source changed before native submission")
			}
			return nil
		}
	}
	if err = c.store.changeDiscussionSendState(c.binding, action, discussionQueuedSend, "executing"); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil
		}
		return err
	}
	if err = c.workers.SemanticSend(action.OperationID, *request.Send); err != nil {
		if retry, err := c.requeuePreparedDiscussionSend(action, *request.Send); retry || err != nil {
			return err
		}
	}
	_, err = c.reconcileAction(ctx, action)
	if errors.Is(err, ErrControllerUncertain) {
		return nil
	}
	return err
}

func (c *Controller) requeuePreparedDiscussionSend(action Action, request protocol.ComposerSendReq) (bool, error) {
	result, err := c.workers.SemanticOperation(request.Session, action.OperationID)
	if err != nil || result.OperationID != action.OperationID || result.Session != request.Session ||
		result.SessionInstance != request.SessionInstance || result.State != "prepared" {
		return false, nil
	}
	return true, c.store.changeDiscussionSendState(c.binding, action, "executing", discussionQueuedSend)
}

func (s *Store) changeDiscussionSendState(b ManagerBinding, expected Action, from, to string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := key(b.ConversationID, expected.ID)
		action := st.Actions[k]
		if action.State != from || action.OperationID != expected.OperationID || action.RequestHash != expected.RequestHash {
			return ErrConflict
		}
		action.State = to
		putRecord(st, "actions", &st.Actions, k, action)
		return nil
	})
}
