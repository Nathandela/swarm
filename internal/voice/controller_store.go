package voice

import (
	"encoding/json"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func (s *Store) observeController(b ManagerBinding, external uint64, steered bool) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		c := st.Conversations[b.ConversationID]
		if c.ExternalObserved && external < c.ExternalInput {
			return ErrStale
		}
		if external != c.ExternalInput || steered {
			c.SchedulerPaused = true
		}
		c.ExternalObserved = true
		c.ExternalInput = external
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
		return nil
	})
}
func (s *Store) resumeController(b ManagerBinding, expected uint64) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if st.Conversations[b.ConversationID].CustodyLost {
			return managerproof.ErrQuarantined
		}
		c := st.Conversations[b.ConversationID]
		if !c.ExternalObserved || c.ExternalInput != expected {
			return ErrStale
		}
		c.SchedulerPaused = false
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
		return nil
	})
}
func (s *Store) prepareControllerInput(b ManagerBinding, seq uint64, nonce, operation string, req protocol.ComposerSendReq, external uint64) (Admission, error) {
	var result Admission
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		c := st.Conversations[b.ConversationID]
		if c.SchedulerPaused || !c.ExternalObserved || c.ExternalInput != external {
			return ErrControllerPaused
		}
		a, err := activeAdmission(st, b, seq)
		if err != nil {
			return err
		}
		if a.Phase != "" {
			result = a
			return nil
		}
		for _, other := range st.Admissions {
			if other.ConversationID == b.ConversationID && other.SettledAt.IsZero() && other.Sequence < seq && !other.NativeBindingRetired && (other.Phase != "completed" || !other.RecoveryAcknowledged) {
				return ErrConflict
			}
		}
		if req.ManagerAdmission == nil || req.ManagerAdmission.Validate() != nil || req.ManagerAdmission.Nonce != nonce || req.ManagerAdmission.Binding != runtimeBinding(b) || req.ManagerAdmission.ExpectedExternalInput != external || req.Session != b.SessionID || req.SessionInstance != b.SessionInstance || operation == "" {
			return ErrInvalid
		}
		if a.RecoveryOnly && !strings.HasPrefix(req.Text, recoveryInputNotice) {
			req.Text = recoveryInputNotice + req.Text
		}
		raw, err := json.Marshal(req)
		if err != nil {
			return err
		}
		raw, err = canonicalJSON(raw)
		if err != nil {
			return err
		}
		for _, other := range st.Admissions {
			if other.NativeInputID == nonce && other.Sequence != seq {
				return ErrConflict
			}
		}
		for k, w := range st.Wakes {
			if w.ConversationID == b.ConversationID && w.Admission == seq {
				if w.State != "admitted" {
					return ErrConflict
				}
				w.OperationID = operation
				w.NativeInputID = nonce
				putRecord(st, "wakes", &st.Wakes, k, w)
			}
		}
		a.NativeInputID = nonce
		a.OperationID = operation
		a.Request = raw
		a.Phase = "prepared"
		a.ExternalInput = external
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, seq), a)
		result = a
		return nil
	})
	return cloneAdmission(result), err
}
func (s *Store) controllerInputPhase(b ManagerBinding, seq uint64, phase, turn, reason string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := admissionKey(b.ConversationID, seq)
		a, ok := st.Admissions[k]
		if !ok {
			return ErrNotFound
		}
		if a.NativeInputID == "" || a.OperationID == "" {
			return ErrInvalid
		}
		if a.Phase == "completed" || a.Phase == "rejected" {
			if a.Phase != phase {
				return ErrExpired
			}
			return nil
		}
		switch phase {
		case "submitted", "admitted", "running", "uncertain", "completed", "rejected":
		default:
			return ErrInvalid
		}
		if turn != "" && a.NativeTurnID != "" && a.NativeTurnID != turn {
			return ErrConflict
		}
		if a.NativeTurnID != "" && (a.Phase == "running" || a.Phase == "uncertain") {
			switch phase {
			case "submitted", "admitted":
				return nil
			case "rejected":
				return ErrConflict
			}
		}
		if turn != "" {
			a.NativeTurnID = turn
			conv := st.Conversations[b.ConversationID]
			if conv.NeedsHandoff && (phase == "running" || phase == "completed") {
				conv.NeedsHandoff = false
				putRecord(st, "conversations", &st.Conversations, b.ConversationID, conv)
			}
		}
		if phase == "submitted" || phase == "admitted" || phase == "running" || phase == "completed" {
			for wk, w := range st.Wakes {
				if w.ConversationID == b.ConversationID && w.Admission == seq {
					w.State = "native_input"
					putRecord(st, "wakes", &st.Wakes, wk, w)
				}
			}
		}
		a.Phase = phase
		a.Reason = reason
		putRecord(st, "admissions", &st.Admissions, k, a)
		return nil
	})
}

