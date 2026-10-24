package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// ExecutionIdentity must identify the exact live native incarnation, never a credential.
type ExecutionIdentity struct {
	// ObservedFromStart requires owned launch plus complete ingress custody from birth.
	ObservedFromStart bool
	Incarnation       string
	ConversationID    string
	OwnerRevision     uint64
}

// RegisterExecutionSession restores custody before publishing the session to ingress.
// Missing or mismatched evidence is conservatively incomplete, including legacy sessions.
func (e *Engine) RegisterExecutionSession(id, token string, pid int, sources []adapter.SignalSource, initial status.Status, identity ExecutionIdentity, saved *persist.ExecutionRecord) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := registeredSession(token, pid, sources, initial)
	s.executionEnabled = true
	s.executionIdentity = identity
	s.executionIncomplete = "missing_snapshot"
	s.untrackedChildren = !identity.ObservedFromStart
	if identity.ObservedFromStart {
		s.executionIncomplete = "unqualified_native_turn"
	}
	if !identity.ObservedFromStart && (s.status.Interaction == status.InteractionNone || s.status.Interaction == status.InteractionUnknown) {
		s.status.Turn = status.TurnUnknown
	}
	var err error
	if saved != nil {
		err = restoreExecution(s, id, *saved)
	}
	e.sessions[id] = s
	return err
}

func executionSnapshot(id string, s *session) persist.ExecutionRecord {
	r := persist.ExecutionRecord{Version: persist.ExecutionVersion, AnonymousActors: s.anonymousChildren, UnknownActors: s.untrackedChildren, SessionID: id, Incarnation: s.executionIdentity.Incarnation, ConversationID: s.executionIdentity.ConversationID, OwnerRevision: s.executionIdentity.OwnerRevision, Incomplete: s.executionIncomplete, TurnSequence: s.turnSeq, InteractionSequence: s.interSeq, ChildSequence: s.childSeq, TypedSequence: s.typedSeq, MainTurn: s.executionTurn, Interaction: s.executionInteraction, InputActor: s.inputActor, Actors: make(map[string]persist.ExecutionActor, len(s.childStates))}
	r.Codex = cloneCodexExecution(s.codexExecution)
	if r.Codex != nil {
		r.TurnID = r.Codex.TurnID
	}
	r.PendingCallbacks = make(map[string]string, len(s.pendingCallbacks))
	for sequence, fingerprint := range s.pendingCallbacks {
		r.PendingCallbacks[strconv.FormatUint(sequence, 10)] = fingerprint
	}
	if s.executionPending {
		r.Incomplete = "persistence_pending"
	}
	for id, a := range s.childStates {
		r.Actors[id] = persist.ExecutionActor{Sequence: a.sequence, StartSequence: a.startSeq, Active: a.active}
	}
	return r
}

func (e *Engine) ExecutionSnapshot(id string) (persist.ExecutionRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[id]
	if !ok {
		return persist.ExecutionRecord{}, fmt.Errorf("engine: unknown session")
	}
	return executionSnapshot(id, s), nil
}

func restoreExecution(s *session, id string, r persist.ExecutionRecord) error {
	if err := r.Validate(); err != nil {
		s.executionIncomplete = "invalid_snapshot"
		s.untrackedChildren = true
		if s.status.Interaction == status.InteractionNone || s.status.Interaction == status.InteractionUnknown {
			s.status.Turn = status.TurnUnknown
		}
		return err
	}
	if r.SessionID != id || r.Incarnation != s.executionIdentity.Incarnation || (r.ConversationID != "" && r.ConversationID != s.executionIdentity.ConversationID) || r.OwnerRevision != s.executionIdentity.OwnerRevision {
		s.executionIncomplete = "identity_mismatch"
		s.untrackedChildren = true
		if s.status.Interaction == status.InteractionNone || s.status.Interaction == status.InteractionUnknown {
			s.status.Turn = status.TurnUnknown
		}
		return fmt.Errorf("engine: execution identity mismatch")
	}
	s.codexExecution = cloneCodexExecution(r.Codex)
	if s.codexExecution != nil {
		s.codexExecution.UnknownCustody = true
		s.codexExecution.Incomplete = "daemon_restart"
	}
	s.pendingCallbacks = make(map[uint64]string, len(r.PendingCallbacks))
	for sequence, fingerprint := range r.PendingCallbacks {
		n, _ := strconv.ParseUint(sequence, 10, 64)
		s.pendingCallbacks[n] = fingerprint
	}
	s.executionIncomplete = r.Incomplete
	if r.ConversationID == "" && s.executionIdentity.ConversationID != "" {
		s.executionIncomplete = "conversation_binding_pending"
	}
	if s.executionIncomplete == "" {
		s.executionIncomplete = "reconciliation_required"
	}
	s.turnSeq = r.TurnSequence
	s.interSeq = r.InteractionSequence
	s.childSeq = r.ChildSequence
	s.typedSeq = r.TypedSequence
	s.mainTurn = r.MainTurn
	s.executionTurn = r.MainTurn
	s.executionInteraction = r.Interaction
	s.status.Interaction = r.Interaction
	s.inputActor = r.InputActor
	s.children = r.AnonymousActors
	s.anonymousChildren = r.AnonymousActors
	s.untrackedChildren = r.UnknownActors
	s.childStates = make(map[string]childState, len(r.Actors))
	for id, a := range r.Actors {
		s.childStates[id] = childState{sequence: a.Sequence, startSeq: a.StartSequence, active: a.Active}
		if a.Active {
			s.children++
		}
	}
	s.status.Turn = s.mainTurn
	if s.children > 0 && s.status.Interaction == status.InteractionNone {
		s.status.Turn = status.TurnActive
	} else if s.untrackedChildren && s.status.Interaction == status.InteractionNone {
		s.status.Turn = status.TurnUnknown
	}
	return nil
}

