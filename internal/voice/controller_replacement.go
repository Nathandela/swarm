package voice

import (
	"context"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// ReplaceManager adopts an explicitly launched fresh manager after exact old-session exit proof.
// It never launches, kills, replays or declares old native/worker tasks successful.
func (c *Controller) ReplaceManager(ctx context.Context, next ManagerBinding, refs ManagerBridgeRefs) (*Controller, error) {
	c.scheduler.Lock()
	defer c.scheduler.Unlock()
	if !validBinding(next) || next.SessionID == c.binding.SessionID || next.ConversationID != c.binding.ConversationID || next.Provider != c.binding.Provider || refs.Socket == "" || refs.AuthFile == "" {
		return nil, ErrInvalid
	}
	oldEndpoint, _, oldValid := protocol.ParseID(c.binding.SessionID)
	nextEndpoint, _, nextValid := protocol.ParseID(next.SessionID)
	if !oldValid || !nextValid || oldEndpoint != nextEndpoint {
		return nil, ErrStale
	}
	roster, ok := c.runtime.(interface {
		List() ([]protocol.SessionView, error)
	})
	if c.workers != nil {
		roster, ok = c.workers, true
	}
	if !ok {
		return nil, ErrControllerUnsupported
	}
	st, err := c.store.Snapshot()
	if err != nil {
		return nil, err
	}
	if err = checkBinding(&st, c.binding); err != nil {
		return nil, err
	}
	if !st.Conversations[c.binding.ConversationID].CustodyLost {
		return nil, ErrInvalid
	}
	rows, err := roster.List()
	if err != nil {
		return nil, err
	}
	oldEnded, newFound := false, false
	for _, row := range rows {
		if row.ID == c.binding.SessionID {
			if row.Role != "manager" || row.ManagerConversationID != c.binding.ConversationID || row.Agent != c.binding.Provider {
				return nil, ErrStale
			}
			if row.Capabilities == nil {
				return nil, ErrControllerNativeUnavailable
			}
			if row.Capabilities.SessionInstance != c.binding.SessionInstance {
				return nil, ErrStale
			}
			if row.Status.Process != status.ProcessExited {
				return nil, ErrControllerUncertain
			}
			oldEnded = true
		}
		if row.ID == next.SessionID {
			if row.Role != "manager" || row.ManagerConversationID != next.ConversationID || row.Agent != next.Provider || row.Status.Process != status.ProcessRunning {
				return nil, ErrStale
			}
			if row.Capabilities == nil {
				return nil, ErrControllerNativeUnavailable
			}
			if row.Capabilities.SessionInstance != next.SessionInstance {
				return nil, ErrStale
			}
			newFound = true
		} else if row.ID != c.binding.SessionID && row.Role == "manager" && row.ManagerConversationID == next.ConversationID && row.Status.Process != status.ProcessExited {
			return nil, ErrConflict
		}
	}
	if !oldEnded || !newFound {
		return nil, ErrNotFound
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	runtime, err := c.runtime.ManagerRuntime(next.SessionID, next.SessionInstance)
	if err != nil {
		return nil, err
	}
	if runtime.Binding.Validate() != nil || runtime.Binding != runtimeBinding(next) || runtime.BridgeSocket != refs.Socket || runtime.AuthFile != refs.AuthFile || !runtime.ReadyForInput {
		return nil, ErrStale
	}
	next, err = c.store.replaceControllerManager(c.binding, next, runtime.Turn.ExternalInput)
	if err != nil {
		return nil, err
	}
	replacement, err := NewController(c.store, c.runtime, next, refs)
	if err != nil {
		return nil, err
	}
	replacement.workers = c.workers
	return replacement, nil
}