// RebindManager is a CAS for an externally reconciled same-provider native resume.
// Old unsettled admissions block renewal; their authority is never transferred.
func (s *Store) RebindManager(previous, next ManagerBinding) (ManagerBinding, error) {
	var result ManagerBinding
	err := s.update(func(st *State) error {
		c, ok := st.Conversations[previous.ConversationID]
		if !ok || identity(c.Binding) != identity(previous) || st.CurrentConversation != previous.ConversationID {
			return ErrStale
		}
		if c.CustodyLost {
			return managerproof.ErrQuarantined
		}
		if !validBinding(next) || next.Provider != previous.Provider || next.ConversationID != previous.ConversationID {
			return ErrInvalid
		}
		for _, a := range st.Admissions {
			if a.ConversationID == previous.ConversationID && a.SettledAt.IsZero() && !a.NativeBindingRetired && (a.Phase != "completed" || !a.RecoveryAcknowledged) {
				return ErrConflict
			}
		}
		for k, p := range st.Permits {
			if p.ConversationID == previous.ConversationID {
				p.Revoked = true
				putRecord(st, "permits", &st.Permits, k, p)
			}
		}
		for k, r := range st.Requests {
			if r.ConversationID == previous.ConversationID && r.State == "pending" && r.Kind == "native" {
				r.State = "expired"
				putRecord(st, "requests", &st.Requests, k, r)
			}
		}
		next.Generation = st.Generation
		c.Binding = identity(next)
		c.SchedulerPaused = true
		c.ExternalObserved = false
		c.ExternalInput = 0
		putRecord(st, "conversations", &st.Conversations, next.ConversationID, c)
		result = next
		return nil
	})
	return result, err
}

func (s *Store) answerControllerQuestion(b ManagerBinding, id, expectedHash, answerID string, answer json.RawMessage, now time.Time) (Admission, error) {
	return s.answerQuestion(b, id, expectedHash, answerID, answer, now, nil, "", false)
}

func (c *Controller) answerDiscussionQuestion(id, expectedHash string, origin schema.MessageOrigin, project string, explicit bool, answer json.RawMessage) (Admission, error) {
	return c.store.answerQuestion(c.binding, id, expectedHash, origin.RequestID, answer, time.Now(), &origin, project, explicit)
}

func (s *Store) answerQuestion(b ManagerBinding, id, expectedHash, answerID string, answer json.RawMessage, now time.Time, origin *schema.MessageOrigin, project string, explicit bool) (Admission, error) {
	var result Admission
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if st.Conversations[b.ConversationID].CustodyLost {
			return managerproof.ErrQuarantined
		}
		k := key(b.ConversationID, id)
		r, ok := st.Requests[k]
		if !ok {
			return ErrNotFound
		}
		if r.Kind != "manager" || r.Hash != expectedHash || answerID == "" {
			return ErrConflict
		}
		raw, err := canonicalJSON(answer)
		if err != nil {
			return err
		}
		source := "answer:" + key(id, answerID)
		if r.State == "answered" {
			if r.AnswerID != answerID || string(r.Answer) != string(raw) {
				return ErrConflict
			}
			for _, a := range st.Admissions {
				if a.ConversationID == b.ConversationID && a.SourceID == source {
					result = a
					return nil
				}
			}
			return ErrExpired
		}
		if r.State != "pending" || !now.Before(r.ExpiresAt) || st.CancelledTasks[key(b.ConversationID, r.TaskID)] {
			return ErrExpired
		}
		var previous Admission
		if origin != nil {
			previous, err = findDiscussionAdmission(*st, *origin)
			if err != nil {
				return err
			}
		}
		a, err := admitDiscussionRevision(st, b, previous, source)
		if err != nil {
			return err
		}
		text, err := json.Marshal(struct {
			RequestID string          `json:"request_id"`
			TaskID    string          `json:"task_id"`
			Answer    json.RawMessage `json:"answer"`
		}{r.ID, r.TaskID, raw})
		if err != nil {
			return err
		}
		if !r.RecoveryContext {
			for _, origin := range st.Admissions {
				if origin.ConversationID == b.ConversationID && origin.NativeInputID == r.NativeInputID && origin.RecoveryOnly {
					a.RecoveryOnly = true
				}
			}
		}
		a.Origin, a.Project, a.ProjectExplicit = origin, project, explicit
		a.InputText = "Human answer to the exact pending clarification: " + string(text)
		if origin != nil {
			speaker, _ := json.Marshal(origin)
			a.InputText += "\nVerified Discord author " + string(speaker) + ". Use publish to respond in the thread."

		}
		if len(a.InputText) > MaxUtteranceBytes {
			return ErrInvalid
		}
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
		r.State = "answered"
		r.AnswerID = answerID
		r.Answer = raw
		putRecord(st, "requests", &st.Requests, k, r)
		result = a
		return nil
	})
	return cloneAdmission(result), err
}

