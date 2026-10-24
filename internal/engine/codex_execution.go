package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Nathandela/swarm/internal/persist"
)

type CodexExecutionKind string
type CodexItemKind = persist.CodexItemKind
type CodexAssistantPhase = persist.CodexAssistantPhase
type CodexTerminalCause = persist.CodexTerminalCause
type CodexDecisionKind = persist.CodexDecisionKind

const (
	CodexReasoning         CodexItemKind       = "reasoning"
	CodexPlan              CodexItemKind       = "plan"
	CodexTurnStarted       CodexExecutionKind  = "turn/started"
	CodexItemStarted       CodexExecutionKind  = "item/started"
	CodexItemCompleted     CodexExecutionKind  = "item/completed"
	CodexTurnCompleted     CodexExecutionKind  = "turn/completed"
	CodexDecisionRequested CodexExecutionKind  = "decision/requested"
	CodexDecisionResolved  CodexExecutionKind  = "decision/resolved"
	CodexChildStarted      CodexExecutionKind  = "child/started"
	CodexChildCompleted    CodexExecutionKind  = "child/completed"
	CodexRetryActive       CodexExecutionKind  = "retry/active"
	CodexUnknownEvidence   CodexExecutionKind  = "observation/unknown"
	CodexUserMessage       CodexItemKind       = "userMessage"
	CodexAgentMessage      CodexItemKind       = "agentMessage"
	CodexCommandExecution  CodexItemKind       = "commandExecution"
	CodexFileChange        CodexItemKind       = "fileChange"
	CodexMcpToolCall       CodexItemKind       = "mcpToolCall"
	CodexContextCompaction CodexItemKind       = "contextCompaction"
	CodexCommentary        CodexAssistantPhase = "commentary"
	CodexFinalAnswer       CodexAssistantPhase = "final_answer"
	CodexTerminalQuota     CodexTerminalCause  = "quota"
	CodexTerminalCompleted CodexTerminalCause  = "completed"
	CodexTerminalCancelled CodexTerminalCause  = "cancelled"
	CodexTerminalFailed    CodexTerminalCause  = "failed"
	CodexTerminalUnknown   CodexTerminalCause  = "unknown"
	CodexApproval          CodexDecisionKind   = "approval"
	CodexQuestion          CodexDecisionKind   = "question"
)

// ObservedFromStart requires independent proof of capture before native work,
// not merely the first connection this daemon happened to observe.
type CodexFeedIdentity struct {
	NativeIncarnation string
	FeedEpoch         string
	ThreadID          string
	OwnerRevision     uint64
	ObservedFromStart bool
}

// CodexExecutionEvent is already normalized authenticated native evidence.
// TerminalQuota requires turn/completed failed + codexErrorInfo=usageLimitExceeded;
// a standalone error, including willRetry=false, is never that terminal fact.
// ToolSucceeded requires native completed status and a successful tool result.
type CodexExecutionEvent struct {
	NativeIncarnation string
	FeedEpoch         string
	ThreadID          string
	TurnID            string
	Sequence          uint64
	Kind              CodexExecutionKind
	ItemID            string
	ItemKind          CodexItemKind
	Phase             CodexAssistantPhase
	ToolSucceeded     bool
	Cause             CodexTerminalCause
	RequestID         string
	Decision          CodexDecisionKind
	ChildID           string
	ChildTurnID       string
}

type CodexQuotaObligation struct {
	NativeIncarnation string
	FeedEpoch         string
	ThreadID          string
	TurnID            string
	OwnerRevision     uint64
	TerminalSequence  uint64
}

func codexID(id string) bool { return id != "" && len(id) <= 256 }