// RestoreExecution is for a caller already holding an ingress barrier. Production
// registration uses RegisterExecutionSession to avoid a register/restore race.
func (e *Engine) RestoreExecution(id string, r persist.ExecutionRecord) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[id]
	if !ok {
		return fmt.Errorf("engine: unknown session")
	}
	return restoreExecution(s, id, r)
}

// InvalidateExecution records an observed gap/loss before any later automatic effect.
func (e *Engine) InvalidateExecution(id string) error {
	e.mu.Lock()
	s, ok := e.sessions[id]
	enabled := ok && s.executionEnabled
	if enabled {
		// Fence immediately; a slow earlier writer must not delay observed loss.
		s.executionIncomplete = "observation_gap"
		s.untrackedChildren = true
		if s.codexExecution != nil {
			s.codexExecution.UnknownCustody = true
			s.codexExecution.Incomplete = "observation_gap"
		}
	}
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("engine: unknown session")
	}
	if !enabled {
		return nil
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	e.mu.Lock()
	if current, ok := e.sessions[id]; !ok || current != s || !s.alive {
		e.mu.Unlock()
		return fmt.Errorf("engine: retired session")
	}
	err := e.persistExecutionLocked(id, s)
	e.mu.Unlock()
	return err
}

// Called with e.mu held and the session's emitMu held; slow disk cannot stall
// unrelated sessions. Failure retains live custody and permanently fences effects.
func (e *Engine) persistExecutionLocked(id string, s *session) error {
	if !s.executionEnabled || e.storeExecution == nil {
		return nil
	}
	r := executionSnapshot(id, s)
	s.executionPending = true
	e.mu.Unlock()
	err := e.storeExecution(r)
	e.mu.Lock()
	s.executionPending = false
	if err != nil {
		s.executionIncomplete = "persistence_failure"
		if s.codexExecution != nil {
			s.codexExecution.Incomplete = "persistence_failure"
		}
	}
	return err
}

// BindExecutionConversation accepts the first authenticated native identity without
// replacing live custody. A different nonempty conversation requires a new incarnation.
func (e *Engine) BindExecutionConversation(id, incarnation, conversation string) error {
	if conversation == "" || len(conversation) > 256 {
		return fmt.Errorf("engine: invalid conversation identity")
	}
	e.mu.Lock()
	s, ok := e.sessions[id]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("engine: unknown session")
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	e.mu.Lock()
	if current, ok := e.sessions[id]; !ok || current != s || s.executionIdentity.Incarnation != incarnation {
		e.mu.Unlock()
		return fmt.Errorf("engine: incarnation mismatch")
	}
	if previous := s.executionIdentity.ConversationID; previous != "" && previous != conversation {
		e.mu.Unlock()
		return fmt.Errorf("engine: conversation already bound")
	}
	s.executionIdentity.ConversationID = conversation
	err := e.persistExecutionLocked(id, s)
	e.mu.Unlock()
	return err
}

// ExecutionCustodyError keeps a valid hook record in durable ingress for retry.
type ExecutionCustodyError struct{ Err error }

func (e *ExecutionCustodyError) Error() string { return "engine: execution custody: " + e.Err.Error() }
func (e *ExecutionCustodyError) Unwrap() error { return e.Err }
func executionCustodyError(err error) error {
	if err == nil {
		return nil
	}
	return &ExecutionCustodyError{Err: err}
}

func callbackFingerprint(cb Callback) string {
	cb.Token = "" // Live authentication is checked separately; never retain a token digest.
	nativeBody := cb.Raw
	cb.Raw = nil
	raw, _ := json.Marshal(cb)
	digest := sha256.New()
	_, _ = digest.Write(raw)
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(nativeBody)
	return hex.EncodeToString(digest.Sum(nil))
}

// AcknowledgeExecutionCallback releases only exact callback custody, after the
// caller has durably committed its outer interaction/seen fold. Failed cleanup
// retains the exact receipt for retry.
func (e *Engine) AcknowledgeExecutionCallback(cb Callback) error {
	e.mu.Lock()
	s, ok := e.sessions[cb.SessionID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("engine: unknown session")
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	e.mu.Lock()
	if current, ok := e.sessions[cb.SessionID]; !ok || current != s || !s.alive || cb.Token == "" || cb.Token != s.token {
		e.mu.Unlock()
		return fmt.Errorf("engine: callback ownership mismatch")
	}
	expected, pending := s.pendingCallbacks[cb.Sequence]
	if !pending {
		e.mu.Unlock()
		return nil
	}
	if expected != callbackFingerprint(cb) {
		e.mu.Unlock()
		return fmt.Errorf("engine: callback custody fingerprint mismatch")
	}
	delete(s.pendingCallbacks, cb.Sequence)
	err := e.persistExecutionLocked(cb.SessionID, s)
	if err != nil {
		s.pendingCallbacks[cb.Sequence] = expected
	}
	e.mu.Unlock()
	return executionCustodyError(err)
}