func (s *Store) finishControllerInput(b ManagerBinding, a Admission) error {
	if err := s.retireUnsentControllerActions(b, a.Sequence); err != nil {
		return err
	}
	if a.NativeTurnID != "" {
		if err := s.EndNativeTurn(b, a.NativeTurnID); err != nil {
			return err
		}
	}
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		return settleControllerInputState(st, b, a.Sequence)
	})
}

func settleControllerInputState(st *State, b ManagerBinding, seq uint64) error {
	if err := settleAdmission(st, b, seq); err != nil {
		return err
	}
	for k, w := range st.Wakes {
		if w.ConversationID == b.ConversationID && w.Admission == seq {
			w.State = "settled"
			putRecord(st, "wakes", &st.Wakes, k, w)
		}
	}
	return nil
}
func (s *Store) controllerActionExecuting(b ManagerBinding, id string, queue bool) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := key(b.ConversationID, id)
		a, ok := st.Actions[k]
		if !ok {
			return ErrNotFound
		}
		if a.State != "prepared" {
			return ErrConflict
		}
		input, err := activeAdmission(st, b, a.Admission)
		if err != nil {
			return err
		}
		if input.RecoveryOnly {
			return ErrControllerUncertain
		}
		if input.Phase != "running" || st.ClosedTurns[key(b.ConversationID, input.NativeTurnID)] {
			return ErrExpired
		}
		a.State = "executing"
		if queue {
			a.State = discussionQueuedSend
		}
		putRecord(st, "actions", &st.Actions, k, a)
		return nil
	})
}

func (s *Store) rejectQueuedControllerInput(b ManagerBinding, expected Admission, reason string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := admissionKey(b.ConversationID, expected.Sequence)
		a, ok := st.Admissions[k]
		if !ok {
			return ErrNotFound
		}
		if a.Phase != expected.Phase || a.NativeTurnID != "" {
			return ErrConflict
		}
		switch a.Phase {
		case "":
			if a.OperationID != "" || a.NativeInputID != "" || len(a.Request) != 0 {
				return ErrConflict
			}
		case "prepared", "superseded":
			if a.SourceCutover || a.OperationID != expected.OperationID || a.NativeInputID != expected.NativeInputID || string(a.Request) != string(expected.Request) {
				return ErrConflict
			}
		default:
			return ErrConflict
		}
		if a.Origin != nil && reason == ErrDiscussionSourceChanged.Error() {
			for rk, question := range st.Requests {
				if question.ConversationID != b.ConversationID || question.Kind != "manager" || question.State != "answered" || question.AnswerID != a.Origin.RequestID {
					continue
				}
				question.State = "pending"
				if !time.Now().Before(question.ExpiresAt) {
					question.State = "expired"
				}
				question.AnswerID, question.Answer = "", nil
				putRecord(st, "requests", &st.Requests, rk, question)
			}
		}
		a.Reason = reason
		if reason == ErrDiscussionSourceChanged.Error() {
			a.Phase = "superseded"
			putRecord(st, "admissions", &st.Admissions, k, a)
			return nil
		}
		a.Phase = "rejected"
		putRecord(st, "admissions", &st.Admissions, k, a)
		return settleControllerInputState(st, b, a.Sequence)
	})
}

func (s *Store) checkControllerBinding(b ManagerBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.View(func(tx *bolt.Tx) error {
		if err := s.load(tx); err != nil {
			return err
		}
		if err := checkBinding(&s.cached, b); err != nil {
			return err
		}
		if s.cached.Conversations[b.ConversationID].CustodyLost {
			return managerproof.ErrQuarantined
		}
		return nil
	})
}

