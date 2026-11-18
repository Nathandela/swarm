// Package voicewire contains the versioned, daemon-free JSON contract for voice sessions.
package voicewire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/jsontext"
)

const (
	ProtocolVersion    = 1
	MaxMessageBytes    = 128 << 10
	MaxAudioFrameBytes = 64 << 10
	MaxTextBytes       = 64 << 10
	MaxIDBytes         = 256
)

const (
	TypeHandshake    = "handshake"
	TypeBegin        = "begin"
	TypeAudio        = "audio"
	TypeFinalize     = "finalize"
	TypeText         = "text"
	TypeStopSpeaking = "stop_speaking"
	TypeMute         = "mute"
	TypeEnd          = "end"
	TypeStatus       = "status"
	TypeResume       = "resume"
	TypePlayed       = "played"
)

type Cursor struct {
	StreamID string `json:"stream_id"`
	Sequence uint64 `json:"sequence"`
}

// AudioFrame carries signed, little-endian, mono PCM16 samples as JSON base64.
type AudioFrame struct {
	SampleRate  int    `json:"sample_rate"`
	Channels    int    `json:"channels"`
	FirstSample uint64 `json:"first_sample"`
	Samples     []byte `json:"samples"`
}

type PlaybackAck struct {
	SpeechEpoch    uint64 `json:"speech_epoch"`
	PhraseSequence uint64 `json:"phrase_sequence"`
	SampleCount    uint64 `json:"sample_count"`
}

type ClientMessage struct {
	Type        string       `json:"type"`
	Version     int          `json:"version"`
	SessionID   string       `json:"session_id"`
	MediaEpoch  uint64       `json:"media_epoch"`
	Sequence    uint64       `json:"sequence"`
	UtteranceID string       `json:"utterance_id,omitempty"`
	Text        string       `json:"text,omitempty"`
	Audio       *AudioFrame  `json:"audio,omitempty"`
	After       *Cursor      `json:"after,omitempty"`
	Muted       *bool        `json:"muted,omitempty"`
	Playback    *PlaybackAck `json:"playback,omitempty"`
}

