package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

func mockEndpoints(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s.URL
}
func TestDiscordREST(t *testing.T) {
	count := 0
	base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot test-secret" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"id":"55","channel_id":"22","content":"original @everyone","author":{"id":"77","username":"User"},"timestamp":"2026-01-01T00:00:00Z"}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mentions := body["allowed_mentions"].(map[string]any)
		if parse, ok := mentions["parse"].([]any); !ok || len(parse) != 0 {
			t.Errorf("mentions %#v", mentions)
		}
		if r.Method == "POST" {
			if r.URL.Path != "/channels/22/messages" || body["nonce"] != "nonce" || body["enforce_nonce"] != true {
				t.Errorf("post %s %#v", r.URL.Path, body)
			}
			if len(body["components"].([]any)) != 2 {
				t.Errorf("components %#v", body)
			}
		} else if r.Method != "PATCH" || r.URL.Path != "/channels/22/messages/66" {
			t.Errorf("edit %s %s", r.Method, r.URL.Path)
		}
		count++
		_, _ = w.Write([]byte(`{"id":"66"}`))
	})
	c, err := NewDiscordClient("test-secret")
	c.apiBase = base
	if err != nil {
		t.Fatal(err)
	}
	route := Route{GuildID: "11", ChannelID: "22", MessageID: "55"}
	actions := []Action{{ID: "approve", Label: "Approve"}, {ID: "pick", Label: "Pick", Choices: []Choice{{ID: "worker", Label: "Worker"}}}}
	id, err := c.Post(context.Background(), Outbound{Route: route, Text: "@everyone", Actions: actions, Nonce: "nonce"})
	if err != nil || id != "66" {
		t.Fatalf("post %q %v", id, err)
	}
	if _, err = c.Edit(context.Background(), Route{ChannelID: "22", MessageID: "66"}, "@everyone", nil); err != nil {
		t.Fatal(err)
	}
	in, err := c.Message(context.Background(), route)
	if err != nil || in.Actor.ID != "77" || in.Actor.Name != "User" || in.Text != "original @everyone" || in.Route != route || in.At.IsZero() {
		t.Fatalf("message %+v %v", in, err)
	}
	if count != 2 {
		t.Fatal(count)
	}
}
func TestDiscordRateLimitAndRedaction(t *testing.T) {
	calls := 0
	base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"retry_after":0.05,"global":false}`))
			return
		}
		if calls == 2 {
			_, _ = w.Write([]byte(`{"id":"66"}`))
			return
		}
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`test-secret`))
	})
	c, _ := NewDiscordClient("test-secret")
	c.apiBase = base
	start := time.Now()
	_, err := c.Post(context.Background(), Outbound{Route: Route{ChannelID: "22"}, Text: "hi", Nonce: "1"})
	if err != nil || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("retry %v after %v", err, time.Since(start))
	}
	_, err = c.Post(context.Background(), Outbound{Route: Route{ChannelID: "22"}, Text: "hi", Nonce: "2"})
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("error %v", err)
	}
}

func TestDiscordGatewayReconnect(t *testing.T) {
	var connections atomic.Int32
	var server *httptest.Server
	upgrader := websocket.Upgrader{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"})
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		n := connections.Add(1)
		if err = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 50}}); err != nil {
			return
		}
		var identify struct {
			Op   int `json:"op"`
			Data struct {
				Token string `json:"token"`
			} `json:"d"`
		}
		if err = conn.ReadJSON(&identify); err != nil {
			return
		}
		if n == 1 && (identify.Op != 2 || identify.Data.Token != "Bot test-secret") {
			t.Errorf("invalid identify op %d", identify.Op)
		}
		if n == 2 && identify.Op != 6 {
			t.Errorf("resume op %d", identify.Op)
		}
		if n == 1 {
			_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"v": 9, "session_id": "session", "user": map[string]any{"id": "1", "username": "bot", "bot": true}}})
		}
		_ = conn.WriteJSON(map[string]any{"op": 0, "s": int(n) + 1, "t": "MESSAGE_CREATE", "d": map[string]any{"id": "m", "channel_id": "22", "content": "hello", "author": map[string]any{"id": "77", "username": "User"}, "timestamp": "2026-01-01T00:00:00Z"}})
		for {
			var msg struct {
				Op int `json:"op"`
			}
			if conn.ReadJSON(&msg) != nil {
				return
			}
			if msg.Op == 1 {
				_ = conn.WriteJSON(map[string]any{"op": 11, "d": nil})
				if n == 1 {
					return
				}
			}
		}
	}))
	defer server.Close()

	c, err := NewDiscordClient("test-secret")
	c.apiBase = server.URL
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan string, 2)
	c.OnMessage = func(m *discordgo.MessageCreate) { events <- m.Content }
	if err = c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for i := 0; i < 2; i++ {
		select {
		case got := <-events:
			if got != "hello" {
				t.Fatal(got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("gateway did not deliver after reconnect")
		}
	}
}

func TestCancellationAndNullResponses(t *testing.T) {
	first := make(chan struct{}, 1)
	base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			first <- struct{}{}
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"retry_after":1}`))
			return
		}
		_, _ = w.Write([]byte(`null`))
	})
	c, _ := NewDiscordClient("test-secret")
	c.apiBase = base
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Post(ctx, Outbound{Route: Route{ChannelID: "22"}, Nonce: "1"}); done <- err }()
	<-first
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("rate limit ignored cancellation")
	}
	if _, err := c.Edit(context.Background(), Route{ChannelID: "22", MessageID: "55"}, "hi", nil); err == nil {
		t.Fatal("accepted null edit")
	}
	if _, err := c.Message(context.Background(), Route{ChannelID: "22", MessageID: "55"}); err == nil {
		t.Fatal("accepted null message")
	}
}
func TestGatewayHelloCancellation(t *testing.T) {
	accepted := make(chan *websocket.Conn, 1)
	var server *httptest.Server
	up := websocket.Upgrader{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"})
			return
		}
		conn, err := up.Upgrade(w, r, nil)
		if err == nil {
			accepted <- conn
		}
	}))
	defer server.Close()
	c, _ := NewDiscordClient("test-secret")
	c.apiBase = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Open(ctx) }()
	conn := <-accepted
	defer func() { _ = conn.Close() }()
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("gateway ignored cancellation")
	}
	_ = c.Close()
}