func cloneCodexExecution(c *persist.CodexExecution) *persist.CodexExecution {
	if c == nil {
		return nil
	}
	clone := *c
	clone.Items = make(map[string]persist.CodexExecutionItem, len(c.Items))
	for id, item := range c.Items {
		clone.Items[id] = item
	}
	clone.Children = make(map[string]persist.CodexExecutionChild, len(c.Children))
	for id, child := range c.Children {
		clone.Children[id] = child
	}
	clone.Decisions = make(map[string]persist.CodexExecutionDecision, len(c.Decisions))
	for id, decision := range c.Decisions {
		clone.Decisions[id] = decision
	}
	return &clone
}

func (e *Engine) BeginCodexExecutionFeed(id string, feed CodexFeedIdentity) error {
	if !codexID(feed.NativeIncarnation) || !codexID(feed.FeedEpoch) || !codexID(feed.ThreadID) {
		return fmt.Errorf("engine: invalid Codex feed identity")
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
	if current, ok := e.sessions[id]; !ok || current != s || !s.executionEnabled || s.token != "" || s.executionIdentity.ConversationID != feed.ThreadID || s.executionIdentity.OwnerRevision != feed.OwnerRevision {
		e.mu.Unlock()
		return fmt.Errorf("engine: Codex feed ownership mismatch")
	}
	c := s.codexExecution
	if c == nil {
		c = &persist.CodexExecution{NativeIncarnation: feed.NativeIncarnation, FeedEpoch: feed.FeedEpoch, ThreadID: feed.ThreadID, OwnerRevision: feed.OwnerRevision, State: "unobserved", Incomplete: "missing_turn_start", UnknownCustody: !feed.ObservedFromStart || s.untrackedChildren || s.children > 0, Items: make(map[string]persist.CodexExecutionItem), Children: make(map[string]persist.CodexExecutionChild), Decisions: make(map[string]persist.CodexExecutionDecision)}
		s.codexExecution = c
	} else if c.NativeIncarnation != feed.NativeIncarnation || c.FeedEpoch != feed.FeedEpoch || c.ThreadID != feed.ThreadID {
		c.NativeIncarnation = feed.NativeIncarnation
		c.FeedEpoch = feed.FeedEpoch
		c.ThreadID = feed.ThreadID
		c.Sequence = 0
		c.LastEventHash = ""
		c.TerminalSequence = 0
		c.UserSequence = 0
		c.CommentarySequence = 0
		c.ToolResultSequence = 0
		c.ToolFailureSequence = 0
		c.LastItemSequence = 0
		c.State = "unobserved"
		c.Cause = ""
		c.Incomplete = "feed_changed"
		c.UnknownCustody = true
	}
	err := e.persistExecutionLocked(id, s)
	e.mu.Unlock()
	return executionCustodyError(err)
}

func (e *Engine) ApplyCodexExecutionEvent(id string, event CodexExecutionEvent) error {
	e.mu.Lock()
	s, ok := e.sessions[id]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("engine: unknown session")
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	e.mu.Lock()
	c := s.codexExecution
	if current, ok := e.sessions[id]; !ok || current != s || c == nil || !s.executionEnabled || event.NativeIncarnation != c.NativeIncarnation || event.FeedEpoch != c.FeedEpoch || event.ThreadID != c.ThreadID {
		e.mu.Unlock()
		return fmt.Errorf("engine: Codex event ownership mismatch")
	}
	if len(event.Kind) > 64 || len(event.ItemKind) > 64 || len(event.Phase) > 64 || len(event.Cause) > 64 || len(event.Decision) > 64 || len(event.TurnID) > 256 || len(event.ItemID) > 256 || len(event.RequestID) > 256 || len(event.ChildID) > 256 || len(event.ChildTurnID) > 256 {
		c.Incomplete = "event_bounds"
		c.UnknownCustody = true
		err := e.persistExecutionLocked(id, s)
		e.mu.Unlock()
		if err != nil {
			return executionCustodyError(err)
		}
		return fmt.Errorf("engine: Codex event bounds")
	}
	raw, _ := json.Marshal(event)
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	if event.Sequence == c.Sequence && event.Sequence != 0 && fingerprint == c.LastEventHash {
		err := e.persistExecutionLocked(id, s)
		e.mu.Unlock()
		return executionCustodyError(err)
	}
	if event.Sequence == 0 || event.Sequence <= c.Sequence {
		c.UnknownCustody = true
		c.Incomplete = "conflicting_event"
		err := e.persistExecutionLocked(id, s)
		e.mu.Unlock()
		if err != nil {
			return executionCustodyError(err)
		}
		return fmt.Errorf("engine: stale or conflicting Codex execution event")
	}

	var eventErr error
	if event.Sequence != c.Sequence+1 {
		c.Incomplete = "sequence_gap"
		c.UnknownCustody = true
		eventErr = fmt.Errorf("engine: Codex execution sequence gap")
	}
	c.Sequence = event.Sequence
	c.LastEventHash = fingerprint
	if eventErr == nil {
		eventErr = reduceCodexExecution(s, c, event)
	}
	if eventErr != nil {
		c.Incomplete = "invalid_event"
		c.UnknownCustody = true
	}
	persistErr := e.persistExecutionLocked(id, s)
	e.mu.Unlock()
	if persistErr != nil {
		return executionCustodyError(persistErr)
	}
	return eventErr
}

func reduceCodexExecution(s *session, c *persist.CodexExecution, event CodexExecutionEvent) error {
	if event.Kind == CodexUnknownEvidence {
		c.Incomplete = "unknown_evidence"
		c.UnknownCustody = true
		return nil
	}
	if event.Kind == CodexDecisionResolved {
		decision, known := c.Decisions[event.RequestID]
		if !known || decision.NativeIncarnation != event.NativeIncarnation || decision.FeedEpoch != event.FeedEpoch || (event.TurnID != "" && event.TurnID != decision.TurnID) {
			return fmt.Errorf("engine: unknown Codex decision resolution")
		}
		delete(c.Decisions, event.RequestID)
		return nil
	}
	if !codexID(event.TurnID) {
		return fmt.Errorf("engine: missing Codex turn identity")
	}
	if event.Kind == CodexTurnStarted {
		if c.State == "active" {
			c.UnknownCustody = true
		}
		if c.TurnID == event.TurnID {
			return fmt.Errorf("engine: repeated Codex turn start")
		}
		for id, item := range c.Items {
			if item.Complete {
				delete(c.Items, id)
			} else {
				c.UnknownCustody = true
			}
		}
		for id, child := range c.Children {
			if !child.Active {
				delete(c.Children, id)
			}
		}
		if len(c.Decisions) > 0 || codexActiveChildren(c) {
			c.UnknownCustody = true
		}
		c.TurnID = event.TurnID
		c.OwnerRevision = s.executionIdentity.OwnerRevision
		c.State = "active"
		c.Cause = ""
		c.TerminalSequence = 0
		c.Denied = ""
		c.UserSequence = 0
		c.CommentarySequence = 0
		c.ToolResultSequence = 0
		c.ToolFailureSequence = 0
		c.LastItemSequence = 0
		c.Incomplete = ""
		if c.UnknownCustody {
			c.Incomplete = "unknown_custody"
		}
		return nil
	}
	if event.Kind == CodexChildCompleted {
		child, known := c.Children[event.ChildID]
		if !known || child.TurnID != event.TurnID || child.ChildTurnID != event.ChildTurnID || child.NativeIncarnation != event.NativeIncarnation || child.FeedEpoch != event.FeedEpoch {
			return fmt.Errorf("engine: unknown Codex child completion")
		}
		child.Active = false
		c.Children[event.ChildID] = child
		return nil
	}
	if event.TurnID != c.TurnID {
		return fmt.Errorf("engine: Codex turn identity mismatch")
	}
	switch event.Kind {
	case CodexTurnCompleted:
		if c.State != "active" {
			return fmt.Errorf("engine: terminal Codex turn was not active")
		}
		switch event.Cause {
		case CodexTerminalQuota, CodexTerminalCompleted, CodexTerminalCancelled, CodexTerminalFailed, CodexTerminalUnknown:
		default:
			return fmt.Errorf("engine: unknown Codex terminal cause")
		}
		c.State = "terminal"
		c.Cause = event.Cause
		c.TerminalSequence = event.Sequence
	case CodexRetryActive:
		c.State = "active"
		c.Cause = ""
		c.TerminalSequence = 0
	case CodexDecisionRequested:
		if !codexID(event.RequestID) || (event.Decision != CodexApproval && event.Decision != CodexQuestion) {
			return fmt.Errorf("engine: invalid Codex decision")
		}
		if _, known := c.Decisions[event.RequestID]; !known && !codexHasCapacity(c) {
			return fmt.Errorf("engine: Codex decision capacity")
		}
		if old, known := c.Decisions[event.RequestID]; known && (old.TurnID != event.TurnID || old.Kind != event.Decision || old.NativeIncarnation != event.NativeIncarnation || old.FeedEpoch != event.FeedEpoch) {
			return fmt.Errorf("engine: overlapping Codex decision identity")
		}
		c.Decisions[event.RequestID] = persist.CodexExecutionDecision{TurnID: event.TurnID, Kind: event.Decision, NativeIncarnation: event.NativeIncarnation, FeedEpoch: event.FeedEpoch}
		c.Denied = "human_decision"
	case CodexChildStarted:
		if !codexID(event.ChildID) || !codexID(event.ChildTurnID) {
			return fmt.Errorf("engine: invalid Codex child")
		}
		if old, known := c.Children[event.ChildID]; known && old.Active && (old.ChildTurnID != event.ChildTurnID || old.TurnID != event.TurnID) {
			return fmt.Errorf("engine: overlapping Codex child incarnation")
		}
		if _, known := c.Children[event.ChildID]; !known && !codexHasCapacity(c) {
			return fmt.Errorf("engine: Codex child capacity")
		}
		c.Children[event.ChildID] = persist.CodexExecutionChild{TurnID: event.TurnID, ChildTurnID: event.ChildTurnID, Active: true, NativeIncarnation: event.NativeIncarnation, FeedEpoch: event.FeedEpoch}
	case CodexItemStarted, CodexItemCompleted:
		return reduceCodexItem(c, event)
	default:
		return fmt.Errorf("engine: unknown Codex control event")
	}
	return nil
}

func codexHasCapacity(c *persist.CodexExecution) bool {
	return len(c.Items)+len(c.Children)+len(c.Decisions) < persist.MaxExecutionActors
}
func codexActiveChildren(c *persist.CodexExecution) bool {
	for _, child := range c.Children {
		if child.Active {
			return true
		}
	}
	return false
}

func reduceCodexItem(c *persist.CodexExecution, event CodexExecutionEvent) error {
	if !codexID(event.ItemID) || c.State != "active" {
		return fmt.Errorf("engine: Codex item without active turn")
	}
	kind := event.ItemKind
	switch kind {
	case CodexUserMessage, CodexAgentMessage, CodexCommandExecution, CodexFileChange, CodexMcpToolCall, CodexContextCompaction, CodexReasoning, CodexPlan:
	default:
		kind = "unknown"
		c.Denied = "unknown_item"
		c.UnknownCustody = true
	}
	phase := event.Phase
	if kind == CodexAgentMessage {
		switch phase {
		case CodexCommentary:
		case CodexFinalAnswer:
			c.Denied = "final_answer"
		default:
			phase = "unknown"
			c.Denied = "unknown_phase"
		}
	} else {
		phase = ""
	}
	if kind == CodexContextCompaction {
		c.Denied = "maintenance"
	}
	if kind != CodexReasoning && kind != CodexPlan {
		c.LastItemSequence = event.Sequence
	}
	if event.Kind == CodexItemStarted {
		if _, known := c.Items[event.ItemID]; known {
			return fmt.Errorf("engine: reused Codex item identity")
		}
		if !codexHasCapacity(c) {
			return fmt.Errorf("engine: Codex item capacity")
		}
		c.Items[event.ItemID] = persist.CodexExecutionItem{TurnID: event.TurnID, Kind: kind, Phase: phase, StartSequence: event.Sequence, NativeIncarnation: event.NativeIncarnation, FeedEpoch: event.FeedEpoch}
		return nil
	}
	item, known := c.Items[event.ItemID]
	if !known || item.Complete || item.TurnID != event.TurnID || item.Kind != kind || item.Phase != phase || item.NativeIncarnation != event.NativeIncarnation || item.FeedEpoch != event.FeedEpoch {
		return fmt.Errorf("engine: unmatched Codex item completion")
	}
	item.Complete = true
	c.Items[event.ItemID] = item
	switch kind {
	case CodexUserMessage:
		c.UserSequence = event.Sequence
	case CodexAgentMessage:
		if phase == CodexCommentary && item.StartSequence > c.ToolFailureSequence {
			c.CommentarySequence = event.Sequence
		}
	case CodexCommandExecution, CodexFileChange, CodexMcpToolCall:
		if !event.ToolSucceeded {
			c.ToolFailureSequence = event.Sequence
			if c.Denied == "" || c.Denied == "tool_unsuccessful" {
				c.Denied = "tool_unsuccessful"
			}
		} else if c.CommentarySequence > c.UserSequence && c.UserSequence > 0 && item.StartSequence > c.CommentarySequence {
			c.ToolResultSequence = event.Sequence
			if c.Denied == "tool_unsuccessful" && c.CommentarySequence > c.ToolFailureSequence {
				c.Denied = ""
			}
		}
	}
	return nil
}

// ObserveExecutionOwnerRevision belongs immediately before the existing owner
// write fence. Advancing it invalidates the old turn even if durability fails.
func (e *Engine) ObserveExecutionOwnerRevision(id string, expected, next uint64) error {
	if next == 0 || expected == ^uint64(0) || next != expected+1 {
		return fmt.Errorf("engine: invalid owner revision")
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
	if current, ok := e.sessions[id]; !ok || current != s || !s.executionEnabled || s.executionIdentity.OwnerRevision != expected {
		e.mu.Unlock()
		return fmt.Errorf("engine: stale owner revision")
	}
	s.executionIdentity.OwnerRevision = next
	if s.codexExecution != nil {
		s.codexExecution.Denied = "owner_input"
	}
	err := e.persistExecutionLocked(id, s)
	e.mu.Unlock()
	return executionCustodyError(err)
}

// CodexQuotaCandidate is evidence inspection, never dispatch authorization.
func (e *Engine) CodexQuotaCandidate(id string) (CodexQuotaObligation, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[id]
	if !ok || !s.executionEnabled || e.storeExecution == nil || s.executionPending || len(s.pendingCallbacks) > 0 || (s.executionInteraction != "none" && s.executionInteraction != "error") || s.untrackedChildren || s.children > 0 {
		return CodexQuotaObligation{}, false
	}
	c := s.codexExecution
	if c == nil || c.UnknownCustody || c.Incomplete != "" || c.Denied != "" || c.State != "terminal" || c.Cause != CodexTerminalQuota || c.OwnerRevision != s.executionIdentity.OwnerRevision || c.UserSequence == 0 || c.CommentarySequence <= c.UserSequence || c.ToolResultSequence <= c.CommentarySequence || c.LastItemSequence != c.ToolResultSequence || len(c.Decisions) > 0 || codexActiveChildren(c) {
		return CodexQuotaObligation{}, false
	}
	for _, item := range c.Items {
		if !item.Complete {
			return CodexQuotaObligation{}, false
		}
	}
	return CodexQuotaObligation{NativeIncarnation: c.NativeIncarnation, FeedEpoch: c.FeedEpoch, ThreadID: c.ThreadID, TurnID: c.TurnID, OwnerRevision: c.OwnerRevision, TerminalSequence: c.TerminalSequence}, true
}
