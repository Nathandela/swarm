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
			w.Write([]byte(`{"id":"55","channel_id":"22","content":"original @everyone","author":{"id":"77","username":"User"},"timestamp":"2026-01-01T00:00:00Z"}`))
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
		w.Write([]byte(`{"id":"66"}`))
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
			w.Write([]byte(`{"retry_after":0.05,"global":false}`))
			return
		}
		if calls == 2 {
			w.Write([]byte(`{"id":"66"}`))
			return
		}
		w.WriteHeader(400)
		w.Write([]byte(`test-secret`))
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
		defer conn.Close()
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
	defer c.Close()
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
			w.Write([]byte(`{"retry_after":1}`))
			return
		}
		w.Write([]byte(`null`))
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
	defer conn.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("gateway ignored cancellation")
	}
	c.Close()
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
		defer conn.Close()
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
		w.Write([]byte(`{"id":"66"}`))
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
