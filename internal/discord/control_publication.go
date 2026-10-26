package discord

import (
	"context"
	"encoding/json"
	"strings"
)

// Control claims public connector acknowledgements separately from deliberate worker replies.
func (s *Service) Control(ctx context.Context, ref, id string, out Outbound) (Publication, error) {
	if id == "" || len(id) > 120 || strings.HasPrefix(id, "control:") {
		return Publication{}, ErrInvalid
	}
	source, err := s.Store.Request(ref, s.Now())
	if err != nil {
		return Publication{}, err
	}
	if out.Route != source.Route {
		return Publication{}, ErrInvalid
	}
	out.Nonce = ""
	canonical, err := json.Marshal(out)
	if err != nil {
		return Publication{}, err
	}
	publication, _, err := s.Store.PreparePublication(ref, "control:"+id, string(canonical), s.Now())
	if err != nil {
		return Publication{}, err
	}
	publication, fresh, err := s.Store.BeginPublication(ref, publication.ID, s.Now())
	if err != nil {
		return publication, err
	}
	if !fresh {
		if publication.State == StateSent {
			return publication, nil
		}
		return publication, ErrDeliveryUncertain
	}
	out.Nonce = publication.Chunks[0].ID
	messageID, postErr := s.Messenger.Post(ctx, out)
	state, evidence := StateSent, "connector control receipt"
	var ids []string
	if postErr != nil {
		state = StateUncertain
		evidence = postErr.Error()
	} else {
		ids = []string{messageID}
	}
	if err = s.Store.FinishPublication(ref, publication.ID, state, ids, evidence); err != nil {
		return publication, err
	}
	publication.State = state
	publication.DiscordMessageIDs = ids
	publication.DiscordEvidence = evidence
	return publication, postErr
}
