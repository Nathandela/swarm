package engine

import "github.com/Nathandela/swarm/internal/status"

// codexCustodyTurn reports retained native work independently of root model status.
func codexCustodyTurn(s *session) status.Turn {
	c := s.codexExecution
	if c == nil || s.executionIncomplete == "persistence_failure" {
		return status.TurnUnknown
	}
	if c.UnknownCustody || c.Incomplete != "" || c.State == "unobserved" || len(c.Stimuli) > 0 || len(c.HostInputs) > 0 {
		return status.TurnUnknown
	}
	active := c.State == "active" || len(c.Items) > 0
	for _, child := range c.Children {
		if child.ParentThreadID == "" {
			return status.TurnUnknown
		}
		active = active || child.Active || child.AwaitingTurn
	}
	if active {
		return status.TurnActive
	}
	return status.TurnIdle
}

func projectCodexCustody(s *session, next status.Status) status.Status {
	c := s.codexExecution
	if next.Process != status.ProcessRunning {
		return next
	}
	if c == nil {
		if s.executionIncomplete == "persistence_failure" {
			next.Turn = status.TurnUnknown
			next.Attention = next.Interaction == status.InteractionPermission || next.Interaction == status.InteractionPrompt
		}
		return next
	}
	for _, decision := range c.Decisions {
		if decision.Kind == CodexApproval {
			next.Interaction = status.InteractionPermission
			break
		}
		if decision.Kind == CodexQuestion {
			next.Interaction = status.InteractionPrompt
		}
	}
	next.Attention = len(c.Decisions) > 0 || next.Interaction == status.InteractionPermission || next.Interaction == status.InteractionPrompt
	if turn := codexCustodyTurn(s); turn != status.TurnIdle {
		next.Turn = turn
	}
	return next
}

// ExecutionCustodyUnsafe inspects native work; a missing qualified record never
// proves a destructive boundary. Callers select sessions requiring native proof.
func (e *Engine) ExecutionCustodyUnsafe(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[id]
	if !ok || !s.executionEnabled {
		return true
	}
	return s.executionPending || len(s.pendingCallbacks) > 0 || s.codexExecution == nil || len(s.codexExecution.Decisions) > 0 || codexCustodyTurn(s) != status.TurnIdle
}

// publishCodexCustody is called with the session emit lock held, including on
// rejected evidence: an observed loss must become visible without another frame.
func (e *Engine) publishCodexCustody(id string, s *session) {
	e.mu.Lock()
	if current, ok := e.sessions[id]; !ok || current != s || !s.alive || s.codexExecution == nil {
		e.mu.Unlock()
		return
	}
	next := s.status
	next.Turn = s.mainTurn
	next.Interaction = s.executionInteraction
	next = projectCodexCustody(s, next)
	changed := commit(s, next)
	e.mu.Unlock()
	if changed {
		e.emit(id, next)
	}
}

// PublishCodexExecutionStatus publishes the registered running session even if
// restoration already changed its internal status. Assembly calls this once the
// status observers are wired, before starting native ingress and fallback polls.
func (e *Engine) PublishCodexExecutionStatus(id string) {
	e.mu.Lock()
	s, ok := e.sessions[id]
	e.mu.Unlock()
	if !ok {
		return
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	e.mu.Lock()
	if current, ok := e.sessions[id]; !ok || current != s || !s.alive || !s.executionEnabled || s.status.Process != status.ProcessRunning {
		e.mu.Unlock()
		return
	}
	next := projectCodexCustody(s, s.status)
	commit(s, next)
	e.mu.Unlock()
	e.emit(id, next)
}
