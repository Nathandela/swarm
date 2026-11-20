package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestCallIntentDeniedKeepsTeamTextOnline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 18*time.Second)
	defer cancel()
	var attempts atomic.Int32
	var second atomic.Int64
	delivered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws://" + r.Host + "/ws"})
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		if wsjson.Write(ctx, ws, map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var ident struct {
			Op int
			D  struct{ Intents discordgo.Intent }
		}
		if wsjson.Read(ctx, ws, &ident) != nil || ident.Op != 2 {
			return
		}
		if attempts.Add(1) == 1 {
			if ident.D.Intents&discordgo.IntentsGuildMembers == 0 {
				t.Error("team call did not request membership updates")
			}
			_ = ws.Close(4014, "fixture denies privileged member intent")
			return
		}
		second.Store(int64(ident.D.Intents))
		if wsjson.Write(ctx, ws, map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]string{"session_id": "text-only"}}) != nil {
			return
		}
		_ = wsjson.Write(ctx, ws, map[string]any{"op": 0, "s": 2, "t": "MESSAGE_CREATE", "d": map[string]string{"id": "99"}})
		<-ctx.Done()
	}))
	defer server.Close()
	defer cancel()
	c, err := NewDiscordClient("fixture")
	if err != nil {
		t.Fatal(err)
	}
	c.apiBase = server.URL
	c.callGateway = &callGateway{guild: "10", room: "20"}
	c.onDispatch = func(f gatewayFrame, _ string) error {
		if f.Type == "MESSAGE_CREATE" {
			delivered <- struct{}{}
		}
		return nil
	}
	defer func() { _ = c.Close() }()
	if err = c.Open(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("text dispatch missing after voice permission was refused")
	}
	base := discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	if attempts.Load() != 2 || discordgo.Intent(second.Load()) != base || !c.callDisabled.Load() {
		t.Fatal("voice denial did not downgrade to existing team text intents")
	}
}
