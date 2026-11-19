package voice

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nathandela/swarm/internal/managerproof"
)

const discussionCustodyDiagnostic = "Inspect the exact manager in the local Swarm dashboard and have it exit there. The connector can recover the discussion once the daemon confirms its exit."

// ResumeManual releases scheduling only for the exact active manager and observed input counter.
func (d *DiscussionController) ResumeManual(ctx context.Context, session, instance string, externalInput uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	controller := d.controller
	if controller == nil || session != controller.binding.SessionID || instance != controller.binding.SessionInstance {
		return ErrStale
	}
	st, err := d.store.readView()
	if err != nil {
		return err
	}
	conversation := st.Conversations[controller.binding.ConversationID]
	if conversation.Parked || conversation.Retirement != nil {
		return ErrStale
	}
	err = controller.ResumeManual(ctx, externalInput)
	if errors.Is(err, managerproof.ErrQuarantined) {
		return fmt.Errorf("%w for %s. %s", err, session, discussionCustodyDiagnostic)
	}
	return err
}
