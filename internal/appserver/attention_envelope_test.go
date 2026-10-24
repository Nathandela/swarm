package appserver_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/coder/websocket"
)

func TestAttentionRejectsAmbiguousEnvelopeBeforeCallbacks(t *testing.T) {
	for _, raw := range []string{
		`{"method":"old","method":"finished","params":{}}`,
		`{"Method":"finished","params":{}}`,
		`{"method":"finished","params":{},"Params":{}}`,
		`{"method":"finished","params":{},"params":{}}`,
		`{"method":"finished","id":1,"ID":2,"params":{}}`,
		`{"method":null,"id":1,"result":{}}`,
		`{"method":"","id":1,"result":{}}`,
		`{"id":1,"params":{"turn":{"status":"failed"}}}`,
		`{"id":null,"result":{}}`, `{"id":true,"result":{}}`, `{"id":123,"error":null}`,
		`{"id":1,"result":{},"error":{"code":1,"message":"ambiguous"}}`,
		`{"method":`, `{}`, "BINARY",
	} {
		t.Run(raw, func(t *testing.T) {
			f := newFakeServer(t)
			f.mu.Lock()
			f.onOpen = func(c *fakeConn) {
				typ := websocket.MessageText
				if raw == "BINARY" {
					typ = websocket.MessageBinary
				}
				_ = c.ws.Write(c.ctx, typ, []byte(raw))
				_ = c.ws.Write(c.ctx, websocket.MessageText, []byte(`{"method":"finished","params":{}}`))
			}
			f.mu.Unlock()
			closed := make(chan struct{})
			var delivered atomic.Int32
			c, err := appserver.Dial(context.Background(), f.sock, appserver.Options{OnFrame: func(string, json.RawMessage) { delivered.Add(1) }, OnClose: func(error) { close(closed) }})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("invalid stream remained usable")
			}
			if delivered.Load() != 0 {
				t.Fatal("invalid stream authorized later evidence")
			}
		})
	}
}

func TestAttentionReceivesOriginalEnvelopeAndBoundsUnknownContent(t *testing.T) {
	f := newFakeServer(t)
	const thread = "019a1234-1234-7123-8123-123456789abc"
	base := `{"method":"thread/status/changed","params":{"threadId":"` + thread + `","status":{"type":"idle"}}}`
	f.mu.Lock()
	f.onOpen = func(c *fakeConn) {
		for _, raw := range []string{
			strings.Replace(base, `"method":`, `"future":"`+strings.Repeat("x", 2<<20)+`","method":`, 1),
			strings.Replace(base, `"method":`, `"future":`+strings.Repeat("[", 40)+`0`+strings.Repeat("]", 40)+`,"method":`, 1),
			base,
			`{"method":"finished","params":{}}`,
		} {
			if err := c.ws.Write(c.ctx, websocket.MessageText, []byte(raw)); err != nil {
				return
			}
		}
	}
	f.mu.Unlock()
	finished := make(chan struct{})
	var normalized, split atomic.Int32
	source := codex.New().(adapter.TypedStatusSource)
	c, err := appserver.Dial(context.Background(), f.sock, appserver.Options{
		OnFrame: func(method string, raw json.RawMessage) {
			if method == "finished" {
				close(finished)
				return
			}
			value, claimed := source.EventStatus(adapter.HookPayload{Event: method, Raw: raw}, thread)
			if claimed && len(value.Dimensions) > 0 {
				normalized.Add(1)
			}
		},
		OnNotify: func(_ string, _ json.RawMessage) { split.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("original frames were not delivered")
	}
	if normalized.Load() != 1 || split.Load() != 0 {
		t.Fatalf("original envelope bounds or exclusive callback lost: normalized=%d split=%d", normalized.Load(), split.Load())
	}
}
