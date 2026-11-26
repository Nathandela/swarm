package managerproof

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const MaxRecords = 1024

var (
	ErrDirectInputUnresolved = errors.New("manager proof: unresolved direct terminal input")
	ErrConflict              = errors.New("manager proof: conflicting native evidence")
	ErrStale                 = errors.New("manager proof: stale native generation or turn")
	ErrUnbound               = errors.New("manager proof: native call has no main input admission")
	ErrUnavailable           = errors.New("manager proof: native evidence unavailable")
	ErrCapacity              = errors.New("manager proof: native evidence capacity reached")
	ErrQuarantined           = fmt.Errorf("%w: custody incomplete; fresh manager session required", ErrStale)
)

type Binding = schema.ManagerRuntimeBinding
type Admission = schema.ManagerAdmission
type Turn = schema.ManagerNativeTurn
type Status = schema.ManagerRuntimeStatus
type CallRequest = schema.ManagerCallReq
type Proof = schema.ManagerCallProof

type inputRecord struct {
	ExpectedExternalInput                    uint64
	HasExternalBaseline                      bool
	ReceiptTurnID                            string
	OperationID, TextHash, TurnID, MessageID string
}
type turnRecord struct {
	InputID, AgentID string
	Closed           bool
	Reason           string
	Unsafe           bool
}
type callRecord struct {
	TurnID, AgentID, ToolName, ArgumentsHash string
	Closed                                   bool
}

// Registry contains only bounded process-generation evidence, never model text.
type Registry struct {
	mu                              sync.Mutex
	replay                          *Replay
	proofPending                    func() bool
	version                         uint64
	terminalCurrent                 Turn
	terminalWorker, terminalVersion string
	binding                         Binding
	retired                         bool
	changed                         chan struct{}
	inputs                          map[string]inputRecord
	turns                           map[string]turnRecord
	calls                           map[string]callRecord
	current                         string
	threadState                     string
	externalInput                   uint64
}

func validID(s string) bool {
	return s != "" && len(s) <= 256 && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}
func ValidHash(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func NewRegistry(binding Binding, baseline ...uint64) (*Registry, error) {
	if binding.Validate() != nil || len(baseline) > 1 {
		return nil, ErrInvalid
	}
	r := &Registry{binding: binding, changed: make(chan struct{}), inputs: map[string]inputRecord{}, turns: map[string]turnRecord{}, calls: map[string]callRecord{}}
	if len(baseline) > 0 {
		r.externalInput = baseline[0]
	}
	return r, nil
}
func (r *Registry) notify() { r.version++; close(r.changed); r.changed = make(chan struct{}) }
func (r *Registry) retire() {
	if !r.retired {
		r.retired = true
		r.notify()
	}
}
func (r *Registry) Retire()        { r.mu.Lock(); defer r.mu.Unlock(); r.retire() }
func (r *Registry) capacity() bool { return len(r.inputs)+len(r.turns)+len(r.calls) < MaxRecords }
func (r *Registry) Prepare(nonce, operationID, textHash string, expectedExternal ...uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrStale
	}
	if !ValidHash(nonce) || !validID(operationID) || !ValidHash(textHash) {
		return ErrInvalid
	}
	if old, ok := r.inputs[nonce]; ok {
		if old.OperationID != operationID || old.TextHash != textHash || len(expectedExternal) > 1 || old.HasExternalBaseline != (len(expectedExternal) == 1) || old.HasExternalBaseline && old.ExpectedExternalInput != expectedExternal[0] {
			return ErrConflict
		}
		return nil
	}
	if len(expectedExternal) > 1 {
		return ErrInvalid
	}
	if len(expectedExternal) > 0 && expectedExternal[0] != r.externalInput {
		return ErrStale
	}
	if !r.capacity() {
		return ErrCapacity
	}
	input := inputRecord{OperationID: operationID, TextHash: textHash}
	if len(expectedExternal) > 0 {
		input.ExpectedExternalInput = expectedExternal[0]
		input.HasExternalBaseline = true
	}
	if err := r.persist(func(s *Replay) error {
		return s.Reserve(r.binding, nonceGuard(nonce, input), operationGuard(nonce, input))
	}); err != nil {
		return err
	}
	r.inputs[nonce] = input
	r.notify()
	return nil
}
func (r *Registry) StartTurn(turnID, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrStale
	}
	if !validID(turnID) || agentID != "" && !validID(agentID) {
		return ErrInvalid
	}
	if old, ok := r.turns[turnID]; ok {
		if old.AgentID != agentID || old.Closed {
			r.retire()
			return ErrConflict
		}
		return nil
	}
	if !r.capacity() {
		return ErrCapacity
	}
	if err := r.persist(func(s *Replay) error { return s.Reserve(r.binding, turnGuard(turnID, agentID)) }); err != nil {
		return err
	}
	if agentID == "" {
		if old, ok := r.turns[r.current]; ok {
			old.Closed = true
			r.turns[r.current] = old
		}
		r.current = turnID
		r.terminalCurrent = Turn{}
		r.threadState = "active"
	}
	r.turns[turnID] = turnRecord{AgentID: agentID}
	r.notify()
	return nil
}