type ServerEvent struct {
	Type        string          `json:"type"`
	Cursor      Cursor          `json:"cursor"`
	SessionID   string          `json:"session_id"`
	MediaEpoch  uint64          `json:"media_epoch"`
	UtteranceID string          `json:"utterance_id,omitempty"`
	State       string          `json:"state,omitempty"`
	Text        string          `json:"text,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

func DecodeClientMessage(data []byte) (ClientMessage, error) {
	var msg ClientMessage
	if err := strictDecode(data, &msg); err != nil {
		return ClientMessage{}, err
	}
	if err := validateClientShape(data); err != nil {
		return ClientMessage{}, err
	}
	if err := msg.Validate(); err != nil {
		return ClientMessage{}, err
	}
	return msg, nil
}

func (msg ClientMessage) Validate() error {
	if msg.Version != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", msg.Version)
	}
	if err := validateID("session_id", msg.SessionID); err != nil {
		return err
	}
	if msg.Sequence == 0 {
		return errors.New("sequence must be positive")
	}
	if msg.Type == TypeHandshake && msg.Sequence != 1 {
		return errors.New("handshake sequence must be 1")
	}
	if requiresMediaEpoch(msg.Type) && msg.MediaEpoch == 0 {
		return errors.New("media_epoch must be positive for this message type")
	}

	needsUtterance := msg.Type == TypeBegin || msg.Type == TypeAudio || msg.Type == TypeFinalize || msg.Type == TypeText
	if needsUtterance {
		if err := validateID("utterance_id", msg.UtteranceID); err != nil {
			return err
		}
	} else if msg.UtteranceID != "" {
		return errors.New("utterance_id is not valid for this message type")
	}

	switch msg.Type {
	case TypeHandshake, TypeBegin, TypeFinalize, TypeStopSpeaking, TypeEnd, TypeStatus:
		if msg.Audio != nil || msg.After != nil || msg.Text != "" || msg.Muted != nil || msg.Playback != nil {
			return fmt.Errorf("message type %q has unexpected fields", msg.Type)
		}
	case TypeAudio:
		if msg.Audio == nil || msg.After != nil || msg.Text != "" || msg.Muted != nil || msg.Playback != nil {
			return errors.New("audio message requires only an audio frame")
		}
		if err := msg.Audio.Validate(); err != nil {
			return err
		}
	case TypeText:
		if msg.Audio != nil || msg.After != nil || msg.Text == "" || len(msg.Text) > MaxTextBytes || !utf8.ValidString(msg.Text) || msg.Muted != nil || msg.Playback != nil {
			return fmt.Errorf("text message requires nonempty valid UTF-8 text of at most %d bytes only", MaxTextBytes)
		}
	case TypeResume:
		if msg.After == nil || msg.Audio != nil || msg.Text != "" || msg.Muted != nil || msg.Playback != nil {
			return errors.New("resume message requires only an after cursor")
		}
		if err := msg.After.Validate(); err != nil {
			return err
		}
	case TypeMute:
		if msg.Muted == nil || msg.Audio != nil || msg.After != nil || msg.Text != "" || msg.Playback != nil {
			return errors.New("mute message requires only a muted boolean")
		}
	case TypePlayed:
		if msg.Playback == nil || msg.Audio != nil || msg.After != nil || msg.Text != "" || msg.Muted != nil {
			return errors.New("played message requires only playback progress")
		}
		if msg.Playback.SpeechEpoch == 0 || msg.Playback.PhraseSequence == 0 {
			return errors.New("played message requires positive speech epoch and phrase sequence")
		}
	default:
		return fmt.Errorf("unsupported client message type %q", msg.Type)
	}
	return nil
}

func (frame AudioFrame) Validate() error {
	if !supportedSampleRate(frame.SampleRate) {
		return fmt.Errorf("unsupported PCM16 sample rate %d", frame.SampleRate)
	}
	if frame.Channels != 1 {
		return errors.New("PCM16 audio must be mono")
	}
	if len(frame.Samples) == 0 || len(frame.Samples) > MaxAudioFrameBytes || len(frame.Samples)%2 != 0 {
		return errors.New("PCM16 audio frame must have an even, nonempty sample buffer within the frame limit")
	}
	if len(frame.Samples)*10 > frame.SampleRate*2 {
		return errors.New("PCM16 audio frame exceeds 100 ms")
	}
	if ^uint64(0)-frame.FirstSample < uint64(len(frame.Samples)/2) {
		return errors.New("PCM16 sample counter overflows")
	}
	return nil
}

func (frame *AudioFrame) UnmarshalJSON(data []byte) error {
	if err := requireJSONField(data, "first_sample"); err != nil {
		return err
	}
	type plain AudioFrame
	var decoded plain
	if err := decodeStrictValue(data, &decoded); err != nil {
		return err
	}
	*frame = AudioFrame(decoded)
	return nil
}

func (ack *PlaybackAck) UnmarshalJSON(data []byte) error {
	if err := requireJSONField(data, "sample_count"); err != nil {
		return err
	}
	type plain PlaybackAck
	var decoded plain
	if err := decodeStrictValue(data, &decoded); err != nil {
		return err
	}
	*ack = PlaybackAck(decoded)
	return nil
}

func DecodeServerEvent(data []byte) (ServerEvent, error) {
	var event ServerEvent
	if err := strictDecode(data, &event); err != nil {
		return ServerEvent{}, err
	}
	if event.Type == "" || !utf8.ValidString(event.Type) {
		return ServerEvent{}, errors.New("event type is required")
	}
	if err := event.Cursor.Validate(); err != nil {
		return ServerEvent{}, err
	}
	if err := validateID("session_id", event.SessionID); err != nil {
		return ServerEvent{}, err
	}
	if event.UtteranceID != "" {
		if err := validateID("utterance_id", event.UtteranceID); err != nil {
			return ServerEvent{}, err
		}
	}
	if !utf8.ValidString(event.State) || !utf8.ValidString(event.Text) {
		return ServerEvent{}, errors.New("event text fields must be valid UTF-8")
	}
	return event, nil
}

func (cursor Cursor) Validate() error {
	if err := validateID("stream_id", cursor.StreamID); err != nil {
		return err
	}
	if cursor.Sequence == 0 {
		return errors.New("cursor sequence must be positive")
	}
	return nil
}

func strictDecode(data []byte, dst any) error {
	if err := validateJSONIntegrity(data); err != nil {
		return err
	}
	return decodeStrictValue(data, dst)
}

func decodeStrictValue(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode voice message: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("message contains trailing JSON")
		}
		return fmt.Errorf("message contains trailing data: %w", err)
	}
	return nil
}

func requireJSONField(data []byte, name string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	value, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return fmt.Errorf("%s must be present and non-null", name)
	}
	return nil
}

func validateClientShape(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode voice message shape: %w", err)
	}
	var kind string
	if err := json.Unmarshal(fields["type"], &kind); err != nil {
		return errors.New("message type is required")
	}
	allowed := map[string]struct{}{
		"type": {}, "version": {}, "session_id": {}, "media_epoch": {}, "sequence": {},
	}
	switch kind {
	case TypeBegin, TypeFinalize:
		allowed["utterance_id"] = struct{}{}
	case TypeAudio:
		allowed["utterance_id"], allowed["audio"] = struct{}{}, struct{}{}
	case TypeText:
		allowed["utterance_id"], allowed["text"] = struct{}{}, struct{}{}
	case TypeResume:
		allowed["after"] = struct{}{}
	case TypeMute:
		allowed["muted"] = struct{}{}
	case TypePlayed:
		allowed["playback"] = struct{}{}
	case TypeHandshake, TypeStopSpeaking, TypeEnd, TypeStatus:
	default:
		return fmt.Errorf("unsupported client message type %q", kind)
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("field %q is not valid for message type %q", key, kind)
		}
	}
	return nil
}

func validateJSONIntegrity(data []byte) error {
	if len(data) > MaxMessageBytes {
		return fmt.Errorf("message exceeds %d bytes", MaxMessageBytes)
	}
	if err := jsontext.ValidateUnicode(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := scanJSONValue(dec, true); err != nil {
		return err
	}
	return nil
}

func scanJSONValue(dec *json.Decoder, typed bool) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("decode voice JSON: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return fmt.Errorf("decode voice JSON key: %w", err)
			}
			key, ok := tok.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, ok := seen[key]; ok {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if typed && caseAliasedJSONKey(key) {
				return fmt.Errorf("case-aliased JSON key %q", key)
			}
			if err := scanJSONValue(dec, typed && key != "data"); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := scanJSONValue(dec, typed); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func caseAliasedJSONKey(key string) bool {
	for _, exact := range [...]string{"type", "version", "session_id", "media_epoch", "sequence", "utterance_id", "text", "audio", "after", "muted", "playback", "sample_rate", "channels", "first_sample", "samples", "stream_id", "cursor", "state", "data", "speech_epoch", "phrase_sequence", "sample_count"} {
		if key != exact && strings.EqualFold(key, exact) {
			return true
		}
	}
	return false
}

func requiresMediaEpoch(kind string) bool {
	switch kind {
	case TypeBegin, TypeAudio, TypeFinalize, TypeText:
		return true
	default:
		return false
	}
}

func validateID(name, id string) error {
	if id == "" || len(id) > MaxIDBytes || !utf8.ValidString(id) {
		return fmt.Errorf("%s must be nonempty valid UTF-8 and at most %d bytes", name, MaxIDBytes)
	}
	return nil
}

func supportedSampleRate(rate int) bool {
	switch rate {
	case 8000, 16000, 22050, 24000, 44100, 48000:
		return true
	default:
		return false
	}
}
