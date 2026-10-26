package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// This server speaks the real Gateway/REST wire; only the external coding worker is replaced.
func TestConnectedMockDiscordContextEndorsementAndDeliberateReply(t *testing.T) {
	s, workers, _ := serviceRig(t)
	dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "discord-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s.StateDir = dir
	text := "<@100> Why do retries fail? 🙂"
	events := make(chan any, 4)
	posts := make(chan map[string]any, 16)
	registered := make(chan struct{}, 1)
	var ids atomic.Int32
	var server *httptest.Server
	upgrader := websocket.Upgrader{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"})
			return
		}
		if r.URL.Path == "/ws" {
			conn, e := upgrader.Upgrade(w, r, nil)
			if e != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			var writeMu sync.Mutex
			write := func(v any) error { writeMu.Lock(); defer writeMu.Unlock(); return conn.WriteJSON(v) }
			if write(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 100}}) != nil {
				return
			}
			var identify map[string]any
			if conn.ReadJSON(&identify) != nil {
				return
			}
			if identify["op"] != float64(2) {
				t.Errorf("identify=%v", identify)
			}
			if write(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"v": 10, "session_id": "session", "application": map[string]string{"id": "100"}, "user": map[string]any{"id": "100", "username": "Atlas", "bot": true}}}) != nil {
				return
			}
			done := make(chan struct{})
			defer close(done)
			go func() {
				for {
					select {
					case event := <-events:
						if write(event) != nil {
							return
						}
					case <-done:
						return
					}
				}
			}()
			for {
				var frame map[string]any
				if conn.ReadJSON(&frame) != nil {
					return
				}
				if frame["op"] == float64(1) {
					if write(map[string]any{"op": 11, "d": nil}) != nil {
						return
					}
				}
			}
		}
		if strings.HasPrefix(r.URL.Path, "/applications/100/commands") {
			select {
			case registered <- struct{}{}:
			default:
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "command"})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/channels/20/messages/30" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "30", "channel_id": "20", "guild_id": "10", "content": text, "author": map[string]string{"id": "2", "username": "Alice"}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/channels/20/messages" {
			var body map[string]any
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			select {
			case posts <- body:
			default:
				t.Error("unexpected publication flood")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprint(ids.Add(1))})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/interactions/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/webhooks/") {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "ephemeral"})
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	client, _ := NewDiscordClient("fake-local-token")
	client.apiBase = server.URL
	s.Messenger = client
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- runConnected(ctx, s, client) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("connector failed to stop")
		}
	})
	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("commands not registered")
	}
	events <- map[string]any{"op": 0, "s": 2, "t": "MESSAGE_CREATE", "d": map[string]any{"id": "30", "guild_id": "10", "channel_id": "20", "content": text, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "author": map[string]any{"id": "2", "username": "Alice"}, "mentions": []map[string]string{{"id": "100"}}}}
	var accepted map[string]any
	select {
	case accepted = <-posts:
	case <-time.After(5 * time.Second):
		t.Fatal("no native submission acknowledgement")
	}
	workers.mu.Lock()
	if len(workers.sends) != 1 {
		workers.mu.Unlock()
		t.Fatal("wrong native submission count")
	}
	ref := workers.sends[0].Origin.RequestID
	workers.mu.Unlock()
	var original LocalContext
	if err = CallLocal(ctx, dir, "context", LocalContextRequest{Ref: ref}, &original); err != nil {
		t.Fatal(err)
	}
	if len(original.Page.Messages) != 1 || original.Page.Messages[0].Text != text || original.Request.Author.ID != "2" {
		t.Fatalf("context=%+v", original)
	}
	if accepted["content"] == text {
		t.Fatal("source copied as unsolicited worker output")
	}
	events <- map[string]any{"op": 0, "s": 3, "t": "INTERACTION_CREATE", "d": map[string]any{"id": "owner-click", "application_id": "100", "type": 3, "token": "test-interaction", "guild_id": "10", "channel_id": "20", "member": map[string]any{"user": map[string]string{"id": "1", "username": "Owner"}}, "data": map[string]any{"custom_id": "endorse:" + ref, "component_type": 2}}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		workers.mu.Lock()
		n := len(workers.sends)
		var kind string
		if n > 1 {
			kind = workers.sends[1].Origin.Kind
		}
		workers.mu.Unlock()
		if n == 2 {
			if kind != "owner_endorsed" {
				t.Fatal(kind)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner click did not endorse")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var publication Publication
	if err = CallLocal(ctx, dir, "reply", LocalReply{Ref: ref, ID: "final", Body: "The explicit answer. 🙂"}, &publication); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-posts:
		if body["content"] != "The explicit answer. 🙂" || body["enforce_nonce"] != true {
			t.Fatalf("reply=%v", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no explicit publication")
	}
	if err = CallLocal(ctx, dir, "reply", LocalReply{Ref: ref, ID: "final", Body: "The explicit answer. 🙂"}, &publication); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-posts:
		t.Fatalf("duplicate post=%v", duplicate)
	default:
	}
}
