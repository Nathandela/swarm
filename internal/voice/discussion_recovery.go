package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func discussionBindingFromNative(observed protocol.ManagerRuntimeStatus) ManagerBinding {
	native := observed.Binding
	return ManagerBinding{NativeCLI: observed.NativeCLI, ConversationID: native.ConversationID, Provider: native.Provider, SessionID: native.Session,
		SessionInstance: native.SessionInstance, NativeSessionID: native.NativeSessionID, NativeGeneration: native.NativeGeneration,
		ObservationGeneration: native.ObservationGeneration, LoginEpoch: native.LoginEpoch}
}

func discussionSameNative(previous, next ManagerBinding) bool {
	previous.Generation, next.Generation = 0, 0
	previous.ObservationGeneration, next.ObservationGeneration = "", ""
	return previous == next
}

func discussionRecoverableBindingError(err error) bool {
	return errors.Is(err, ErrStale) || errors.Is(err, managerproof.ErrStale) || errors.Is(err, managerproof.ErrUnavailable) ||
		errors.Is(err, ErrControllerNativeUnavailable) || errors.Is(err, managerproof.ErrQuarantined)
}

func discussionManagerExited(rows []protocol.SessionView, binding protocol.ManagerRuntimeBinding) bool {
	for _, row := range rows {
		if row.ID == binding.Session && row.Role == "manager" && row.ManagerConversationID == binding.ConversationID &&
			row.Agent == binding.Provider && row.Status.Process == status.ProcessExited &&
			(row.Capabilities == nil || row.Capabilities.SessionInstance == binding.SessionInstance) {
			return true
		}
	}
	return false
}

func requireDeletedManagerAbsent(rows []protocol.SessionView, binding protocol.ManagerRuntimeBinding) error {
	for _, row := range rows {
		if row.Role == "manager" && row.ManagerConversationID == binding.ConversationID && row.ID != binding.Session && row.Status.Process != status.ProcessExited {
			return errCompetingManagers
		}
		if row.ID == binding.Session {
			return ErrControllerUncertain
		}
	}
	return nil
}

func (d *DiscussionController) recoverActive(ctx context.Context, original error) error {
	previous := d.controller.binding
	native, err := d.runtime.ManagerRuntime(previous.SessionID, previous.SessionInstance)
	if err == nil {
		next := discussionBindingFromNative(native)
		if native.Binding.Validate() != nil || !discussionSameNative(previous, next) || native.AuthFile != d.refs().AuthFile || native.BridgeSocket != d.refs().Socket {
			return ErrStale
		}
		if identity(previous) == identity(next) {
			return original
		}
		binding, err := d.recoverBinding(previous, next, native.Turn, false, nil)
		if err != nil {
			return err
		}
		return d.bind(binding)
	}
	if errors.Is(err, managerproof.ErrDirectInputUnresolved) {
		return d.inputHold(ErrDiscussionDirectInputHeld, err)
	}
	if !discussionRecoverableBindingError(err) {
		return original
	}
	errFromRuntime := err
	rows, err := d.runtime.List()
	if err != nil {
		return err
	}
	exited := discussionManagerExited(rows, runtimeBinding(previous))
	if !exited {
		if err := requireDeletedManagerAbsent(rows, runtimeBinding(previous)); err != nil {
			if errors.Is(err, ErrControllerUncertain) && errors.Is(original, managerproof.ErrQuarantined) && errors.Is(errFromRuntime, managerproof.ErrQuarantined) {
				return d.startReplacement(ctx, schema.ManagerReplaceCustody)
			}
			return err
		}
	}
	st, err := d.store.Snapshot()
	if err != nil {
		return err
	}
	if !discussionPending(st) {
		return original
	}
	reqExit := exitedDiscussionManager(previous, st.Conversations[previous.ConversationID].ManagerGeneration)
	receipt, err := d.runtime.ManagerRetire(ctx, reqExit, true)
	if err != nil {
		if !exited && errors.Is(err, managerproof.ErrStale) {
			return errors.Join(ErrDiscussionActivationUnavailable, ErrControllerUncertain, errMissingDispatcherExit)
		}
		return err
	}
	if receipt.OperationID != protocol.ManagerRetireOperationID(reqExit) {
		return ErrControllerUncertain
	}
	if !exited && receipt.State == schema.ManagerRetireDeleted {
		return d.recordExitedManager(ctx, previous, reqExit, nil, nil)
	}
	if !exited || receipt.State != "retired" {
		return ErrControllerUncertain
	}
	for _, action := range st.Actions {
		if action.ConversationID == previous.ConversationID && action.State == "executing" {
			if _, err := d.controller.reconcileAction(ctx, action); err != nil && !errors.Is(err, ErrControllerUncertain) {
				return err
			}
		}
	}
	return d.recordExitedManager(ctx, previous, reqExit, nil, nil)
}

