package voice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func discussionBindingFromNative(native protocol.ManagerRuntimeBinding) ManagerBinding {
	return ManagerBinding{ConversationID: native.ConversationID, Provider: native.Provider, SessionID: native.Session,
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

func (d *DiscussionController) recoverActive(ctx context.Context, original error) error {
	previous := d.controller.binding
	native, err := d.runtime.ManagerRuntime(previous.SessionID, previous.SessionInstance)
	if err == nil {
		next := discussionBindingFromNative(native.Binding)
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
	if !discussionRecoverableBindingError(err) {
		return original
	}
	rows, err := d.runtime.List()
	if err != nil {
		return err
	}
	if !discussionManagerExited(rows, runtimeBinding(previous)) {
		return ErrControllerUncertain
	}
	st, err := d.store.Snapshot()
	if err != nil {
		return err
	}
	if !discussionPending(st) {
		return original
	}
	for _, action := range st.Actions {
		if action.ConversationID == previous.ConversationID && action.State == "executing" {
			if _, err := d.controller.reconcileAction(ctx, action); err != nil {
				return err
			}
		}
	}
	if d.scope.VerifyAudience != nil {
		if err := d.scope.VerifyAudience(ctx); err != nil {
			return err
		}
	}
	req := managerLaunchRequest(d.cfg, d.refs())
	intent := "reconstruct-" + hashValue(identity(previous)) + ".json"
	selected, err := bootstrapManagerRequest(ctx, d.dir, d.cfg, d.refs(), d.endpoint, nil, d.runtime, req, intent, previous.SessionID)
	if err != nil {
		return err
	}
	if selected.Binding.SessionID == previous.SessionID || selected.Binding.SessionInstance == previous.SessionInstance {
		return ErrStale
	}
	binding, err := d.recoverBinding(previous, selected.Binding, selected.Runtime.Turn, true, nil)
	if err != nil {
		return err
	}
	return d.bind(binding)
}

func (d *DiscussionController) recoverBinding(previous, next ManagerBinding, turn protocol.ManagerNativeTurn, reconstructed bool, parked *Conversation) (ManagerBinding, error) {
	err := d.store.update(func(st *State) error {
		if err := checkBinding(st, previous); err != nil {
			return err
		}
		id := previous.ConversationID
		c := st.Conversations[id]
		if parked != nil {
			if !reconstructed || !sameParkedManager(st, previous.Generation, c, *parked) || c.FailedResume == nil || c.FailedResume.Attempt != 2 {
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
				case "executing":
					return ErrControllerUncertain
				case "prepared":
					action.State, action.Receipt, action.SettledAt = "settled", json.RawMessage(`{"state":"rejected","reason":"manager exited before action dispatch"}`), d.now().UTC()
					putRecord(st, "actions", &st.Actions, k, action)
					deleteRecord(st, "workers", &st.Workers, key(id, "reservation:"+action.ID))
				}
			}
		}
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
				a.Phase, a.Reason, a.NativeBindingRetired = "uncertain", "manager exited; prior native outcome is uncertain", true
				putRecord(st, "admissions", &st.Admissions, k, a)
				if err := settleControllerInputState(st, previous, a.Sequence); err != nil {
					return err
				}
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
				Text: "I recovered this discussion after the previous manager exited. Its native conversation history could not be resumed. I kept the discussion and tracked jobs, and I will not replay uncertain work."}
			p.TextHash = hashBytes([]byte(p.Text))
			putRecord(st, "publications", &st.Publications, key(id, p.ID), p)
		}
		return nil
	})
	return next, err
}
