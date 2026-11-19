package teammatemanager

import "github.com/Nathandela/swarm/internal/discord"

// Dormant checks whether the discussion can leave the connector's active set.
func (r *Runtime) Dormant(bound discord.TeammateDiscussion) (bool, error) {
	entry, err := r.discussion(bound, nil, true)
	if err != nil {
		return false, err
	}
	if entry == nil {
		return true, nil
	}
	defer r.release(entry)
	return entry.voice.Dormant()
}
