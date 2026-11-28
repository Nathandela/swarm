package claudeinput

import (
	"context"
	"errors"

	"github.com/Nathandela/swarm/internal/managerproof"
)

// Turn is an observed main-loop native turn. Manager input coordinates require
// the authenticated nonce/origin/stored-message join, never prompt-text matching.
type Turn struct {
	ReadyForInput     bool   `json:"ready_for_input"`
	ExternallySteered bool   `json:"externally_steered,omitempty"`
	NativeInputID     string `json:"native_input_id,omitempty"`
	NativeMessageID   string `json:"native_message_id,omitempty"`
	TurnID            string `json:"turn_id"`
	State             string `json:"state"`
	Reason            string `json:"reason,omitempty"`
	ExternalInput     uint64 `json:"external_input"`
}

type Interrupt struct {
	OperationID string `json:"operation_id"`
	TurnID      string `json:"turn_id"`
	State       string `json:"state"`
}

func (c *Client) Turn(ctx context.Context, worker Worker) (Turn, error) {
	var out Turn
	err := c.call(ctx, "/turn-status", request{Worker: worker}, &out)
	return out, err
}

// Interrupt queues one exact native abort; a lost dispatch is never retried.
func (c *Client) Interrupt(ctx context.Context, worker Worker, operationID, turnID string) (Interrupt, error) {
	var out Interrupt
	err := c.call(ctx, "/interrupt", request{Worker: worker, OperationID: operationID, TurnID: turnID}, &out)
	return out, err
}