// RecordTurnReceipt records a native RPC result without advancing native lifecycle order.
func (r *Registry) RecordTurnReceipt(nonce, turnID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrStale
	}
	if !validID(turnID) {
		return ErrInvalid
	}
	input, ok := r.inputs[nonce]
	if !ok {
		return ErrUnbound
	}
	if input.ReceiptTurnID != "" && input.ReceiptTurnID != turnID || input.TurnID != "" && input.TurnID != turnID {
		r.retire()
		return ErrConflict
	}
	input.ReceiptTurnID = turnID
	r.inputs[nonce] = input
	r.notify()
	return nil
}
func (r *Registry) BindInput(nonce, turnID, messageID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrStale
	}
	input, ok := r.inputs[nonce]
	turn, exists := r.turns[turnID]
	if !ok || !exists || turn.AgentID != "" || turn.Closed || r.current != turnID {
		return ErrUnbound
	}
	if input.ReceiptTurnID != "" && input.ReceiptTurnID != turnID {
		r.retire()
		return ErrConflict
	}
	if input.TurnID != "" && input.TurnID != turnID || turn.InputID != "" && turn.InputID != nonce {
		r.retire()
		return ErrConflict
	}
	if input.MessageID != "" && input.MessageID != messageID {
		r.retire()
		return ErrConflict
	}
	if input.HasExternalBaseline && input.ExpectedExternalInput != r.externalInput {
		turn.Unsafe = true
	}
	if messageID != "" {
		if err := r.persist(func(s *Replay) error {
			return s.Reserve(r.binding, ReplayKey{Kind: ReplayMessage, ID: messageID, Owner: ReplayFingerprint(turnID, nonce)})
		}); err != nil {
			return err
		}
	}
	input.TurnID = turnID
	input.MessageID = messageID
	turn.InputID = nonce
	r.inputs[nonce] = input
	r.turns[turnID] = turn
	r.notify()
	return nil
}
func (r *Registry) CheckExternalInput(expected uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrStale
	}
	if expected != r.externalInput {
		return ErrStale
	}
	return nil
}
func (r *Registry) ExternalActivity() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.externalInput++
	r.threadState = "active"
	if turn, ok := r.turns[r.current]; ok && !turn.Closed {
		turn.Unsafe = true
		r.turns[r.current] = turn
	}
	r.notify()
}
func (r *Registry) ObserveThreadState(state string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrStale
	}
	if state != "idle" && state != "active" && state != "systemError" {
		return ErrInvalid
	}
	r.threadState = state
	r.notify()
	return nil
}
func (r *Registry) ReadyForInput() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	turn := r.turns[r.current]
	return !r.retired && !r.pending() && r.threadState == "idle" && (r.current == "" || turn.Closed || r.terminalCurrent.NativeTurnID == r.current)
}
func (r *Registry) CloseTurn(turnID string, reason ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return
	}
	if !validID(turnID) {
		r.retire()
		return
	}
	turn, known := r.turns[turnID]
	if !known && !r.capacity() {
		r.retire()
		return
	}
	if err := r.persist(func(s *Replay) error { return s.CloseKeys(r.binding, turnGuard(turnID, turn.AgentID)) }); err != nil {
		return
	}
	turn.Closed = true
	if len(reason) > 0 {
		turn.Reason = reason[0]
	}
	r.turns[turnID] = turn
	r.notify()
	_ = r.releaseTerminal(turnID)
}

