package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/voice/callspeech"
	"github.com/bwmarrin/discordgo"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestCallGatewayMemberIntentScope(t *testing.T) {
	for _, test := range []struct {
		name                    string
		personal, calls, denied bool
		wantMembers             bool
	}{
		{name: "text only"},
		{name: "personal calls", personal: true, calls: true},
		{name: "team calls", calls: true, wantMembers: true},
		{name: "team calls unavailable", calls: true, denied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &DiscordClient{personal: test.personal}
			if test.calls {
				client.callGateway = &callGateway{}
			}
			client.callDisabled.Store(test.denied)
			bits := client.gatewayIntents()
			if got := bits&discordgo.IntentsGuildMembers != 0; got != test.wantMembers {
				t.Fatalf("guild members intent = %t, want %t", got, test.wantMembers)
			}
			if got := bits&discordgo.IntentsGuildVoiceStates != 0; got != (test.calls && !test.denied) {
				t.Fatalf("unexpected voice intent in %d", bits)
			}
		})
	}
}

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
	cancelled, cancelJoin := context.WithCancel(stale)
	cancelJoin()
	checkJoin(cancelled, "10", "20", false)
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

func TestCallGatewayRestartsOnlyOnceAfterDAVEChange(t *testing.T) {
	for _, fault := range []string{"dave_changed", "dave_required", "dave_roster_mismatch"} {
		t.Run(fault, func(t *testing.T) {
			f := newDispatcherCallFixture(t, false)
			f.calls.mu.Lock()
			f.calls.dependencies.runMedia = func(ctx context.Context, path, guild, room, bot string, ids []string, input <-chan any, onLoss func(), handle func(callMediaEvent) error) error {
				return f.runMedia(ctx, path, guild, room, bot, ids, input, onLoss, func(e callMediaEvent) error {
					if e.Type == "audio" && e.PCM[0] == 127 {
						onLoss()
						return handle(callMediaEvent{Type: "fault", Code: fault})
					}
					return handle(e)
				})
			}
			f.calls.mu.Unlock()

			oldSpeech, oldTurn := f.begin()
			faultAndLeave := func() {
				f.audio(127)
				f.expectRoom("")
				// Traffic from the old voice connection can arrive before the leave confirmation.
				f.send("VOICE_SERVER_UPDATE", json.RawMessage(`{"guild_id":"10","token":"retired","endpoint":"example.invalid"}`))
				f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("99", "20"))))
				f.message(f.marker)
				assertNoCallGatewayWrite(t, f.gateway.writes)
				f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("99", ""))))
			}
			faultAndLeave()
			if fault != "dave_changed" {
				assertNoCallGatewayWrite(t, f.gateway.writes)
				return
			}
			f.expectRoom("20")
			f.send("VOICE_SERVER_UPDATE", json.RawMessage(`{"guild_id":"10","token":"replacement","endpoint":"example.invalid"}`))
			f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("99", "20"))))
			freshSpeech := callFixtureReceive(t, f.ctx, f.ack, "replacement media and speech readiness")
			if freshSpeech == oldSpeech || oldSpeech.ctx.Err() == nil {
				t.Fatal("replacement must retire the previous speech lane")
			}
			if err := oldSpeech.cfg.Play(f.ctx, make([]byte, callMediaPCMBytes)); !errors.Is(err, ErrNotAuthorized) {
				t.Fatalf("old playback survived a DAVE change: %v", err)
			}
			f.audio(2)
			newTurn := callFixtureReceive(t, f.ctx, f.native.accepted, "replacement voice admission")
			if newTurn.Voice.CallID == oldTurn.Voice.CallID || newTurn.Voice.Generation != oldTurn.Voice.Generation+1 || newTurn.Text != "read the discussion marker 2" {
				t.Fatalf("unexpected replacement admission: %+v", newTurn)
			}
			f.publish(TeammatePublication{ID: "old-dave-publication", AdmissionID: oldTurn.OperationID, Body: "old generation answer", Sequence: 1})
			if got := callFixtureReceive(t, f.ctx, f.written, "old generation written answer"); got != "old generation answer" {
				t.Fatalf("written answer = %q", got)
			}
			faultAndLeave()
			assertNoCallGatewayWrite(t, f.gateway.writes)
			select {
			case render := <-f.render:
				t.Fatalf("old answer reached the replacement speech lane: %+v", render)
			default:
			}
		})
	}
}

