package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/Nathandela/swarm/internal/persist"
)

type CodexExecutionKind string
type CodexItemKind = persist.CodexItemKind
type CodexAssistantPhase = persist.CodexAssistantPhase
type CodexTerminalCause = persist.CodexTerminalCause
type CodexDecisionKind = persist.CodexDecisionKind

const (
	CodexReasoning           CodexItemKind       = "reasoning"
	CodexPlan                CodexItemKind       = "plan"
	CodexTurnStarted         CodexExecutionKind  = "turn/started"
	CodexItemStarted         CodexExecutionKind  = "item/started"
	CodexItemCompleted       CodexExecutionKind  = "item/completed"
	CodexTurnCompleted       CodexExecutionKind  = "turn/completed"
	CodexDecisionRequested   CodexExecutionKind  = "decision/requested"
	CodexDecisionResolved    CodexExecutionKind  = "decision/resolved"
	CodexRetryActive         CodexExecutionKind  = "retry/active"
	CodexUnknownEvidence     CodexExecutionKind  = "observation/unknown"
	CodexUserMessage         CodexItemKind       = "userMessage"
	CodexAgentMessage        CodexItemKind       = "agentMessage"
	CodexCommandExecution    CodexItemKind       = "commandExecution"
	CodexFileChange          CodexItemKind       = "fileChange"
	CodexMcpToolCall         CodexItemKind       = "mcpToolCall"
	CodexCollabAgentToolCall CodexItemKind       = "collabAgentToolCall"
	CodexContextCompaction   CodexItemKind       = "contextCompaction"
	CodexCommentary          CodexAssistantPhase = "commentary"
	CodexFinalAnswer         CodexAssistantPhase = "final_answer"
	CodexTerminalQuota       CodexTerminalCause  = "quota"
	CodexTerminalCompleted   CodexTerminalCause  = "completed"
	CodexTerminalCancelled   CodexTerminalCause  = "cancelled"
	CodexTerminalFailed      CodexTerminalCause  = "failed"
	CodexTerminalUnknown     CodexTerminalCause  = "unknown"
	CodexApproval            CodexDecisionKind   = "approval"
	CodexQuestion            CodexDecisionKind   = "question"
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
	ClientID          string
	ActorThreadID     string
	CollabTool        string
	ReceiverThreadIDs []string
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
	clone.HostInputs = make(map[string]persist.CodexHostInput, len(c.HostInputs))
	for id, input := range c.HostInputs {
		clone.HostInputs[id] = input
	}
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
	clone.Stimuli = make(map[string]persist.CodexExecutionStimulus, len(c.Stimuli))
	for id, stimulus := range c.Stimuli {
		clone.Stimuli[id] = stimulus
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
	defer e.publishCodexCustody(id, s)
	e.mu.Lock()
	if current, ok := e.sessions[id]; !ok || current != s || !s.executionEnabled || s.token != "" || s.executionIdentity.ConversationID != feed.ThreadID || s.executionIdentity.OwnerRevision != feed.OwnerRevision {
		e.mu.Unlock()
		return fmt.Errorf("engine: Codex feed ownership mismatch")
	}
	c := s.codexExecution
	if c == nil {
		c = &persist.CodexExecution{NativeIncarnation: feed.NativeIncarnation, FeedEpoch: feed.FeedEpoch, ThreadID: feed.ThreadID, OwnerRevision: feed.OwnerRevision, State: "unobserved", Incomplete: "missing_turn_start", UnknownCustody: !feed.ObservedFromStart || s.untrackedChildren || s.children > 0, Items: make(map[string]persist.CodexExecutionItem), Children: make(map[string]persist.CodexExecutionChild), Decisions: make(map[string]persist.CodexExecutionDecision), Stimuli: make(map[string]persist.CodexExecutionStimulus)}
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
	defer e.publishCodexCustody(id, s)
	e.mu.Lock()
	c := s.codexExecution
	if current, ok := e.sessions[id]; !ok || current != s || c == nil || !s.executionEnabled || event.NativeIncarnation != c.NativeIncarnation || event.FeedEpoch != c.FeedEpoch || event.ThreadID != c.ThreadID {
		e.mu.Unlock()
		return fmt.Errorf("engine: Codex event ownership mismatch")
	}
	receiverBounds := true
	for _, receiver := range event.ReceiverThreadIDs {
		if !codexID(receiver) {
			receiverBounds = false
			break
		}
	}
	if len(event.ClientID) > 256 || (event.ClientID != "" && event.ItemKind != CodexUserMessage) || !receiverBounds || len(event.ActorThreadID) > 256 || len(event.CollabTool) > 64 || len(event.ReceiverThreadIDs) > persist.MaxExecutionActors || len(event.Kind) > 64 || len(event.ItemKind) > 64 || len(event.Phase) > 64 || len(event.Cause) > 64 || len(event.Decision) > 64 || len(event.TurnID) > 256 || len(event.ItemID) > 256 || len(event.RequestID) > 256 {
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

	previousHostInputs := maps.Clone(c.HostInputs)
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
	if eventErr == nil && event.Kind == CodexItemCompleted && event.ItemKind == CodexUserMessage && (event.ActorThreadID == "" || event.ActorThreadID == c.ThreadID) && event.ClientID != "" {
		joinCodexHostInput(c, event.ClientID, s.executionIdentity.OwnerRevision)
	}
	if eventErr != nil {
		c.Incomplete = "invalid_event"
		c.UnknownCustody = true
	}
	persistErr := e.persistExecutionLocked(id, s)
	if persistErr != nil {
		for client, input := range previousHostInputs {
			if _, exists := c.HostInputs[client]; !exists {
				c.HostInputs[client] = input
				c.UnknownCustody = true
			}
		}
	}
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
	actor := event.ActorThreadID
	if actor == "" {
		actor = c.ThreadID
	}
	if event.Kind == CodexDecisionResolved {
		key := codexActorKey(actor, event.RequestID)
		if event.ActorThreadID == "" {
			matches := 0
			for id, decision := range c.Decisions {
				if decision.RequestID == event.RequestID || (decision.RequestID == "" && id == event.RequestID) {
					key = id
					matches++
				}
			}
			if matches != 1 {
				return fmt.Errorf("engine: ambiguous Codex decision resolution")
			}
		}
		decision, known := c.Decisions[key]
		if !known || decision.NativeIncarnation != event.NativeIncarnation || decision.FeedEpoch != event.FeedEpoch || (event.TurnID != "" && event.TurnID != decision.TurnID) {
			return fmt.Errorf("engine: unknown Codex decision resolution")
		}
		delete(c.Decisions, key)
		return nil
	}
	if !codexID(event.TurnID) {
		return fmt.Errorf("engine: missing Codex turn identity")
	}
	if event.Kind == CodexItemCompleted {
		if actor != c.ThreadID {
			child, known := c.Children[actor]
			if !known || child.NativeIncarnation != event.NativeIncarnation || child.FeedEpoch != event.FeedEpoch {
				return fmt.Errorf("engine: unknown Codex item actor")
			}
		}
		return reduceCodexItem(c, event, actor)
	}
	if actor != c.ThreadID {
		if err := codexObserveActor(c, actor, event); err != nil {
			return err
		}
		if event.Kind == CodexTurnStarted || event.Kind == CodexTurnCompleted || event.Kind == CodexRetryActive {
			return nil
		}
	} else if event.Kind == CodexTurnStarted {
		if c.State == "active" {
			c.UnknownCustody = true
		}
		if c.TurnID == event.TurnID {
			return fmt.Errorf("engine: repeated Codex turn start")
		}
		for id, item := range c.Items {
			if item.Complete {
				delete(c.Items, id)
			}
		}
		c.TurnID = event.TurnID
		if len(c.HostInputs) == 0 {
			c.OwnerRevision = s.executionIdentity.OwnerRevision
		}
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

	if actor == c.ThreadID && event.TurnID != c.TurnID {
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
		key := codexActorKey(actor, event.RequestID)
		if _, known := c.Decisions[key]; !known && !codexHasCapacity(c) {
			return fmt.Errorf("engine: Codex decision capacity")
		}
		if old, known := c.Decisions[key]; known && (old.TurnID != event.TurnID || old.Kind != event.Decision || old.NativeIncarnation != event.NativeIncarnation || old.FeedEpoch != event.FeedEpoch) {
			return fmt.Errorf("engine: overlapping Codex decision identity")
		}
		c.Decisions[key] = persist.CodexExecutionDecision{ActorThreadID: actor, RequestID: event.RequestID, TurnID: event.TurnID, Kind: event.Decision, NativeIncarnation: event.NativeIncarnation, FeedEpoch: event.FeedEpoch}
		c.Denied = "human_decision"

	case CodexItemStarted:
		return reduceCodexItem(c, event, actor)
	default:
		return fmt.Errorf("engine: unknown Codex control event")
	}
	return nil
}

func codexActorKey(actor, id string) string {
	key, _ := json.Marshal([2]string{actor, id})
	return string(key)
}

func codexActorHasWork(c *persist.CodexExecution, actor string) bool {
	for _, stimulus := range c.Stimuli {
		if stimulus.ActorThreadID == actor || stimulus.ReceiverThreadID == actor {
			return true
		}
	}
	for _, item := range c.Items {
		if item.ActorThreadID == actor && !item.Complete {
			return true
		}
	}
	for _, decision := range c.Decisions {
		if decision.ActorThreadID == actor {
			return true
		}
	}
	return false
}

func codexHasCapacity(c *persist.CodexExecution) bool {
	if len(c.Items)+len(c.Children)+len(c.Decisions)+len(c.Stimuli)+len(c.HostInputs) < persist.MaxExecutionActors {
		return true
	}
	for id, child := range c.Children {
		if child.Active || child.AwaitingTurn || child.ParentThreadID == "" || codexActorHasWork(c, id) {
			continue
		}
		leaf := true
		for _, descendant := range c.Children {
			if descendant.ParentThreadID == id {
				leaf = false
				break
			}
		}
		if leaf {
			// shortcut: retired ancestry is unavailable, reconcile native ancestry before reusing it.
			delete(c.Children, id)
			return true
		}
	}
	return false
}

func codexActiveChildren(c *persist.CodexExecution) bool {
	for _, child := range c.Children {
		if child.Active || child.AwaitingTurn || child.ParentThreadID == "" {
			return true
		}
	}
	return false
}

func codexActorCycle(c *persist.CodexExecution, parent, child string) bool {
	if child == c.ThreadID {
		return true
	}
	for steps := 0; parent != ""; steps++ {
		if parent == child || steps > len(c.Children) {
			return true
		}
		if parent == c.ThreadID {
			return false
		}
		parent = c.Children[parent].ParentThreadID
	}
	return false
}

func codexObserveActor(c *persist.CodexExecution, actor string, event CodexExecutionEvent) error {
	child, known := c.Children[actor]
	if !known {
		pendingSpawn := false
		for _, item := range c.Items {
			if item.CollabTool == "spawnAgent" && !item.Complete {
				pendingSpawn = true
				break
			}
		}
		if !pendingSpawn || event.Kind != CodexTurnStarted || !codexHasCapacity(c) {
			return fmt.Errorf("engine: unattached Codex actor")
		}
		child = persist.CodexExecutionChild{NativeIncarnation: event.NativeIncarnation, FeedEpoch: event.FeedEpoch, AwaitingTurn: true, Active: true}
	}
	if child.NativeIncarnation != event.NativeIncarnation || child.FeedEpoch != event.FeedEpoch {
		return fmt.Errorf("engine: obsolete Codex actor")
	}
	if event.Kind == CodexTurnStarted {
		if child.ChildTurnID == event.TurnID || (child.Active && !child.AwaitingTurn) {
			return fmt.Errorf("engine: overlapping Codex actor turn")
		}
		child.ChildTurnID = event.TurnID
		child.Active = true
		child.AwaitingTurn = false
		c.Children[actor] = child
		return nil
	}
	if !child.Active || child.AwaitingTurn || child.ChildTurnID != event.TurnID {
		return fmt.Errorf("engine: Codex actor turn mismatch")
	}
	if event.Kind == CodexTurnCompleted {
		switch event.Cause {
		case CodexTerminalQuota, CodexTerminalCompleted, CodexTerminalCancelled, CodexTerminalFailed, CodexTerminalUnknown:
		default:
			return fmt.Errorf("engine: unknown Codex actor terminal cause")
		}
		child.Active = false
		c.Children[actor] = child
	}
	return nil
}

func codexCollabStarted(c *persist.CodexExecution, actor string, event CodexExecutionEvent) error {
	switch event.CollabTool {
	case "spawnAgent":
		if len(event.ReceiverThreadIDs) != 0 {
			return fmt.Errorf("engine: Codex spawn has premature receivers")
		}
	case "sendInput", "resumeAgent", "wait", "closeAgent":
		if len(event.ReceiverThreadIDs) == 0 || (event.CollabTool != "wait" && len(event.ReceiverThreadIDs) != 1) {
			return fmt.Errorf("engine: invalid Codex collaboration receivers")
		}
		for _, receiver := range event.ReceiverThreadIDs {
			child, known := c.Children[receiver]
			if !known || receiver == actor || receiver == c.ThreadID || child.ParentThreadID == "" {
				return fmt.Errorf("engine: unknown Codex receiver")
			}
			if event.CollabTool == "sendInput" {
				key := codexActorKey(actor, event.ItemID)
				if _, known := c.Stimuli[key]; known || !codexHasCapacity(c) {
					return fmt.Errorf("engine: Codex stimulus capacity or overlapping identity")
				}
				if c.Stimuli == nil {
					c.Stimuli = make(map[string]persist.CodexExecutionStimulus)
				}
				c.Stimuli[key] = persist.CodexExecutionStimulus{NativeIncarnation: event.NativeIncarnation, FeedEpoch: event.FeedEpoch, ActorThreadID: actor, TurnID: event.TurnID, ItemID: event.ItemID, ReceiverThreadID: receiver, PreviousTurnID: child.ChildTurnID, StartSequence: event.Sequence, Phase: "started"}
			}
		}
	default:
		return fmt.Errorf("engine: unknown Codex collaboration tool")
	}
	return nil
}

func codexCollabCompleted(c *persist.CodexExecution, actor string, event CodexExecutionEvent) error {
	if event.CollabTool == "sendInput" {
		key := codexActorKey(actor, event.ItemID)
		stimulus, known := c.Stimuli[key]
		if !known || stimulus.NativeIncarnation != event.NativeIncarnation || stimulus.FeedEpoch != event.FeedEpoch || stimulus.TurnID != event.TurnID || stimulus.Phase != "started" || len(event.ReceiverThreadIDs) != 1 || event.ReceiverThreadIDs[0] != stimulus.ReceiverThreadID {
			return fmt.Errorf("engine: unmatched Codex stimulus receipt")
		}
		stimulus.Phase = "failed"
		if event.ToolSucceeded {
			stimulus.Phase = "accepted"
		}
		stimulus.CompletionSequence = event.Sequence
		c.Stimuli[key] = stimulus
	}
	if event.CollabTool != "spawnAgent" {
		for _, receiver := range event.ReceiverThreadIDs {
			if child, known := c.Children[receiver]; !known || child.ParentThreadID == "" {
				return fmt.Errorf("engine: unknown Codex collaboration result actor")
			}
		}
		return nil
	}
	if len(event.ReceiverThreadIDs) > 1 || (event.ToolSucceeded && len(event.ReceiverThreadIDs) != 1) {
		return fmt.Errorf("engine: invalid Codex spawn receiver")
	}
	for _, receiver := range event.ReceiverThreadIDs {
		if !codexID(receiver) || receiver == c.ThreadID || receiver == actor {
			return fmt.Errorf("engine: invalid Codex spawn receiver")
		}
		if codexActorCycle(c, actor, receiver) {
			return fmt.Errorf("engine: cyclic Codex actors")
		}
		child, known := c.Children[receiver]
		if known && (child.NativeIncarnation != event.NativeIncarnation || child.FeedEpoch != event.FeedEpoch) {
			return fmt.Errorf("engine: obsolete Codex spawn actor")
		}
		if known && child.ParentThreadID != "" {
			return fmt.Errorf("engine: reused Codex child identity")
		}
		if !known {
			if !codexHasCapacity(c) {
				return fmt.Errorf("engine: Codex child capacity")
			}
			child = persist.CodexExecutionChild{NativeIncarnation: event.NativeIncarnation, FeedEpoch: event.FeedEpoch, Active: true, AwaitingTurn: true}
		}
		child.ParentThreadID = actor
		child.TurnID = event.TurnID
		c.Children[receiver] = child
	}
	return nil
}

func reduceCodexItem(c *persist.CodexExecution, event CodexExecutionEvent, actor string) error {
	parent := actor == c.ThreadID
	late := parent && (c.State == "terminal" || event.TurnID != c.TurnID)
	if !parent {
		child := c.Children[actor]
		late = !child.Active || child.AwaitingTurn || child.ChildTurnID != event.TurnID
	}
	if !codexID(event.ItemID) || (parent && c.State != "active" && c.State != "terminal") || (late && event.Kind != CodexItemCompleted) {
		return fmt.Errorf("engine: Codex item without active turn")
	}
	kind := event.ItemKind
	switch kind {
	case CodexUserMessage, CodexAgentMessage, CodexCommandExecution, CodexFileChange, CodexMcpToolCall, CodexCollabAgentToolCall, CodexContextCompaction, CodexReasoning, CodexPlan:
	default:
		kind = "unknown"
		c.Denied = "unknown_item"
		c.UnknownCustody = true
	}
	key := codexActorKey(actor, event.ItemID)
	phase := event.Phase
	if kind == CodexAgentMessage {
		switch phase {
		case CodexCommentary:
		case CodexFinalAnswer:
			if parent && !late {
				c.Denied = "final_answer"
			}
		default:
			phase = "unknown"
			if parent && !late {
				c.Denied = "unknown_phase"
			} else if !parent {
				c.UnknownCustody = true
			}
		}
	} else {
		phase = ""
	}
	if kind == CodexContextCompaction && parent && !late {
		c.Denied = "maintenance"
	}
	if parent && !late && kind != CodexReasoning && kind != CodexPlan {
		c.LastItemSequence = event.Sequence
	}
	if event.Kind == CodexItemStarted {
		if _, known := c.Items[key]; known {
			return fmt.Errorf("engine: reused Codex item identity")
		}
		if !codexHasCapacity(c) {
			return fmt.Errorf("engine: Codex item capacity")
		}
		c.Items[key] = persist.CodexExecutionItem{ClientID: event.ClientID, ActorThreadID: actor, CollabTool: event.CollabTool, TurnID: event.TurnID, Kind: kind, Phase: phase, StartSequence: event.Sequence, NativeIncarnation: event.NativeIncarnation, FeedEpoch: event.FeedEpoch}
		if kind == CodexCollabAgentToolCall {
			return codexCollabStarted(c, actor, event)
		}
		return nil
	}
	item, known := c.Items[key]
	if !known || item.ClientID != event.ClientID || item.Complete || item.TurnID != event.TurnID || item.Kind != kind || item.Phase != phase || item.CollabTool != event.CollabTool || item.NativeIncarnation != event.NativeIncarnation || item.FeedEpoch != event.FeedEpoch {
		return fmt.Errorf("engine: unmatched Codex item completion")
	}
	if parent && kind == CodexUserMessage && event.ClientID != "" {
		if err := codexHostInputEcho(c, event); err != nil {
			return err
		}
	}
	// The pinned native live feed does not replay completed item lifecycles.
	delete(c.Items, key)

	if kind == CodexCollabAgentToolCall {
		if err := codexCollabCompleted(c, actor, event); err != nil {
			return err
		}
	}
	if late {
		if (kind == CodexCommandExecution || kind == CodexFileChange || kind == CodexMcpToolCall || kind == CodexCollabAgentToolCall) && !event.ToolSucceeded {
			c.ToolFailureSequence = event.Sequence
			if c.Denied == "" || c.Denied == "tool_unsuccessful" {
				c.Denied = "tool_unsuccessful"
			}
		}
		return nil
	}
	if !parent {
		return nil
	}
	switch kind {
	case CodexUserMessage:
		c.UserSequence = event.Sequence
	case CodexAgentMessage:
		if phase == CodexCommentary && item.StartSequence > c.ToolFailureSequence {
			c.CommentarySequence = event.Sequence
		}
	case CodexCommandExecution, CodexFileChange, CodexMcpToolCall, CodexCollabAgentToolCall:
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
	return e.observeExecutionOwnerInput(id, &expected)
}

// ObserveExecutionOwnerInput serializes owner writes across all attached and API
// writers. Persistence failure refuses the effect but retains its observed fence.
func (e *Engine) ObserveExecutionOwnerInput(id string) error {
	return e.observeExecutionOwnerInput(id, nil)
}

func (e *Engine) observeExecutionOwnerInput(id string, expected *uint64) error {
	e.mu.Lock()
	s, ok := e.sessions[id]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("engine: unknown session")
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	e.mu.Lock()
	if current, ok := e.sessions[id]; !ok || current != s || !s.executionEnabled || (expected != nil && s.executionIdentity.OwnerRevision != *expected) || s.executionIdentity.OwnerRevision == ^uint64(0) {
		e.mu.Unlock()
		return fmt.Errorf("engine: stale owner revision")
	}
	s.executionIdentity.OwnerRevision++
	if s.codexExecution != nil {
		s.codexExecution.Denied = "owner_input"
		s.codexExecution.UnknownCustody = true
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
	if c == nil || c.UnknownCustody || c.Incomplete != "" || c.Denied != "" || c.State != "terminal" || c.Cause != CodexTerminalQuota || c.OwnerRevision != s.executionIdentity.OwnerRevision || c.UserSequence == 0 || c.CommentarySequence <= c.UserSequence || c.ToolResultSequence <= c.CommentarySequence || c.LastItemSequence != c.ToolResultSequence || len(c.Decisions) > 0 || len(c.Stimuli) > 0 || len(c.HostInputs) > 0 || codexActiveChildren(c) {
		return CodexQuotaObligation{}, false
	}
	for _, item := range c.Items {
		if !item.Complete {
			return CodexQuotaObligation{}, false
		}
	}
	return CodexQuotaObligation{NativeIncarnation: c.NativeIncarnation, FeedEpoch: c.FeedEpoch, ThreadID: c.ThreadID, TurnID: c.TurnID, OwnerRevision: c.OwnerRevision, TerminalSequence: c.TerminalSequence}, true
}
