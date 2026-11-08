package voicewire

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeClientMessageRejectsMalformedInput(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field":         `{"type":"status","version":1,"session_id":"s","sequence":1,"surprise":true}`,
		"trailing JSON":         `{"type":"status","version":1,"session_id":"s","sequence":1}{}`,
		"duplicate field":       `{"type":"status","type":"end","version":1,"session_id":"s","sequence":1}`,
		"nested duplicate":      `{"type":"audio","version":1,"session_id":"s","sequence":1,"media_epoch":1,"utterance_id":"u","audio":{"sample_rate":16000,"sample_rate":16000,"channels":1,"first_sample":0,"samples":"AAA="}}`,
		"case alias":            `{"TYPE":"status","version":1,"session_id":"s","sequence":1}`,
		"nested case alias":     `{"type":"audio","version":1,"session_id":"s","sequence":1,"media_epoch":1,"utterance_id":"u","audio":{"SAMPLE_RATE":16000,"channels":1,"first_sample":0,"samples":"AAA="}}`,
		"invalid type":          `{"type":"not-a-message","version":1,"session_id":"s","sequence":1}`,
		"bad protocol version":  `{"type":"status","version":2,"session_id":"s","sequence":1}`,
		"invalid UTF-8 text":    "{\"type\":\"text\",\"version\":1,\"session_id\":\"s\",\"sequence\":1,\"utterance_id\":\"u\",\"text\":\"\xff\"}",
		"lone high surrogate":   `{"type":"text","version":1,"session_id":"s","sequence":1,"media_epoch":1,"utterance_id":"u","text":"\ud800"}`,
		"lone low surrogate":    `{"type":"text","version":1,"session_id":"s","sequence":1,"media_epoch":1,"utterance_id":"u","text":"\udc00"}`,
		"forbidden empty field": `{"type":"status","version":1,"session_id":"s","sequence":1,"text":""}`,
		"forbidden null field":  `{"type":"handshake","version":1,"session_id":"s","sequence":1,"audio":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeClientMessage([]byte(raw)); err == nil {
				t.Fatal("accepted malformed message")
			}
		})
	}
}

func TestDecodeClientMessageRejectsOversize(t *testing.T) {
	raw := []byte(`{"type":"text","version":1,"session_id":"s","sequence":1,"utterance_id":"u","text":"` + strings.Repeat("x", MaxMessageBytes) + `"}`)
	if _, err := DecodeClientMessage(raw); err == nil {
		t.Fatal("accepted message over the limit")
	}
	if _, err := DecodeClientMessage([]byte(strings.Repeat(" ", MaxMessageBytes) + "?")); err == nil || !strings.Contains(err.Error(), "message exceeds") {
		t.Fatalf("oversized malformed input was parsed before the byte limit: %v", err)
	}
}

func TestRequiredCountersMustBePresentAndNonNull(t *testing.T) {
	for name, raw := range map[string]string{
		"missing capture counter": `{"type":"audio","version":1,"session_id":"s","sequence":1,"media_epoch":1,"utterance_id":"u","audio":{"sample_rate":8000,"channels":1,"samples":"AAA="}}`,
		"null capture counter":    `{"type":"audio","version":1,"session_id":"s","sequence":1,"media_epoch":1,"utterance_id":"u","audio":{"sample_rate":8000,"channels":1,"first_sample":null,"samples":"AAA="}}`,
		"missing played count":    `{"type":"played","version":1,"session_id":"s","sequence":1,"playback":{"speech_epoch":1,"phrase_sequence":1}}`,
		"null played count":       `{"type":"played","version":1,"session_id":"s","sequence":1,"playback":{"speech_epoch":1,"phrase_sequence":1,"sample_count":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeClientMessage([]byte(raw)); err == nil {
				t.Fatal("accepted missing or null sample counter")
			}
		})
	}
	for _, msg := range []ClientMessage{validClientMessageOf(TypeAudio), validClientMessageOf(TypePlayed)} {
		raw, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeClientMessage(raw); err != nil {
			t.Fatalf("rejected explicit zero counter in %q: %v", msg.Type, err)
		}
	}
}