func (r *Registry) ToolStep(callID, turnID, agentID, toolName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrStale
	}
	if !validID(callID) || !validID(turnID) || !validID(toolName) || agentID != "" && !validID(agentID) {
		return ErrInvalid
	}
	if turn, known := r.turns[turnID]; known && turn.Closed {
		return ErrStale
	}
	if old, ok := r.calls[callID]; ok {
		if old.TurnID != turnID || old.AgentID != agentID || old.ToolName != toolName {
			r.retire()
			return ErrConflict
		}
		return nil
	}
	if !r.capacity() {
		return ErrCapacity
	}
	call := callRecord{TurnID: turnID, AgentID: agentID, ToolName: toolName}
	if err := r.persist(func(s *Replay) error {
		return s.Reserve(r.binding, turnGuard(turnID, agentID), callGuard(callID, call))
	}); err != nil {
		return err
	}
	r.calls[callID] = call
	r.notify()
	return nil
}
func (r *Registry) AttestTool(callID, turnID, agentID, toolName, argumentsHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrStale
	}
	if !ValidHash(argumentsHash) {
		return ErrInvalid
	}
	call, ok := r.calls[callID]
	if !ok {
		return ErrUnavailable
	}
	if call.TurnID != turnID || call.AgentID != agentID || call.ToolName != toolName || call.ArgumentsHash != "" && call.ArgumentsHash != argumentsHash {
		r.retire()
		return ErrConflict
	}
	call.ArgumentsHash = argumentsHash
	r.calls[callID] = call
	r.notify()
	return nil
}
func (r *Registry) CompleteTool(callID, turnID, agentID, toolName, argumentsHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrStale
	}
	if !validID(callID) || !validID(turnID) || !validID(toolName) || !ValidHash(argumentsHash) {
		return ErrInvalid
	}
	call, ok := r.calls[callID]
	if ok && (call.TurnID != turnID || call.AgentID != agentID || call.ToolName != toolName || call.ArgumentsHash != "" && call.ArgumentsHash != argumentsHash) {
		r.retire()
		return ErrConflict
	}
	if !ok {
		if !r.capacity() {
			return ErrCapacity
		}
		call = callRecord{TurnID: turnID, AgentID: agentID, ToolName: toolName}
	}
	call.ArgumentsHash = argumentsHash
	if err := r.persist(func(s *Replay) error { return s.CloseKeys(r.binding, callGuard(callID, call)) }); err != nil {
		return err
	}
	call.Closed = true
	if r.replay != nil {
		delete(r.calls, callID)
	} else {
		r.calls[callID] = call
	}
	r.notify()
	return r.releaseTerminal(turnID)
}
func (r *Registry) Snapshot() (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

// WaitSnapshot lets already queued native events establish custody before an RPC
// reads the live turn. Native observers continue to use the nonblocking Snapshot.
func (r *Registry) WaitSnapshot(ctx context.Context) (Status, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Status{}, err
		}
		r.mu.Lock()
		status, err := r.snapshotLocked()
		changed := r.changed
		r.mu.Unlock()
		if !errors.Is(err, ErrUnavailable) {
			return status, err
		}
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-changed:
		}
	}
}

func (r *Registry) snapshotLocked() (Status, error) {
	if r.retired {
		return Status{}, r.retiredError()
	}
	if r.pending() {
		return Status{}, ErrUnavailable
	}
	turn := r.turns[r.current]
	if r.terminalCurrent.NativeTurnID != "" && r.terminalCurrent.NativeTurnID == r.current {
		return Status{Binding: r.binding, NativeThreadState: r.threadState, ReadyForInput: r.threadState == "idle", Turn: Turn{NativeInputID: r.terminalCurrent.NativeInputID, NativeTurnID: r.current, State: "completed", Reason: r.terminalCurrent.Reason, ExternallySteered: r.terminalCurrent.ExternallySteered, ExternalInput: r.externalInput}}, nil
	}
	state := "unknown"
	if r.current == "" && r.threadState == "idle" {
		state = "idle"
	}
	if r.current != "" {
		state = "running"
		if turn.Closed {
			state = "completed"
		}
	}
	return Status{Binding: r.binding, NativeThreadState: r.threadState, ReadyForInput: r.threadState == "idle" && (r.current == "" || turn.Closed || r.terminalCurrent.NativeTurnID == r.current), Turn: Turn{NativeInputID: turn.InputID, NativeTurnID: r.current, State: state, Reason: turn.Reason, ExternallySteered: turn.Unsafe, ExternalInput: r.externalInput}}, nil
}

