package skeleton

import (
	"context"
	"errors"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

const managerRetireReason = "is retiring a quiescent manager"

func (a *coreAPI) ManagerRetireSupported() bool { return a.managerRetire != nil }

func (a *coreAPI) ManagerRetire(ctx context.Context, local, operation string, req protocol.ManagerRetireReq, op string) (protocol.ManagerRetireReceipt, error) {
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
	if req.Replacement != "" && !schema.IsDiscordManager(m.Role, m.ManagerConversationID) {
		return receipt, managerproof.ErrStale
	}
	if req.Replacement == schema.ManagerReplaceExited {
		if op != protocol.OpManagerRetireStatus {
			return receipt, managerproof.ErrStale
		}
		if a.matchResumeInstance != nil && a.matchResumeInstance(m, req.Binding.SessionInstance) && a.managerWritersExited(m, req.Binding) {
			receipt.State = "retired"
		}
		return receipt, nil
	}
	target := persist.ManagerRetirement{OperationID: operation, Replacement: req.Replacement, Instance: req.Binding.SessionInstance, NativeSessionID: req.Binding.NativeSessionID,
		NativeGeneration: req.Binding.NativeGeneration, Generation: req.Generation, Checkpoint: req.Checkpoint,
		ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime, ClaimedAt: m.ManagerRetirement.ClaimedAt}
	if m.ManagerRetirement.OperationID != "" {
		if m.ManagerRetirement != target || a.matchResumeInstance == nil || !a.matchResumeInstance(m, target.Instance) {
			return receipt, managerproof.ErrStale
		}
		receipt.State = a.core.ManagerRetirementStatus(m, target)
		if receipt.State == "retired" && req.Replacement != "" && !a.managerWritersExited(m, req.Binding) {
			receipt.State = "uncertain"
		}
		if op == protocol.OpManagerRetire && receipt.State == "uncertain" {
			_, _ = a.managerRetire(ctx, local, req, target)
		}
		return receipt, nil
	}
	if op == protocol.OpManagerRetireCancel {
		if a.matchResumeInstance == nil || !a.matchResumeInstance(m, target.Instance) {
			return receipt, managerproof.ErrStale
		}
		err := a.core.CancelManagerRetire(ctx, local, req.Binding.ConversationID, req.Binding.Provider, target)
		if err == nil {
			receipt.State = "cancelled"
		}
		return receipt, err
	}
	if op == protocol.OpManagerRetireStatus {
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
		if req.Replacement != "" && d.blockedRetirementInput(local, req.Replacement) {
			return managerproof.ErrDirectInputUnresolved
		}
		if d.anyControlled(local) || req.Replacement == "" && d.directInputUnresolved(local) || d.contextGuardCompactionInFlight(local) ||
			req.Replacement == "" && (d.composerOutcomeUnresolved(local) || d.core.UnobservedSemanticExecution(local, "")) {
			return managerproof.ErrUnavailable
		}
		retirement := ""
		if redrive {
			retirement = target.OperationID
		}
		current, err := d.managerRuntimeContext(ctx, local, target.Instance, retirement)
		if req.Replacement != "" {
			// A submitted terminal line is retained as an uncertain old effect. A draft or attached
			// operator above always vetoes the claim. Retry only the exact durable physical claim.
			if !redrive && !(req.Replacement == schema.ManagerReplaceCustody && errors.Is(err, managerproof.ErrQuarantined) ||
				req.Replacement == schema.ManagerReplaceQuota && err == nil && current.ProviderExhausted && current.ReadyForInput && current.Binding == req.Binding && !current.Turn.ExternallySteered) {
				return managerproof.ErrStale
			}
		} else if err != nil {
			return err
		}
		meta, found := d.core.Get(local)
		if !found || !d.matchesResumeSourceInstance(meta, target.Instance) || req.Replacement == "" && !retirementReady(req, current, redrive) {
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

func (d *Daemon) blockedRetirementInput(local, replacement string) bool {
	d.directInput.mu.Lock()
	defer d.directInput.mu.Unlock()
	r, ok := d.directInput.unresolved[local]
	return ok && (replacement == "" || r.Class != protocol.DirectInputSubmitted || d.directInput.pendingDurability[local])
}

func (a *coreAPI) managerWritersExited(m persist.Meta, b protocol.ManagerRuntimeBinding) bool {
	if m.Status.Process != status.ProcessExited || m.ConversationID != b.NativeSessionID || verifyNativeWritersStopped(a.stateDir, m, false) != nil {
		return false
	}
	var native shim.NativeProcessInfo
	return readNativeProof(a.stateDir, m.ID, shim.NativeProcessFile, &native) == nil && native.Generation == b.NativeGeneration
}
