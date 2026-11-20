package openailive

import (
	"bytes"
	"context"
	"io"
	"testing"
)

type renderFixture struct {
	events []Event
}

func (s *renderFixture) SendAudio(context.Context, []byte) error { return nil }

func (s *renderFixture) Greet(context.Context, string) (string, error) { return "ack", nil }

func (s *renderFixture) Next(context.Context) (Event, error) {
	if len(s.events) == 0 {
		return Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}

func (s *renderFixture) Close() error { return nil }

func TestVerifiedIsolatedReadout(t *testing.T) {
	pcm := []byte{0xFE, 0x13, 0x20, 0x04}
	ack := Event{Type: "session.commentary.appended", ClientEventID: "ack"}
	audio := Event{Type: "session.output_audio.delta", Audio: pcm}
	closed := Event{Type: "session.closed"}
	transcript := Event{Type: "session.output_transcript.delta", Text: "Your build check passed."}
	for _, tc := range []struct {
		name   string
		events []Event
		ok     bool
	}{
		{"acknowledged preview paraphrase", []Event{ack, audio, transcript, closed}, true},
		{"early audio", []Event{audio, ack, transcript, closed}, false},
		{"incomplete output", []Event{ack, audio, transcript}, false},
		{"wrong acknowledgement", []Event{{Type: "session.commentary.appended", ClientEventID: "wrong"}, audio, transcript, closed}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := renderPCM(t.Context(), &renderFixture{events: tc.events}, "quote")
			if tc.ok && (err != nil || !bytes.Equal(out, pcm)) {
				t.Fatalf("verified render PCM %v, error %v", out, err)
			}
			if !tc.ok && (err == nil || len(out) != 0) {
				t.Fatalf("unconfirmed audio was returned, %v", err)
			}
		})
	}
}