func exitedDiscussionManager(binding ManagerBinding, generation uint64) protocol.ManagerRetireReq {
	return protocol.ManagerRetireReq{Binding: runtimeBinding(binding), Generation: max(generation, 1), Checkpoint: hashValue(identity(binding)), Replacement: schema.ManagerReplaceExited, ExpectedTurn: protocol.ManagerNativeTurn{State: "unknown"}}
}

func (d *DiscussionController) recoverDeletedRetirement(ctx context.Context, req protocol.ManagerRetireReq) error {
	rows, err := d.runtime.List()
	if err != nil {
		return err
	}
	if err := requireDeletedManagerAbsent(rows, req.Binding); err != nil {
		return err
	}
	st, err := d.store.readView()
	if err != nil {
		return err
	}
	conv := st.Conversations[req.Binding.ConversationID]
	binding := conv.Binding
	binding.Generation = st.Generation
	if runtimeBinding(binding) != req.Binding {
		return ErrStale
	}
	return d.recordExitedManager(ctx, binding, exitedDiscussionManager(binding, conv.ManagerGeneration), &req, nil)
}

func (d *DiscussionController) recordExitedManager(ctx context.Context, previous ManagerBinding, reqExit protocol.ManagerRetireReq, prior *protocol.ManagerRetireReq, parked *Conversation) error {
	if d.scope.VerifyAudience != nil {
		if err := d.scope.VerifyAudience(ctx); err != nil {
			return err
		}
	}
	// Persist verified writer exit like a completed retirement, so a
	// service restart or unavailable login pool reuses the same replacement intent.
	return d.store.update(func(current *State) error {
		if err := checkBinding(current, previous); err != nil {
			return err
		}
		c := current.Conversations[previous.ConversationID]
		if reqExit.Generation != max(c.ManagerGeneration, 1) {
			return ErrConflict
		}
		if parked != nil {
			if !sameParkedManager(current, previous.Generation, c, *parked) {
				return ErrStale
			}
		} else if c.Parked || (c.Retirement == nil) != (prior == nil) || prior != nil && *c.Retirement != *prior {
			return ErrConflict
		}
		c.Retirement, c.Parked = &reqExit, true
		putRecord(current, "conversations", &current.Conversations, previous.ConversationID, c)
		return nil
	})
}

