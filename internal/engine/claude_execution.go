package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func newClaudeExecution(conversation string) *persist.ClaudeExecution {
	return &persist.ClaudeExecution{BaseInteraction: status.InteractionNone, NativeSessionID: conversation, StartupPrebootstrapUnknown: true, Invocations: map[string]persist.ClaudeInvocation{}, Calls: map[string]persist.ClaudeCall{}, Turns: map[string]persist.ClaudeTurn{}, Children: map[string]persist.ClaudeChild{}, Cancelled: map[string]persist.ClaudeCancellation{}}
}
func cloneClaudeExecution(c *persist.ClaudeExecution) *persist.ClaudeExecution {
	if c == nil {
		return nil
	}
	copy := *c
	copy.Invocations = make(map[string]persist.ClaudeInvocation, len(c.Invocations))
	for key, frame := range c.Invocations {
		frame.Context = bytes.Clone(frame.Context)
		frame.Data = bytes.Clone(frame.Data)
		copy.Invocations[key] = frame
	}
	copy.Calls = maps.Clone(c.Calls)
	copy.Turns = maps.Clone(c.Turns)
	copy.Children = maps.Clone(c.Children)
	copy.Cancelled = maps.Clone(c.Cancelled)
	return &copy
}
func claudeReady(e *Engine, s *session) bool {
	return s.executionIdentity.ClaudeNativeControl && s.executionEnabled && e.storeExecution != nil && s.claudeExecution != nil && s.codexExecution == nil
}

// AuthenticateClaudeControl checks owned durable ingress before first identity admission.
func (e *Engine) AuthenticateClaudeControl(envelope claudeinput.ControlEnvelope) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[envelope.SessionID]
	if !ok || !s.alive || !claudeReady(e, s) || s.token == "" || s.token != envelope.Token || s.pid != envelope.ShimPID || s.executionIdentity.Incarnation != fmt.Sprintf("%d:%d", envelope.ShimPID, envelope.ShimStartTime) || s.executionIdentity.ConversationID != "" && s.executionIdentity.ConversationID != envelope.Worker.NativeSessionID {
		return executionCustodyError(errors.New("claude control authentication/store mismatch"))
	}
	if s.claudeExecution.Failure != "" && s.claudeExecution.Failure != "persistence_failure" {
		return executionCustodyError(errors.New("claude control observation already invalid"))
	}
	return nil
}

