package discord

import (
	"encoding/json"
	bolt "go.etcd.io/bbolt"
	"time"
)

const MaxPublicationIndex = 8192

func aggregatePublication(p *Publication) {
	p.DiscordMessageIDs = nil
	allSent := len(p.Chunks) > 0
	uncertain, sending, failed := false, false, false
	for _, chunk := range p.Chunks {
		if chunk.State == StateSent && chunk.MessageID != "" {
			p.DiscordMessageIDs = append(p.DiscordMessageIDs, chunk.MessageID)
		} else {
			allSent = false
		}
		uncertain = uncertain || chunk.State == StateUncertain
		sending = sending || chunk.State == StateSending
		failed = failed || chunk.State == StateFailed
	}
	switch {
	case allSent:
		p.State = StateSent
	case uncertain:
		p.State = StateUncertain
	case sending:
		p.State = StateSending
	case failed:
		p.State = StateFailed
	default:
		p.State = StatePrepared
	}
}
func publicationIndexKey(channel, message string) []byte {
	raw, _ := json.Marshal([]string{channel, message})
	return raw
}
func indexPublication(tx *bolt.Tx, p Publication, messageID string) error {
	record, e := loadRequest(tx, p.RequestRef)
	if e != nil {
		return e
	}
	bucket := tx.Bucket([]byte("publication_index"))
	key := publicationIndexKey(record.Request.Route.ChannelID, messageID)
	if old := bucket.Get(key); old != nil {
		var ref string
		if e = json.Unmarshal(old, &ref); e != nil {
			return e
		}
		if ref != p.RequestRef {
			return ErrConflict
		}
		return nil
	}
	if bucket.Stats().KeyN >= MaxPublicationIndex {
		return ErrCapacity
	}
	return putJSON(tx, "publication_index", key, p.RequestRef)
}
func mergePublicationChunk(tx *bolt.Tx, p *Publication, index int, state, messageID string) error {
	if index < 0 || index >= len(p.Chunks) {
		return ErrInvalid
	}
	chunk := &p.Chunks[index]
	if state == StateSent {
		if messageID == "" || len(messageID) > 128 {
			return ErrInvalid
		}
		if e := indexPublication(tx, *p, messageID); e != nil {
			return e
		}
		if chunk.MessageID == "" {
			chunk.MessageID = messageID
		}
		chunk.State = StateSent
		return nil
	}
	if chunk.State == StateSent {
		return nil
	}
	if state != StateUncertain && state != StateFailed {
		return ErrInvalid
	}
	chunk.State = state
	return nil
}
func (s *Store) publication(ref, id string) (out Publication, err error) {
	err = s.db.View(func(tx *bolt.Tx) error { var e error; out, e = loadPublication(tx, ref, id); return e })
	return
}
func (s *Store) beginPublicationChunk(ref, id, chunkID string, now time.Time) (out Chunk, fresh bool, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		if _, e := publicationRequest(tx, ref, now); e != nil {
			return e
		}
		p, e := loadPublication(tx, ref, id)
		if e != nil {
			return e
		}
		for i := range p.Chunks {
			if p.Chunks[i].ID != chunkID {
				continue
			}
			out = p.Chunks[i]
			if out.State != StatePrepared {
				return nil
			}
			p.Chunks[i].State = StateSending
			p.State = StateSending
			if e = putJSON(tx, "publications", publicationKey(ref, id), p); e != nil {
				return e
			}
			out = p.Chunks[i]
			fresh = true
			return nil
		}
		return ErrNotFound
	})
	return
}
func (s *Store) finishPublicationChunk(ref, id, chunkID, state, messageID, evidence string) (out Publication, err error) {
	if len(evidence) > 16<<10 {
		return out, ErrInvalid
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		p, e := loadPublication(tx, ref, id)
		if e != nil {
			return e
		}
		if p.State == StateSent && len(p.Chunks) == 0 {
			out = p
			return nil
		}
		for i, chunk := range p.Chunks {
			if chunk.ID != chunkID {
				continue
			}
			if e = mergePublicationChunk(tx, &p, i, state, messageID); e != nil {
				return e
			}
			if state == StateSent {
				p.Chunks[i].RESTAcknowledged = true
			}
			aggregatePublication(&p)
			if state == StateSent || p.State != StateSent {
				p.DiscordEvidence = evidence
			}
			if e = putJSON(tx, "publications", publicationKey(ref, id), p); e != nil {
				return e
			}
			out = p
			return nil
		}
		return ErrNotFound
	})
	return
}
func prunePublicationIndex(tx *bolt.Tx, now time.Time) error {
	bucket := tx.Bucket([]byte("publication_index"))
	var stale [][]byte
	if e := bucket.ForEach(func(k, raw []byte) error {
		var ref string
		if e := json.Unmarshal(raw, &ref); e != nil {
			return e
		}
		record, e := loadRequest(tx, ref)
		if e == ErrNotFound || (e == nil && !now.Before(record.GuardUntil)) {
			stale = append(stale, append([]byte(nil), k...))
			return nil
		}
		return e
	}); e != nil {
		return e
	}
	for _, key := range stale {
		if e := bucket.Delete(key); e != nil {
			return e
		}
	}
	return nil
}
