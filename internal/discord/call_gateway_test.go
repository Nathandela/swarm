package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func receiveCallGatewayFixture[T any](t *testing.T, ctx context.Context, queue <-chan T) T {
	t.Helper()
	select {
	case item := <-queue:
		return item
	case <-ctx.Done():
		t.Fatal("timed out waiting for Gateway fixture")
		var zero T
		return zero
	}
}

func TestCallGatewayRosterAndTextRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type connection struct {
		socket    *websocket.Conn
		writes    chan gatewayFrame
		heartbeat chan gatewayFrame
	}
	connections := make(chan connection, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws://" + r.Host + "/ws"})
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = ws.CloseNow() }()
		if err = wsjson.Write(ctx, ws, map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 150}}); err != nil {
			t.Error(err)
			return
		}
		writes := make(chan gatewayFrame, 32)
		heartbeat := make(chan gatewayFrame, 1)
		connections <- connection{ws, writes, heartbeat}
		for {
			var f gatewayFrame
			if wsjson.Read(ctx, ws, &f) != nil {
				return
			}
			if f.Op == 1 {
				if wsjson.Write(ctx, ws, map[string]int{"op": 11}) != nil {
					return
				}
				select {
				case heartbeat <- f:
				default:
				}
				continue
			}
			writes <- f
		}
	}))
	defer server.Close()

	client, err := NewDiscordClient("test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	client.apiBase = server.URL
	client.UsePersonalDMs()

	voice := make(chan string, 32)
	text := make(chan string, 32)
	client.callGateway = &callGateway{
		guild: "10",
		room:  "20",
		dispatch: func(f gatewayFrame) {
			voice <- f.Type
		},
		disconnect: func() { voice <- "disconnect" },
	}
	client.onDispatch = func(f gatewayFrame, session string) error {
		if f.replayed {
			session += "/replay"
		}
		text <- session + "/" + f.Type
		return nil
	}
	opened := make(chan error, 1)
	go func() { opened <- client.Open(ctx) }()
	send := func(conn connection, op int, event string, seq int64, data string) {
		t.Helper()
		frame := gatewayFrame{Op: op, Type: event, Data: json.RawMessage(data)}
		if op == 0 {
			frame.Sequence = &seq
		}
		if err := wsjson.Write(ctx, conn.socket, frame); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(queue <-chan string, want string) {
		t.Helper()
		if got := receiveCallGatewayFixture(t, ctx, queue); got != want {
			t.Fatalf("event %s, want %s", got, want)
		}
	}
	expectWrite := func(conn connection, op int) gatewayFrame {
		t.Helper()
		f := receiveCallGatewayFixture(t, ctx, conn.writes)
		if f.Op != op {
			t.Fatalf("opcode %d, want %d", f.Op, op)
		}
		return f
	}
	checkJoin := func(ctx context.Context, guild, room string, ok bool) {
		t.Helper()
		if err := client.setCallVoiceState(ctx, guild, room); (err == nil) != ok {
			t.Fatalf("set voice state %s/%s, success wanted %t, got %v", guild, room, ok, err)
		}
	}

	first := receiveCallGatewayFixture(t, ctx, connections)
	identify := expectWrite(first, 2)
	var payload struct{ Intents discordgo.Intent }
	if err := json.Unmarshal(identify.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if want := discordgo.IntentsDirectMessages | discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates; payload.Intents != want {
		t.Fatalf("intents %d, want %d", payload.Intents, want)
	}
	send(first, 0, "READY", 1, `{"session_id":"first"}`)
	if err := receiveCallGatewayFixture(t, ctx, opened); err != nil {
		t.Fatal(err)
	}
	expect(voice, "READY")
	expect(text, "first/READY")
	checkJoin(ctx, "10", "20", false)
	send(first, 0, "GUILD_CREATE", 2, `{"id":"99","voice_states":[]}`)
	send(first, 0, "VOICE_SERVER_UPDATE", 3, `{"guild_id":"10","token":"fixture"}`)
	send(first, 0, "MESSAGE_CREATE", 4, `{"id":"101"}`)
	expect(text, "first/MESSAGE_CREATE")
	checkJoin(ctx, "10", "20", false)
	send(first, 0, "GUILD_CREATE", 5, `{"id":"10","voice_states":[]}`)
	expect(voice, "GUILD_CREATE")
	stale := client.callVoiceContext(ctx)
	checkJoin(ctx, "99", "20", false)
	checkJoin(ctx, "10", "21", false)
	checkJoin(stale, "10", "20", true)
	join := expectWrite(first, 4)
	var request struct {
		GuildID   string  `json:"guild_id"`
		ChannelID *string `json:"channel_id"`
	}
	if err := json.Unmarshal(join.Data, &request); err != nil || request.GuildID != "10" || request.ChannelID == nil || *request.ChannelID != "20" {
		t.Fatalf("join data %s, error %v", join.Data, err)
	}
	checkJoin(stale, "10", "", true)
	leave := expectWrite(first, 4)
	if err := json.Unmarshal(leave.Data, &request); err != nil || request.ChannelID != nil {
		t.Fatalf("leave data %s, error %v", leave.Data, err)
	}
	send(first, 0, "VOICE_SERVER_UPDATE", 6, `{"guild_id":"10","token":"fixture"}`)
	send(first, 0, "VOICE_STATE_UPDATE", 7, `{"guild_id":"10","channel_id":"20"}`)
	send(first, 0, "VOICE_STATE_UPDATE", 7, `{"guild_id":"10","channel_id":"21"}`)
	send(first, 0, "THREAD_MEMBERS_UPDATE", 8, `{"guild_id":"10","id":"30","removed_member_ids":["40"]}`)
	expect(voice, "VOICE_SERVER_UPDATE")
	expect(voice, "VOICE_STATE_UPDATE")
	expect(voice, "THREAD_MEMBERS_UPDATE")
	expect(text, "first/THREAD_MEMBERS_UPDATE")
	send(first, 0, "GUILD_DELETE", 9, `{"id":"10","unavailable":true}`)
	expect(voice, "GUILD_DELETE")
	expect(text, "first/GUILD_DELETE")
	checkJoin(stale, "10", "20", false)
	send(first, 0, "GUILD_CREATE", 10, `{"id":"10","voice_states":[]}`)
	expect(voice, "GUILD_CREATE")
	checkJoin(stale, "10", "20", false)
	send(first, 0, "MESSAGE_CREATE", 11, `{"id":"102"}`)
	expect(text, "first/MESSAGE_CREATE")
	send(first, 7, "", 0, "null")
	expect(voice, "disconnect")
	checkJoin(ctx, "10", "20", false)

	replay := receiveCallGatewayFixture(t, ctx, connections)
	resume := expectWrite(replay, 6)
	var resumeData struct {
		Sequence int64 `json:"seq"`
	}
	if err := json.Unmarshal(resume.Data, &resumeData); err != nil || resumeData.Sequence != 11 {
		t.Fatalf("resume data %s, error %v", resume.Data, err)
	}
	send(replay, 0, "GUILD_CREATE", 12, `{"id":"10","voice_states":[]}`)
	send(replay, 0, "VOICE_STATE_UPDATE", 13, `{"guild_id":"10"}`)
	send(replay, 0, "MESSAGE_CREATE", 14, `{"id":"103"}`)
	expect(text, "first/replay/MESSAGE_CREATE")
	checkJoin(ctx, "10", "20", false)
	send(replay, 0, "RESUMED", 15, "null")
	expect(text, "first/replay/RESUMED")

	fresh := replay
	send(fresh, 0, "MESSAGE_CREATE", 16, `{"id":"104"}`)
	expect(text, "first/MESSAGE_CREATE")
	checkJoin(ctx, "10", "20", false)
	send(fresh, 0, "GUILD_CREATE", 17, `{"id":"10","voice_states":[]}`)
	expect(voice, "GUILD_CREATE")
	checkJoin(stale, "10", "20", false)
	checkJoin(client.callVoiceContext(ctx), "10", "20", true)
	expectWrite(fresh, 4)
	send(fresh, 0, "VOICE_STATE_UPDATE", 18, `{"guild_id":"10","channel_id":"20"}`)
	send(fresh, 0, "VOICE_SERVER_UPDATE", 19, `{"guild_id":"10","token":"fixture"}`)
	expect(voice, "VOICE_STATE_UPDATE")
	expect(voice, "VOICE_SERVER_UPDATE")
	for {
		heartbeat := receiveCallGatewayFixture(t, ctx, fresh.heartbeat)
		if string(heartbeat.Data) == "19" {
			break
		}
	}
	send(fresh, 0, "GUILD_ROLE_DELETE", 20, `{"role_id":"42"}`)
	expect(voice, "disconnect")
	checkJoin(ctx, "10", "20", false)
	send(fresh, 0, "MESSAGE_CREATE", 21, `{"id":"104"}`)
	expect(text, "first/GUILD_ROLE_DELETE")
	expect(text, "first/MESSAGE_CREATE")
}
