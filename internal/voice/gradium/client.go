// Package gradium provides single-request PCM speech streams. It never retries input.
package gradium

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/jsontext"
	"github.com/coder/websocket"
)

const (
	MaxMessageBytes = 128 << 10
	MaxAudioBytes   = 64 << 10
	MaxPhraseBytes  = 4096
	EventCapacity   = 16
	setupTimeout    = 10 * time.Second
	writeTimeout    = 5 * time.Second
	stt             = "asr"
	tts             = "tts"
)

type Config struct{ Region, Language, Voice, Format string }

// Ready keeps configured wire input separate from the provider's internal STT rate.
// OutputFormat is the PCM format implied by the TTS provider's actual output rate.
type Ready struct {
	RequestID            string `json:"request_id"`
	ModelName            string `json:"model_name"`
	ModelExt             string `json:"model_ext,omitempty"`
	SampleRate           int    `json:"sample_rate"`
	FrameSize            int    `json:"frame_size"`
	DelayInFrames        int    `json:"delay_in_frames"`
	Residency            string `json:"residency,omitempty"`
	ConfiguredFormat     string `json:"configured_format"`
	ConfiguredSampleRate int    `json:"configured_sample_rate"`
	OutputFormat         string `json:"output_format,omitempty"`
}

type VAD struct {
	HorizonS       float64 `json:"horizon_s"`
	InactivityProb float64 `json:"inactivity_prob"`
}
type Event struct {
	Type           string  `json:"type"`
	Text           string  `json:"text,omitempty"`
	Audio          []byte  `json:"-"`
	StreamID       int     `json:"stream_id,omitempty"`
	StartS         float64 `json:"start_s,omitempty"`
	StopS          float64 `json:"stop_s,omitempty"`
	FlushID        uint64  `json:"flush_id,omitempty"`
	StepIdx        int     `json:"step_idx,omitempty"`
	StepDurationS  float64 `json:"step_duration_s,omitempty"`
	TotalDurationS float64 `json:"total_duration_s,omitempty"`
	VAD            []VAD   `json:"vad,omitempty"`
	Late           bool    `json:"late,omitempty"`
}

// Error contains only public categories and numeric status, never provider text or headers.
type Error struct {
	Kind             string
	HTTPStatus, Code int
}

func (e *Error) Error() string {
	return fmt.Sprintf("Gradium %s (HTTP %d, code %d)", e.Kind, e.HTTPStatus, e.Code)
}
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other.Kind == e.Kind
}
func (e *Error) Unwrap() error {
	if e.Kind == "cancelled" {
		return context.Canceled
	}
	if e.Kind == "timeout" {
		return context.DeadlineExceeded
	}
	return nil
}

var (
	ErrAuth         = &Error{Kind: "authentication"}
	ErrQuota        = &Error{Kind: "quota"}
	ErrProvider     = &Error{Kind: "provider"}
	ErrPolicy       = &Error{Kind: "policy"}
	ErrProtocol     = &Error{Kind: "protocol"}
	ErrTransport    = &Error{Kind: "transport"}
	ErrCancelled    = &Error{Kind: "cancelled"}
	ErrTimeout      = &Error{Kind: "timeout"}
	ErrBackpressure = &Error{Kind: "backpressure"}
	ErrClosed       = &Error{Kind: "closed"}
	ErrInvalid      = &Error{Kind: "invalid_input"}
	ErrFlushPending = &Error{Kind: "flush_pending"}
	ErrFinalized    = &Error{Kind: "finalized"}
)

type Session struct {
	conn                     *websocket.Conn
	kind                     string
	ready                    Ready
	ctx                      context.Context
	cancel                   context.CancelFunc
	events                   chan Event
	done                     chan struct{}
	writer                   chan struct{}
	mu                       sync.Mutex
	err                      error
	ended, complete, flushed bool
	pendingFlush             uint64
	activeWrite              context.Context
}

type dialFunc func(context.Context, string, *websocket.DialOptions) (*websocket.Conn, *http.Response, error)

func OpenSTT(ctx context.Context, key string, config Config) (*Session, error) {
	return open(ctx, key, config, stt, websocket.Dial)
}
func OpenTTS(ctx context.Context, key string, config Config) (*Session, error) {
	return open(ctx, key, config, tts, websocket.Dial)
}

