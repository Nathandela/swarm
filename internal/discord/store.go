package discord

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrConflict = errors.New("discord: identity already bound to different input")
	ErrCapacity = errors.New("discord: durable capacity reached")
	ErrExpired  = errors.New("discord: request context expired")
	ErrEventAge = errors.New("discord: event outside admission age window")
	ErrOptedOut = errors.New("discord: target opted out")
	ErrNotFound = errors.New("discord: reference not found")
	ErrInvalid  = errors.New("discord: invalid request or transition")
)
var bucketNames = []string{"requests", "events", "operations", "publications", "optouts", "meta"}

type Store struct{ db *bolt.DB }
type requestRecord struct {
	Request    Request   `json:"request"`
	Hash       string    `json:"hash"`
	GuardUntil time.Time `json:"guard_until"`
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "discord.db")
	if info, err = os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, ErrInvalid
		}
		if err = os.Chmod(path, 0600); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range bucketNames {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		for _, name := range []string{"requests", "publications"} {
			b := tx.Bucket([]byte(name))
			c := b.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				if name == "requests" {
					var r requestRecord
					if err := json.Unmarshal(v, &r); err != nil {
						return err
					}
					if r.Request.DeliveryState != StateSending {
						continue
					}
					r.Request.DeliveryState = StateUncertain
					if err := putJSON(tx, name, k, r); err != nil {
						return err
					}
				} else {
					var p Publication
					if err := json.Unmarshal(v, &p); err != nil {
						return err
					}
					if p.State != StateSending {
						continue
					}
					p.State = StateUncertain
					if err := putJSON(tx, name, k, p); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// Persist the database name before any external effect can be admitted.
	for _, p := range []string{dir, filepath.Dir(filepath.Clean(dir))} {
		f, e := os.Open(p)
		if e != nil {
			_ = db.Close()
			return nil, e
		}
		e = f.Sync()
		_ = f.Close()
		if e != nil {
			_ = db.Close()
			return nil, e
		}
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func eventKey(r Request) []byte {
	raw, _ := json.Marshal([]string{r.EventID, r.MessageRevision})
	return raw
}
func targetKey(t Target) []byte { raw, _ := json.Marshal([]string{t.ID, t.Instance}); return raw }
func publicationKey(ref, id string) []byte {
	if id == "" {
		id = "final"
	}
	raw, _ := json.Marshal([]string{ref, id})
	return raw
}
func requestHash(r Request) string {
	r.Ref = ""
	r.OperationID = ""
	r.CreatedAt = time.Time{}
	r.ExpiresAt = time.Time{}
	r.DeliveryState = ""
	r.NativeEvidence = ""
	return digest(r)
}
func optedOut(tx *bolt.Tx, t Target) bool {
	return tx.Bucket([]byte("optouts")).Get(targetKey(t)) != nil
}
func loadRequest(tx *bolt.Tx, ref string) (requestRecord, error) {
	var r requestRecord
	raw := tx.Bucket([]byte("requests")).Get([]byte(ref))
	if raw == nil {
		return r, ErrNotFound
	}
	return r, json.Unmarshal(raw, &r)
}
func activeRequest(tx *bolt.Tx, ref string, now time.Time) (requestRecord, error) {
	r, e := loadRequest(tx, ref)
	if e != nil {
		return r, e
	}
	if optedOut(tx, r.Request.Target) {
		return r, ErrOptedOut
	}
	if !now.Before(r.Request.ExpiresAt) {
		return r, ErrExpired
	}
	return r, nil
}
func putJSON(tx *bolt.Tx, bucket string, key []byte, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b := tx.Bucket([]byte(bucket))
	delta := len(raw) - len(b.Get(key))
	if b.Get(key) == nil {
		delta += len(key)
	}
	used := 0
	for _, name := range bucketNames {
		if name == "meta" {
			continue
		}
		if err := tx.Bucket([]byte(name)).ForEach(func(k, v []byte) error { used += len(k) + len(v); return nil }); err != nil {
			return err
		}
	}
	if used+delta > MaxStoreBytes {
		return ErrCapacity
	}
	return b.Put(key, raw)
}
func validateRequest(r Request) error {
	if r.EventID == "" || r.Author.ID == "" || r.Target.ID == "" || r.Target.Instance == "" || r.Route.ChannelID == "" || r.Route.MessageID == "" || !utf8.ValidString(r.Text) {
		return ErrInvalid
	}
	if r.Authority != AuthorityExternal && r.Authority != AuthorityOwnerDirect && r.Authority != AuthorityOwnerEndorsed {
		return ErrInvalid
	}
	if (r.Authority == AuthorityOwnerEndorsed) != (r.EndorserID != "") {
		return ErrInvalid
	}
	for _, v := range []string{r.Ref, r.OperationID, r.EventID, r.MessageRevision, r.Author.ID, r.Author.Name, r.EndorserID, r.Route.GuildID, r.Route.ChannelID, r.Route.MessageID, r.Target.ID, r.Target.Instance, r.Target.Name, r.Target.Agent} {
		if len(v) > 512 || !utf8.ValidString(v) {
			return ErrInvalid
		}
	}
	for _, m := range r.Context {
		if len(m.ID) > 512 || len(m.Author.ID) > 512 || len(m.Author.Name) > 512 || !utf8.ValidString(m.Text) {
			return ErrInvalid
		}
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	if len(raw) > MaxRequestBytes {
		return ErrCapacity
	}
	return nil
}

// Admit binds immutable source input to a native operation before dispatch.
func (s *Store) Admit(input Request, now time.Time) (out Request, fresh bool, err error) {
	if err = validateRequest(input); err != nil {
		return
	}
	hash := requestHash(input)
	err = s.db.Update(func(tx *bolt.Tx) error {
		key := eventKey(input)
		events := tx.Bucket([]byte("events"))
		ops := tx.Bucket([]byte("operations"))
		ref := events.Get(key)
		if ref == nil && input.OperationID != "" {
			ref = ops.Get([]byte(input.OperationID))
		}
		if ref != nil {
			var storedRef string
			if e := json.Unmarshal(ref, &storedRef); e != nil {
				return e
			}
			old, e := loadRequest(tx, storedRef)
			if e != nil {
				return e
			}
			if old.Hash != hash || (input.OperationID != "" && input.OperationID != old.Request.OperationID) || (input.Ref != "" && input.Ref != old.Request.Ref) {
				return ErrConflict
			}
			out = old.Request
			return nil
		}
		if input.EventAt.IsZero() || now.Sub(input.EventAt) > EventMaxAge || input.EventAt.Sub(now) > EventFutureSkew {
			return ErrEventAge
		}
		if optedOut(tx, input.Target) {
			return ErrOptedOut
		}
		if tx.Bucket([]byte("requests")).Stats().KeyN >= MaxRequests {
			return ErrCapacity
		}
		r := input
		r.Context = append([]ContextMessage(nil), input.Context...)
		r.CreatedAt = now
		r.ExpiresAt = now.Add(RequestTTL)
		r.DeliveryState = StatePrepared
		r.NativeEvidence = ""
		if r.Ref == "" {
			r.Ref = "discord:" + digest([]string{r.EventID, r.MessageRevision})[:32]
		}
		if r.OperationID == "" {
			r.OperationID = fmt.Sprintf("semantic:%d:%s", now.UnixMilli(), hash[:32])
		}
		if tx.Bucket([]byte("requests")).Get([]byte(r.Ref)) != nil || ops.Get([]byte(r.OperationID)) != nil {
			return ErrConflict
		}
		rec := requestRecord{Request: r, Hash: hash, GuardUntil: now.Add(GuardTTL)}
		if e := putJSON(tx, "requests", []byte(r.Ref), rec); e != nil {
			return e
		}
		if e := putJSON(tx, "events", key, r.Ref); e != nil {
			return e
		}
		if e := putJSON(tx, "operations", []byte(r.OperationID), r.Ref); e != nil {
			return e
		}
		out = r
		fresh = true
		return nil
	})
	return
}
func (s *Store) Request(ref string, now time.Time) (out Request, err error) {
	err = s.db.View(func(tx *bolt.Tx) error { r, e := activeRequest(tx, ref, now); out = r.Request; return e })
	return
}
func (s *Store) IsOptedOut(target Target) (out bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error { out = optedOut(tx, target); return nil })
	return
}
func (s *Store) SetOptOut(target Target, optOut bool) error {
	if target.ID == "" || target.Instance == "" {
		return ErrInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("optouts"))
		if optOut {
			if b.Get(targetKey(target)) == nil && b.Stats().KeyN >= MaxRequests {
				return ErrCapacity
			}
			return putJSON(tx, "optouts", targetKey(target), true)
		}
		return b.Delete(targetKey(target))
	})
}
func (s *Store) BeginDelivery(ref string, now time.Time) (out Request, fresh bool, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		r, e := activeRequest(tx, ref, now)
		if e != nil {
			return e
		}
		out = r.Request
		if out.DeliveryState != StatePrepared {
			return nil
		}
		r.Request.DeliveryState = StateSending
		if e = putJSON(tx, "requests", []byte(ref), r); e != nil {
			return e
		}
		out = r.Request
		fresh = true
		return nil
	})
	return
}
func terminal(state string) bool { return state == StateSent || state == StateFailed }
func (s *Store) FinishDelivery(ref, state, evidence string) error {
	if !terminal(state) && state != StateUncertain || len(evidence) > 16<<10 {
		return ErrInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		r, e := loadRequest(tx, ref)
		if e != nil {
			return e
		}
		if terminal(r.Request.DeliveryState) {
			if r.Request.DeliveryState == state && r.Request.NativeEvidence == evidence {
				return nil
			}
			return ErrConflict
		}
		if r.Request.DeliveryState != StateSending && r.Request.DeliveryState != StateUncertain {
			return ErrInvalid
		}
		r.Request.DeliveryState = state
		r.Request.NativeEvidence = evidence
		return putJSON(tx, "requests", []byte(ref), r)
	})
}

