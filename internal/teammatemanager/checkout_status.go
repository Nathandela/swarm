package teammatemanager

import (
	"path/filepath"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/voice"
)

// CheckoutStatus never opens a discussion controller or wakes a manager.
func (r *Runtime) CheckoutStatus(bound discord.TeammateDiscussion) (discord.TeammateNativeStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	endpoint, _, _, err := r.supervisorForDiscussion(bound)
	if err != nil {
		return discord.TeammateNativeStatus{}, err
	}
	id := r.mailDiscussionID(bound, endpoint)
	var status voice.DiscussionNativeStatus
	evidence := "durable_ledger"
	if entry := r.discussions[id]; entry != nil {
		status, err = entry.voice.CheckoutStatus()
		evidence = "open_ledger"
	} else {
		status, err = voice.InspectDiscussion(filepath.Join(r.dir, id))
	}
	result := CheckoutStatusProjection(status)
	if err == nil {
		result.CheckoutEvidence = evidence
	}
	return result, err
}
