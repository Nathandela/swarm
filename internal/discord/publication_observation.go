package discord

import (
	"context"
	"encoding/json"
	bolt "go.etcd.io/bbolt"
)

type PublicationMessage struct{ AuthorID, ChannelID, MessageID, Nonce, Text, WebhookID string }

func (s *Service) SetBotID(id string) {
	if id == "" {
		return
	}
	s.publicationMu.Lock()
	s.botID = id
	s.publicationMu.Unlock()
}
func (s *Service) ObservePublication(ctx context.Context, message PublicationMessage) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	s.publicationMu.RLock()
	botID := s.botID
	s.publicationMu.RUnlock()
	if botID == "" {
		botID = s.Config.ApplicationID
	}
	if message.AuthorID != botID || message.WebhookID != "" {
		return ErrNotAuthorized
	}
	if message.Nonce == "" || message.MessageID == "" {
		return nil
	}
	return s.Store.db.Update(func(tx *bolt.Tx) error {
		var matched *Publication
		var index int
		if e := tx.Bucket([]byte("publications")).ForEach(func(_, raw []byte) error {
			var p Publication
			if e := json.Unmarshal(raw, &p); e != nil {
				return e
			}
			if p.State == StateFailed {
				return nil
			}
			for i, chunk := range p.Chunks {
				if chunk.ID != message.Nonce || chunk.Body != message.Text || chunk.State == StateFailed {
					continue
				}
				source, e := loadRequest(tx, p.RequestRef)
				if e != nil {
					return e
				}
				if source.Request.Route.ChannelID == message.ChannelID {
					copy := p
					matched = &copy
					index = i
				}
			}
			return nil
		}); e != nil {
			return e
		}
		if matched == nil {
			return nil
		}
		if e := mergePublicationChunk(tx, matched, index, StateSent, message.MessageID); e != nil {
			return e
		}
		matched.Chunks[index].GatewaySeen = true
		matched.DiscordEvidence = "authenticated own-bot Gateway message"
		aggregatePublication(matched)
		return putJSON(tx, "publications", publicationKey(matched.RequestRef, matched.ID), matched)
	})
}
func (s *Service) TargetForReply(ctx context.Context, channel, messageID string) (Target, error) {
	var ref string
	e := s.Store.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte("publication_index")).Get(publicationIndexKey(channel, messageID))
		if raw == nil {
			return ErrNotFound
		}
		return json.Unmarshal(raw, &ref)
	})
	if e != nil {
		return Target{}, e
	}
	source, e := s.Store.Request(ref, s.Now())
	if e != nil {
		return Target{}, e
	}
	if source.Route.ChannelID != channel {
		return Target{}, ErrNotFound
	}
	if source.Target.ID == "routing" || source.Target.ID == "control" {
		return Target{}, ErrSelection
	}
	row, e := s.target(ctx, source.Target.ID)
	if e != nil {
		return Target{}, e
	}
	return row.Target, nil
}