func loadPublication(tx *bolt.Tx, ref, id string) (p Publication, err error) {
	raw := tx.Bucket([]byte("publications")).Get(publicationKey(ref, id))
	if raw == nil {
		return p, ErrNotFound
	}
	err = json.Unmarshal(raw, &p)
	return
}
func (s *Store) PreparePublication(ref, id, body string, now time.Time) (out Publication, fresh bool, err error) {
	if id == "" {
		id = "final"
	}
	if len(id) > 128 || body == "" || !utf8.ValidString(body) {
		return out, false, ErrInvalid
	}
	if len(body) > MaxPublicationBytes {
		return out, false, ErrCapacity
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		if _, e := activeRequest(tx, ref, now); e != nil {
			return e
		}
		old, e := loadPublication(tx, ref, id)
		if e == nil {
			if old.BodyHash != digest(body) {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if tx.Bucket([]byte("publications")).Stats().KeyN >= MaxPublications {
			return ErrCapacity
		}
		p := Publication{RequestRef: ref, ID: id, Body: body, BodyHash: digest(body), Version: 1, State: StatePrepared, CreatedAt: now}
		for _, part := range splitUTF16(body, 1900) {
			p.Chunks = append(p.Chunks, Chunk{ID: digest([]string{ref, id, p.BodyHash, fmt.Sprint(len(p.Chunks)), "1"})[:24], Body: part})
		}
		if e := putJSON(tx, "publications", publicationKey(ref, id), p); e != nil {
			return e
		}
		out = p
		fresh = true
		return nil
	})
	return
}
func splitUTF16(body string, limit int) []string {
	var chunks []string
	start, units := 0, 0
	for i, r := range body {
		size := utf16.RuneLen(r)
		if units+size > limit {
			chunks = append(chunks, body[start:i])
			start = i
			units = 0
		}
		units += size
	}
	if start < len(body) {
		chunks = append(chunks, body[start:])
	}
	return chunks
}
func (s *Store) BeginPublication(ref, id string, now time.Time) (out Publication, fresh bool, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		if _, e := activeRequest(tx, ref, now); e != nil {
			return e
		}
		p, e := loadPublication(tx, ref, id)
		if e != nil {
			return e
		}
		out = p
		if p.State != StatePrepared {
			return nil
		}
		p.State = StateSending
		if e = putJSON(tx, "publications", publicationKey(ref, id), p); e != nil {
			return e
		}
		out = p
		fresh = true
		return nil
	})
	return
}
func (s *Store) FinishPublication(ref, id, state string, messageIDs []string, evidence string) error {
	if (!terminal(state) && state != StateUncertain) || len(evidence) > 16<<10 {
		return ErrInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		p, e := loadPublication(tx, ref, id)
		if e != nil {
			return e
		}
		if terminal(p.State) {
			if p.State == state && digest(p.DiscordMessageIDs) == digest(messageIDs) && p.DiscordEvidence == evidence {
				return nil
			}
			return ErrConflict
		}
		if p.State != StateSending && p.State != StateUncertain {
			return ErrInvalid
		}
		if len(messageIDs) > len(p.Chunks) {
			return ErrInvalid
		}
		for _, v := range messageIDs {
			if len(v) > 128 || v == "" {
				return ErrInvalid
			}
		}
		p.State = state
		p.DiscordMessageIDs = messageIDs
		p.DiscordEvidence = evidence
		return putJSON(tx, "publications", publicationKey(ref, id), p)
	})
}