func open(ctx context.Context, key string, config Config, kind string, dial dialFunc) (*Session, error) {
	host := map[string]string{"": "api.gradium.ai", "auto": "api.gradium.ai", "eu": "eu.api.gradium.ai", "us": "us.api.gradium.ai"}[config.Region]
	if config.Format == "" {
		config.Format = "pcm_24000"
	}
	rate, valid := map[string]int{"pcm_8000": 8000, "pcm_16000": 16000, "pcm_24000": 24000, "pcm_44100": 44100, "pcm_48000": 48000}[config.Format]
	if host == "" || !valid || len(key) == 0 || len(key) > 4096 || key != strings.TrimSpace(key) || strings.ContainsAny(key, "\r\n\x00") {
		return nil, ErrInvalid
	}
	if config.Language == "" {
		config.Language = "en"
	}
	if kind == stt && !map[string]bool{"en": true, "fr": true, "es": true, "pt": true, "de": true, "any": true}[config.Language] {
		return nil, ErrInvalid
	}
	if kind == tts && (len(config.Voice) == 0 || len(config.Voice) > 128 || !utf8.ValidString(config.Voice) || strings.ContainsAny(config.Voice, "\r\n\x00")) {
		return nil, ErrInvalid
	}
	handshake, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	options := &websocket.DialOptions{HTTPHeader: http.Header{"X-Api-Key": []string{key}}, HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	conn, response, err := dial(handshake, "wss://"+host+"/api/speech/"+kind, options)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
			if response.Body != nil {
				_ = response.Body.Close()
			}
		}
		if status == 401 || status == 403 {
			return nil, &Error{Kind: ErrAuth.Kind, HTTPStatus: status}
		}
		if status == 429 {
			return nil, &Error{Kind: ErrQuota.Kind, HTTPStatus: status}
		}
		if status >= 300 && status < 400 {
			return nil, &Error{Kind: ErrProtocol.Kind, HTTPStatus: status}
		}
		return nil, transportError(handshake, err)
	}
	conn.SetReadLimit(MaxMessageBytes + 1)
	setup := map[string]any{"type": "setup", "model_name": "default", "close_ws_on_eos": true}
	if kind == stt {
		setup["input_format"] = config.Format
		setup["json_config"] = map[string]any{"language": config.Language, "delay_in_frames": 10}
	} else {
		setup["output_format"] = config.Format
		setup["voice_id"] = config.Voice
	}
	raw, _ := json.Marshal(setup)
	if err = conn.Write(handshake, websocket.MessageText, raw); err != nil {
		_ = conn.CloseNow()
		return nil, transportError(handshake, err)
	}
	frame, err := readWire(handshake, conn)
	if err != nil {
		_ = conn.CloseNow()
		return nil, err
	}
	if frame.Type != "ready" || frame.RequestID == "" || len(frame.RequestID) > 256 || frame.ModelName == "" || frame.SampleRate < 8000 || frame.SampleRate > 192000 || frame.FrameSize <= 0 || frame.FrameSize > frame.SampleRate*2 || frame.DelayInFrames < 0 || frame.DelayInFrames > 100 || frame.ClientReqID != "" {
		_ = conn.CloseNow()
		return nil, ErrProtocol
	}
	life, stop := context.WithCancel(ctx)
	ready := Ready{RequestID: frame.RequestID, ModelName: frame.ModelName, ModelExt: frame.ModelExt, SampleRate: frame.SampleRate, FrameSize: frame.FrameSize, DelayInFrames: frame.DelayInFrames, Residency: frame.Residency, ConfiguredFormat: config.Format, ConfiguredSampleRate: rate}
	if kind == tts {
		ready.OutputFormat = "pcm_" + strconv.Itoa(frame.SampleRate)
	}
	s := &Session{conn: conn, kind: kind, ready: ready, ctx: life, cancel: stop, events: make(chan Event, EventCapacity), done: make(chan struct{}), writer: make(chan struct{}, 1)}
	go s.readLoop()
	return s, nil
}

func (s *Session) Ready() Ready { return s.ready }

