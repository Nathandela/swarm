package voice

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
)

type VoiceCursor struct {
	StreamID string `json:"stream_id"`
	Sequence uint64 `json:"sequence"`
}

type StreamEvent struct {
	Cursor  VoiceCursor     `json:"cursor"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// VoiceStream is ephemeral device replay. Its cursor is never a daemon checkpoint.
// A gap requires the caller to send a fresh durable-state snapshot.
type VoiceStream struct {
	mu     sync.Mutex
	cursor VoiceCursor
	events []streamEntry
	bytes  int
}

type streamEntry struct {
	event StreamEvent
	bytes int
}

func NewVoiceStream() (*VoiceStream, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &VoiceStream{cursor: VoiceCursor{StreamID: hex.EncodeToString(id[:])}}, nil
}
func (s *VoiceStream) Append(kind string, payload json.RawMessage) (StreamEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "" || len(kind) > 128 {
		return StreamEvent{}, ErrInvalid
	}
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return StreamEvent{}, err
	}
	if s.cursor.Sequence == ^uint64(0) {
		return StreamEvent{}, ErrCapacity
	}
	next := s.cursor
	next.Sequence++
	event := StreamEvent{Cursor: next, Kind: kind, Payload: canonical}
	encoded, err := json.Marshal(event)
	if err != nil || len(encoded) > MaxControlFrameBytes {
		return StreamEvent{}, ErrInvalid
	}
	s.cursor = next
	s.events = append(s.events, streamEntry{event: event, bytes: len(encoded)})
	s.bytes += len(encoded)
	for len(s.events) > MaxStreamEvents || s.bytes > MaxStreamBytes {
		s.bytes -= s.events[0].bytes
		s.events[0] = streamEntry{}
		s.events = s.events[1:]
	}
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return event, nil
}
func (s *VoiceStream) Replay(after VoiceCursor) ([]StreamEvent, bool, VoiceCursor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if after.StreamID != s.cursor.StreamID || after.Sequence > s.cursor.Sequence {
		return nil, true, s.cursor
	}
	if len(s.events) > 0 && after.Sequence < s.events[0].event.Cursor.Sequence-1 {
		return nil, true, s.cursor
	}
	var events []StreamEvent
	for _, entry := range s.events {
		event := entry.event
		if event.Cursor.Sequence > after.Sequence {
			event.Payload = append(json.RawMessage(nil), event.Payload...)
			events = append(events, event)
		}
	}
	return events, false, s.cursor
}
