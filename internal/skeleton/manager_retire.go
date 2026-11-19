package skeleton

import (
	"context"
	"errors"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

const managerRetireReason = "is retiring a quiescent manager"

func (a *coreAPI) ManagerRetireSupported() bool { return a.managerRetire != nil }

func (a *coreAPI) ManagerRetire(ctx context.Context, local, operation string, req protocol.ManagerRetireReq, statusOnly bool) (protocol.ManagerRetireReceipt, error) {
	receipt := protocol.ManagerRetireReceipt{OperationID: operation, State: "unknown"}
	if a.core == nil || !a.ManagerRetireSupported() || req.Validate() != nil || req.Binding.Session != local {
		return receipt, managerproof.ErrUnavailable
	}
	wire := req
	wire.Binding.Session = protocol.NamespacedID(a.endpointID, local)
	if protocol.ManagerRetireOperationID(wire) != operation {
		return receipt, managerproof.ErrStale
	}
	m, found := a.core.Get(local)
	if !found || m.Role != "manager" || m.ManagerConversationID != req.Binding.ConversationID || m.AgentType != req.Binding.Provider {
		return receipt, managerproof.ErrStale
	}
	target := persist.ManagerRetirement{OperationID: operation, Instance: req.Binding.SessionInstance, NativeSessionID: req.Binding.NativeSessionID,
		NativeGeneration: req.Binding.NativeGeneration, Generation: req.Generation, Checkpoint: req.Checkpoint,
		ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime, ClaimedAt: m.ManagerRetirement.ClaimedAt}
	if m.ManagerRetirement.OperationID != "" {
		if m.ManagerRetirement != target || a.matchResumeInstance == nil || !a.matchResumeInstance(m, target.Instance) {
			return receipt, managerproof.ErrStale
		}
		receipt.State = a.core.ManagerRetirementStatus(m, target)
		if !statusOnly && receipt.State == "uncertain" {
			_, _ = a.managerRetire(ctx, local, req, target)
		}
		return receipt, nil
	}
	if statusOnly {
		return receipt, nil
	}
	claimed, err := a.managerRetire(ctx, local, req, target)
	if claimed {
		receipt.State = "uncertain"
		return receipt, nil
	}
	return receipt, err
}

func (d *Daemon) retireManager(ctx context.Context, local string, req protocol.ManagerRetireReq, target persist.ManagerRetirement) (bool, error) {
	var claimed bool
	redrive := !target.ClaimedAt.IsZero()
	err := d.withSessionStopFence(local, redrive, managerRetireReason, func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.anyControlled(local) || d.directInputUnresolved(local) || d.contextGuardCompactionInFlight(local) ||
			d.composerOutcomeUnresolved(local) || d.core.UnobservedSemanticExecution(local, "") {
			return managerproof.ErrUnavailable
		}
		retirement := ""
		if redrive {
			retirement = target.OperationID
		}
		current, err := d.managerRuntimeContext(ctx, local, target.Instance, retirement)
		if err != nil {
			return err
		}
		meta, found := d.core.Get(local)
		if !found || !d.matchesResumeSourceInstance(meta, target.Instance) || !retirementReady(req, current, redrive) {
			return managerproof.ErrStale
		}
		claimed, err = d.core.RetireManager(ctx, local, req.Binding.ConversationID, req.Binding.Provider, target)
		if claimed && err != nil {
			// Keep the original durable checkpoint and permit a fenced redrive.
			return errors.Join(errAuthRecycleObligationRetained, err)
		}
		return err
	})
	if errors.Is(err, errAuthOwnerEnding) {
		err = managerproof.ErrStale
	}
	return claimed, err
}

// A restarted observer can establish idle anew. The durable claim closed input
// before the old observer disappeared, so only its observation epoch may change.
func retirementReady(req protocol.ManagerRetireReq, current protocol.ManagerRuntimeStatus, redrive bool) bool {
	if !current.ReadyForInput {
		return false
	}
	if current.Binding == req.Binding {
		return current.Turn == req.ExpectedTurn
	}
	if !redrive {
		return false
	}
	current.Binding.ObservationGeneration = req.Binding.ObservationGeneration
	return current.Binding == req.Binding && (current.Turn == req.ExpectedTurn || current.Turn.State == "idle")
}