func TestDecodeClientMessageValidatesAudioFormatAndDuration(t *testing.T) {
	tests := []struct {
		name  string
		audio AudioFrame
	}{
		{"unsupported rate", AudioFrame{SampleRate: 32000, Channels: 1, Samples: make([]byte, 2)}},
		{"stereo", AudioFrame{SampleRate: 16000, Channels: 2, Samples: make([]byte, 2)}},
		{"empty", AudioFrame{SampleRate: 16000, Channels: 1}},
		{"odd PCM16 byte count", AudioFrame{SampleRate: 16000, Channels: 1, Samples: []byte{1}}},
		{"over 100ms", AudioFrame{SampleRate: 8000, Channels: 1, Samples: make([]byte, 1602)}},
		{"over frame limit", AudioFrame{SampleRate: 48000, Channels: 1, Samples: make([]byte, MaxAudioFrameBytes+2)}},
		{"sample counter overflow", AudioFrame{SampleRate: 16000, Channels: 1, FirstSample: ^uint64(0), Samples: []byte{0, 0}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := validClientMessage()
			msg.Type, msg.UtteranceID, msg.MediaEpoch, msg.Audio = TypeAudio, "utterance-1", 1, &tt.audio
			if err := msg.Validate(); err == nil {
				t.Fatal("accepted invalid audio frame")
			}
		})
	}
	for _, rate := range [...]int{8000, 16000, 22050, 24000, 44100, 48000} {
		msg := validClientMessage()
		msg.Type, msg.UtteranceID, msg.MediaEpoch = TypeAudio, "utterance-1", 1
		msg.Audio = &AudioFrame{SampleRate: rate, Channels: 1, Samples: make([]byte, rate/10*2)}
		if err := msg.Validate(); err != nil {
			t.Errorf("rate %d: %v", rate, err)
		}
	}
}

func TestTextLimitAndUnicodeEscapes(t *testing.T) {
	msg := validClientMessageOf(TypeText)
	msg.Text = strings.Repeat("x", MaxTextBytes)
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeClientMessage(raw); err != nil {
		t.Fatalf("accepted exact text limit: %v", err)
	}
	msg.Text += "x"
	raw, err = json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeClientMessage(raw); err == nil {
		t.Fatal("accepted text above 64 KiB")
	}

	for raw, want := range map[string]string{
		`{"type":"text","version":1,"session_id":"s","sequence":1,"media_epoch":1,"utterance_id":"u","text":"\ud83d\ude80"}`: "🚀",
		`{"type":"text","version":1,"session_id":"s","sequence":1,"media_epoch":1,"utterance_id":"u","text":"\\uD800"}`:      `\uD800`,
	} {
		got, err := DecodeClientMessage([]byte(raw))
		if err != nil {
			t.Fatalf("valid JSON Unicode %q rejected: %v", raw, err)
		}
		if got.Text != want {
			t.Errorf("text = %q, want %q", got.Text, want)
		}
	}
}