func assertNoCallGatewayWrite(t *testing.T, writes <-chan gatewayFrame) {
	t.Helper()
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case f := <-writes:
		t.Fatalf("unexpected Gateway write: opcode %d, %s", f.Op, f.Data)
	case <-timer.C:
	}
}

func TestCallPermissionRearmRechecksRevokedThreadMember(t *testing.T) {
	var revoked atomic.Bool
	checked := make(chan bool, 12)
	conn, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/30":
			_, _ = fmt.Fprint(w, `{"id":"30","guild_id":"10","parent_id":"31","type":12,"thread_metadata":{"archived":false,"locked":false}}`)
		case "/channels/31":
			_, _ = fmt.Fprint(w, `{"id":"31","guild_id":"10","type":0}`)
		case "/channels/31/permissions/member/1", "/channels/31/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/30/thread-members/99":
			_, _ = fmt.Fprint(w, `{"id":"30","user_id":"99"}`)
		case "/channels/30/thread-members/1":
			denied := revoked.Load()
			checked <- denied
			if denied {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprint(w, `{"id":"30","user_id":"1"}`)
		default:
			t.Errorf("unexpected Discord access check %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	cfg := &conn.service.Config
	cfg.OwnerID = "1"
	cfg.Calls = &DispatcherCallsConfig{Mode: "team", GuildID: "10", RoomID: "20", DiscussionID: "30", UserIDs: []string{"1"}, SpeechStateDir: filepath.Join(t.TempDir(), "speech")}
	if err := voice.SaveOpenAIKey(cfg.Calls.SpeechStateDir, strings.NewReader(callFixtureKey)); err != nil {
		t.Fatal(err)
	}
	b := TeammateDiscussion{ID: "dt_" + digest([]string{cfg.InstallationID, "10", "30"})[:32], InstallationID: cfg.InstallationID,
		Route: Route{GuildID: "10", ChannelID: "30", ParentID: "31"}}
	if err := conn.service.Store.teammateBind(teammateBinding{TeammateDiscussion: b, Opened: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	ack := make(chan *callFixtureSpeech, 3)
	ready := make(chan struct{})
	close(ready)
	d := enableDispatcherCalls(ctx, conn, dispatcherCallDependencies{
		newSpeech: func(ctx context.Context, cfg callspeech.Config) dispatcherCallSpeech {
			return &callFixtureSpeech{ctx: ctx, cfg: cfg, ready: ready, ack: ack}
		},
		runMedia: func(ctx context.Context, _, _, _, _ string, _ []string, _ <-chan any, _ func(), handle func(callMediaEvent) error) error {
			if err := handle(callMediaEvent{Type: "ready"}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})
	defer func() { cancel(); d.close() }()
	dispatch := func(seq int64, kind, data string) {
		d.dispatch(gatewayFrame{Type: kind, Sequence: &seq, Data: json.RawMessage(data)})
	}
	checkREST := func(denied bool) {
		t.Helper()
		if got := receiveCallGatewayFixture(t, ctx, checked); got != denied {
			t.Fatalf("REST observed denied = %t, want %t", got, denied)
		}
	}
	dispatch(1, "READY", callRosterReady)
	dispatch(2, "GUILD_CREATE", callRosterGuild(callRosterVoice("1", "20")))
	d.command("voice join", "1", "101")
	checkREST(false)
	oldSpeech := receiveCallGatewayFixture(t, ctx, ack)
	revoked.Store(true)
	dispatch(3, "THREAD_MEMBERS_UPDATE", `{"guild_id":"10","id":"30","removed_member_ids":["1"]}`)
	if err := oldSpeech.cfg.Play(ctx, make([]byte, callMediaPCMBytes)); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("revoked listener still has playback: %v", err)
	}
	checkREST(true)
	dispatch(4, "GUILD_ROLE_UPDATE", `{"guild_id":"10","role":{"id":"44","permissions":"0"}}`)
	checkREST(true)
	select {
	case <-ack:
		t.Fatal("harmless event rearmed media for a revoked listener")
	default:
	}
	revoked.Store(false)
	dispatch(5, "GUILD_ROLE_UPDATE", `{"guild_id":"10","role":{"id":"44","permissions":"0"}}`)
	checkREST(false)
	freshSpeech := receiveCallGatewayFixture(t, ctx, ack)
	if freshSpeech == oldSpeech || oldSpeech.ctx.Err() == nil {
		t.Fatal("fresh REST authorization must create a new generation")
	}
}
