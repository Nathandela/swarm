package discord

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Configure before Open. Callbacks run on the Gateway reader and must only
// update local state and signal workers. Network I/O belongs on those workers.
type callGateway struct {
	guild, room string
	dispatch    func(gatewayFrame)
	disconnect  func()
	unavailable func(string)

	mu     sync.Mutex
	active *callGatewaySession
}

type callGatewaySession struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

type callGatewaySessionKey struct{}

// Bind a call worker to the roster generation that authorized it, before
// starting the worker or handing the context to the voice helper.
func (c *DiscordClient) callVoiceContext(ctx context.Context) context.Context {
	var session *callGatewaySession
	if g := c.callGateway; g != nil {
		g.mu.Lock()
		session = g.active
		g.mu.Unlock()
	}
	return context.WithValue(ctx, callGatewaySessionKey{}, session)
}

func (c *DiscordClient) setCallVoiceState(ctx context.Context, guild, channel string) error {
	g := c.callGateway
	if g == nil || !snowflake(guild) || !snowflake(g.room) || guild != g.guild || channel != "" && channel != g.room {
		return ErrInvalid
	}
	g.mu.Lock()
	active := g.active
	bound, boundOK := ctx.Value(callGatewaySessionKey{}).(*callGatewaySession)
	g.mu.Unlock()
	if active == nil || boundOK && active != bound || active.ctx.Err() != nil {
		return errors.New("discord: call Gateway roster unavailable")
	}

	var room *string
	if channel != "" {
		room = &channel
	}
	// Canceling a media generation must not cancel an in-flight shared socket write.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(active.ctx), time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := active.ctx.Err(); err != nil {
		return err
	}
	return wsjson.Write(writeCtx, active.conn, map[string]any{"op": 4, "d": map[string]any{
		"guild_id": guild, "channel_id": room, "self_mute": false, "self_deaf": false,
	}})
}

func (g *callGateway) invalidate() {
	g.mu.Lock()
	if g.active != nil {
		g.active.cancel()
		g.active = nil
	}
	g.mu.Unlock()
}

func (g *callGateway) handle(ctx context.Context, conn *websocket.Conn, f gatewayFrame) {
	var scope struct {
		ID          string          `json:"id"`
		GuildID     string          `json:"guild_id"`
		Unavailable bool            `json:"unavailable"`
		VoiceStates json.RawMessage `json:"voice_states"`
	}
	switch f.Type {
	case "READY", "GUILD_CREATE", "GUILD_UPDATE", "GUILD_DELETE",
		"VOICE_STATE_UPDATE", "VOICE_SERVER_UPDATE", "GUILD_MEMBER_ADD", "GUILD_MEMBER_UPDATE", "GUILD_MEMBER_REMOVE",
		"GUILD_ROLE_CREATE", "GUILD_ROLE_UPDATE", "GUILD_ROLE_DELETE", "CHANNEL_UPDATE", "CHANNEL_DELETE",
		"THREAD_CREATE", "THREAD_UPDATE", "THREAD_DELETE", "THREAD_LIST_SYNC", "THREAD_MEMBER_UPDATE", "THREAD_MEMBERS_UPDATE":
	default:
		return
	}
	if f.Type != "READY" {
		err := json.Unmarshal(f.Data, &scope)
		guild := scope.GuildID
		switch f.Type {
		case "GUILD_CREATE", "GUILD_UPDATE", "GUILD_DELETE":
			guild = scope.ID
		}
		if err == nil && snowflake(guild) && guild != g.guild {
			return
		}
		if err != nil || !snowflake(guild) || f.Sequence == nil {
			g.invalidate()
			if g.disconnect != nil {
				g.disconnect()
			}
			return
		}
	}

	switch f.Type {
	case "GUILD_CREATE":
		g.invalidate()
		if !scope.Unavailable && len(scope.VoiceStates) > 0 && scope.VoiceStates[0] == '[' {
			sessionCtx, cancel := context.WithCancel(ctx)
			g.mu.Lock()
			g.active = &callGatewaySession{conn: conn, ctx: sessionCtx, cancel: cancel}
			g.mu.Unlock()
		}
	case "GUILD_DELETE":
		g.invalidate()
	case "GUILD_UPDATE":
		if scope.Unavailable {
			g.invalidate()
		}
	case "VOICE_STATE_UPDATE", "VOICE_SERVER_UPDATE":
		g.mu.Lock()
		active := g.active != nil
		g.mu.Unlock()
		if !active {
			return
		}
	}
	if g.dispatch != nil {
		g.dispatch(f)
	}
}

func callPrivateDispatch(event string) bool {
	switch event {
	case "GUILD_CREATE", "VOICE_STATE_UPDATE", "VOICE_SERVER_UPDATE":
		return true
	}
	return false
}