// ApplyClaudeControlEnvelope retains authenticated metadata before spool folding.
func (e *Engine) ApplyClaudeControlEnvelope(id string, envelope claudeinput.ControlEnvelope) error {
	raw, err := json.Marshal(envelope)
	if err != nil {
		return executionCustodyError(err)
	}
	if _, err = claudeinput.DecodeControlEnvelope(raw); err != nil {
		return executionCustodyError(err)
	}
	e.mu.Lock()
	s, ok := e.sessions[id]
	e.mu.Unlock()
	if !ok {
		return executionCustodyError(errors.New("claude control session missing"))
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	defer e.publishClaudeCustody(id, s)
	e.mu.Lock()
	if current, ok := e.sessions[id]; !ok || current != s || !s.alive || !claudeReady(e, s) || envelope.SessionID != id || envelope.Token == "" || envelope.Token != s.token || s.pid != envelope.ShimPID || s.executionIdentity.Incarnation != fmt.Sprintf("%d:%d", envelope.ShimPID, envelope.ShimStartTime) || s.executionIdentity.ConversationID != envelope.Worker.NativeSessionID {
		e.mu.Unlock()
		return executionCustodyError(errors.New("claude control ownership/store unavailable"))
	}
	c := s.claudeExecution
	if c.Failure != "" && c.Failure != "persistence_failure" {
		e.mu.Unlock()
		return executionCustodyError(errors.New("claude control failed epoch cannot fold"))
	}
	fingerprint := claudeinput.ControlEnvelopeFingerprint(envelope)
	if envelope.Sequence == c.Sequence && fingerprint == c.LastFingerprint {
		err := e.persistExecutionLocked(id, s)
		e.mu.Unlock()
		return executionCustodyError(err)
	}
	failure := ""
	if c.Sequence > 0 && (c.DescriptorGeneration != envelope.DescriptorGeneration || c.ObserverEpoch != envelope.Worker.Generation || c.NativeSessionID != envelope.Worker.NativeSessionID) {
		failure = "observer_identity_conflict"
	} else if envelope.Sequence != c.Sequence+1 {
		failure = "sequence_conflict"
	}
	if failure != "" {
		c.Failure = failure
		s.claudeLeaseUntil = time.Time{}
		err := e.persistExecutionLocked(id, s)
		e.mu.Unlock()
		return executionCustodyError(errors.Join(errors.New("claude control feed conflict"), err))
	}
	candidate := cloneClaudeExecution(c)
	candidate.DescriptorGeneration = envelope.DescriptorGeneration
	candidate.ObserverEpoch = envelope.Worker.Generation
	candidate.NativeSessionID = envelope.Worker.NativeSessionID
	candidate.Sequence = envelope.Sequence
	candidate.LastFingerprint = fingerprint
	if err = reduceClaudeExecution(candidate, envelope); err != nil {
		c.Failure = "invalid_observation"
		s.claudeLeaseUntil = time.Time{}
		persistErr := e.persistExecutionLocked(id, s)
		e.mu.Unlock()
		return executionCustodyError(errors.Join(err, persistErr))
	}
	if err = candidate.Validate(); err != nil {
		c.Failure = "observation_capacity"
		s.claudeLeaseUntil = time.Time{}
		persistErr := e.persistExecutionLocked(id, s)
		e.mu.Unlock()
		return executionCustodyError(errors.Join(err, persistErr))
	}
	s.claudeExecution = candidate
	s.claudeLeaseUntil = time.Time{}
	withdrawClaudeWorkingLocked(s, e.now())
	err = e.persistExecutionLocked(id, s)
	if err != nil {
		failure := s.claudeExecution.Failure
		s.claudeExecution = c
		if failure != "" && failure != "persistence_failure" {
			c.Failure = failure
		} else {
			c.Failure = "persistence_failure"
		}
		s.claudeLeaseUntil = time.Time{}
	}
	e.mu.Unlock()
	return executionCustodyError(err)
}

type claudeContext struct {
	ActorID        string `json:"agent_id"`
	TurnID         string `json:"turn_id"`
	Index          uint64 `json:"index"`
	ToolID         string `json:"tool_use_id"`
	Tool           string `json:"tool"`
	ContextUnknown bool   `json:"context_unknown"`
	ParentActorID  string `json:"parent_agent_id"`
	Origin         *struct {
		Plugin string `json:"plugin"`
		Tier   string `json:"tier"`
	} `json:"origin"`
}
type claudeData struct {
	Reason       string `json:"reason"`
	Aborted      bool   `json:"is_aborted"`
	Approver     string `json:"approver"`
	Decision     string `json:"decision"`
	RequestID    string `json:"request_id"`
	AgentStatus  string `json:"agent_status"`
	AgentID      string `json:"agent_id"`
	Background   bool   `json:"background"`
	Fork         bool   `json:"fork"`
	SubagentType string `json:"subagent_type"`
	Provider     *struct {
		Plugin string `json:"plugin"`
		Tier   string `json:"tier"`
	} `json:"provider"`
	Teammate bool `json:"is_teammate_present"`
	Workflow bool `json:"workflow_present"`
	Resume   bool `json:"resume_present"`
	Name     bool `json:"name_present"`
	Denied   bool `json:"denied"`
	IsError  bool `json:"is_error"`
}

func nativeClaudeOrigin(context claudeContext) bool {
	return context.Origin != nil && context.Origin.Plugin == "engine" && context.Origin.Tier == "core"
}
func reduceClaudeExecution(c *persist.ClaudeExecution, event claudeinput.ControlEnvelope) error {
	var context claudeContext
	var data claudeData
	if json.Unmarshal(event.Context, &context) != nil || json.Unmarshal(event.Data, &data) != nil {
		return errors.New("claude metadata decode")
	}
	key := strconv.FormatUint(event.Invocation, 10)
	turnKey := persist.ClaudeCustodyKey(context.ActorID, context.TurnID)
	callKey := persist.ClaudeCustodyKey(context.ActorID, context.TurnID, context.ToolID)
	if event.Phase == "entered" {
		c.Invocations[key] = persist.ClaudeInvocation{Event: event.NativeEvent, Sequence: event.Sequence, Context: bytes.Clone(event.Context), Data: bytes.Clone(event.Data)}
		switch event.NativeEvent {
		case "turn.start", "turn.step":
			turn := c.Turns[turnKey]
			if event.NativeEvent == "turn.start" && turn.TurnID != "" {
				break
			}
			turn.ActorID = context.ActorID
			turn.TurnID = context.TurnID
			turn.Invocation = event.Invocation
			turn.NativeOrigin = nativeClaudeOrigin(context)
			if event.NativeEvent == "turn.step" {
				turn.ActiveStep = true
				turn.Index = context.Index
			}
			c.Turns[turnKey] = turn
		case "tool.call":
			if _, exists := c.Calls[callKey]; exists {
				return errors.New("claude native call identity reused")
			}
			c.Calls[callKey] = persist.ClaudeCall{ActorID: context.ActorID, TurnID: context.TurnID, ToolID: context.ToolID, Tool: context.Tool, Invocation: event.Invocation, ContextKnown: !context.ContextUnknown && context.TurnID != "" && nativeClaudeOrigin(context) && c.Turns[turnKey].TurnID == context.TurnID}
		}
		return nil
	}
	frame, exists := c.Invocations[key]
	if !exists || frame.Event != event.NativeEvent || !claudeinput.ControlTerminalMatches(frame.Event, frame.Context, frame.Data, event) {
		return errors.New("claude control invocation mismatch")
	}
	delete(c.Invocations, key)
	if event.Phase == "threw" {
		if event.NativeEvent == "turn.step" {
			turn := c.Turns[turnKey]
			if turn.Invocation == event.Invocation {
				turn.ActiveStep = false
				c.Turns[turnKey] = turn
			}
		}
		return nil
	}
	switch event.NativeEvent {
	case "turn.step":
		turn, exists := c.Turns[turnKey]
		if !exists || turn.Invocation != event.Invocation {
			return errors.New("claude step incarnation mismatch")
		}
		turn.ActiveStep = false
		c.Turns[turnKey] = turn
	case "turn.complete":
		if data.Reason == "aborted" && data.Aborted {
			c.Cancelled[turnKey] = persist.ClaudeCancellation{ActorID: context.ActorID, TurnID: context.TurnID, EnteredSequence: event.Invocation, ReturnedSequence: event.Sequence}
		}
		_, observed := c.Turns[turnKey]
		delete(c.Turns, turnKey)
		otherTurn := false
		for _, turn := range c.Turns {
			if turn.ActorID == context.ActorID {
				otherTurn = true
			}
		}
		if observed && !otherTurn && context.ActorID != "" {
			delete(c.Children, context.ActorID)
			for key, call := range c.Calls {
				if call.Returned && call.SpawnActorID == context.ActorID {
					delete(c.Calls, key)
				}
			}
		}
	case "tool.call":
		call, exists := c.Calls[callKey]
		if !exists || call.Invocation != event.Invocation {
			return errors.New("claude tool incarnation mismatch")
		}
		if call.Tool != "Agent" {
			delete(c.Calls, callKey)
		} else {
			call.Returned = true
			call.AgentStatus = data.AgentStatus
			call.AgentActorID = data.AgentID
			if data.Denied || data.IsError {
				call.AgentStatus = ""
			}
			c.Calls[callKey] = call
			joinClaudeChild(c, call)
			openSpawn := false
			for _, frame := range c.Invocations {
				if frame.Event == "agent.spawn" {
					var spawn claudeContext
					_ = json.Unmarshal(frame.Context, &spawn)
					if spawn.ToolID == call.ToolID {
						openSpawn = true
					}
				}
			}
			if !openSpawn && (data.AgentStatus == "completed" || data.AgentStatus == "remote_launched" || (data.Denied && call.SpawnActorID == "")) {
				delete(c.Calls, callKey)
			}
		}
	case "tool.check":
		if call, exists := c.Calls[callKey]; exists && !call.Returned && context.ToolID != "" {
			call.AskPerson = data.Decision == "ask" && data.Approver == "person"
			c.Calls[callKey] = call
		}
	case "ui.render":
		matches := 0
		var matched string
		for key, call := range c.Calls {
			if call.ToolID == data.RequestID && call.Tool == "AskUserQuestion" && call.ContextKnown && !call.Returned {
				matches++
				matched = key
			}
		}
		if matches == 1 {
			call := c.Calls[matched]
			call.HumanRequestID = data.RequestID
			c.Calls[matched] = call
		}
	case "agent.spawn":
		if data.Denied {
			return nil
		}
		matches := 0
		var matched string
		for key, call := range c.Calls {
			if call.ToolID == context.ToolID && call.Tool == "Agent" && call.ContextKnown && call.ActorID == context.ParentActorID {
				matches++
				matched = key
			}
		}
		if matches == 1 {
			call := c.Calls[matched]
			call.SpawnActorID = data.AgentID
			call.SpawnSequence = event.Invocation
			call.FreshBackground = data.Background && !data.Fork && !data.Teammate && !data.Workflow && !data.Resume && !data.Name && data.SubagentType == "general-purpose" && data.Provider != nil && data.Provider.Plugin == "engine" && data.Provider.Tier == "core" && nativeClaudeOrigin(context)
			c.Calls[matched] = call
			joinClaudeChild(c, call)
		}
	}
	return nil
}
func joinClaudeChild(c *persist.ClaudeExecution, call persist.ClaudeCall) {
	if call.Returned && call.FreshBackground && call.AgentStatus == "async_launched" && call.AgentActorID != "" && call.AgentActorID == call.SpawnActorID {
		c.Children[call.SpawnActorID] = persist.ClaudeChild{ToolID: call.ToolID, ParentActorID: call.ActorID, ParentTurnID: call.TurnID, SpawnSequence: call.SpawnSequence}
	}
}

func markClaudeLeaseLost(s *session, reason string) {
	s.claudeLeaseUntil = time.Time{}
	if s.claudeExecution.Failure == "" {
		s.claudeExecution.Failure = reason
		s.claudeLossPending = true
	}
}

// A storage writer may own emitMu. Withdraw the positive assertion under the
// state lock now; its ordered publisher flushes the negative status afterward.
func withdrawClaudeWorkingLocked(s *session, now time.Time) {
	if s.status.Turn == status.TurnActive && (s.claudeExecution.Failure != "" || s.executionPending || !now.Before(s.claudeLeaseUntil)) {
		next := s.status
		next.Turn = status.TurnUnknown
		if commit(s, next) {
			s.claudeProjectionDirty = true
		}
	}
}
func projectClaudeCustody(s *session, next status.Status, now time.Time) status.Status {
	c := s.claudeExecution
	if c == nil || next.Process != status.ProcessRunning {
		return next
	}
	evidence := c
	base := s.claudeBaseInteraction
	if s.executionPending {
		evidence = s.claudeCommitted
		if evidence != nil {
			base = evidence.BaseInteraction
		}
	}
	next.Turn = status.TurnUnknown
	next.Interaction = base
	next.Attention = next.Interaction == status.InteractionPrompt || next.Interaction == status.InteractionPermission
	for _, call := range safeClaudeCalls(evidence) {
		if call.HumanRequestID != "" {
			next.Interaction = status.InteractionPrompt
			next.Attention = true
		}
	}
	if c.Failure == "" && !s.executionPending && now.Before(s.claudeLeaseUntil) {
		for key, turn := range c.Turns {
			_, knownChild := c.Children[turn.ActorID]
			_, cancelled := c.Cancelled[key]
			if !cancelled && turn.ActiveStep && turn.NativeOrigin && (turn.ActorID == "" || knownChild && turn.Invocation > c.Children[turn.ActorID].SpawnSequence) {
				next.Turn = status.TurnActive
				break
			}
		}
	}
	return next
}
func (e *Engine) publishClaudeCustody(id string, s *session) {
	e.mu.Lock()
	if current, ok := e.sessions[id]; !ok || current != s || !s.alive || s.claudeExecution == nil {
		e.mu.Unlock()
		return
	}
	if !s.claudeLeaseUntil.IsZero() && !e.now().Before(s.claudeLeaseUntil) {
		markClaudeLeaseLost(s, "producer_lease_expired")
	}
	if s.claudeLossPending && claudeReady(e, s) {
		_ = e.persistExecutionLocked(id, s)
	}
	if current, ok := e.sessions[id]; !ok || current != s || !s.alive {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	e.emitClaudeCurrent(id, s, false)
}

// SetClaudeObservationLease consumes only the existing authenticated broker view.
// Failed proof withdraws liveness before waiting on any writer; no disk snapshot
// or delayed response can create a new lease. Existing Tick retries publication.
func (e *Engine) SetClaudeObservationLease(id string, view claudeinput.ObservationLease, roundtrip time.Duration) error {
	e.mu.Lock()
	s, ok := e.sessions[id]
	if !ok || !s.alive || !claudeReady(e, s) {
		e.mu.Unlock()
		return executionCustodyError(errors.New("claude lease session/store unavailable"))
	}
	c := s.claudeExecution
	s.claudeLeaseUntil = time.Time{}
	if c.Sequence == 0 {
		e.mu.Unlock()
		e.emitClaudeCurrent(id, s, false)
		return nil
	}
	identity := c.Sequence > 0 && view.Generation == c.DescriptorGeneration && view.Worker.NativeSessionID == c.NativeSessionID && view.Worker.Generation == c.ObserverEpoch && view.Worker.Version == "2.1.310"
	if !identity || view.Revoked || !view.SinkAvailable {
		markClaudeLeaseLost(s, "producer_lost")
	} else if view.Live && view.Captured == c.Sequence && view.Durable == c.Sequence && view.Queued == 0 && view.LeaseRemainingMS > 0 && view.LeaseRemainingMS <= 3000 && roundtrip >= 0 {
		remaining := time.Duration(view.LeaseRemainingMS)*time.Millisecond - roundtrip
		if remaining > 0 && c.Failure == "" {
			s.claudeLeaseUntil = e.now().Add(remaining)
		} else if remaining <= 0 {
			markClaudeLeaseLost(s, "producer_lease_expired")
		}
	} else if view.GuardAgeMS >= 3000 {
		markClaudeLeaseLost(s, "producer_lost")
	}
	withdrawClaudeWorkingLocked(s, e.now())
	e.mu.Unlock()
	e.emitClaudeCurrent(id, s, false)
	if s.emitMu.TryLock() {
		e.publishClaudeCustody(id, s)
		s.emitMu.Unlock()
	}
	return nil
}

func safeClaudeCalls(c *persist.ClaudeExecution) map[string]persist.ClaudeCall {
	if c == nil {
		return nil
	}
	return c.Calls
}

// Status serialization is independent of execution storage. Every Claude emit
// rechecks the exact session and current projection after taking this lock.
func (e *Engine) emitClaudeCurrent(id string, s *session, force bool) {
	s.claudeStatusMu.Lock()
	defer s.claudeStatusMu.Unlock()
	e.mu.Lock()
	if current, ok := e.sessions[id]; !ok || current != s || !s.alive || s.claudeExecution == nil {
		e.mu.Unlock()
		return
	}
	next := projectClaudeCustody(s, s.status, e.now())
	changed := commit(s, next) || s.claudeProjectionDirty || force
	s.claudeProjectionDirty = false
	e.mu.Unlock()
	if changed {
		e.emit(id, next)
	}
}

// PublishClaudeExecutionStatus is the existing post-reconcile/pre-ingress seam.
func (e *Engine) PublishClaudeExecutionStatus(id string) {
	e.mu.Lock()
	s, ok := e.sessions[id]
	e.mu.Unlock()
	if ok {
		e.emitClaudeCurrent(id, s, true)
	}
}

// Ordinary merge reads its preserved base, never its own projected human wait.
func applySignalWithClaudeBase(s *session, event string, payload map[string]string, sequence uint64, now time.Time) (status.Status, bool, bool, error) {
	if s.claudeExecution == nil {
		return applySignal(s, event, payload, sequence, now)
	}
	projected := s.status
	s.status.Interaction = s.claudeBaseInteraction
	s.status.Attention = s.claudeBaseInteraction == status.InteractionPrompt || s.claudeBaseInteraction == status.InteractionPermission
	next, advanced, typed, err := applySignal(s, event, payload, sequence, now)
	s.status = projected
	return next, advanced, typed, err
}