// Events are ordered. Terminal failure invalidates and discards queued output;
// consumers must fence already-received events with their media generation.
func (s *Session) Events() <-chan Event  { return s.events }
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Err() error            { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *Session) Close() error {
	s.fail(ErrClosed)
	<-s.done
	for range s.events {
	}
	return nil
}

func (s *Session) SendAudio(ctx context.Context, audio []byte) error {
	if s.kind != stt || len(audio) == 0 || len(audio) > MaxAudioBytes || len(audio)%2 != 0 {
		return ErrInvalid
	}
	return s.write(ctx, "audio", map[string]any{"type": "audio", "audio": base64.StdEncoding.EncodeToString(audio)})
}

// SendPhrase sends a complete phrase and reserves <flush> for the flush flag.
func (s *Session) SendPhrase(ctx context.Context, text string, flush bool) error {
	if s.kind != tts || !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > MaxPhraseBytes || strings.Contains(text, "<flush>") {
		return ErrInvalid
	}
	if flush {
		text += " <flush>"
	}
	kind := "text"
	if flush {
		kind = "phrase_flush"
	}
	return s.write(ctx, kind, map[string]any{"type": "text", "text": text})
}

// Flush seals this utterance. A new utterance needs a new Session, not reused audio.
func (s *Session) Flush(ctx context.Context, id uint64) error {
	if s.kind != stt || id == 0 || id > 1<<53-1 {
		return ErrInvalid
	}
	return s.write(ctx, "flush", map[string]any{"type": "flush", "flush_id": id})
}
func (s *Session) End(ctx context.Context) error {
	return s.write(ctx, "end_of_stream", map[string]any{"type": "end_of_stream"})
}

func (s *Session) write(ctx context.Context, kind string, value map[string]any) error {
	if s.ctx.Err() != nil {
		s.mu.Lock()
		closed := s.ended || s.complete || s.err != nil
		s.mu.Unlock()
		if closed {
			return ErrClosed
		}
		return transportError(s.ctx, s.ctx.Err())
	}
	operation, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	select {
	case s.writer <- struct{}{}:
	case <-operation.Done():
		return transportError(operation, operation.Err())
	}
	defer func() { <-s.writer }()
	if operation.Err() != nil {
		return transportError(operation, operation.Err())
	}
	s.mu.Lock()
	if s.err != nil || s.ended || s.complete {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.pendingFlush != 0 && kind != "end_of_stream" {
		s.mu.Unlock()
		return ErrFlushPending
	}
	if s.flushed && kind != "end_of_stream" {
		s.mu.Unlock()
		return ErrFinalized
	}
	if kind == "flush" {
		s.pendingFlush = value["flush_id"].(uint64)
	}
	if kind == "phrase_flush" {
		s.pendingFlush = 1
	}
	if kind == "end_of_stream" {
		s.ended = true
	}
	s.mu.Unlock()
	raw, _ := json.Marshal(value)
	s.mu.Lock()
	if s.err != nil {
		cause := s.err
		s.mu.Unlock()
		return cause
	}
	s.activeWrite = operation
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.activeWrite = nil; s.mu.Unlock() }()
	if err := s.conn.Write(operation, websocket.MessageText, raw); err != nil {
		terminal := transportError(operation, err)
		s.fail(terminal)
		if cause := s.Err(); cause != nil {
			return cause
		}
		return terminal
	}
	return nil
}
func (s *Session) fail(err error) {
	s.mu.Lock()
	// A timed-out write can close the socket before its reader observes why.
	if errors.Is(err, ErrTransport) && s.activeWrite != nil && s.activeWrite.Err() != nil {
		err = transportError(s.activeWrite, s.activeWrite.Err())
	}
	if s.err == nil && !s.complete {
		s.err = err
	}
	s.mu.Unlock()
	s.cancel()
	_ = s.conn.CloseNow()
}

