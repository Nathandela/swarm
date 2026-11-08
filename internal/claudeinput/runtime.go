package claudeinput

import "context"

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
		if b.revoked || req.Worker != b.guard || b.guard.Generation == "" || req.ExternalInput <= b.turn.ExternalInput {
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
		if b.revoked || req.Worker != b.guard || b.guard.Generation == "" || req.Turn == nil {
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
		command := Interrupt{OperationID: req.OperationID, TurnID: req.TurnID, State: "prepared"}
		b.interrupts[req.OperationID] = command
		respond(command)
	case "/interrupt-result":
		command, ok := b.interrupts[req.OperationID]
		if !ok || req.Worker != b.guard || req.TurnID != command.TurnID || req.State != "accepted" && req.State != "rejected" && req.State != "uncertain" {
			refuse("invalid native interrupt disposition")
			return true
		}
		if command.State == "dispatched" {
			command.State = req.State
			b.interrupts[req.OperationID] = command
		}
		respond(command)
	default:
		return false
	}
	return true
}
