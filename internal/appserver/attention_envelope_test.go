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
	f := newFakeServer(t)
	f.mu.Lock()
	f.onOpen = func(c *fakeConn) {
		for _, raw := range []string{
			`{"method":"old","method":"thread/status/changed","params":{}}`,
			`{"Method":"thread/status/changed","params":{}}`,
			`{"method":"thread/status/changed","params":{},"Params":{}}`,
			`{"method":"thread/status/changed","params":{},"params":{}}`,
			`{"method":"item/tool/requestUserInput","id":1,"id":2,"params":{}}`,
			`{"method":"item/tool/requestUserInput","id":1,"ID":2,"params":{}}`,
			`{"method":"finished","params":{}}`,
		} {
			if err := c.ws.Write(c.ctx, websocket.MessageText, []byte(raw)); err != nil {
				return
			}
		}
	}
	f.mu.Unlock()
	finished := make(chan struct{})
	var malformed atomic.Int32
	c, err := appserver.Dial(context.Background(), f.sock, appserver.Options{
		OnNotify: func(method string, _ json.RawMessage) {
			if method == "finished" {
				close(finished)
			} else {
				malformed.Add(1)
			}
		},
		OnRequest: func(_ json.RawMessage, _ string, _ json.RawMessage) { malformed.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("valid frame did not survive malformed predecessors")
	}
	if malformed.Load() != 0 {
		t.Fatalf("%d ambiguous envelopes reached callbacks", malformed.Load())
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
	case <-time.After(2 * time.Second):
		t.Fatal("original frames were not delivered")
	}
	if normalized.Load() != 1 || split.Load() != 0 {
		t.Fatalf("original envelope bounds or exclusive callback lost: normalized=%d split=%d", normalized.Load(), split.Load())
	}
}
