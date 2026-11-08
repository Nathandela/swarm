package gradium

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestProviderReviewClosePurgesAlreadyCompletedAudio(t *testing.T) {
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		_ = readFrame(ctx, c)
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, map[string]any{"type": "audio", "audio": "AQI="})
		_ = sendFrame(ctx, c, map[string]any{"type": "end_of_stream"})
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu", Voice: "voice"}, tts, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.SendPhrase(context.Background(), "done", false); err != nil {
		t.Fatal(err)
	}
	if err = s.End(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, s)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if e, ok := <-s.Events(); ok {
		t.Fatalf("Close returned with old queued event still readable: %s", e.Type)
	}
}
func TestProviderReviewInvalidUnicodeTranscriptRejected(t *testing.T) {
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"text","text":"\ud800"}`))
		<-ctx.Done()
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu"}, stt, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	e, ok := <-s.Events()
	if ok {
		t.Fatalf("invalid Unicode became accepted transcript %q", e.Text)
	}
	if !errors.Is(s.Err(), ErrProtocol) {
		t.Fatal(s.Err())
	}
}
func TestProviderReviewInlineFlushRejectedBeforeEffect(t *testing.T) {
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, map[string]any{"type": "flushed"})
		<-ctx.Done()
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu", Voice: "voice"}, tts, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.SendPhrase(context.Background(), "The tag is <flush>.", false); errors.Is(err, ErrInvalid) {
		return
	}
	awaitDone(t, s)
	if errors.Is(s.Err(), ErrProtocol) {
		t.Fatal("accepted inline flush tag generated a protocol failure after dispatch")
	}
}

func TestProviderReviewUnicodePreserved(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		valid     bool
	}{
		{`{"type":"text","text":"\udc00"}`, "", false},
		{`{"type":"text","text":"\ud800\ud800"}`, "", false},
		{`{"type":"text","text":"\ud800\\udc00"}`, "", false},
		{`{"type":"text","text":"\ud83d\ude80"}`, "🚀", true},
		{`{"type":"text","text":"\\ud800"}`, `\ud800`, true},
		{`{"type":"text","text":"🚀"}`, "🚀", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
				_ = readFrame(ctx, c)
				_ = sendFrame(ctx, c, readyFrame())
				_ = c.Write(ctx, websocket.MessageText, []byte(tc.raw))
				<-ctx.Done()
			})
			s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu"}, stt, dial)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			e, ok := <-s.Events()
			if tc.valid {
				if !ok || e.Text != tc.want {
					t.Fatalf("valid Unicode changed: %+v error=%v", e, s.Err())
				}
			} else if ok || !errors.Is(s.Err(), ErrProtocol) {
				t.Fatalf("invalid Unicode accepted: %+v error=%v", e, s.Err())
			}
		})
	}
}

func TestProviderReviewReservedFlushRejected(t *testing.T) {
	dial := socketFixture(t, func(ctx context.Context, c *websocket.Conn) {
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		<-ctx.Done()
	})
	s, err := open(context.Background(), "secret-sentinel", Config{Region: "eu", Voice: "voice"}, tts, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, text := range []string{"The tag is <flush>.", "one <flush> two <flush>"} {
		for _, flush := range []bool{false, true} {
			if !errors.Is(s.SendPhrase(context.Background(), text, flush), ErrInvalid) {
				t.Fatal("reserved provider syntax crossed write boundary")
			}
		}
	}
}

func TestProviderReviewCharacterizationRedactsErrors(t *testing.T) {
	frame := map[string]any{"type": "error", "message": "secret-sentinel", "code": float64(1008), "request_id": "secret-sentinel", "audio": "secret-sentinel", "extra": "secret-sentinel"}
	output, err := json.Marshal(characterizationFields(frame))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), "secret-sentinel") || string(output) != `{"code":1008,"type":"error"}` {
		t.Fatalf("diagnostic leaked provider error: %s", output)
	}
	output, _ = json.Marshal(characterizationFields(map[string]any{"type": "secret-sentinel", "code": "secret-sentinel"}))
	if strings.Contains(string(output), "secret-sentinel") {
		t.Fatal("unknown provider type leaked")
	}
}

func TestProviderReviewReadFailureBlockedWriteConcurrentClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	type connectionKey struct{}
	halfClose := make(chan *net.TCPConn, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		raw := r.Context().Value(connectionKey{}).(*net.TCPConn)
		_ = raw.SetReadBuffer(1024)
		_ = readFrame(ctx, c)
		_ = sendFrame(ctx, c, readyFrame())
		halfClose <- raw
		<-ctx.Done()
	}))
	server.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connectionKey{}, c)
	}
	server.Start()
	t.Cleanup(func() { cancel(); server.Close() })
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
	defer func() { _ = s.Close() }()
	written := make(chan error, 1)
	go func() {
		for i := 0; i < 256; i++ {
			if err := s.SendAudio(context.Background(), make([]byte, MaxAudioBytes)); err != nil {
				written <- err
				return
			}
		}
		written <- nil
	}()
	active := func() context.Context {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.activeWrite == nil {
			return nil
		}
		return s.activeWrite
	}
	deadline := time.Now().Add(2 * time.Second)
	blocked := false
	var held context.Context
	for time.Now().Before(deadline) {
		before := active()
		time.Sleep(20 * time.Millisecond)
		after := active()
		if before != nil && after != nil && before.Done() == after.Done() {
			blocked = true
			held = before
			break
		}
	}
	if !blocked {
		t.Fatal("fixture did not establish a blocked background writer")
	}
	if deadline, ok := held.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		t.Fatal("background write has no five-second internal bound")
	}
	raw := <-halfClose
	closed := make(chan struct{})
	shutdown := time.NewTimer(time.Second)
	defer shutdown.Stop()
	go func() { _ = s.Close(); close(closed) }()
	_ = raw.CloseWrite()
	select {
	case <-closed:
	case <-shutdown.C:
		t.Fatal("Close waited behind blocked writer/read failure")
	}
	select {
	case err := <-written:
		if err == nil || !errors.Is(err, s.Err()) {
			t.Fatalf("teardown cause mismatch: write=%v retained=%v", err, s.Err())
		}
	case <-shutdown.C:
		t.Fatal("background writer did not stop")
	}
	awaitDone(t, s)
}