// refreshPreparedInput can change transport identity only before the first executing boundary.
func (s *Store) refreshPreparedInput(b ManagerBinding, expected Admission, operation string, external uint64) (Admission, error) {
	if !protocol.SemanticOperationReplayable(operation, time.Now()) {
		return Admission{}, ErrInvalid
	}
	var result Admission
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		c := st.Conversations[b.ConversationID]
		if c.SchedulerPaused || !c.ExternalObserved || c.ExternalInput != external {
			return ErrControllerPaused
		}
		k := admissionKey(b.ConversationID, expected.Sequence)
		a, ok := st.Admissions[k]
		if !ok {
			return ErrNotFound
		}
		if a.Phase != "prepared" || a.OperationID != expected.OperationID || a.NativeInputID != expected.NativeInputID || a.RecoveryOnly != expected.RecoveryOnly || string(a.Request) != string(expected.Request) {
			return ErrConflict
		}
		var req protocol.ComposerSendReq
		if json.Unmarshal(a.Request, &req) != nil || req.ManagerAdmission == nil {
			return ErrCorrupt
		}
		req.ManagerAdmission.ExpectedExternalInput = external
		raw, err := json.Marshal(req)
		if err != nil {
			return err
		}
		raw, err = canonicalJSON(raw)
		if err != nil {
			return err
		}
		for wk, w := range st.Wakes {
			if w.ConversationID == b.ConversationID && w.Admission == a.Sequence {
				if w.State != "admitted" || w.OperationID != a.OperationID {
					return ErrConflict
				}
				w.OperationID = operation
				putRecord(st, "wakes", &st.Wakes, wk, w)
			}
		}
		a.OperationID = operation
		a.Request = raw
		a.ExternalInput = external
		putRecord(st, "admissions", &st.Admissions, k, a)
		result = a
		return nil
	})
	return cloneAdmission(result), err
}

// A durable draft refusal proves the prior operation never reached provider input.
func (s *Store) deferDraftRefusal(b ManagerBinding, expected Admission, operation string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := admissionKey(b.ConversationID, expected.Sequence)
		a, ok := st.Admissions[k]
		if !ok || !a.SettledAt.IsZero() || a.NativeTurnID != "" || a.NativeBindingRetired ||
			a.Phase != "executing" && a.Phase != "uncertain" ||
			a.OperationID != expected.OperationID || a.NativeInputID != expected.NativeInputID || string(a.Request) != string(expected.Request) {
			return ErrConflict
		}
		for wk, w := range st.Wakes {
			if w.ConversationID == b.ConversationID && w.Admission == a.Sequence {
				if w.State != "admitted" || w.OperationID != a.OperationID {
					return ErrConflict
				}
				w.OperationID = operation
				putRecord(st, "wakes", &st.Wakes, wk, w)
			}
		}
		// Keep the original Discord source sealed after crossing its cutover.
		a.Phase, a.OperationID, a.Reason = "prepared", operation, ""
		putRecord(st, "admissions", &st.Admissions, k, a)
		return nil
	})
}

func (s *Store) beginControllerInput(b ManagerBinding, expected Admission) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := admissionKey(b.ConversationID, expected.Sequence)
		a, ok := st.Admissions[k]
		if !ok {
			return ErrNotFound
		}
		c := st.Conversations[b.ConversationID]
		if c.SchedulerPaused || c.ExternalInput != a.ExternalInput {
			return ErrControllerPaused
		}
		if a.Phase != "prepared" || a.OperationID != expected.OperationID || a.NativeInputID != expected.NativeInputID || a.RecoveryOnly != expected.RecoveryOnly || string(a.Request) != string(expected.Request) {
			return ErrConflict
		}
		if !protocol.SemanticOperationReplayable(a.OperationID, time.Now()) {
			return ErrExpired
		}
		if a.Origin != nil && a.Project != "" && c.Project != a.Project {
			c.Project = a.Project
			putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
		}
		a.Phase = "executing"
		a.SourceCutover = a.Origin != nil
		putRecord(st, "admissions", &st.Admissions, k, a)
		return nil
	})
}

