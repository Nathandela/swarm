package gradium

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/testbin"
	"github.com/coder/websocket"
)

func TestMain(m *testing.M) {
	if code, parent := testbin.Supervise(); parent {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestBlockedSocketWriterTimesOutAndStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		<-ctx.Done()
	}))
	defer server.Close()
	dial := func(ctx context.Context, _ string, o *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err == nil {
				_ = c.(*net.TCPConn).SetWriteBuffer(1024)
			}
			return c, err
		}
		client := *o.HTTPClient
		client.Transport = transport
		copy := *o
		copy.HTTPClient = &client
		return websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &copy)
	}
	s, err := open(ctx, "secret-sentinel", Config{Region: "eu"}, stt, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 256; i++ {
		write, stop := context.WithTimeout(ctx, 50*time.Millisecond)
		err = s.SendAudio(write, make([]byte, MaxAudioBytes))
		stop()
		if errors.Is(err, ErrTimeout) {
			awaitDone(t, s)
			if !errors.Is(s.Err(), ErrTimeout) {
				t.Fatalf("write timeout cause changed: %v", s.Err())
			}
			return
		}
		if err != nil {
			t.Fatalf("writer failure: %v", err)
		}
	}
	t.Fatal("loopback writer did not encounter bounded socket pressure")
}

func socketFixture(t *testing.T, serve func(context.Context, *websocket.Conn)) dialFunc {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret-sentinel" {
			t.Error("missing authentication header")
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		serve(ctx, c)
	}))
	t.Cleanup(func() { cancel(); server.Close() })
	return func(ctx context.Context, url string, options *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
		if !strings.HasPrefix(url, "wss://eu.api.gradium.ai/api/speech/") {
			t.Errorf("unexpected official endpoint: %s", url)
		}
		return websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), options)
	}
}
func sendFrame(ctx context.Context, c *websocket.Conn, value any) error {
	b, _ := json.Marshal(value)
	return c.Write(ctx, websocket.MessageText, b)
}
func readFrame(ctx context.Context, c *websocket.Conn) map[string]any {
	_, b, err := c.Read(ctx)
	if err != nil {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
func readyFrame() map[string]any {
	return map[string]any{"type": "ready", "request_id": "request", "model_name": "default", "model_ext": "resolved", "sample_rate": 24000, "frame_size": 1920, "delay_in_frames": 10, "residency": "best-effort; zone=eu"}
}
func awaitEvent(t *testing.T, s *Session, kind string) Event {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		if !ok || e.Type != kind {
			t.Fatalf("event got %q open=%v err=%v, want %s", e.Type, ok, s.Err(), kind)
		}
		return e
	case <-time.After(2 * time.Second):
		t.Fatalf("event %s timed out", kind)
	}
	return Event{}
}
func awaitDone(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("provider goroutine did not stop")
	}
}