func (s *Store) ContextPage(ref string, offset, limit int, now time.Time) (page ContextPage, err error) {
	if offset < 0 || limit < 1 || limit > MaxContextPageSize {
		return page, ErrInvalid
	}
	r, err := s.Request(ref, now)
	if err != nil {
		return page, err
	}
	page.Ref = ref
	page.NextOffset = -1
	// A large source message is returned as consecutive fragments with its identity preserved.
	var fragments []ContextMessage
	for _, m := range r.Context {
		parts := splitUTF16(m.Text, 2000)
		if len(parts) == 0 {
			parts = []string{""}
		}
		for _, part := range parts {
			fragment := m
			fragment.Text = part
			fragments = append(fragments, fragment)
		}
	}
	if offset > len(fragments) {
		return page, ErrInvalid
	}
	size := 0
	for i := offset; i < len(fragments); i++ {
		raw, e := json.Marshal(fragments[i])
		if e != nil {
			return page, e
		}
		if len(page.Messages) >= limit || size+len(raw) > MaxContextPageBytes {
			page.NextOffset = i
			break
		}
		size += len(raw)
		page.Messages = append(page.Messages, fragments[i])
	}
	return page, nil
}

// Prune removes settled source bodies, retaining guards beyond the incoming event window.
func (s *Store) Prune(now time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		requests := tx.Bucket([]byte("requests"))
		var expired []requestRecord
		if e := requests.ForEach(func(_, v []byte) error {
			var r requestRecord
			if e := json.Unmarshal(v, &r); e != nil {
				return e
			}
			if terminal(r.Request.DeliveryState) && !now.Before(r.Request.ExpiresAt) {
				expired = append(expired, r)
			}
			return nil
		}); e != nil {
			return e
		}
		for _, r := range expired {
			unresolved := false
			pubs := tx.Bucket([]byte("publications"))
			var deleteKeys [][]byte
			if e := pubs.ForEach(func(k, v []byte) error {
				var p Publication
				if e := json.Unmarshal(v, &p); e != nil {
					return e
				}
				if p.RequestRef == r.Request.Ref {
					if !terminal(p.State) {
						unresolved = true
					} else if !now.Before(r.GuardUntil) {
						deleteKeys = append(deleteKeys, append([]byte(nil), k...))
					} else {
						p.Body = ""
						p.Chunks = nil
						if e := putJSON(tx, "publications", k, p); e != nil {
							return e
						}
					}
				}
				return nil
			}); e != nil {
				return e
			}
			if unresolved {
				continue
			}
			if now.Before(r.GuardUntil) {
				r.Request.Text = ""
				r.Request.Context = nil
				if e := putJSON(tx, "requests", []byte(r.Request.Ref), r); e != nil {
					return e
				}
				continue
			}
			for _, k := range deleteKeys {
				if e := pubs.Delete(k); e != nil {
					return e
				}
			}
			if e := tx.Bucket([]byte("events")).Delete(eventKey(r.Request)); e != nil {
				return e
			}
			if e := tx.Bucket([]byte("operations")).Delete([]byte(r.Request.OperationID)); e != nil {
				return e
			}
			if e := requests.Delete([]byte(r.Request.Ref)); e != nil {
				return e
			}
		}
		return nil
	})
}

// FollowTarget carries an explicit resume's opt-out to its replacement identity.
func (s *Store) FollowTarget(previous, next Target) error {
	if strings.TrimSpace(next.ID) == "" || next.Instance == "" {
		return ErrInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if !optedOut(tx, previous) {
			return nil
		}
		return putJSON(tx, "optouts", targetKey(next), true)
	})
}