func (s *Store) settleControllerWorkerAction(b ManagerBinding, action Action, session, instance string, receipt json.RawMessage) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := key(b.ConversationID, action.ID)
		stored, ok := st.Actions[k]
		if !ok {
			return ErrNotFound
		}
		if stored.OperationID != action.OperationID || stored.RequestHash != action.RequestHash {
			return ErrConflict
		}
		if stored.State == "settled" {
			return nil
		}
		source, ok := st.Admissions[admissionKey(b.ConversationID, stored.Admission)]
		if !ok {
			return ErrNotFound
		}
		raw, err := canonicalJSON(receipt)
		if err != nil {
			return err
		}
		var grant controllerActionRequest
		if strings.HasPrefix(b.ConversationID, DiscussionConversationPrefix) {
			if json.Unmarshal(stored.Request, &grant) != nil {
				return ErrCorrupt
			}
			if grant.Tool == "swarm_send" {
				var result schema.SemanticMessageReceipt
				if json.Unmarshal(raw, &result) != nil {
					return ErrCorrupt
				}
				if result.State == "rejected" {
					session, instance = "", ""
				}
			}
		}
		wk := key(b.ConversationID, key(session, instance))
		reservation := key(b.ConversationID, "reservation:"+stored.ID)
		reservationRecovery := st.Workers[reservation].RecoveryRequired
		if _, exists := st.Workers[reservation]; exists {
			deleteRecord(st, "workers", &st.Workers, reservation)
		}
		if existing, exists := st.Workers[wk]; session != "" && (!exists || existing.Admission <= source.Sequence) {
			recovery := existing.RecoveryRequired
			recovery = recovery || reservationRecovery
			if !source.RecoveryOnly && (source.Origin != nil || strings.HasPrefix(source.SourceID, "utterance:") || strings.HasPrefix(source.SourceID, "answer:")) && source.Sequence > existing.Admission {
				recovery = false
			}
			worker := WorkerAssociation{RecoveryRequired: recovery, ConversationID: b.ConversationID, Session: session, SessionInstance: instance, Admission: source.Sequence, SourceID: source.SourceID, ActionID: stored.ID}
			if grant.Origin != nil {
				original := existing
				if grant.Tool == "swarm_resume" {
					original = st.Workers[key(b.ConversationID, stored.Target)]
				}
				worker.Project = grant.Project
				worker.Parent = original.Parent
				if grant.Launch != nil {
					worker.Parent = grant.Launch.SpawnedFrom
				}
				worker.Ended = existing.Ended
				worker.JobID, worker.InitialSourceID = original.JobID, original.InitialSourceID
				if worker.JobID == "" {
					worker.JobID, worker.InitialSourceID = stored.ID, source.SourceID
				}
				worker.AttemptID = stored.ID
				if grant.Tool == "swarm_send" && original.AttemptID != "" {
					worker.AttemptID = original.AttemptID
				}
			}
			putRecord(st, "workers", &st.Workers, wk, worker)
		}

		stored.State = "settled"
		stored.Receipt = raw
		stored.SettledAt = time.Now().UTC()
		putRecord(st, "actions", &st.Actions, k, stored)
		return nil
	})
}

func (s *Store) reserveControllerWorker(b ManagerBinding, action Action, session, instance string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		a, ok := st.Actions[key(b.ConversationID, action.ID)]
		if !ok || a.OperationID != action.OperationID || a.RequestHash != action.RequestHash {
			return ErrConflict
		}
		if a.State != "prepared" {
			return nil
		}
		input, ok := st.Admissions[admissionKey(b.ConversationID, a.Admission)]
		if !ok {
			return ErrNotFound
		}
		k := key(b.ConversationID, key(session, instance))
		reserved := session == ""
		if reserved {
			k = key(b.ConversationID, "reservation:"+a.ID)
		}
		if _, exists := st.Workers[k]; exists {
			return nil
		}
		association := WorkerAssociation{Reserved: reserved, ConversationID: b.ConversationID, Session: session, SessionInstance: instance, Admission: a.Admission, SourceID: input.SourceID, ActionID: a.ID}
		putRecord(st, "workers", &st.Workers, k, association)
		return nil
	})
}
func (s *Store) stopControllerWorker(b ManagerBinding, expected WorkerAssociation) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := key(b.ConversationID, key(expected.Session, expected.SessionInstance))
		actual, ok := st.Workers[k]
		if !ok {
			return ErrNotFound
		}
		actual.RecoveryRequired = expected.RecoveryRequired
		if actual != expected || expected.ConversationID != b.ConversationID || expected.Reserved {
			return ErrStale
		}
		deleteRecord(st, "workers", &st.Workers, k)
		return nil
	})
}

func (s *Store) controllerSpeechPermit(b ManagerBinding, seq uint64, input, turn string) (SpeechPermit, error) {
	var result SpeechPermit
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		admission, err := activeAdmission(st, b, seq)
		if err != nil {
			return err
		}
		if admission.NativeInputID != input || turn == "" {
			return ErrStale
		}
		k := key(b.ConversationID, turn)
		if admission.Phase != "running" || st.ClosedTurns[k] {
			existing, ok := st.Permits[k]
			if !ok {
				existing = SpeechPermit{ConversationID: b.ConversationID, Admission: seq, NativeInputID: input, NativeTurnID: turn}
			}
			existing.Revoked = true
			putRecord(st, "permits", &st.Permits, k, existing)
			result = existing
			return nil
		}
		if existing, ok := st.Permits[k]; ok {
			if existing.Revoked {
				result = existing
				return nil
			}
			if existing.Admission != seq || existing.NativeInputID != input {
				return ErrConflict
			}
			result = existing
			return nil
		}
		c := st.Conversations[b.ConversationID]
		if c.LastSpeechEpoch == ^uint64(0) {
			return ErrCapacity
		}
		c.LastSpeechEpoch++
		result = SpeechPermit{ConversationID: b.ConversationID, Admission: seq, NativeInputID: input, NativeTurnID: turn, Epoch: c.LastSpeechEpoch}
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
		putRecord(st, "permits", &st.Permits, k, result)
		return nil
	})
	return result, err
}