func TestCloseStopsReconnect(t *testing.T) {
	var server *httptest.Server
	up := websocket.Upgrader{}
	var attempts atomic.Int32
	drop := make(chan struct{})
	failed := make(chan struct{})
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"})
			return
		}
		if attempts.Add(1) > 1 {
			w.WriteHeader(503)
			select {
			case <-failed:
			default:
				close(failed)
			}
			return
		}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 10000}})
		var data any
		if conn.ReadJSON(&data) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"session_id": "session", "user": map[string]any{"id": "1"}}})
		<-drop
	}))
	defer server.Close()
	c, _ := NewDiscordClient("test-secret")
	c.apiBase = server.URL
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(drop)
	select {
	case <-failed:
	case <-time.After(4 * time.Second):
		t.Fatal("no reconnect attempt")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	before := attempts.Load()
	time.Sleep(1200 * time.Millisecond)
	if attempts.Load() != before {
		t.Fatal("reconnected after close")
	}
}

func TestRateLimitHeadersAndCloseDuringHello(t *testing.T) {
	calls := 0
	base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset-After", "0.1")
		_, _ = w.Write([]byte(`{"id":"66"}`))
	})
	c, _ := NewDiscordClient("secret")
	c.apiBase = base
	if _, err := c.Post(context.Background(), Outbound{Route: Route{ChannelID: "1"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Post(ctx, Outbound{Route: Route{ChannelID: "1"}}); err == nil || calls != 1 {
		t.Fatalf("rate wait: calls %d error %v", calls, err)
	}
}

func TestReviewGatewayRecovery(t *testing.T) {
	for _, mode := range []string{"expired-session", "resume-url", "large-event"} {
		t.Run(mode, func(t *testing.T) {
			var server *httptest.Server
			up := websocket.Upgrader{}
			var attempts atomic.Int32
			observed := make(chan struct {
				op   int
				path string
			}, 3)
			messages := make(chan struct{}, 3)
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/gateway" {
					_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"})
					return
				}
				conn, e := up.Upgrade(w, r, nil)
				if e != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				n := attempts.Add(1)
				_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 10000}})
				var identify struct {
					Op int `json:"op"`
				}
				if conn.ReadJSON(&identify) != nil {
					return
				}
				if n > 1 {
					observed <- struct {
						op   int
						path string
					}{identify.Op, r.URL.Path}
				}
				_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"session_id": "session", "resume_gateway_url": "ws" + strings.TrimPrefix(server.URL, "http") + "/resume", "user": map[string]any{"id": "1"}}})
				if n == 1 {
					switch mode {
					case "expired-session":
						_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4009, "expired"), time.Now().Add(time.Second))
						return
					case "resume-url":
						_ = conn.WriteJSON(map[string]any{"op": 7, "d": nil})
					case "large-event":
						_ = conn.WriteJSON(map[string]any{"op": 0, "s": 2, "t": "GUILD_CREATE", "d": map[string]any{"id": "1", "name": strings.Repeat("a", 40000)}})
						_ = conn.WriteJSON(map[string]any{"op": 0, "s": 3, "t": "MESSAGE_CREATE", "d": map[string]any{"id": "2", "channel_id": "3", "content": "hello", "author": map[string]any{"id": "4"}}})
					}
				}
				var frame any
				for {
					if conn.ReadJSON(&frame) != nil {
						return
					}
				}
			}))
			defer server.Close()
			c, _ := NewDiscordClient("test-secret")
			c.apiBase = server.URL
			c.OnMessage = func(*discordgo.MessageCreate) { messages <- struct{}{} }
			if e := c.Open(context.Background()); e != nil {
				t.Fatal(e)
			}
			defer func() { _ = c.Close() }()
			if mode == "large-event" {
				select {
				case <-messages:
				case <-time.After(300 * time.Millisecond):
					t.Error("large ignored guild event disconnects before following message")
				}
				return
			}
			select {
			case got := <-observed:
				if mode == "expired-session" && got.op != 2 {
					t.Errorf("expired session resumed with opcode %d", got.op)
				}
				if mode == "resume-url" && got.path != "/resume" {
					t.Errorf("resume used wrong gateway %s", got.path)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no recovery attempt")
			}
		})
	}
}

