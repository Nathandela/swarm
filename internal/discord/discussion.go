package discord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
)

type Discussion struct {
	Route    Route     `json:"route"`
	Targets  []Target  `json:"targets"`
	JoinedAt time.Time `json:"joined_at"`
}

func discussionKey(channel string) []byte { return []byte("discussion:" + channel) }
func (s *Service) Discussion(channel string) (Discussion, error) {
	var d Discussion
	err := s.Store.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("discussions"))
		if b == nil {
			return ErrNotFound
		}
		raw := b.Get(discussionKey(channel))
		if raw == nil {
			return ErrNotFound
		}
		return json.Unmarshal(raw, &d)
	})
	return d, err
}
func (s *Service) Join(route Route, target Target) error {
	if route.ChannelID == "" || target.ID == "" || target.Instance == "" {
		return ErrInvalid
	}
	err := s.Store.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("discussions"))
		if err != nil {
			return err
		}
		key := discussionKey(route.ChannelID)
		d := Discussion{Route: route, JoinedAt: s.Now()}
		if raw := b.Get(key); raw != nil {
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
		} else {
			count := 0
			if err := b.ForEach(func(k, _ []byte) error {
				if strings.HasPrefix(string(k), "discussion:") {
					count++
				}
				return nil
			}); err != nil {
				return err
			}
			if count >= 256 {
				return ErrCapacity
			}
		}
		for _, t := range d.Targets {
			if t.ID == target.ID {
				return nil
			}
		}
		if len(d.Targets) >= 25 {
			return ErrCapacity
		}
		d.Targets = append(d.Targets, target)
		raw, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if len(raw) > 16<<10 {
			return ErrCapacity
		}
		return b.Put(key, raw)
	})
	if err == nil {
		s.invalidateContext(route.ChannelID)
	}
	return err
}
func (s *Service) Leave(channel string) error {
	if err := s.Store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("discussions"))
		if b == nil {
			return nil
		}
		return b.Delete(discussionKey(channel))
	}); err != nil {
		return err
	}
	s.discussionMu.Lock()
	delete(s.recent, channel)
	delete(s.discussionEpoch, channel)
	s.discussionMu.Unlock()
	return nil
}
func (s *Service) observe(in Input) {
	if _, err := s.Discussion(in.Route.ChannelID); err != nil {
		return
	}
	s.discussionMu.Lock()
	defer s.discussionMu.Unlock()
	if in.CacheEpoch != nil && *in.CacheEpoch != s.discussionEpoch[in.Route.ChannelID] {
		return
	}
	if s.recent == nil {
		s.recent = make(map[string][]ContextMessage)
	}
	rows := s.recent[in.Route.ChannelID]
	message := ContextMessage{ID: in.Route.MessageID, Author: in.Actor, Text: in.Text, CreatedAt: in.At}
	if in.Bot {
		message.Author.Name += " (bot)"
	}
	updated := false
	for i := range rows {
		if rows[i].ID == message.ID {
			rows[i] = message
			updated = true
			break
		}
	}
	if !updated {
		rows = append(rows, message)
	}
	s.recent[in.Route.ChannelID] = boundedContext(rows)
}
func boundedContext(rows []ContextMessage) []ContextMessage {
	size := 0
	for _, row := range rows {
		size += len(row.Text) + len(row.Author.Name) + len(row.ID)
	}
	for len(rows) > 50 || size > 64<<10 {
		size -= len(rows[0].Text) + len(rows[0].Author.Name) + len(rows[0].ID)
		rows = rows[1:]
	}
	return rows
}
func (s *Service) ForgetMessage(channel, id string) {
	s.invalidateContext(channel)
	s.discussionMu.Lock()
	defer s.discussionMu.Unlock()
	rows := s.recent[channel]
	for i, row := range rows {
		if row.ID == id {
			s.recent[channel] = append(rows[:i], rows[i+1:]...)
			return
		}
	}
}

func (b *Bridge) messageUpdate(ctx context.Context, msg *discordgo.MessageUpdate, epoch uint64) {
	if msg == nil || msg.Message == nil {
		return
	}
	discussion, err := b.Service.Discussion(msg.ChannelID)
	if err != nil {
		return
	}
	if _, err = b.resolveRoute(ctx, discussion.Route); err != nil {
		return
	}
	s := b.Service
	s.discussionMu.Lock()
	defer s.discussionMu.Unlock()
	if epoch != s.discussionEpoch[msg.ChannelID] {
		return
	}
	for i, row := range s.recent[msg.ChannelID] {
		if row.ID != msg.ID {
			continue
		}
		s.recent[msg.ChannelID][i].Text = msg.Content
		s.recent[msg.ChannelID] = boundedContext(s.recent[msg.ChannelID])
		return
	}
}

func (s *Service) contextEpoch(channel string) uint64 {
	s.discussionMu.Lock()
	defer s.discussionMu.Unlock()
	return s.discussionEpoch[channel]
}
func (s *Service) invalidateContext(channel string) uint64 {
	if _, err := s.Discussion(channel); err != nil {
		return 0
	}
	s.discussionMu.Lock()
	defer s.discussionMu.Unlock()
	if s.discussionEpoch == nil {
		s.discussionEpoch = make(map[string]uint64)
	}
	s.discussionClock++
	s.discussionEpoch[channel] = s.discussionClock
	return s.discussionClock
}

func (b *Bridge) MessageDelete(msg *discordgo.MessageDelete) {
	if msg != nil && msg.Message != nil {
		b.Service.ForgetMessage(msg.ChannelID, msg.ID)
	}
}
func (s *Service) threadContext(channel string) []ContextMessage {
	s.discussionMu.Lock()
	defer s.discussionMu.Unlock()
	return append([]ContextMessage(nil), s.recent[channel]...)
}
func (s *Service) boundTarget(channel string) (string, error) {
	d, err := s.Discussion(channel)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(d.Targets) == 1 {
		return d.Targets[0].ID, nil
	}
	return "", ErrSelection
}
