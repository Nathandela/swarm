// Package openailive streams mono PCM16LE at 24 kHz through a GPT-Live session.
package openailive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/jsontext"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	endpoint             = "wss://api.openai.com/v1/live/sessions"
	model                = "gpt-live-1"
	defaultVoice         = "marin"
	instructions         = "You are Swarm's voice interface. Briefly relay the native manager's commentary without embellishment. You cannot work with agents or operate workers here. Say so if asked. Never claim to perform or complete work yourself."
	sampleRate           = 24000
	maxSendAudioBytes    = sampleRate * 2
	maxReceiveAudioBytes = 64 << 10
	maxEventBytes        = 1 << 20
	openTimeout          = 15 * time.Second
	writeTimeout         = 5 * time.Second
)

// MaxCommentaryBytes conservatively bounds commentary to the API's 500-token limit.
const MaxCommentaryBytes = 500

type sessionConfig struct {
	Model        string `json:"model"`
	Instructions string `json:"instructions,omitempty"`
	Audio        struct {
		Format struct {
			Type string `json:"type"`
			Rate int    `json:"rate"`
		} `json:"format"`
		Output struct {
			Voice string `json:"voice"`
		} `json:"output"`
	} `json:"audio"`
	Delegation struct {
		Type string `json:"type"`
	} `json:"delegation"`
}

type serverEvent struct {
	Type    string `json:"type"`
	Delta   string `json:"delta"`
	Reason  string `json:"reason"`
	Session struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		sessionConfig
	} `json:"session"`
}

// Event carries an output PCM chunk, a transcript fragment, or session lifecycle metadata.
type Event struct {
	Type  string
	Text  string
	Audio []byte
}

// Session supports one Next reader and concurrent writes. A canceled read or
// write may terminate the underlying WebSocket.
type Session struct {
	conn      *websocket.Conn
	writeGate chan struct{}
	finishing bool // guarded by writeGate
	finalized bool // only accessed by Next
}

// Open connects to the primary Live endpoint and validates session.started
// before returning. Its context bounds startup only.
func Open(ctx context.Context, key, voice string) (*Session, error) {
	return open(ctx, endpoint, key, voice)
}

func open(ctx context.Context, url, key, voice string) (*Session, error) {
	if key == "" || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return nil, errors.New("OpenAI Live requires a valid API key")
	}
	if voice == "" {
		voice = defaultVoice
	}
	if !utf8.ValidString(voice) || len(voice) > 128 || strings.IndexFunc(voice, unicode.IsSpace) >= 0 || strings.IndexFunc(voice, unicode.IsControl) >= 0 {
		return nil, errors.New("OpenAI Live requires a valid voice name")
	}
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + key}},
	})
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case response != nil:
			return nil, fmt.Errorf("OpenAI Live WebSocket upgrade failed (HTTP %d)", response.StatusCode)
		default:
			return nil, errors.New("OpenAI Live WebSocket connection failed")
		}
	}
	session := &Session{conn: conn, writeGate: make(chan struct{}, 1)}
	conn.SetReadLimit(maxEventBytes)
	config := sessionConfig{Model: model, Instructions: instructions}
	config.Audio.Format.Type = "audio/pcm"
	config.Audio.Format.Rate = sampleRate
	config.Audio.Output.Voice = voice
	config.Delegation.Type = "client"
	if err := session.write(ctx, map[string]any{"type": "session.start", "session": config}, false); err != nil {
		_ = session.Close()
		return nil, err
	}
	event, err := session.read(ctx)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if event.Type != "session.started" || event.Session.ID == "" || event.Session.Status != "active" ||
		event.Session.Model != config.Model || event.Session.Audio != config.Audio ||
		event.Session.Delegation != config.Delegation {
		_ = session.Close()
		return nil, errors.New("OpenAI Live returned an invalid startup configuration")
	}
	return session, nil
}

// SendAudio sends up to one second of PCM16LE audio with complete samples.
func (s *Session) SendAudio(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > maxSendAudioBytes {
		return errors.New("OpenAI Live requires nonempty, sample-aligned PCM chunks of at most one second")
	}
	return s.write(ctx, map[string]any{"type": "session.input_audio.append", "audio": pcm}, false)
}

// Speak supplies manager-authored commentary as speech context. Live controls
// whether and when to speak it, and can paraphrase.
func (s *Session) Speak(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" || len(text) > MaxCommentaryBytes || !utf8.ValidString(text) {
		return errors.New("OpenAI Live requires valid UTF-8 commentary of 1–500 bytes")
	}
	return s.write(ctx, map[string]any{
		"type": "session.commentary.append", "content": text, "delegation_id": nil,
	}, false)
}

// Finish requests finalization. Read through session.closed to confirm it.
func (s *Session) Finish(ctx context.Context) error {
	return s.write(ctx, map[string]any{"type": "session.close"}, true)
}

func (s *Session) write(ctx context.Context, event map[string]any, finish bool) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	select {
	case s.writeGate <- struct{}{}:
		defer func() { <-s.writeGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.finishing {
		if finish {
			return nil
		}
		return errors.New("OpenAI Live session is finishing")
	}
	if err := wsjson.Write(ctx, s.conn, event); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("OpenAI Live WebSocket write failed")
	}
	s.finishing = finish
	return nil
}

func (s *Session) read(ctx context.Context) (serverEvent, error) {
	kind, data, err := s.conn.Read(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return serverEvent{}, ctx.Err()
		}
		return serverEvent{}, errors.New("OpenAI Live WebSocket read failed before session.closed")
	}
	var event serverEvent
	if kind != websocket.MessageText || jsontext.ValidateUnicode(data) != nil || json.Unmarshal(data, &event) != nil || event.Type == "" {
		return serverEvent{}, errors.New("OpenAI Live sent an invalid event")
	}
	if event.Type == "error" {
		return serverEvent{}, errors.New("OpenAI Live rejected a command")
	}
	return event, nil
}

// Next returns media and lifecycle events in order. It returns EOF on reads
// after a successful session.closed event. Any other read error ends the session.
// A quiet session can block until ctx is canceled or Close is called.
func (s *Session) Next(ctx context.Context) (Event, error) {
	if s.finalized {
		return Event{}, io.EOF
	}
	for {
		event, err := s.read(ctx)
		if err != nil {
			_ = s.Close()
			return Event{}, err
		}
		switch event.Type {
		case "session.output_audio.delta":
			audio, err := base64.StdEncoding.DecodeString(event.Delta)
			if err != nil || len(audio) == 0 || len(audio)%2 != 0 || len(audio) > maxReceiveAudioBytes {
				_ = s.Close()
				return Event{}, errors.New("OpenAI Live sent invalid PCM audio")
			}
			return Event{Type: event.Type, Audio: audio}, nil
		case "session.input_transcript.delta", "session.output_transcript.delta":
			return Event{Type: event.Type, Text: event.Delta}, nil
		case "session.commentary.appended", "session.delegation.created":
			return Event{Type: event.Type}, nil
		case "session.closed":
			_ = s.Close()
			switch event.Reason {
			case "close_requested", "remote_hangup":
				s.finalized = true
				return Event{Type: event.Type}, nil
			case "expired", "content", "connection_lost":
				return Event{}, fmt.Errorf("OpenAI Live session closed (%s)", event.Reason)
			default:
				return Event{}, errors.New("OpenAI Live session closed with an invalid reason")
			}
		}
	}
}

// Close aborts the WebSocket, interrupting in-flight operations.
func (s *Session) Close() error {
	if err := s.conn.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
		return errors.New("OpenAI Live WebSocket close failed")
	}
	return nil
}