func TestSTTExactFlushAndLateText(t *testing.T) {
	seen, release := make(chan struct{}), make(chan struct{})
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		setup := readFrame(ctx, c)
		if setup["input_format"] != "pcm_44100" {
			t.Error("configured input format changed")
		}
		_ = sendFrame(ctx, c, readyFrame())
		audio := readFrame(ctx, c)
		if audio["audio"] != base64.StdEncoding.EncodeToString([]byte{1, 2}) {
			t.Error("audio changed")
		}
		flush := readFrame(ctx, c)
		if flush["flush_id"] != float64(7) {
			t.Error("missing flush identity")
		}
		close(seen)
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		for _, f := range []any{
			map[string]any{"type": "text", "text": "hello", "stream_id": 2, "start_s": 0.1},
			map[string]any{"type": "end_text", "stream_id": 2, "stop_s": 0.4},
			map[string]any{"type": "step", "step_idx": 3, "vad": []any{map[string]any{"horizon_s": 3, "inactivity_prob": 0.2}, map[string]any{"horizon_s": 0.5, "inactivity_prob": 0.9}}},
			map[string]any{"type": "flushed", "flush_id": 7},
			map[string]any{"type": "text", "text": "late", "stream_id": 2},
		} {
			_ = sendFrame(ctx, c, f)
		}
		if readFrame(ctx, c)["type"] != "end_of_stream" {
			t.Error("EOS missing")
		}
		_ = sendFrame(ctx, c, map[string]any{"type": "end_of_stream"})
		_ = sendFrame(ctx, c, map[string]any{"type": "text", "text": "after EOS"})
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu", Language: "en", Format: "pcm_44100"}, stt, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := s.Ready()
	if r.SampleRate != 24000 || r.ConfiguredSampleRate != 44100 || r.Residency != "best-effort; zone=eu" {
		t.Fatalf("ready rates collapsed: %+v", r)
	}
	if err := s.SendAudio(context.Background(), []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	<-seen
	if !errors.Is(s.SendAudio(context.Background(), []byte{1, 2}), ErrFlushPending) {
		t.Fatal("audio accepted during flush")
	}
	close(release)
	e := awaitEvent(t, s, "text")
	if e.Text != "hello" || e.StreamID != 2 || e.StartS != 0.1 || e.Late {
		t.Fatalf("text metadata: %+v", e)
	}
	if awaitEvent(t, s, "end_text").StopS != 0.4 {
		t.Fatal("end_text timing lost")
	}
	e = awaitEvent(t, s, "step")
	if len(e.VAD) != 2 || e.VAD[0].HorizonS != 3 || e.VAD[1].HorizonS != 0.5 {
		t.Fatalf("VAD horizons lost: %+v", e)
	}
	if awaitEvent(t, s, "flushed").FlushID != 7 {
		t.Fatal("flush identity lost")
	}
	if !awaitEvent(t, s, "text").Late {
		t.Fatal("late transcript not marked")
	}
	if !errors.Is(s.SendAudio(context.Background(), []byte{1, 2}), ErrFinalized) {
		t.Fatal("flushed utterance reused for new audio")
	}
	if err := s.End(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitEvent(t, s, "end_of_stream")
	awaitDone(t, s)
	if s.Err() != nil {
		t.Fatal(s.Err())
	}
	if _, ok := <-s.Events(); ok {
		t.Fatal("late post-EOS event delivered")
	}
}

func TestTTSWholePhraseAndPCM(t *testing.T) {
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		setup := readFrame(ctx, c)
		if setup["voice_id"] != "voice" || setup["output_format"] != "pcm_24000" {
			t.Error("TTS setup changed")
		}
		r := readyFrame()
		r["sample_rate"] = 48000
		_ = sendFrame(ctx, c, r)
		phrase := readFrame(ctx, c)
		if phrase["text"] != "Hello world. <flush>" {
			t.Error("whole phrase or flush changed")
		}
		_ = sendFrame(ctx, c, map[string]any{"type": "audio", "audio": "AQI=", "start_s": 0, "stop_s": 0.08})
		_ = sendFrame(ctx, c, map[string]any{"type": "flushed"})
		if readFrame(ctx, c)["type"] != "end_of_stream" {
			t.Error("EOS missing")
		}
		_ = sendFrame(ctx, c, map[string]any{"type": "end_of_stream"})
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu", Voice: "voice", Format: "pcm_24000"}, tts, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Ready().OutputFormat != "pcm_48000" || s.Ready().ConfiguredSampleRate != 24000 {
		t.Fatal("actual output rate not exposed")
	}
	if !errors.Is(s.SendPhrase(context.Background(), string([]byte{255}), true), ErrInvalid) {
		t.Fatal("invalid UTF-8 phrase accepted")
	}
	if err := s.SendPhrase(context.Background(), "Hello world.", true); err != nil {
		t.Fatal(err)
	}
	if e := awaitEvent(t, s, "audio"); string(e.Audio) != string([]byte{1, 2}) {
		t.Fatal("audio changed")
	}
	if awaitEvent(t, s, "flushed").FlushID != 0 {
		t.Fatal("TTS model flush fabricated STT correlation")
	}
	if err := s.End(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitEvent(t, s, "end_of_stream")
	awaitDone(t, s)
	if !errors.Is(s.SendPhrase(context.Background(), "again", false), ErrClosed) {
		t.Fatal("post-EOS text accepted")
	}
}

func TestProviderRejectsMalformedAndUncorrelatedFrames(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ready      bool
	}{
		{"not_ready", `{"type":"text","text":"early"}`, false},
		{"missing_ready_rate", `{"type":"ready","request_id":"r","model_name":"default"}`, false},
		{"json", `{`, true}, {"unknown", `{"type":"surprise"}`, true},
		{"flush", `{"type":"flushed","flush_id":99}`, true},
		{"audio_base64", `{"type":"audio","audio":"!!!!"}`, true},
		{"oversize", strings.Repeat("x", MaxMessageBytes+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
				_ = readFrame(ctx, c)
				if tc.ready {
					_ = sendFrame(ctx, c, readyFrame())
				}
				_ = c.Write(ctx, websocket.MessageText, []byte(tc.body))
				<-ctx.Done()
			})
			s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu", Voice: "voice"}, tts, dial)
			if !tc.ready {
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("unexpected ready failure: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			awaitDone(t, s)
			if !errors.Is(s.Err(), ErrProtocol) {
				t.Fatalf("frame accepted: %v", s.Err())
			}
		})
	}
}

func TestTerminalErrorsAreRedacted(t *testing.T) {
	for _, tc := range []struct {
		message string
		code    int
		want    error
	}{{"API key secret-sentinel expired", 1008, ErrAuth}, {"credits exhausted secret-sentinel", 1008, ErrQuota}, {"internal secret-sentinel", 1011, ErrProvider}} {
		t.Run(tc.message[:3], func(t *testing.T) {
			dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
				_ = readFrame(ctx, c)
				_ = sendFrame(ctx, c, map[string]any{"type": "error", "code": tc.code, "message": tc.message})
			})
			_, err := open(context.Background(), "secret-sentinel", Config{Region: "eu"}, stt, dial)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatalf("untyped or unredacted failure: %v", err)
			}
		})
	}
}

