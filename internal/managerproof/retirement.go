package managerproof

// Persistent mutations are serialized by the source's custody worker. Readers
// never wait for disk while holding the registry lock; a concurrent retirement
// invalidates an in-progress plan instead of publishing stale positive proof.
func NewPersistentRegistry(binding Binding, replay *Replay, baseline ...uint64) (*Registry, error) {
	if replay == nil {
		return nil, ErrUnavailable
	}
	if err := replay.bind(binding); err != nil {
		return nil, err
	}
	r, err := NewRegistry(binding, baseline...)
	if err != nil {
		return nil, err
	}
	r.replay = replay
	return r, nil
}
func (s *Replay) bind(binding Binding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.claimed || s.poisoned {
		return ErrQuarantined
	}
	if binding.Validate() != nil || binding.Session != s.header.Session || binding.ConversationID != s.header.Conversation {
		return ErrInvalid
	}
	scope := replayScope(binding)
	if s.bound && s.boundScope != scope {
		return ErrQuarantined
	}
	s.boundScope, s.bound = scope, true
	return nil
}
func (r *Registry) persist(fn func(*Replay) error) error {
	if r.replay == nil {
		return nil
	}
	generation := r.version
	r.mu.Unlock()
	err := fn(r.replay)
	r.mu.Lock()
	if err != nil {
		r.retire()
		return err
	}
	if r.retired || r.version != generation {
		r.retire()
		return ErrQuarantined
	}
	return nil
}
func nonceGuard(nonce string, input inputRecord) ReplayKey {
	return ReplayKey{Kind: ReplayNonce, ID: nonce, Owner: ReplayFingerprint(input.OperationID, input.TextHash)}
}
func operationGuard(nonce string, input inputRecord) ReplayKey {
	return ReplayKey{Kind: ReplayOperation, ID: input.OperationID, Owner: ReplayFingerprint(nonce, input.TextHash)}
}
func turnGuard(id, agent string) ReplayKey {
	return ReplayKey{Kind: ReplayTurn, ID: id, Owner: ReplayFingerprint(agent)}
}
func callGuard(id string, call callRecord) ReplayKey {
	return ReplayKey{Kind: ReplayCall, ID: id, Owner: ReplayFingerprint(call.TurnID, call.AgentID, call.ToolName), Detail: call.ArgumentsHash}
}
func (r *Registry) releaseTerminal(turnID string) error {
	if r.replay == nil {
		return nil
	}
	turn, ok := r.turns[turnID]
	if !ok || !turn.Closed || turn.Reason == "" {
		return nil
	}
	for _, call := range r.calls {
		if call.TurnID == turnID && !call.Closed {
			return nil
		}
	}
	var keys []ReplayKey
	keys = append(keys, turnGuard(turnID, turn.AgentID))
	if turn.InputID != "" {
		input, exists := r.inputs[turn.InputID]
		if !exists {
			return ErrUnbound
		}
		receipt := TerminalReceipt{Binding: r.binding, Nonce: turn.InputID, OperationID: input.OperationID, TextHash: input.TextHash, NativeTurnID: turnID, NativeMessageID: input.MessageID, Reason: turn.Reason, ExternallySteered: turn.Unsafe, WorkerGeneration: r.terminalWorker, ProviderVersion: r.terminalVersion}
		keys = append(keys, nonceGuard(turn.InputID, input), operationGuard(turn.InputID, input))
		if input.MessageID != "" {
			keys = append(keys, ReplayKey{Kind: ReplayMessage, ID: input.MessageID, Owner: ReplayFingerprint(turnID, turn.InputID)})
		}
		if err := r.persist(func(s *Replay) error { return s.RecordTerminal(receipt, keys...) }); err != nil {
			return err
		}
		delete(r.inputs, turn.InputID)
	} else if err := r.persist(func(s *Replay) error { return s.CloseKeys(r.binding, keys...) }); err != nil {
		return err
	}
	if r.current == turnID {
		r.terminalCurrent = Turn{NativeInputID: turn.InputID, NativeTurnID: turnID, State: "completed", Reason: turn.Reason, ExternallySteered: turn.Unsafe}
	}
	delete(r.turns, turnID)
	r.notify()
	return nil
}

// SetProofPending installs a nonblocking atomic source-queue probe before exposure.
// The custody worker must call NotifyCustodyProgress after draining a batch.
func (r *Registry) SetProofPending(probe func() bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.proofPending = probe
}
func (r *Registry) NotifyCustodyProgress() {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(r.changed)
	r.changed = make(chan struct{})
}
func (r *Registry) pending() bool { return r.proofPending != nil && r.proofPending() }

// CustodyError ignores queue readiness; writers use it after void native events.
func (r *Registry) CustodyError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		if r.replay != nil {
			return ErrQuarantined
		}
		return ErrStale
	}
	return nil
}

// InputDetailRetained is meaningful for a nonce already successfully prepared by
// this registry. False then means durable terminal history owns its detail.
func (r *Registry) InputDetailRetained(nonce string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.inputs[nonce]
	return ok
}

func (r *Registry) BindingSnapshot() (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return Status{}, ErrQuarantined
	}
	return Status{Binding: r.binding}, nil
}
func (r *Registry) SetTerminalIdentity(worker, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.terminalWorker, r.terminalVersion = worker, version
}

func (r *Registry) RejectInput(nonce string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired || r.replay == nil {
		return ErrQuarantined
	}
	input, ok := r.inputs[nonce]
	if !ok {
		return ErrUnbound
	}
	if input.TurnID != "" || input.ReceiptTurnID != "" {
		return ErrConflict
	}
	receipt := TerminalReceipt{State: "rejected", Binding: r.binding, Nonce: nonce, OperationID: input.OperationID, TextHash: input.TextHash, Reason: "native_queue_rejected", WorkerGeneration: r.terminalWorker, ProviderVersion: r.terminalVersion}
	if err := r.persist(func(s *Replay) error {
		return s.RecordTerminal(receipt, nonceGuard(nonce, input), operationGuard(nonce, input))
	}); err != nil {
		return err
	}
	delete(r.inputs, nonce)
	r.notify()
	return nil
}

func (r *Registry) retiredError() error {
	if r.replay != nil {
		return ErrQuarantined
	}
	return ErrStale
}

// ImmutableBinding identifies historical receipts; it conveys no live authority.
func (r *Registry) ImmutableBinding() Binding { r.mu.Lock(); defer r.mu.Unlock(); return r.binding }