func (s *Store) acknowledgeControllerUncertainty(b ManagerBinding, ack InputRecoveryAck) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if st.Conversations[b.ConversationID].CustodyLost {
			return managerproof.ErrQuarantined
		}
		convo := st.Conversations[b.ConversationID]
		if convo.ExternalInput != ack.ExternalInput || !convo.ExternalObserved {
			return ErrStale
		}
		k := admissionKey(b.ConversationID, ack.Admission)
		a, ok := st.Admissions[k]
		if !ok || a.Phase != "completed" || !a.SettledAt.IsZero() || a.OperationID != ack.OperationID || a.NativeTurnID != ack.NativeTurnID {
			return ErrStale
		}
		unresolved := false
		for _, action := range st.Actions {
			if action.ConversationID == b.ConversationID && action.Admission == a.Sequence && action.State != "settled" {
				unresolved = true
			}
		}
		if !unresolved {
			return ErrInvalid
		}
		a.RecoveryAcknowledged = true
		putRecord(st, "admissions", &st.Admissions, k, a)
		return nil
	})
}
func (s *Store) restrictControllerRecovery(b ManagerBinding, seq uint64) (Admission, error) {
	var result Admission
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		a, ok := st.Admissions[admissionKey(b.ConversationID, seq)]
		if !ok {
			return ErrNotFound
		}
		if a.Phase != "" && a.Phase != "prepared" {
			return ErrConflict
		}
		if err := restrictRecoveryState(st, b, seq); err != nil {
			return err
		}
		result = st.Admissions[admissionKey(b.ConversationID, seq)]
		return nil
	})
	return cloneAdmission(result), err
}

func (s *Store) acceptControllerRecoverySnapshot(b ManagerBinding, cursor JournalCursor, snapshot JournalSnapshot, id, operation string) (Wake, error) {
	var result Wake
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		metadata, err := canonicalJSON(snapshot.Metadata)
		if err != nil {
			return err
		}
		k := key(b.ConversationID, id)
		if prior, ok := st.Events[k]; ok {
			if prior.Cursor != cursor || !prior.RecoveryOnly || string(prior.Metadata) != string(metadata) {
				return ErrConflict
			}
			w, ok := st.Wakes[k]
			if !ok {
				return ErrCorrupt
			}
			result = w
			return nil
		}
		if err = reconcileJournalState(st, b, cursor, snapshot); err != nil {
			return err
		}
		if err = quarantineControllerState(st, b, "", ""); err != nil {
			return err
		}
		text := "Daemon journal recovery snapshot: history is unavailable or the source changed. Inspect tracked workers and report uncertainty; do not rerun prior work. Snapshot reference: " + id
		raw, err := json.Marshal(struct {
			Text string `json:"text"`
		}{text})
		if err != nil {
			return err
		}
		a, err := allocateAdmission(st, b, "wake:"+id)
		if err != nil {
			return err
		}
		a.RecoveryOnly = true
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
		event := Event{ID: id, ConversationID: b.ConversationID, Cursor: cursor, Metadata: metadata, WakeID: id, RecoveryOnly: true}
		wake := Wake{ID: id, ConversationID: b.ConversationID, EventID: id, OperationID: operation, Request: raw, RequestHash: hashBytes(raw), State: "admitted", Admission: a.Sequence, RecoveryOnly: true}
		putRecord(st, "events", &st.Events, k, event)
		putRecord(st, "wakes", &st.Wakes, k, wake)
		result = wake
		return nil
	})
	return cloneWake(result), err
}

func (s *Store) retireUnsentControllerActions(b ManagerBinding, seq uint64) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		input, ok := st.Admissions[admissionKey(b.ConversationID, seq)]
		if !ok {
			return ErrNotFound
		}
		if input.Phase != "completed" && input.Phase != "rejected" {
			return ErrInvalid
		}
		for k, a := range st.Actions {
			if a.ConversationID == b.ConversationID && a.Admission == seq && a.State == "prepared" {
				a.State = "settled"
				a.Receipt = json.RawMessage(`{"state":"rejected","reason":"native input ended before dispatch"}`)
				a.SettledAt = time.Now().UTC()
				putRecord(st, "actions", &st.Actions, k, a)
				for wk, w := range st.Workers {
					if w.ConversationID == b.ConversationID && w.ActionID == a.ID {
						deleteRecord(st, "workers", &st.Workers, wk)
					}
				}
			}
		}
		return nil
	})
}