func TestSlowConsumerEOFAndCancellation(t *testing.T) {
	for _, kind := range []string{"slow", "eof", "cancel", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
				_ = readFrame(ctx, c)
				_ = sendFrame(ctx, c, readyFrame())
				switch kind {
				case "slow":
					for i := 0; i < EventCapacity+1; i++ {
						if sendFrame(ctx, c, map[string]any{"type": "text", "text": "bounded"}) != nil {
							return
						}
					}
				case "eof":
					return
				}
				<-ctx.Done()
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "timeout" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 50*time.Millisecond)
				defer stop()
			}
			s, err := open(ctx, "secret-sentinel", Config{Region: "eu"}, stt, dial)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if kind == "cancel" {
				cancel()
			}
			awaitDone(t, s)
			want := map[string]error{"slow": ErrBackpressure, "eof": ErrTransport, "cancel": ErrCancelled, "timeout": ErrTimeout}[kind]
			if !errors.Is(s.Err(), want) {
				t.Fatalf("terminal got %v, want %v", s.Err(), want)
			}
		})
	}
}

func TestHandshakeTimeoutAuthAndRedirect(t *testing.T) {
	t.Run("ready_timeout", func(t *testing.T) {
		dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) { _ = readFrame(ctx, c); <-ctx.Done() })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := open(ctx, "secret-sentinel", Config{Region: "eu"}, stt, dial)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("ready timeout: %v", err)
		}
	})
	for _, status := range []int{401, 429, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var leaked atomic.Int32
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
			defer sink.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", sink.URL)
				w.WriteHeader(status)
				_, _ = w.Write([]byte("secret-sentinel"))
			}))
			defer server.Close()
			dial := func(ctx context.Context, _ string, o *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
				return websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), o)
			}
			_, err := open(context.Background(), "secret-sentinel", Config{Region: "eu"}, stt, dial)
			want := ErrAuth
			if status == 429 {
				want = ErrQuota
			}
			if status == 302 {
				want = ErrProtocol
			}
			if !errors.Is(err, want) || strings.Contains(err.Error(), "secret-sentinel") || leaked.Load() != 0 {
				t.Fatalf("unsafe handshake: %v, redirected=%d", err, leaked.Load())
			}
		})
	}
}

func TestConfigurationAndFrameBoundsBeforeEffect(t *testing.T) {
	for _, tc := range []struct {
		key    string
		config Config
		kind   string
	}{
		{"secret-sentinel", Config{Region: "https://evil.invalid"}, stt},
		{"secret-sentinel\r\nheader", Config{Region: "eu"}, stt},
		{"secret-sentinel", Config{Region: "eu", Language: "en|fr"}, stt},
		{"secret-sentinel", Config{Region: "eu", Format: "wav"}, stt},
		{"secret-sentinel", Config{Region: "eu"}, tts},
	} {
		calls := 0
		dial := func(context.Context, string, *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
			calls++
			return nil, nil, ErrTransport
		}
		_, err := open(context.Background(), tc.key, tc.config, tc.kind, dial)
		if !errors.Is(err, ErrInvalid) || calls != 0 {
			t.Fatalf("invalid configuration crossed trust boundary: %v calls=%d", err, calls)
		}
	}
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		<-ctx.Done()
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu"}, stt, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, audio := range [][]byte{nil, {1}, make([]byte, MaxAudioBytes+2)} {
		if !errors.Is(s.SendAudio(context.Background(), audio), ErrInvalid) {
			t.Fatal("malformed audio accepted")
		}
	}
	if !errors.Is(s.Flush(context.Background(), 0), ErrInvalid) {
		t.Fatal("zero flush ID accepted")
	}
}

func TestCloseDiscardsQueuedOldGeneration(t *testing.T) {
	sent := make(chan struct{})
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		_ = sendFrame(ctx, c, map[string]any{"type": "text", "text": "old"})
		close(sent)
		<-ctx.Done()
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu"}, stt, dial)
	if err != nil {
		t.Fatal(err)
	}
	<-sent
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, s)
	if _, open := <-s.Events(); open {
		t.Fatal("old-generation event survived Close")
	}
	if !errors.Is(s.Err(), ErrClosed) {
		t.Fatalf("Close outcome: %v", s.Err())
	}
}

func TestEOSQueueOverflowIsNotSuccess(t *testing.T) {
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		_ = readFrame(ctx, c)
		for i := 0; i < EventCapacity; i++ {
			_ = sendFrame(ctx, c, map[string]any{"type": "text", "text": "filled"})
		}
		_ = sendFrame(ctx, c, map[string]any{"type": "end_of_stream"})
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu"}, stt, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.End(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, s)
	if !errors.Is(s.Err(), ErrBackpressure) {
		t.Fatalf("lost terminal event became success: %v", s.Err())
	}
}

func TestCancelledWriterAndInputSize(t *testing.T) {
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		<-ctx.Done()
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu", Voice: "voice"}, tts, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !errors.Is(s.SendPhrase(context.Background(), strings.Repeat("x", MaxPhraseBytes+1), false), ErrInvalid) {
		t.Fatal("unbounded phrase accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.SendPhrase(ctx, "bounded", false); !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancelled writer: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, s)
}