// InputStatus reads only this generation's retained native lifecycle evidence.
func (r *Registry) InputStatus(nonce string) (Turn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return Turn{}, r.retiredError()
	}
	input, ok := r.inputs[nonce]
	if !ok {
		if r.replay == nil {
			return Turn{}, ErrUnavailable
		}
		r.mu.Unlock()
		receipt, err := r.replay.Terminal(r.binding, nonce)
		r.mu.Lock()
		if err != nil {
			return Turn{}, err
		}
		state := receipt.State
		if state == "" {
			state = "completed"
		}
		return Turn{NativeInputID: receipt.Nonce, NativeTurnID: receipt.NativeTurnID, State: state, Reason: receipt.Reason, ExternallySteered: receipt.ExternallySteered, ExternalInput: r.externalInput}, nil
	}
	id := input.TurnID
	if id == "" {
		id = input.ReceiptTurnID
	}
	result := Turn{NativeInputID: nonce, NativeTurnID: id, State: "prepared", ExternalInput: r.externalInput}
	if id == "" {
		return result, nil
	}
	result.State = "admitted"
	if turn, ok := r.turns[id]; ok {
		result.ExternallySteered = turn.Unsafe
		result.Reason = turn.Reason
		if turn.Closed {
			result.State = "unknown"
			if turn.Reason != "" {
				result.State = "completed"
			}
		} else if input.TurnID != "" {
			result.State = "running"
		}
	}
	return result, nil
}
func (r *Registry) Resolve(ctx context.Context, request CallRequest) (Proof, error) {
	if !validID(request.ToolName) || !ValidHash(request.ArgumentsHash) {
		return Proof{}, ErrInvalid
	}
	callID, turnID, err := hostCoordinates(request.Binding, request.HostMeta)
	if err != nil {
		return Proof{}, err
	}
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return Proof{}, ctx.Err()
		}
		r.mu.Lock()
		if r.retired {
			e := r.retiredError()
			r.mu.Unlock()
			return Proof{}, e
		}
		if request.Binding != r.binding {
			r.mu.Unlock()
			return Proof{}, ErrStale
		}
		if r.pending() {
			changed := r.changed
			r.mu.Unlock()
			select {
			case <-ctx.Done():
				return Proof{}, ctx.Err()
			case <-wait.Done():
				return Proof{}, ErrUnavailable
			case <-changed:
			}
			continue
		}
		call, known := r.calls[callID]
		if !known && r.replay != nil {
			r.mu.Unlock()
			seen, err := r.replay.Seen(ReplayCall, callID)
			r.mu.Lock()
			if err != nil {
				r.mu.Unlock()
				return Proof{}, err
			}
			if seen {
				r.mu.Unlock()
				return Proof{}, ErrStale
			}
			call, known = r.calls[callID]
		}
		if known && call.ArgumentsHash != "" {
			if call.Closed {
				r.mu.Unlock()
				return Proof{}, ErrStale
			}
			if call.ToolName != request.ToolName || call.ArgumentsHash != request.ArgumentsHash || turnID != "" && turnID != call.TurnID {
				r.mu.Unlock()
				return Proof{}, ErrConflict
			}
			turn, exists := r.turns[call.TurnID]
			if call.AgentID != "" || turn.AgentID != "" {
				r.mu.Unlock()
				return Proof{}, ErrUnbound
			}
			if exists && (turn.Closed || turn.Unsafe) || r.threadState == "systemError" {
				r.mu.Unlock()
				return Proof{}, ErrStale
			}
			if exists && turn.InputID != "" {
				if r.current != call.TurnID {
					r.mu.Unlock()
					return Proof{}, ErrStale
				}
				proof := Proof{NativeInputID: turn.InputID, NativeTurnID: call.TurnID, NativeCallID: callID, Main: true}
				if r.replay != nil {
					version := r.version
					input := r.inputs[turn.InputID]
					r.mu.Unlock()
					err := r.replay.AssertReserved(r.binding, nonceGuard(turn.InputID, input), operationGuard(turn.InputID, input), turnGuard(call.TurnID, turn.AgentID), callGuard(callID, call))
					r.mu.Lock()
					if err != nil {
						r.mu.Unlock()
						return Proof{}, err
					}
					if r.retired || r.version != version || r.pending() {
						r.mu.Unlock()
						continue
					}
				}
				r.mu.Unlock()
				return proof, nil
			}
			pending := false
			for _, input := range r.inputs {
				if input.TurnID == "" && (input.ReceiptTurnID == "" || input.ReceiptTurnID == call.TurnID) {
					pending = true
					break
				}
			}
			if !pending {
				r.mu.Unlock()
				return Proof{}, ErrUnbound
			}
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return Proof{}, ctx.Err()
		case <-wait.Done():
			return Proof{}, ErrUnavailable
		case <-changed:
		}
	}
}
func hostCoordinates(binding Binding, raw json.RawMessage) (string, string, error) {
	canonical, err := CanonicalJSON(raw)
	if err != nil {
		return "", "", ErrInvalid
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(canonical, &meta) != nil || meta == nil {
		return "", "", ErrInvalid
	}
	field := func(fields map[string]json.RawMessage, key string) string {
		var value string
		_ = json.Unmarshal(fields[key], &value)
		return value
	}
	if binding.Provider == "claude" {
		id := field(meta, "claudecode/toolUseId")
		if !validID(id) {
			return "", "", ErrInvalid
		}
		return id, "", nil
	}
	if binding.Provider != "codex" {
		return "", "", ErrInvalid
	}
	callID := field(meta, "callId")
	thread := field(meta, "threadId")
	session := field(meta, "sessionId")
	var native map[string]json.RawMessage
	if json.Unmarshal(meta["x-codex-turn-metadata"], &native) != nil {
		return "", "", ErrInvalid
	}
	turnID := field(native, "turn_id")
	if !validID(callID) || !validID(turnID) || thread != binding.NativeSessionID || session != thread || field(native, "thread_id") != thread || field(native, "session_id") != session {
		return "", "", ErrStale
	}
	return callID, turnID, nil
}