func (s *Session) readLoop() {
	defer func() {
		s.cancel()
		_ = s.conn.CloseNow()
		if s.Err() != nil {
		discard:
			for {
				select {
				case <-s.events:
				default:
					break discard
				}
			}
		}
		close(s.events)
		close(s.done)
	}()
	for {
		frame, err := readWire(s.ctx, s.conn)
		if err != nil {
			s.fail(err)
			return
		}
		event := frame.Event
		s.mu.Lock()
		var valid bool
		switch event.Type {
		case "text", "end_text":
			valid = event.Type != "end_text" || s.kind == stt
			event.Late = s.flushed
		case "step", "vad":
			valid = s.kind == stt
		case "audio":
			valid = s.kind == tts
		case "flushed":
			valid = s.pendingFlush != 0 && (s.kind == stt && s.pendingFlush == event.FlushID || s.kind == tts && event.FlushID == 0)
			if valid {
				s.pendingFlush = 0
				s.flushed = s.kind == stt
			}
		case "end_of_stream":
			valid = s.ended && s.pendingFlush == 0
		default:
			valid = false
		}
		s.mu.Unlock()
		if !valid || frame.ClientReqID != "" {
			s.fail(ErrProtocol)
			return
		}
		if event.Type == "audio" {
			event.Audio, err = base64.StdEncoding.Strict().DecodeString(frame.Audio)
			if err != nil || len(event.Audio) == 0 || len(event.Audio) > MaxAudioBytes || len(event.Audio)%2 != 0 {
				s.fail(ErrProtocol)
				return
			}
		}
		select {
		case <-s.ctx.Done():
			s.fail(transportError(s.ctx, s.ctx.Err()))
			return
		default:
		}
		s.mu.Lock()
		if s.err != nil {
			s.mu.Unlock()
			return
		}
		select {
		case s.events <- event:
			if event.Type == "end_of_stream" {
				s.complete = true
			}
		default:
			s.mu.Unlock()
			s.fail(ErrBackpressure)
			return
		}
		s.mu.Unlock()
		if event.Type == "end_of_stream" {
			return
		}
	}
}

type wireFrame struct {
	Event
	Audio         string `json:"audio,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	ModelName     string `json:"model_name,omitempty"`
	ModelExt      string `json:"model_ext,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	FrameSize     int    `json:"frame_size,omitempty"`
	DelayInFrames int    `json:"delay_in_frames,omitempty"`
	Residency     string `json:"residency,omitempty"`
	ClientReqID   string `json:"client_req_id,omitempty"`
	Message       string `json:"message,omitempty"`
	Code          int    `json:"code,omitempty"`
}

func readWire(ctx context.Context, conn *websocket.Conn) (wireFrame, error) {
	kind, reader, err := conn.Reader(ctx)
	if err != nil {
		return wireFrame{}, transportError(ctx, err)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxMessageBytes+1))
	if err != nil {
		return wireFrame{}, transportError(ctx, err)
	}
	var frame wireFrame
	if len(raw) > MaxMessageBytes || kind != websocket.MessageText || jsontext.ValidateUnicode(raw) != nil || json.Unmarshal(raw, &frame) != nil || frame.Type == "" {
		return frame, ErrProtocol
	}
	if frame.Type == "error" {
		category := ErrProvider.Kind
		message := strings.ToLower(frame.Message)
		if frame.Code == 1008 {
			category = ErrPolicy.Kind
		}
		if strings.Contains(message, "credit") || strings.Contains(message, "quota") || strings.Contains(message, "concurr") || strings.Contains(message, "rate limit") {
			category = ErrQuota.Kind
		} else if frame.Code == 1008 && (strings.Contains(message, "api key") || strings.Contains(message, "auth") || strings.Contains(message, "token")) {
			category = ErrAuth.Kind
		}
		return wireFrame{}, &Error{Kind: category, Code: frame.Code}
	}
	if frame.StartS < 0 || frame.StopS < 0 || frame.StreamID < 0 || frame.StepIdx < 0 || frame.StepDurationS < 0 || frame.TotalDurationS < 0 {
		return frame, ErrProtocol
	}
	for _, v := range frame.VAD {
		if v.HorizonS < 0 || v.InactivityProb < 0 || v.InactivityProb > 1 {
			return frame, ErrProtocol
		}
	}
	return frame, nil
}
func transportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimeout
	}
	if ctx.Err() != nil {
		return ErrCancelled
	}
	if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
		return ErrProtocol
	}
	return ErrTransport
}
