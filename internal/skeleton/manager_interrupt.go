package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
)

const managerInterruptAction = "manager_interrupt"

func (a *coreAPI) ManagerInterruptSupported() bool {
	return a.managerInterruptTurn != nil && a.managerInputStatus != nil
}

func (a *coreAPI) ManagerInterrupt(ctx context.Context, local, operation string, req protocol.ManagerInterruptReq, statusOnly bool) (protocol.ManagerInterruptReceipt, error) {
	receipt := protocol.ManagerInterruptReceipt{OperationID: operation, NativeInputID: req.NativeInputID, NativeTurnID: req.NativeTurnID, State: "unknown"}
	if a.core == nil || !a.ManagerInterruptSupported() || req.Validate() != nil || req.Binding.Session != local {
		return receipt, managerproof.ErrUnavailable
	}
	namespaced := req
	namespaced.Binding.Session = protocol.NamespacedID(a.endpointID, local)
	if protocol.ManagerInterruptOperationID(namespaced) != operation {
		return receipt, managerproof.ErrStale
	}
	raw, _ := json.Marshal(req)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	if rec, found := a.core.ComposerOperation(operation); found {
		if rec.Action != managerInterruptAction || rec.SessionID != local || rec.SessionInstance != req.Binding.SessionInstance || rec.RequestHash != hash {
			return receipt, managerproof.ErrStale
		}
		switch rec.Phase {
		case idempotency.PhaseCompleted, idempotency.PhaseFailed:
			var saved protocol.ManagerInterruptReceipt
			if json.Unmarshal(rec.Outcome, &saved) != nil || saved.OperationID != operation || saved.NativeInputID != req.NativeInputID ||
				saved.NativeTurnID != req.NativeTurnID || (saved.State != "interrupted" && saved.State != "ended") {
				return receipt, managerproof.ErrUnavailable
			}
			return saved, nil
		case idempotency.PhasePrepared:
			receipt.State = "prepared"
		default:
			return a.observeManagerInterrupt(ctx, local, req, receipt)
		}
	}
	if statusOnly {
		return receipt, nil
	}
	dispatched := false
	err := a.managerInterruptTurn(ctx, local, operation, req, func() error {
		phase, _, err := a.core.ClaimComposerOperation(operation, managerInterruptAction, local, req.Binding.SessionInstance, hash)
		if err != nil {
			return err
		}
		if phase != string(idempotency.PhasePrepared) {
			return managerproof.ErrStale
		}
		if err = a.core.BindManagerInterruptRequest(operation, raw); err != nil {
			return err
		}
		if err = a.core.BeginComposerOperation(operation); err != nil {
			return err
		}
		dispatched = true
		return nil
	})
	if !dispatched {
		return receipt, err
	}
	// Native admission or an empty RPC reply is insufficient to settle an interrupt.
	return a.observeManagerInterrupt(ctx, local, req, receipt)
}

func (a *coreAPI) observeManagerInterrupt(ctx context.Context, local string, req protocol.ManagerInterruptReq, receipt protocol.ManagerInterruptReceipt) (protocol.ManagerInterruptReceipt, error) {
	receipt.State = "uncertain"
	turn, err := a.managerInputStatus(ctx, local, protocol.ManagerAdmission{Binding: req.Binding, Nonce: req.NativeInputID})
	if err != nil || turn.NativeInputID != req.NativeInputID || turn.NativeTurnID != req.NativeTurnID {
		return receipt, nil
	}
	switch turn.State {
	case "running":
		receipt.State = "pending"
	case "completed":
		receipt.State, receipt.Reason = "ended", turn.Reason
		if turn.Reason == "aborted" {
			receipt.State = "interrupted"
		}
		raw, _ := json.Marshal(receipt)
		if err := a.core.CommitComposerOperation(receipt.OperationID, raw, true); err != nil {
			receipt.State, receipt.Reason = "uncertain", ""
		}
	}
	return receipt, nil
}

func (d *Daemon) interruptManagerTurn(ctx context.Context, local, operation string, req protocol.ManagerInterruptReq, begin func() error) error {
	current, err := d.checkedManagerBindingContext(ctx, local, req.Binding)
	if err != nil {
		return err
	}
	if current.Turn.State != "running" || current.Turn.NativeInputID != req.NativeInputID || current.Turn.NativeTurnID != req.NativeTurnID {
		return managerproof.ErrStale
	}
	switch req.Binding.Provider {
	case "claude":
		desc, err := d.core.SessionClaudeInput(local)
		if err != nil {
			return err
		}
		client := claudeinput.NewClient(desc)
		worker, err := client.Ready(ctx)
		if err != nil {
			return err
		}
		observer, err := client.ManagerObservation(ctx, worker)
		if err != nil {
			return err
		}
		if worker.NativeSessionID != req.Binding.NativeSessionID || observer != req.Binding.ObservationGeneration {
			return managerproof.ErrStale
		}
		if err = begin(); err != nil {
			return err
		}
		_, err = client.Interrupt(ctx, worker, operation, req.NativeTurnID)
		return err
	case "codex":
		backend, ok := d.sessionBackendFor(local)
		if !ok || backend.feed == nil || backend.feed.retired.Load() || backend.feed.epoch != req.Binding.ObservationGeneration ||
			backend.sessionInstance != req.Binding.SessionInstance || backend.threadID != req.Binding.NativeSessionID {
			return managerproof.ErrStale
		}
		if err = begin(); err != nil {
			return err
		}
		return backend.conn.Call(ctx, "turn/interrupt", map[string]any{"threadId": backend.threadID, "turnId": req.NativeTurnID}, nil)
	default:
		return managerproof.ErrUnavailable
	}
}