func restrictRecoveryState(st *State, b ManagerBinding, seq uint64) error {
	k := admissionKey(b.ConversationID, seq)
	a, ok := st.Admissions[k]
	if !ok {
		return ErrNotFound
	}
	if !a.SettledAt.IsZero() {
		return nil
	}
	if !a.RecoveryOnly && a.Phase == "prepared" {
		var req protocol.ComposerSendReq
		if json.Unmarshal(a.Request, &req) != nil {
			return ErrCorrupt
		}
		if !strings.HasPrefix(req.Text, recoveryInputNotice) {
			req.Text = recoveryInputNotice + req.Text
		}
		raw, err := json.Marshal(req)
		if err != nil {
			return err
		}
		raw, err = canonicalJSON(raw)
		if err != nil {
			return err
		}
		a.Request = raw
	}
	a.RecoveryOnly = true
	putRecord(st, "admissions", &st.Admissions, k, a)
	for wk, w := range st.Wakes {
		if w.ConversationID == b.ConversationID && w.Admission == seq {
			w.RecoveryOnly = true
			putRecord(st, "wakes", &st.Wakes, wk, w)
		}
	}
	return nil
}
func quarantineControllerState(st *State, b ManagerBinding, session, instance string) error {
	for wk, worker := range st.Workers {
		if worker.ConversationID != b.ConversationID || (session != "" && (worker.Session != session || worker.SessionInstance != instance)) {
			continue
		}
		// Losing the dispatcher observation or journal does not revoke the
		// independent supervisor grant of an ordinary mailbox worker.
		if session == "" && worker.supervisorMailbox() {
			continue
		}
		worker.RecoveryRequired = true
		putRecord(st, "workers", &st.Workers, wk, worker)
		if _, ok := st.Admissions[admissionKey(b.ConversationID, worker.Admission)]; ok {
			if err := restrictRecoveryState(st, b, worker.Admission); err != nil {
				return err
			}
		}
	}
	if session == "" {
		for _, a := range st.Admissions {
			// Discord human proof is independent of worker journal coverage.
			if a.ConversationID == b.ConversationID && a.SettledAt.IsZero() && a.Origin == nil && !queuedMailboxNote(st, b.ConversationID, a) {
				if err := restrictRecoveryState(st, b, a.Sequence); err != nil {
					return err
				}
			}
		}
		for k, u := range st.Utterances {
			if u.ConversationID == b.ConversationID && u.Admission == 0 && (u.Phase == "capturing" || u.Phase == "finalizing" || u.Phase == "committed") {
				u.RecoveryOnly = true
				putRecord(st, "utterances", &st.Utterances, k, u)
			}
		}
	}
	return nil
}
func (s *Store) noteDiscussionWorkerExit(b ManagerBinding, session, instance string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := key(b.ConversationID, key(session, instance))
		if worker, ok := st.Workers[k]; ok && !worker.Ended {
			worker.Ended = true
			putRecord(st, "workers", &st.Workers, k, worker)
		}
		return nil
	})
}

func (s *Store) acceptControllerWorkerEvent(b ManagerBinding, session, instance string, e Event, w Wake) (Wake, error) {
	var result Wake
	err := s.update(func(st *State) error {
		if e.WorkerProof {
			worker, ok := st.Workers[key(b.ConversationID, key(session, instance))]
			if !ok || e.ID != discussionEvidenceID(worker) || e.JobID != worker.JobID || e.AttemptID != worker.AttemptID || worker.RecoveryRequired && !e.RecoveryOnly {
				return ErrStale
			}
		}
		_, exists := st.Events[key(b.ConversationID, e.ID)]
		if err := acceptEventState(st, b, e, w, &result); err != nil {
			return err
		}

		if !exists && e.RecoveryOnly {
			return quarantineControllerState(st, b, session, instance)
		}
		return nil
	})
	return cloneWake(result), err
}

func (s *Store) checkpointControllerRecovery(b ManagerBinding, cursor JournalCursor, snapshot JournalSnapshot) error {
	return s.update(func(st *State) error {
		if err := reconcileJournalState(st, b, cursor, snapshot); err != nil {
			return err
		}
		return quarantineControllerState(st, b, "", "")
	})
}

func (s *Store) noteControllerCustodyLost(b ManagerBinding) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		c := st.Conversations[b.ConversationID]
		c.CustodyLost = true
		c.SchedulerPaused = true
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
		for k, p := range st.Permits {
			if p.ConversationID == b.ConversationID {
				p.Revoked = true
				putRecord(st, "permits", &st.Permits, k, p)
			}
		}
		if err := quarantineControllerState(st, b, "", ""); err != nil {
			return err
		}
		for k, a := range st.Admissions {
			if a.ConversationID == b.ConversationID && a.SettledAt.IsZero() {
				switch a.Phase {
				case "executing", "submitted", "admitted", "running":
					a.Phase = "uncertain"
					a.Reason = "native custody lost; original outcome remains unknown"
					putRecord(st, "admissions", &st.Admissions, k, a)
				}
			}
		}
		return nil
	})
}