func TestMuteAndPlayedRequireTypedFields(t *testing.T) {
	msg := validClientMessageOf(TypeMute)
	msg.Muted = nil
	if err := msg.Validate(); err == nil {
		t.Fatal("accepted mute without explicit state")
	}
	for _, value := range []bool{false, true} {
		msg := validClientMessageOf(TypeMute)
		msg.Muted = &value
		if err := msg.Validate(); err != nil {
			t.Errorf("muted=%t: %v", value, err)
		}
	}

	for name, ack := range map[string]PlaybackAck{
		"zero speech epoch":    {PhraseSequence: 1},
		"zero phrase sequence": {SpeechEpoch: 1},
	} {
		t.Run(name, func(t *testing.T) {
			msg := validClientMessageOf(TypePlayed)
			msg.Playback = &ack
			if err := msg.Validate(); err == nil {
				t.Fatal("accepted invalid playback acknowledgement")
			}
		})
	}
	if err := validClientMessageOf(TypePlayed).Validate(); err != nil {
		t.Fatalf("accepted zero played-sample watermark: %v", err)
	}

	mute := validClientMessageOf(TypeMute)
	raw, err := json.Marshal(mute)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"muted":false`)) {
		t.Fatalf("false mute state was omitted: %s", raw)
	}
	decodedMute, err := DecodeClientMessage(raw)
	if err != nil || decodedMute.Muted == nil || *decodedMute.Muted {
		t.Fatalf("mute false roundtrip: %#v, %v", decodedMute, err)
	}

	played := validClientMessageOf(TypePlayed)
	played.Playback.SampleCount = 7680
	raw, err = json.Marshal(played)
	if err != nil {
		t.Fatal(err)
	}
	decodedPlayed, err := DecodeClientMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decodedPlayed.Playback == nil || *decodedPlayed.Playback != *played.Playback {
		t.Fatalf("played roundtrip: %#v, want %#v", decodedPlayed.Playback, played.Playback)
	}
}

func TestHandshakeSequenceAndMediaEpochRequirements(t *testing.T) {
	msg := validClientMessageOf(TypeHandshake)
	msg.Sequence = 2
	if err := msg.Validate(); err == nil {
		t.Fatal("accepted handshake sequence other than 1")
	}
	msg = validClientMessageOf(TypeAudio)
	msg.MediaEpoch = 0
	if err := msg.Validate(); err == nil {
		t.Fatal("accepted audio without a media epoch")
	}
}

func TestDecodeClientMessageValidatesResumeCursor(t *testing.T) {
	for name, cursor := range map[string]Cursor{
		"missing stream":     {Sequence: 1},
		"zero sequence":      {StreamID: "stream", Sequence: 0},
		"oversize stream ID": {StreamID: strings.Repeat("s", MaxIDBytes+1), Sequence: 1},
	} {
		t.Run(name, func(t *testing.T) {
			msg := validClientMessage()
			msg.Type, msg.After = TypeResume, &cursor
			if err := msg.Validate(); err == nil {
				t.Fatal("accepted invalid cursor")
			}
		})
	}
}

func TestClientMessageSupportsContractTypes(t *testing.T) {
	messages := []ClientMessage{
		validClientMessageOf(TypeHandshake),
		validClientMessageOf(TypeBegin),
		validClientMessageOf(TypeAudio),
		validClientMessageOf(TypeFinalize),
		validClientMessageOf(TypeText),
		validClientMessageOf(TypeStopSpeaking),
		validClientMessageOf(TypeMute),
		validClientMessageOf(TypeEnd),
		validClientMessageOf(TypeStatus),
		validClientMessageOf(TypeResume),
		validClientMessageOf(TypePlayed),
	}
	for _, msg := range messages {
		if err := msg.Validate(); err != nil {
			t.Errorf("type %q: %v", msg.Type, err)
		}
	}
}

func TestClientAndServerRoundTrip(t *testing.T) {
	client := validClientMessage()
	client.Type, client.UtteranceID, client.MediaEpoch = TypeAudio, "utterance-1", 3
	client.Audio = &AudioFrame{SampleRate: 22050, Channels: 1, FirstSample: 4096, Samples: []byte{0, 1, 255, 254}}
	raw, err := json.Marshal(client)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"samples":"AAH//g=="`)) {
		t.Fatalf("PCM bytes not encoded as JSON base64: %s", raw)
	}
	got, err := DecodeClientMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Audio.Samples, client.Audio.Samples) || got.Audio.FirstSample != client.Audio.FirstSample {
		t.Fatalf("audio = %#v, want %#v", got.Audio, client.Audio)
	}

	event := ServerEvent{Type: "transcript", Cursor: Cursor{StreamID: "stream-1", Sequence: 7}, SessionID: "session-1", MediaEpoch: 2, UtteranceID: "utterance-1", State: "final", Text: "hello", Data: json.RawMessage(`{"source":"speech"}`)}
	raw, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeServerEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Cursor != event.Cursor || decoded.Text != event.Text || !bytes.Equal(decoded.Data, event.Data) {
		t.Fatalf("server event roundtrip mismatch: %#v", decoded)
	}
}

func TestServerEventDataRemainsOpaqueButIntegrityChecked(t *testing.T) {
	for _, raw := range []string{`{"State":"opaque"}`, `{"metric":1e400}`} {
		event := ServerEvent{Type: "status", Cursor: Cursor{StreamID: "str", Sequence: 1}, SessionID: "s", Data: json.RawMessage(raw)}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeServerEvent(encoded); err != nil {
			t.Errorf("rejected opaque data %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"metric":1,"metric":2}`, `{"text":"\ud800"}`} {
		event := ServerEvent{Type: "status", Cursor: Cursor{StreamID: "str", Sequence: 1}, SessionID: "s", Data: json.RawMessage(raw)}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeServerEvent(encoded); err == nil {
			t.Errorf("accepted malformed opaque data %s", raw)
		}
	}
}

func validClientMessage() ClientMessage {
	return ClientMessage{Type: TypeStatus, Version: ProtocolVersion, SessionID: "session-1", Sequence: 1}
}

func validClientMessageOf(kind string) ClientMessage {
	msg := validClientMessage()
	msg.Type = kind
	switch kind {
	case TypeBegin, TypeFinalize:
		msg.UtteranceID, msg.MediaEpoch = "utterance-1", 1
	case TypeAudio:
		msg.UtteranceID, msg.MediaEpoch = "utterance-1", 1
		msg.Audio = &AudioFrame{SampleRate: 16000, Channels: 1, Samples: []byte{0, 0}}
	case TypeText:
		msg.UtteranceID, msg.Text, msg.MediaEpoch = "utterance-1", "hello", 1
	case TypeResume:
		msg.After = &Cursor{StreamID: "stream-1", Sequence: 1}
	case TypeMute:
		muted := false
		msg.Muted = &muted
	case TypePlayed:
		msg.Playback = &PlaybackAck{SpeechEpoch: 1, PhraseSequence: 1}
	}
	return msg
}
