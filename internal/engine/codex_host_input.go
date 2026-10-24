package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nathandela/swarm/internal/persist"
)

// BeginCodexHostInput persists the semantic main-thread input fence before RPC
// bytes. Raw PTY writes use ObserveExecutionOwnerInput and retain unknown custody.
func (e *Engine) BeginCodexHostInput(id string, claim persist.CodexHostInputClaim) (persist.CodexHostInputClaim, error) {
	if !codexID(claim.ClientID) || !codexID(claim.NativeIncarnation) || !codexID(claim.FeedEpoch) || !codexID(claim.ThreadID) || len(claim.ExpectedTurnID) > 256 || claim.OwnerRevision != 0 {
		return claim, fmt.Errorf("engine: invalid Codex host input claim")
	}
	e.mu.Lock()
	s, ok := e.sessions[id]
	e.mu.Unlock()
	if !ok {
		return claim, fmt.Errorf("engine: unknown session")
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	defer e.publishCodexCustody(id, s)
	e.mu.Lock()
	defer e.mu.Unlock()
	c := s.codexExecution
	if current, ok := e.sessions[id]; !ok || current != s || !s.executionEnabled || s.token != "" || c == nil || c.NativeIncarnation != claim.NativeIncarnation || c.FeedEpoch != claim.FeedEpoch || c.ThreadID != claim.ThreadID || s.executionIdentity.OwnerRevision == ^uint64(0) {
		return claim, fmt.Errorf("engine: Codex host input ownership mismatch")
	}
	if _, exists := c.HostInputs[claim.ClientID]; exists {
		return claim, fmt.Errorf("engine: unresolved Codex host input")
	}
	if len(c.Items)+len(c.Children)+len(c.Decisions)+len(c.Stimuli)+len(c.HostInputs) >= persist.MaxExecutionActors {
		return claim, fmt.Errorf("engine: Codex host input capacity")
	}
	claim.OwnerRevision = s.executionIdentity.OwnerRevision + 1
	next := cloneCodexExecution(c)
	if next.HostInputs == nil {
		next.HostInputs = map[string]persist.CodexHostInput{}
	}
	next.HostInputs[claim.ClientID] = persist.CodexHostInput{Claim: claim}
	// Reserve every unresolved receipt's largest bounded native IDs before effects.
	reservation := executionSnapshot(id, s)
	reservation.OwnerRevision = claim.OwnerRevision
	reservation.Codex = cloneCodexExecution(next)
	for client, input := range reservation.Codex.HostInputs {
		turn := input.Claim.ExpectedTurnID
		if turn == "" {
			turn = strings.Repeat("<", 256)
		}
		input.AcceptedTurnID = turn
		input.EchoTurnID = turn
		input.EchoItemID = strings.Repeat("<", 256)
		input.EchoSequence = ^uint64(0)
		reservation.Codex.HostInputs[client] = input
	}
	encoded, err := json.Marshal(reservation)
	if err != nil || len(encoded) > persist.MaxExecutionBytes {
		return claim, fmt.Errorf("engine: Codex host input byte capacity")
	}
	s.executionIdentity.OwnerRevision = claim.OwnerRevision
	s.codexExecution = next
	if next.Denied == "" {
		next.Denied = "owner_input"
	}
	err = e.persistExecutionLocked(id, s)
	if err != nil {
		next.UnknownCustody = true
	}
	return claim, executionCustodyError(err)
}

// RecordCodexHostInputResult accepts only a typed reply from the claimed physical
// connection. current is checked outside e.mu, before and after persistence.
func (e *Engine) RecordCodexHostInputResult(id string, claim persist.CodexHostInputClaim, turn string, current func() bool) error {
	return e.settleCodexHostInput(id, claim, turn, false, current)
}

// RefuseCodexHostInput is only for the exact typed no-active-turn steer refusal.
func (e *Engine) RefuseCodexHostInput(id string, claim persist.CodexHostInputClaim, current func() bool) error {
	return e.settleCodexHostInput(id, claim, "", true, current)
}

func (e *Engine) settleCodexHostInput(id string, claim persist.CodexHostInputClaim, turn string, refused bool, current func() bool) error {
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
	defer e.mu.Unlock()
	c := s.codexExecution
	if current, ok := e.sessions[id]; !ok || current != s || !s.executionEnabled || c == nil || c.NativeIncarnation != claim.NativeIncarnation || c.FeedEpoch != claim.FeedEpoch || c.ThreadID != claim.ThreadID {
		return fmt.Errorf("engine: stale Codex host input result")
	}
	input, exists := c.HostInputs[claim.ClientID]
	if !exists || input.Claim != claim {
		return fmt.Errorf("engine: unknown Codex host input claim")
	}
	checkCurrent := func() bool {
		if current == nil {
			return true
		}
		// Keep custody fenced while physical checks run without either global lock.
		s.executionPending = true
		e.mu.Unlock()
		valid := current()
		e.mu.Lock()
		s.executionPending = false
		return valid
	}
	valid := checkCurrent()
	if e.sessions[id] != s || s.codexExecution != c {
		return fmt.Errorf("engine: replaced Codex host input session")
	}
	if !valid {
		c.UnknownCustody = true
		if err := e.persistExecutionLocked(id, s); err != nil {
			return executionCustodyError(err)
		}
		return fmt.Errorf("engine: host input receipt connection changed")
	}
	if refused {
		if claim.ExpectedTurnID == "" || input.AcceptedTurnID != "" || input.EchoTurnID != "" {
			return fmt.Errorf("engine: contradictory Codex host input refusal")
		}
		delete(c.HostInputs, claim.ClientID)
	} else {
		if !codexID(turn) || (claim.ExpectedTurnID != "" && turn != claim.ExpectedTurnID) || (input.AcceptedTurnID != "" && input.AcceptedTurnID != turn) || (input.EchoTurnID != "" && input.EchoTurnID != turn) {
			return fmt.Errorf("engine: conflicting Codex host input result")
		}
		input.AcceptedTurnID = turn
		c.HostInputs[claim.ClientID] = input
		joinCodexHostInput(c, claim.ClientID, s.executionIdentity.OwnerRevision)
	}
	err := e.persistExecutionLocked(id, s)
	valid = err == nil && checkCurrent()
	if e.sessions[id] != s || s.codexExecution != c {
		c.HostInputs[claim.ClientID] = input
		c.UnknownCustody = true
		return fmt.Errorf("engine: replaced Codex host input session")
	}
	if err == nil && !valid {
		c.HostInputs[claim.ClientID] = input
		c.UnknownCustody = true
		if err = e.persistExecutionLocked(id, s); err == nil {
			err = fmt.Errorf("engine: host input receipt connection changed")
		}
	}
	if err != nil {
		c.HostInputs[claim.ClientID] = input
		c.UnknownCustody = true
	}
	return executionCustodyError(err)
}

func codexHostInputEcho(c *persist.CodexExecution, event CodexExecutionEvent) error {
	input, known := c.HostInputs[event.ClientID]
	if !known {
		return nil
	}
	claim := input.Claim
	if claim.NativeIncarnation != event.NativeIncarnation || claim.FeedEpoch != event.FeedEpoch || claim.ThreadID != event.ThreadID || (claim.ExpectedTurnID != "" && claim.ExpectedTurnID != event.TurnID) || (input.AcceptedTurnID != "" && input.AcceptedTurnID != event.TurnID) || (input.EchoSequence != 0 && (input.EchoTurnID != event.TurnID || input.EchoItemID != event.ItemID || input.EchoSequence != event.Sequence)) {
		return fmt.Errorf("engine: conflicting Codex host input echo")
	}
	input.EchoTurnID = event.TurnID
	input.EchoItemID = event.ItemID
	input.EchoSequence = event.Sequence
	c.HostInputs[event.ClientID] = input
	return nil
}

func joinCodexHostInput(c *persist.CodexExecution, client string, currentOwner uint64) {
	input := c.HostInputs[client]
	if input.AcceptedTurnID == "" || input.EchoTurnID != input.AcceptedTurnID {
		return
	}
	delete(c.HostInputs, client)
	// Historical delivery cannot transfer another input's quota evidence.
	if input.Claim.OwnerRevision == currentOwner && c.TurnID == input.AcceptedTurnID && input.EchoSequence != 0 && input.EchoSequence == c.UserSequence && (c.TerminalSequence == 0 || input.EchoSequence < c.TerminalSequence) {
		c.OwnerRevision = input.Claim.OwnerRevision
		if c.Denied == "owner_input" {
			c.Denied = ""
		}
	}
}