func (s *Store) replaceControllerManager(previous, next ManagerBinding, external uint64) (ManagerBinding, error) {
	var result ManagerBinding
	err := s.update(func(st *State) error {
		if err := checkBinding(st, previous); err != nil {
			return err
		}
		c := st.Conversations[previous.ConversationID]
		if !c.CustodyLost || !validBinding(next) || next.ConversationID != previous.ConversationID || next.Provider != previous.Provider || next.SessionID == previous.SessionID {
			return ErrInvalid
		}
		for k, a := range st.Admissions {
			if a.ConversationID == previous.ConversationID && a.SettledAt.IsZero() {
				a.NativeBindingRetired = true
				a.RecoveryOnly = true
				putRecord(st, "admissions", &st.Admissions, k, a)
			}
		}
		if previous.ObservationGeneration == "" {
			for k, w := range st.Wakes {
				if w.ConversationID == previous.ConversationID && w.State == "prepared" {
					// Legacy wake payloads did not guarantee controller-native input text.
					w.State = "retired"
					putRecord(st, "wakes", &st.Wakes, k, w)
				}
			}
		}
		for k, u := range st.Utterances {
			if u.ConversationID == previous.ConversationID && u.Admission == 0 {
				u.Phase = "failed"
				u.RecoveryOnly = true
				putRecord(st, "utterances", &st.Utterances, k, u)
			}
		}
		for k, p := range st.Permits {
			if p.ConversationID == previous.ConversationID {
				p.Revoked = true
				putRecord(st, "permits", &st.Permits, k, p)
			}
		}
		for k, r := range st.Requests {
			if r.ConversationID == previous.ConversationID && r.Kind == "native" && r.State == "pending" {
				r.State = "expired"
				putRecord(st, "requests", &st.Requests, k, r)
			}
		}
		next.Generation = st.Generation
		c.Binding = identity(next)
		c.CustodyLost = false
		c.SchedulerPaused = false
		c.ExternalObserved = true
		c.ExternalInput = external
		putRecord(st, "conversations", &st.Conversations, next.ConversationID, c)
		recovery, err := allocateAdmission(st, next, "replacement:"+next.SessionID)
		if err != nil {
			return err
		}
		recovery.RecoveryOnly = true
		recovery.InputText = "Explicit fresh manager replacement. The old session exited, but its unproven native input and worker effects remain uncertain. Do not replay old operations or treat exit as task success. Inspect retained task references and ask the human before any new mutation. Old session: " + previous.SessionID
		putRecord(st, "admissions", &st.Admissions, admissionKey(next.ConversationID, recovery.Sequence), recovery)
		result = next
		return nil
	})
	return result, err
}

// The exited, deliberately replaced binding cannot create more typed effects.
// Task outcome stays uncertain; authority settles only after all retained effect receipts settle.
func (s *Store) settleRetiredControllerInput(b ManagerBinding, seq uint64) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := admissionKey(b.ConversationID, seq)
		a, ok := st.Admissions[k]
		if !ok {
			return ErrNotFound
		}
		if !a.NativeBindingRetired {
			return ErrInvalid
		}
		for _, effect := range st.Actions {
			if effect.ConversationID == b.ConversationID && effect.Admission == seq && effect.State != "settled" {
				return ErrControllerUncertain
			}
		}
		if a.NativeInputID == "" && a.OperationID == "" && len(a.Request) == 0 {
			a.Phase = "rejected"
			a.Reason = "manager replaced before dispatch"
			putRecord(st, "admissions", &st.Admissions, k, a)
		}
		return settleAdmission(st, b, seq)
	})
}

func queuedMailboxNote(st *State, conversation string, a Admission) bool {
	if a.Phase != "" && a.Phase != "prepared" || a.SourceCutover {
		return false
	}
	id, ok := strings.CutPrefix(a.SourceID, "wake:")
	if !ok {
		return false
	}
	w := st.Wakes[key(conversation, id)]
	e := st.Events[key(conversation, w.EventID)]
	var meta workerNoteMetadata
	if w.Admission != a.Sequence || !e.WorkerMessage || e.RecoveryOnly || json.Unmarshal(e.Metadata, &meta) != nil || meta.NoteID == "" {
		return false
	}
	worker := st.Workers[key(conversation, key(meta.Session, meta.Instance))]
	return worker.supervisorMailbox() && !worker.RecoveryRequired && worker.JobID == e.JobID && worker.AttemptID == e.AttemptID &&
		e.ID == "worker-message-"+hashValue([]string{worker.Session, worker.SessionInstance, worker.AttemptID, meta.NoteID})
}
