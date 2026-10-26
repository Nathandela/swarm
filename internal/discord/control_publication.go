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
	publication, _, err := s.Store.preparePublication(ref, "control:"+id, string(canonical), s.Now(), &out)
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
	chunk, claimed, err := s.Store.beginPublicationChunk(ref, publication.ID, publication.Chunks[0].ID, s.Now())
	if err != nil {
		return publication, err
	}
	if !claimed {
		if chunk.State == StateSent {
			return s.Store.publication(ref, publication.ID)
		}
		return publication, ErrDeliveryUncertain
	}
	out.Nonce = chunk.ID
	messageID, postErr := s.Messenger.Post(ctx, out)
	state, evidence := StateSent, "connector control receipt"
	if postErr != nil {
		state = StateUncertain
		evidence = "REST control outcome unconfirmed"
	}
	publication, err = s.Store.finishPublicationChunk(ref, publication.ID, chunk.ID, state, messageID, evidence)
	if err != nil {
		return publication, err
	}
	if publication.State == StateSent {
		return publication, nil
	}
	return publication, postErr
}