func (b *Broker) serveRuntimeLocked(path string, req request, live bool, respond func(any), refuse func(string)) bool {
	switch path {
	case "/activity", "/turn", "/turn-status", "/interrupt", "/interrupt-result":
		if !b.descriptor.NativeTurns {
			refuse("native turn runtime is not qualified")
			return true
		}
	default:
		return false
	}
	switch path {
	case "/activity":
		if !b.currentGuardObservation(req) || req.ExternalInput <= b.turn.ExternalInput {
			refuse("invalid external input observation")
			return true
		}
		b.turn.ExternalInput = req.ExternalInput
		b.nativeInputReady = false
		if b.manager != nil {
			b.manager.ExternalActivity()
		}
		if b.turn.State == "running" {
			b.turn.ExternallySteered = true
		}
		respond(nil)
	case "/turn":
		if !b.currentGuardObservation(req) || req.Turn == nil {
			refuse("native lifecycle identity unavailable")
			return true
		}
		turn := *req.Turn
		if len(turn.TurnID) > 128 ||
			turn.State != "idle" && turn.State != "running" && turn.State != "completed" ||
			turn.State != "idle" && turn.TurnID == "" ||
			turn.State == "completed" && (turn.TurnID != b.turn.TurnID || turn.Reason != "answer" && turn.Reason != "aborted" && turn.Reason != "refusal" && turn.Reason != "error") {
			refuse("invalid native lifecycle transition")
			return true
		}
		if err := b.observeManagerTurnLocked(req); err != nil {
			b.revokeLocked()
			refuse("manager native turn evidence rejected")
			return true
		}
		if turn.State == "completed" {
			turn.NativeInputID = b.turn.NativeInputID
			turn.NativeMessageID = b.turn.NativeMessageID
			turn.ExternallySteered = b.turn.ExternallySteered
		}
		turn.ExternalInput = b.turn.ExternalInput
		b.turn = turn
		respond(nil)
	case "/turn-status":
		if b.custody != nil && b.custody.Pending() {
			refuse("native custody pending")
			return true
		}
		if !live || req.Worker != b.worker || b.turn.State == "" {
			refuse("native lifecycle proof unavailable")
			return true
		}
		turn := b.turn
		turn.ReadyForInput = b.nativeInputReady && b.manager != nil && b.manager.ReadyForInput()
		respond(turn)
	case "/interrupt":
		if req.Worker != b.worker || b.worker.Generation == "" || req.OperationID == "" || len(req.OperationID) > 128 || req.TurnID == "" || len(req.TurnID) > 128 {
			refuse("native interrupt identity unavailable")
			return true
		}
		if old, ok := b.interrupts[req.OperationID]; ok {
			if old.TurnID != req.TurnID {
				refuse("native interrupt identity conflict")
			} else {
				respond(old)
			}
			return true
		}
		old, err := b.compactInterruptLocked(req.OperationID, req.TurnID)
		if err == nil {
			respond(old)
			return true
		}
		if !errors.Is(err, managerproof.ErrUnbound) {
			refuse("native interrupt identity conflict or custody unavailable")
			return true
		}
		if !live {
			refuse("native interrupt proof unavailable")
			return true
		}
		if b.turn.State != "running" || b.turn.TurnID != req.TurnID {
			refuse("native turn changed")
			return true
		}
		if len(b.interrupts) >= maxOperations {
			refuse("native interrupt receipt capacity reached")
			return true
		}
		if b.interrupts == nil {
			b.interrupts = make(map[string]Interrupt)
		}
		if b.replay != nil {
			if b.manager == nil {
				refuse("manager interrupt custody unavailable")
				return true
			}
			binding, err := b.manager.BindingSnapshot()
			if err != nil {
				refuse("manager interrupt custody unavailable")
				return true
			}
			if err := b.registryIO(func() error {
				return b.replay.Reserve(binding.Binding, managerproof.ReplayKey{Kind: managerproof.ReplayInterrupt, ID: req.OperationID, Owner: managerproof.ReplayFingerprint(req.TurnID)})
			}); err != nil {
				refuse("manager interrupt expired or unavailable")
				return true
			}
		}
		command := Interrupt{OperationID: req.OperationID, TurnID: req.TurnID, State: "prepared"}
		b.interrupts[req.OperationID] = command
		respond(command)
	case "/interrupt-result":
		command, ok := b.interrupts[req.OperationID]
		if !ok && b.replay != nil && req.Worker == b.guard {
			old, err := b.compactInterruptLocked(req.OperationID, req.TurnID)
			if err == nil && old.State == req.State {
				respond(old)
				return true
			}
		}
		if !ok || req.Worker != b.guard || req.TurnID != command.TurnID || req.State != "accepted" && req.State != "rejected" && req.State != "uncertain" {
			refuse("invalid native interrupt disposition")
			return true
		}
		if command.State == "dispatched" {
			command.State = req.State
			b.interrupts[req.OperationID] = command
		}
		if b.replay != nil && (command.State == "accepted" || command.State == "rejected") {
			binding, err := b.manager.BindingSnapshot()
			if err == nil {
				err = b.registryIO(func() error {
					return b.replay.CloseKeys(binding.Binding, managerproof.ReplayKey{Kind: managerproof.ReplayInterrupt, ID: req.OperationID, Owner: managerproof.ReplayFingerprint(req.TurnID), Detail: managerproof.ReplayFingerprint(command.State)})
				})
			}
			if err != nil {
				refuse("native interrupt closure custody unavailable")
				return true
			}
			delete(b.interrupts, req.OperationID)
		}
		respond(command)
	default:
		return false
	}
	return true
}

func (b *Broker) currentGuardObservation(req request) bool {
	return !b.revoked && !b.guardRecovering && req.Worker == b.guard && b.guard.Generation != "" && req.Observation == b.guardObservation
}

func (b *Broker) compactInterruptLocked(id, turn string) (Interrupt, error) {
	if b.replay == nil || b.manager == nil {
		return Interrupt{}, managerproof.ErrUnbound
	}
	binding := b.manager.ImmutableBinding()
	var detail string
	err := b.registryIO(func() error {
		var err error
		detail, err = b.replay.ClosedKeyDetail(binding, managerproof.ReplayKey{
			Kind: managerproof.ReplayInterrupt, ID: id, Owner: managerproof.ReplayFingerprint(turn),
		})
		return err
	})
	if err != nil {
		return Interrupt{}, err
	}
	for _, state := range []string{"accepted", "rejected"} {
		if detail == managerproof.ReplayFingerprint(state) {
			return Interrupt{OperationID: id, TurnID: turn, State: state}, nil
		}
	}
	return Interrupt{}, managerproof.ErrQuarantined
}
