package voice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
		scope.InputID = receipt.NativeInputID
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
	for k, worker := range st.Workers {
		if worker.ConversationID != c.binding.ConversationID || worker.Reserved || worker.ActionID == "" {
			continue
		}
		if worker.VMChild || worker.Mailbox {
			// Older VM roster observation mislabeled enrolled workers. Repair even after switching back to enrolled policy.
			if err := c.store.update(func(current *State) error {
				if err := checkBinding(current, c.binding); err != nil {
					return err
				}
				if current.Workers[k] != worker {
					return ErrStale
				}
				worker.VMChild = false
				if worker.Mailbox {
					worker.Mailbox, worker.RecoveryRequired = false, true
					worker.AttemptID = exactEnrolledLaunchAttempt(current, worker)
				}
				putRecord(current, "workers", &current.Workers, k, worker)
				return nil
			}); err != nil {
				return err
			}
		}
		if worker.EvidenceLimited || worker.JobID == "" || worker.AttemptID == "" || !c.projectPath(worker.Project, worker.Project) {
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

func (c *Controller) readDiscussionWorker(ctx context.Context, input ReadInput, admission Admission) (json.RawMessage, error) {
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
	if c.discussion.VMProjectAccess {
		if original.Origin == nil || original.Origin.Voice.Generation == 0 ||
			c.discussion.VerifyAudience == nil || c.discussion.VerifyGrant == nil {
			return nil, ErrExpired
		}
		source := st.Admissions[admissionKey(c.binding.ConversationID, attempt.Admission)]
		if !source.SourceCutover || source.Origin == nil || *source.Origin != *original.Origin {
			return nil, ErrExpired
		}
		if admission.Origin == nil || !admission.SourceCutover || *admission.Origin != *original.Origin {
			wakeID, fromWake := strings.CutPrefix(admission.SourceID, "wake:")
			wake := st.Wakes[key(c.binding.ConversationID, wakeID)]
			event := st.Events[key(c.binding.ConversationID, wake.EventID)]
			if !fromWake || admission.Sequence == 0 || wake.Admission != admission.Sequence || !event.WorkerProof ||
				event.ID != discussionEvidenceID(worker) || event.JobID != worker.JobID || event.AttemptID != worker.AttemptID {
				return nil, ErrExpired
			}
		}
	}
	if err := c.verifyStoredWorkerAttempt(ctx, st, worker); err != nil {
		return nil, err
	}
	turn := input.NativeTurnID
	if wakeID, fromWake := strings.CutPrefix(admission.SourceID, "wake:"); fromWake && turn == "" {
		wake := st.Wakes[key(c.binding.ConversationID, wakeID)]
		event := st.Events[key(c.binding.ConversationID, wake.EventID)]
		if proof, err := storedDiscussionWorkerProof(event, worker); err != nil {
			return nil, err
		} else if proof != nil {
			turn = proof.TurnID
		}
	}
	var proof *schema.WorkerTurnEvidence
	if turn != "" {
		for _, event := range st.Events {
			if event.NativeTurnID != turn || event.ConversationID != c.binding.ConversationID {
				continue
			}
			proof, err = storedDiscussionWorkerProof(event, worker)
			if err != nil {
				return nil, err
			}
			if proof != nil {
				break
			}
		}
		if proof == nil {
			return nil, ErrNotFound
		}
	} else if event, exists := st.Events[key(c.binding.ConversationID, discussionEvidenceID(worker))]; exists {
		proof, err = storedDiscussionWorkerProof(event, worker)
		if err != nil {
			return nil, err
		}
	} else {
		if worker.EvidenceLimited {
			return nil, ErrControllerWorkerEvidenceLimit
		}
		if _, err = c.workerTarget(input.SessionInput, false); err != nil {
			return nil, err
		}
		proof, err = c.discussionWorkerEvidence(ctx, worker, st.Actions[key(c.binding.ConversationID, worker.ActionID)])
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

func storedDiscussionWorkerProof(event Event, worker WorkerAssociation) (*schema.WorkerTurnEvidence, error) {
	if !event.WorkerProof || event.JobID != worker.JobID || event.AttemptID != worker.AttemptID {
		return nil, nil
	}
	var metadata struct {
		Session, Instance string
		Data              schema.WorkerTurnEvidence
	}
	if json.Unmarshal(event.Metadata, &metadata) != nil || metadata.Data.TurnID != event.NativeTurnID || metadata.Data.TurnID == "" {
		return nil, ErrCorrupt
	}
	if metadata.Session != worker.Session || metadata.Instance != worker.SessionInstance {
		return nil, nil
	}
	return &metadata.Data, nil
}

func (c *Controller) readDiscussionWorkerNote(ctx context.Context, input SessionInput, admission Admission) (json.RawMessage, error) {
	wakeID, fromWake := strings.CutPrefix(admission.SourceID, "wake:")
	if !fromWake || admission.Sequence == 0 || c.discussion == nil {
		return nil, ErrExpired
	}
	st, err := c.store.readView()
	if err != nil {
		return nil, err
	}
	if err := checkBinding(&st, c.binding); err != nil {
		return nil, err
	}
	wake := st.Wakes[key(c.binding.ConversationID, wakeID)]
	event := st.Events[key(c.binding.ConversationID, wake.EventID)]
	var source workerNoteMetadata
	if wake.Admission != admission.Sequence || !event.WorkerMessage ||
		json.Unmarshal(event.Metadata, &source) != nil || source.NoteID == "" ||
		source.Session != input.Session || source.Instance != input.SessionInstance {
		return nil, ErrStale
	}
	worker := st.Workers[key(c.binding.ConversationID, key(source.Session, source.Instance))]
	if worker.Session != source.Session || worker.SessionInstance != source.Instance || worker.JobID != event.JobID || worker.AttemptID != event.AttemptID {
		return nil, ErrStale
	}
	if worker.supervisorMailbox() {
		if !c.discussion.VMProjectAccess {
			return nil, ErrExpired
		}
	} else {
		if err := c.verifyStoredWorkerEvent(ctx, st, event, source); err != nil {
			return nil, err
		}
		current, err := c.store.readView()
		if err != nil {
			return nil, err
		}
		if checkBinding(&current, c.binding) != nil || current.Workers[key(c.binding.ConversationID, key(worker.Session, worker.SessionInstance))] != worker {
			return nil, ErrStale
		}
	}
	return event.Metadata, nil
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