func TestGatewayFatalAndMessageChanges(t *testing.T) {
	var server *httptest.Server
	up := websocket.Upgrader{}
	var attempts atomic.Int32
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"})
			return
		}
		attempts.Add(1)
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 10000}})
		var input any
		if conn.ReadJSON(&input) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"session_id": "session"}})
		for i, typ := range []string{"MESSAGE_UPDATE", "MESSAGE_DELETE"} {
			_ = conn.WriteJSON(map[string]any{"op": 0, "s": i + 2, "t": typ, "d": map[string]any{"id": "5", "channel_id": "6", "content": ""}})
		}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4004, "bad token"), time.Now().Add(time.Second))
	}))
	defer server.Close()
	c, _ := NewDiscordClient("secret")
	c.apiBase = server.URL
	changed := make(chan string, 2)
	fatal := make(chan error, 1)
	c.OnMessageUpdate = func(m *discordgo.MessageUpdate) { changed <- m.ID }
	c.OnMessageDelete = func(m *discordgo.MessageDelete) { changed <- m.ID }
	c.OnError = func(err error) { fatal <- err }
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for i := 0; i < 2; i++ {
		select {
		case id := <-changed:
			if id != "5" {
				t.Fatal(id)
			}
		case <-time.After(time.Second):
			t.Fatal("missing message change")
		}
	}
	select {
	case err := <-fatal:
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing fatal error")
	}
	time.Sleep(1200 * time.Millisecond)
	if attempts.Load() != 1 {
		t.Fatal("retried fatal close")
	}
}