func (d *DiscussionController) recoverBinding(previous, next ManagerBinding, turn protocol.ManagerNativeTurn, reconstructed bool, parked *Conversation) (ManagerBinding, error) {
	err := d.store.update(func(st *State) error {
		if err := checkBinding(st, previous); err != nil {
			return err
		}
		id := previous.ConversationID
		c := st.Conversations[id]
		if parked != nil {
			migration := d.cfg.VMProjectAccess && !previous.NativeCLI && next.NativeCLI
			exhausted := c.FailedResume != nil && c.FailedResume.Attempt >= 2
			if !reconstructed || !sameParkedManager(st, previous.Generation, c, *parked) || (!migration && !exhausted && parked.Retirement.Replacement == "") {
				return ErrStale
			}
		} else if c.Parked || c.Retirement != nil {
			return ErrConflict
		}
		if next.Provider != previous.Provider || next.ConversationID != id || !validBinding(next) {
			return ErrConflict
		}
		if reconstructed {
			for k, action := range st.Actions {
				if action.ConversationID != id {
					continue
				}
				switch action.State {
				case "prepared":
					action.State, action.Receipt, action.SettledAt = "settled", json.RawMessage(`{"state":"rejected","reason":"manager exited before action dispatch"}`), d.now().UTC()
					putRecord(st, "actions", &st.Actions, k, action)
					deleteRecord(st, "workers", &st.Workers, key(id, "reservation:"+action.ID))
				}
			}
		}
		var interrupted Admission
		for k, a := range st.Admissions {
			if a.ConversationID != id || !a.SettledAt.IsZero() {
				continue
			}
			switch a.Phase {
			case "", "superseded":
				continue
			case "prepared":
				a.Phase, a.NativeInputID, a.OperationID, a.Request, a.ExternalInput = "", "", "", nil, 0
				putRecord(st, "admissions", &st.Admissions, k, a)
			default:
				if !reconstructed {
					continue
				}
				if a.Sequence > interrupted.Sequence {
					interrupted = a
				}
				a.Phase, a.Reason, a.NativeBindingRetired = "uncertain", "manager exited; prior native outcome is uncertain", true
				putRecord(st, "admissions", &st.Admissions, k, a)
				if !outstandingDiscussionAction(st, id, a.Sequence) {
					if err := settleControllerInputState(st, previous, a.Sequence); err != nil {
						return err
					}
				}
			}
		}
		hasReadyInput := false
		for _, a := range st.Admissions {
			if a.ConversationID == id && a.SettledAt.IsZero() && !a.NativeBindingRetired {
				hasReadyInput = true
				break
			}
		}
		if reconstructed && interrupted.Sequence != 0 && interrupted.ReusedFinal == nil && !hasReadyInput {
			output := discussionOutputSource(st, id, interrupted)
			for _, p := range st.Publications {
				if p.ConversationID == id && p.Admission == interrupted.Sequence && p.Final {
					output = DiscussionOutputSource{}
					break
				}
			}
			if output.AdmissionID != "" {
				recovery, err := allocateAdmission(st, previous, "manager-recovery:"+hashValue(identity(previous)))
				if err != nil {
					return err
				}
				recovery.RecoveryOnly, recovery.RecoveryOutput, recovery.Project = true, &output, interrupted.Project
				recovery.InputText = "Reconcile the interrupted discussion turn from its original thread, worker sessions and receipts. Report confirmed results or the specific outcome still unknown. Do not repeat prior operations or spawn another worker."
				putRecord(st, "admissions", &st.Admissions, admissionKey(id, recovery.Sequence), recovery)
			}
		}
		c = st.Conversations[id]
		next.Generation = st.Generation
		c.Binding, c.CustodyLost = identity(next), false
		if reconstructed {
			c.NeedsHandoff = true
		}
		if parked != nil {
			c.Parked = false
			if c.Retirement.Replacement != "" {
				c.ReplacementAfter = d.now().Add(discussionReplacementInterval).Unix()
			}
			c.Retirement = nil
			c.FailedResume = nil
		}
		manualActivity := !reconstructed && (turn.ExternallySteered || c.ExternalObserved && c.ExternalInput != turn.ExternalInput)
		c.ExternalObserved, c.ExternalInput = true, turn.ExternalInput
		c.SchedulerPaused = manualActivity || !reconstructed && c.SchedulerPaused
		if reconstructed {
			if c.ManagerGeneration == ^uint64(0) {
				return ErrCapacity
			}
			c.ManagerGeneration = max(c.ManagerGeneration, 1) + 1
		}
		putRecord(st, "conversations", &st.Conversations, id, c)
		if reconstructed {
			sequence, err := nextDiscussionOutput(st, id)
			if err != nil {
				return err
			}
			p := Publication{ID: "continuity-" + hashValue(identity(previous)), ConversationID: id, OutputSequence: sequence, DiscussionSource: DiscussionOutputSource{SystemCause: "manager_continuity_gap"},
				Text: "Dispatcher replaced. Native history unavailable. Queued messages and jobs are preserved. Uncertain actions stay on hold."}
			if parked != nil && !previous.NativeCLI && next.NativeCLI {
				p.Text = "Swarm moved this discussion to a new manager using the VM’s normal CLI permissions. Queued messages and tracked jobs were preserved."
			}
			p.TextHash = hashBytes([]byte(p.Text))
			putRecord(st, "publications", &st.Publications, key(id, p.ID), p)
		}
		return nil
	})
	return next, err
}

// startReplacement checkpoints pending work before the supervisor independently fences the old
// process. A crash resumes the same retirement ID and launch intent.
func (d *DiscussionController) startReplacement(ctx context.Context, reason string) error {
	b := d.controller.binding
	rows, err := d.runtime.List()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Role == "manager" && row.ManagerConversationID == b.ConversationID && row.ID != b.SessionID && row.Status.Process != status.ProcessExited {
			return errCompetingManagers
		}
	}
	req := protocol.ManagerRetireReq{Binding: runtimeBinding(b), Replacement: reason, ExpectedTurn: protocol.ManagerNativeTurn{State: "unknown"}}
	if err := d.store.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		c := st.Conversations[b.ConversationID]
		if !discussionPending(*st) {
			return ErrControllerUncertain
		}
		if c.Retirement != nil {
			return ErrConflict
		}
		req.Generation = max(c.ManagerGeneration, 1)
		req.Checkpoint = hashValue(struct {
			Binding   ManagerBinding
			Reason    string
			Admission uint64
			At        int64
		}{b, reason, c.LastAdmission, d.now().UnixNano()})
		c.Retirement = &req
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
		return nil
	}); err != nil {
		return err
	}
	return d.finishRetirement(ctx, req, false)
}

func outstandingDiscussionAction(st *State, conversation string, admission uint64) bool {
	for _, a := range st.Actions {
		if a.ConversationID == conversation && a.Admission == admission && a.State == "executing" {
			return true
		}
	}
	return false
}

const discussionReplacementInterval = 5 * time.Minute

func replacementRetryError(at int64) error {
	return fmt.Errorf("%w: dispatcher replacement retry at %s", ErrDiscussionActivationUnavailable, time.Unix(at, 0).UTC().Format(time.RFC3339))
}
