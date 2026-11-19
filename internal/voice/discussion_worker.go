package voice

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

var ErrControllerWorkerEvidenceLimit = errors.New("voice: exact worker evidence exceeds the verified transcript scan limit")

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
	scope := schema.WorkerHistoryScope{Parent: worker.Parent, SessionInstance: worker.SessionInstance, ManagerConversation: c.binding.ConversationID}
	switch intent.Tool {
	case "swarm_create":
		var receipt createdWorkerResult
		if intent.Launch == nil || intent.Launch.InitialPrompt == "" || json.Unmarshal(action.Receipt, &receipt) != nil ||
			receipt.State != "created" || receipt.Session != worker.Session || receipt.SessionInstance != worker.SessionInstance {
			return nil, ErrControllerNativeUnavailable
		}
		scope.Parent = intent.Launch.SpawnedFrom
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
			receipt.Origin == nil || *receipt.Origin != *intent.Origin || receipt.NativeInputID == "" || receipt.NativeTurnID == "" ||
			(receipt.State != "admitted" && receipt.State != "completed" && receipt.State != "failed" && receipt.State != "cancelled") {
			return nil, ErrControllerNativeUnavailable
		}
		scope.TurnID = receipt.NativeTurnID
		scope.PromptHash = hashBytes([]byte(intent.Send.Text))
	default:
		return nil, ErrControllerNativeUnavailable
	}
	if c.workers == nil || scope.Parent == "" || ctx.Err() != nil {
		return nil, ErrControllerNativeUnavailable
	}
	page, err := c.workers.NativeHistoryPage(schema.InteractionHistoryReq{Native: true, Session: worker.Session, Limit: 1, Worker: &scope})
	if err != nil || page.Scope != "authorized_worker_turn" || len(page.Items) != 0 ||
		page.Worker != nil && (page.Worker.TurnID == "" || scope.TurnID != "" && page.Worker.TurnID != scope.TurnID) {
		return nil, ErrControllerNativeUnavailable
	}
	if page.WorkerEvidenceLimited {
		return nil, ErrControllerWorkerEvidenceLimit
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
		if worker.ConversationID != c.binding.ConversationID || worker.Reserved || worker.EvidenceLimited || worker.ActionID == "" || worker.JobID == "" ||
			!c.projectPath(worker.Project, worker.Project) {
			continue
		}
		id := discussionEvidenceID(worker)
		if _, exists := st.Events[key(c.binding.ConversationID, id)]; exists {
			continue
		}
		proof, err := c.discussionWorkerEvidence(ctx, worker, st.Actions[key(c.binding.ConversationID, worker.ActionID)])
		if errors.Is(err, ErrControllerWorkerEvidenceLimit) {
			if err = c.store.noteDiscussionWorkerEvidenceLimit(c.binding, worker); err != nil && !errors.Is(err, ErrStale) {
				return err
			}
			continue
		}
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

func (c *Controller) readDiscussionWorker(input ReadInput, admission Admission) (json.RawMessage, error) {
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
	if input.JobID != "" && input.JobID != worker.JobID || input.AttemptID != "" && input.AttemptID != worker.AttemptID {
		return nil, ErrStale
	}
	if !c.projectPath(worker.Project, worker.Project) {
		return nil, ErrExpired
	}
	var original controllerActionRequest
	attempt := st.Actions[key(c.binding.ConversationID, worker.AttemptID)]
	if attempt.State != "settled" || json.Unmarshal(attempt.Request, &original) != nil || original.Project != worker.Project {
		return nil, ErrStale
	}
	if worker.EvidenceLimited {
		return nil, ErrControllerWorkerEvidenceLimit
	}
	var proof *schema.WorkerTurnEvidence
	if event, exists := st.Events[key(c.binding.ConversationID, discussionEvidenceID(worker))]; exists {
		var metadata struct{ Data schema.WorkerTurnEvidence }
		if json.Unmarshal(event.Metadata, &metadata) != nil || !event.WorkerProof || metadata.Data.TurnID != event.NativeTurnID ||
			event.JobID != worker.JobID || event.AttemptID != worker.AttemptID {
			return nil, ErrCorrupt
		}
		proof = &metadata.Data
	} else {
		if _, err = c.workerTarget(input.SessionInput, false); err != nil {
			return nil, err
		}
		proof, err = c.discussionWorkerEvidence(context.Background(), worker, st.Actions[key(c.binding.ConversationID, worker.ActionID)])
		if err != nil {
			return nil, err
		}
	}
	current, err := c.store.readView()
	if err != nil {
		return nil, err
	}
	if worker != current.Workers[workerKey] {
		return nil, ErrStale
	}
	if !c.projectPath(worker.Project, worker.Project) {
		return nil, ErrExpired
	}
	if proof != nil && admission.Sequence != 0 {
		err := c.store.update(func(st *State) error {
			if err := checkBinding(st, c.binding); err != nil {
				return err
			}
			k := admissionKey(c.binding.ConversationID, admission.Sequence)
			a := st.Admissions[k]
			if st.Workers[workerKey] != worker || a.NativeInputID != admission.NativeInputID || a.NativeTurnID != admission.NativeTurnID || !a.SettledAt.IsZero() || a.Phase != "running" {
				return ErrStale
			}
			a.WorkerProjects = addDiscussionScopeValue(a.WorkerProjects, worker.Project)
			a.WorkerAttempts = addDiscussionScopeValue(a.WorkerAttempts, worker.AttemptID)
			putRecord(st, "admissions", &st.Admissions, k, a)
			return nil
		})
		if err != nil {
			return nil, err
		}
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

func truncateDiscussionWakeEvidence(text string) string {
	end := min(len(text), 1024)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func (s *Store) noteDiscussionWorkerEvidenceLimit(b ManagerBinding, expected WorkerAssociation) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := key(b.ConversationID, key(expected.Session, expected.SessionInstance))
		worker := st.Workers[k]
		if discussionEvidenceID(worker) != discussionEvidenceID(expected) {
			return ErrStale
		}
		if worker.EvidenceLimited {
			return nil
		}
		worker.EvidenceLimited = true
		putRecord(st, "workers", &st.Workers, k, worker)
		id := "evidence-limit-" + hashValue([]string{worker.JobID, worker.AttemptID})
		publicationKey := key(b.ConversationID, id)
		if _, exists := st.Publications[publicationKey]; exists {
			return nil
		}
		sequence, err := nextDiscussionOutput(st, b.ConversationID)
		if err != nil {
			return err
		}
		p := Publication{ID: id, ConversationID: b.ConversationID, OutputSequence: sequence, Text: discussionEvidenceLimitNotice,
			TextHash: hashBytes([]byte(discussionEvidenceLimitNotice)), DiscussionSource: DiscussionOutputSource{SystemCause: "worker_evidence_limited", JobID: worker.JobID, AttemptID: worker.AttemptID}}
		putRecord(st, "publications", &st.Publications, publicationKey, p)
		return nil
	})
}
