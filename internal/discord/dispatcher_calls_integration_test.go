package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/voice/callspeech"
	"github.com/bwmarrin/discordgo"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const callFixtureKey = "synthetic-speech-key"

type callFixtureNative struct {
	mu         sync.Mutex
	admissions map[string]TeammateAdmission
	accepted   chan TeammateAdmission
	outputs    []TeammatePublication
}

func (m *callFixtureNative) AdmissionState(_ context.Context, a TeammateAdmission) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.admissions[a.OperationID]; ok {
		if previous != a {
			return "", ErrConflict
		}
		return TeammateAdmissionCutover, nil
	}
	return TeammateAdmissionMissing, nil
}

func (*callFixtureNative) CancelAdmission(context.Context, TeammateAdmission) error {
	return ErrConflict
}

func (m *callFixtureNative) Admit(ctx context.Context, a TeammateAdmission, history TeammateHistoryReader) error {
	q := TeammateHistoryQuery{Source: "admission", AdmissionID: a.OperationID, Revision: a.Revision,
		MessageID: a.Route.MessageID, AuthorID: a.Author.ID, Authority: a.Authority, Voice: a.Voice}
	if _, err := history(ctx, q); err != nil {
		return err
	}
	if a.Voice.CallID != "" {
		page, err := history(ctx, TeammateHistoryQuery{Source: "thread"})
		if err != nil {
			return err
		}
		if len(page.Messages) != 1 || !strings.Contains(page.Messages[0].Text, "marker-"+a.Discussion.InstallationID) {
			return errors.New("voice admission did not see its own written discussion marker")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, already := m.admissions[a.OperationID]; already {
		return ErrConflict
	}
	m.admissions[a.OperationID] = a
	select {
	case m.accepted <- a:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *callFixtureNative) Sync(_ context.Context, _ TeammateDiscussion, _ TeammateHistoryReader, emit func(TeammatePublication) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.outputs {
		if err := emit(p); err != nil {
			return err
		}
	}
	m.outputs = nil
	return nil
}

func (m *callFixtureNative) ValidatePublication(_ context.Context, d TeammateDiscussion, p TeammatePublication) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.admissions[p.AdmissionID]
	if !ok || a.Discussion.ID != d.ID || a.Discussion.Route != d.Route {
		return ErrNotAuthorized
	}
	return nil
}

func (*callFixtureNative) PublicationCallAdmission(_ context.Context, _ TeammateDiscussion, p TeammatePublication) (string, error) {
	// The fixture can present a foreign receipt as a voice provenance claim.
	return p.AdmissionID, nil
}

type callFixtureSpeech struct {
	ctx    context.Context
	cfg    callspeech.Config
	ready  chan struct{}
	ack    chan<- *callFixtureSpeech
	render chan<- callFixtureRender
}

type callFixtureRender struct {
	delegation, body string
}

func (s *callFixtureSpeech) Ready() <-chan struct{} { return s.ready }
func (s *callFixtureSpeech) Done() <-chan struct{}  { return s.ctx.Done() }
func (*callFixtureSpeech) Close()                   {}

func (s *callFixtureSpeech) Acknowledge() error {
	if s.ack != nil {
		s.ack <- s
		s.ack = nil
	}
	return nil
}

func (s *callFixtureSpeech) Push(speaker string, pcm []byte) error {
	id := strconv.Itoa(int(pcm[0]))
	s.cfg.Turn(s.ctx, callspeech.Turn{ID: id, Speaker: speaker, Text: "read the discussion marker " + id})
	return nil
}

func (s *callFixtureSpeech) PublishForTurn(delegation, body string) error {
	select {
	case s.render <- callFixtureRender{delegation, body}:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

type callFixtureGateway struct {
	socket *websocket.Conn
	writes chan gatewayFrame
}

type dispatcherCallFixture struct {
	t          *testing.T
	ctx        context.Context
	team       bool
	connector  *teammateConnector
	calls      *dispatcherCalls
	native     *callFixtureNative
	marker     *discordgo.Message
	speakers   []string
	gateways   chan callFixtureGateway
	gateway    callFixtureGateway
	sequence   int64
	messages   chan string
	mediaAudio chan callMediaEvent
	spoken     chan []byte
	ack        chan *callFixtureSpeech
	render     chan callFixtureRender
	written    chan string
	postID     atomic.Uint64
}

func newDispatcherCallFixture(t *testing.T, team bool) *dispatcherCallFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	f := &dispatcherCallFixture{t: t, ctx: ctx, team: team,
		native:   &callFixtureNative{admissions: make(map[string]TeammateAdmission), accepted: make(chan TeammateAdmission, 8)},
		gateways: make(chan callFixtureGateway, 3), messages: make(chan string, 16),
		mediaAudio: make(chan callMediaEvent, 8), spoken: make(chan []byte, 8),
		ack: make(chan *callFixtureSpeech, 1), render: make(chan callFixtureRender, 8), written: make(chan string, 8),
	}
	cfg := Config{Mode: "personal", InstallationID: "private-fixture", OwnerID: "70", ApplicationID: "99",
		DMChannelID: "40", DaemonSocket: "/fixture/supervisor.sock", GuildIDs: []string{"10"},
		Calls: &DispatcherCallsConfig{Mode: "personal", GuildID: "10", RoomID: "20", DiscussionID: "40", SpeechStateDir: filepath.Join(t.TempDir(), "speech")}}
	guild, speaker := "", cfg.OwnerID
	f.speakers = []string{speaker}
	if team {
		guild, speaker = "10", "71"
		cfg.Mode, cfg.InstallationID, cfg.DMChannelID = "teammate", "team-fixture", ""
		cfg.Calls.Mode, cfg.Calls.DiscussionID = "team", "30"
		cfg.Calls.UserIDs = []string{cfg.OwnerID, speaker}
		f.speakers = cfg.Calls.UserIDs
	}
	f.marker = teammateTestMessage("100", cfg.Calls.DiscussionID, speaker, "<@99> marker-"+cfg.InstallationID, true)
	f.marker.GuildID, f.marker.Timestamp = guild, time.Now()
	if err := voice.SaveOpenAIKey(cfg.Calls.SpeechStateDir, strings.NewReader(callFixtureKey)); err != nil {
		cancel()
		t.Fatal(err)
	}

	store, err := OpenStore(t.TempDir())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(f.serveDiscord))
	client, err := NewDiscordClient("synthetic-bot-token")
	if err != nil {
		cancel()
		server.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	client.apiBase = server.URL
	if !team {
		client.UsePersonalDMs()
	}
	service := NewService(cfg, store, nil, ScopedMessenger{Client: client, Config: cfg})
	key, err := store.teammateCursorKey()
	if err != nil {
		cancel()
		server.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	f.connector = &teammateConnector{service: service, client: client, manager: f.native, cursorKey: key, wake: make(chan struct{}, 1)}
	f.calls = enableDispatcherCalls(ctx, f.connector, dispatcherCallDependencies{
		newSpeech: func(ctx context.Context, cfg callspeech.Config) dispatcherCallSpeech {
			if cfg.Key != callFixtureKey || cfg.Team != team {
				t.Error("unexpected speech fixture configuration")
			}
			ready := make(chan struct{})
			close(ready)
			return &callFixtureSpeech{ctx: ctx, cfg: cfg, ready: ready, ack: f.ack, render: f.render}
		},
		runMedia: f.runMedia,
	})
	client.OnMessage = func(msg *discordgo.MessageCreate) { f.messages <- msg.ID }
	loopDone := make(chan error, 1)
	go func() { loopDone <- f.connector.run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		f.calls.close()
		if err := <-loopDone; err != nil {
			t.Error(err)
		}
		server.Close()
		_ = store.Close()
	})

	opened := make(chan error, 1)
	go func() { opened <- client.Open(ctx) }()
	f.gateway = callFixtureReceive(t, ctx, f.gateways, "gateway connection")
	f.expectOpcode(2)
	f.send("READY", json.RawMessage(`{"session_id":"gateway-one","user":{"id":"99","bot":true},"application":{"id":"99"}}`))
	if err := callFixtureReceive(t, ctx, opened, "gateway ready"); err != nil {
		t.Fatal(err)
	}
	return f
}

func callFixtureReceive[T any](t *testing.T, ctx context.Context, queue <-chan T, label string) T {
	t.Helper()
	select {
	case item := <-queue:
		return item
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", label)
		var zero T
		return zero
	}
}

func (f *dispatcherCallFixture) serveDiscord(w http.ResponseWriter, r *http.Request) {
	t := f.t
	discussion := "/channels/" + f.marker.ChannelID
	switch r.Method + " " + r.URL.Path {
	case "GET /gateway":
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws://" + r.Host + "/ws"})
		return
	case "GET /ws":
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = socket.CloseNow() }()
		if wsjson.Write(f.ctx, socket, map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 200}}) != nil {
			return
		}
		g := callFixtureGateway{socket: socket, writes: make(chan gatewayFrame, 8)}
		f.gateways <- g
		for {
			var frame gatewayFrame
			if wsjson.Read(f.ctx, socket, &frame) != nil {
				return
			}
			if frame.Op == 1 {
				if wsjson.Write(f.ctx, socket, map[string]int{"op": 11}) != nil {
					return
				}
				continue
			}
			select {
			case g.writes <- frame:
			case <-f.ctx.Done():
				return
			}
		}
	case "GET " + discussion:
		if f.team {
			_, _ = fmt.Fprint(w, `{"id":"30","guild_id":"10","parent_id":"31","type":12,"thread_metadata":{"archived":false,"locked":false}}`)
		} else {
			_, _ = fmt.Fprint(w, `{"id":"40","type":1,"recipients":[{"id":"70","username":"Owner"}]}`)
		}
	case "GET " + discussion + "/messages/" + f.marker.ID:
		_ = json.NewEncoder(w).Encode(f.marker)
	case "GET " + discussion + "/messages":
		page := []*discordgo.Message{}
		if before := r.URL.Query().Get("before"); before == "" || snowflakeLess(f.marker.ID, before) {
			page = append(page, f.marker)
		}
		_ = json.NewEncoder(w).Encode(page)
	case "POST " + discussion + "/messages":
		var body struct{ Content string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(discordgo.Message{ID: strconv.FormatUint(200+f.postID.Add(1), 10), ChannelID: f.marker.ChannelID, GuildID: f.marker.GuildID,
			Content: body.Content, Author: &discordgo.User{ID: "99", Bot: true}})
		f.written <- body.Content
	case "GET /channels/31", "GET /channels/31/permissions/member/99", "GET /channels/31/permissions/member/70", "GET /channels/31/permissions/member/71",
		"GET /channels/30/thread-members/99", "GET /channels/30/thread-members/70", "GET /channels/30/thread-members/71", "GET /channels/31/messages/30":
		if !f.team {
			t.Errorf("personal call accessed a team endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch {
		case r.URL.Path == "/channels/31":
			_, _ = fmt.Fprint(w, `{"id":"31","guild_id":"10","type":0}`)
		case strings.Contains(r.URL.Path, "/permissions/member/"):
			teammateTestPermissions(w)
		case strings.Contains(r.URL.Path, "/thread-members/"):
			_, _ = fmt.Fprintf(w, `{"id":"30","user_id":%q}`, r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:])
		default:
			http.NotFound(w, r)
		}
	default:
		t.Errorf("unexpected fixture REST request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func (f *dispatcherCallFixture) send(event string, data any) {
	f.t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		f.t.Fatal(err)
	}
	f.sequence++
	seq := f.sequence
	if err = wsjson.Write(f.ctx, f.gateway.socket, gatewayFrame{Op: 0, Type: event, Sequence: &seq, Data: raw}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *dispatcherCallFixture) expectOpcode(op int) gatewayFrame {
	f.t.Helper()
	result := callFixtureReceive(f.t, f.ctx, f.gateway.writes, fmt.Sprintf("gateway opcode %d", op))
	if result.Op != op {
		f.t.Fatalf("gateway opcode = %d, want %d", result.Op, op)
	}
	return result
}

func (f *dispatcherCallFixture) expectRoom(room string) {
	f.t.Helper()
	var data struct {
		GuildID   string `json:"guild_id"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal(f.expectOpcode(4).Data, &data); err != nil || data.GuildID != "10" || data.ChannelID != room {
		f.t.Fatalf("voice state = %+v, want guild 10 room %q: %v", data, room, err)
	}
}

func (f *dispatcherCallFixture) message(msg *discordgo.Message) {
	f.t.Helper()
	f.send("MESSAGE_CREATE", msg)
	if got := callFixtureReceive(f.t, f.ctx, f.messages, "message dispatch"); got != msg.ID {
		f.t.Fatalf("gateway dispatched message %s, want %s", got, msg.ID)
	}
}

func (f *dispatcherCallFixture) runMedia(ctx context.Context, _, guild, room, bot string, speakers []string, input <-chan any, onLoss func(), handle func(callMediaEvent) error) error {
	if guild != "10" || room != "20" || bot != "99" || !slices.Equal(speakers, f.speakers) {
		f.t.Error("media received the wrong audience")
		return ErrInvalid
	}
	join := json.RawMessage(`{"op":4,"d":{"guild_id":"10","channel_id":"20","self_mute":false,"self_deaf":false}}`)
	if err := handle(callMediaEvent{Type: "gateway", Data: join}); err != nil {
		return err
	}
	var state, server bool
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case audio, ok := <-f.mediaAudio:
			if !ok {
				onLoss()
				return errors.New("test media process exited")
			}
			if err := handle(audio); err != nil {
				return err
			}
		case msg := <-input:
			wire, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			event, err := decodeCallMedia(wire, false)
			if err != nil {
				return err
			}
			switch event.Type {
			case "voice_state", "voice_server":
				alreadyReady := state && server
				state = state || event.Type == "voice_state"
				server = server || event.Type == "voice_server"
				if state && server && !alreadyReady {
					if err := handle(callMediaEvent{Type: "ready"}); err != nil {
						return err
					}
				}
			case "play":
				select {
				case f.spoken <- event.PCM:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

func (f *dispatcherCallFixture) command(id, channel, speaker, action string, age time.Duration) {
	f.t.Helper()
	msg := teammateTestMessage(id, channel, speaker, "<@99> voice "+action, true)
	msg.Timestamp = time.Now().Add(-age)
	f.message(msg)
}

func (f *dispatcherCallFixture) audio(id byte) {
	f.mediaAudio <- callMediaEvent{Type: "audio", UserID: f.marker.Author.ID, PCM: bytes.Repeat([]byte{id}, callMediaPCMBytes)}
}

func (f *dispatcherCallFixture) publish(p ...TeammatePublication) {
	f.native.mu.Lock()
	f.native.outputs = append(f.native.outputs, p...)
	f.native.mu.Unlock()
	f.connector.signal()
}

func (f *dispatcherCallFixture) expectConsent(ids ...string) {
	f.t.Helper()
	f.calls.mu.Lock()
	defer f.calls.mu.Unlock()
	if len(f.calls.consent) != len(ids) || f.calls.invited != (len(ids) != 0) {
		f.t.Fatalf("call invite = %t, consent = %v, want %v", f.calls.invited, f.calls.consent, ids)
	}
	for _, id := range ids {
		if f.calls.consent[id].IsZero() {
			f.t.Fatalf("missing consent for %s", id)
		}
	}
}

func (f *dispatcherCallFixture) expectWaiting() {
	f.t.Helper()
	f.calls.mu.Lock()
	defer f.calls.mu.Unlock()
	if _, permitted := f.calls.permittedLocked(); permitted || f.calls.active != nil {
		f.t.Fatal("call became eligible before the audience was authorized")
	}
}

func (f *dispatcherCallFixture) expectControlWatermark(id string) {
	f.t.Helper()
	f.calls.mu.Lock()
	defer f.calls.mu.Unlock()
	if f.calls.lastControl != id {
		f.t.Fatalf("control watermark = %q, want %q", f.calls.lastControl, id)
	}
}

func (f *dispatcherCallFixture) expectNoTextIngress() {
	f.t.Helper()
	pending, err := f.connector.service.Store.teammatePending(f.connector.service.Config.InstallationID)
	if err != nil || len(pending) != 0 {
		f.t.Fatalf("call control entered written ingress: %v, %v", pending, err)
	}
}

func (f *dispatcherCallFixture) waitDiscussionOpened(d TeammateDiscussion) {
	f.t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		b, err := f.connector.service.Store.teammateBindingByID(d.InstallationID, d.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		if b.Opened {
			return
		}
		callFixtureReceive(f.t, f.ctx, tick.C, "written discussion cutover")
	}
}

func (f *dispatcherCallFixture) requestJoin() TeammateAdmission {
	f.t.Helper()
	f.message(f.marker)
	text := callFixtureReceive(f.t, f.ctx, f.native.accepted, "written marker admission")
	if text.Text != f.marker.Content || text.Route.ChannelID != f.marker.ChannelID {
		f.t.Fatalf("unexpected written marker admission: %+v", text)
	}

	f.waitDiscussionOpened(text.Discussion)
	states := []string{callRosterVoice(f.speakers[0], "20")}
	if f.team {
		states = append(states, callRosterVoice(f.speakers[1], "20"))
	} else {
		f.send("GUILD_CREATE", json.RawMessage(callRosterGuild(callRosterVoice("72", "20"))))
		// A guild command cannot become written input for the owner's DM.
		f.command("101", "30", "70", "join", 0)
		f.expectWaiting()
		f.expectConsent()
	}
	f.send("GUILD_CREATE", json.RawMessage(callRosterGuild(states...)))
	if f.team {
		f.command("101", "30", "72", "join", 0)
		f.command("102", "30", "70", "join", time.Minute)
		f.expectConsent()
		f.expectNoTextIngress()
		f.command("103", "30", "70", "join", 0)
		f.expectConsent("70")
		f.expectWaiting()
		f.command("104", "30", "71", "consent", 0)
		f.expectConsent("70", "71")
		f.command("103", "30", "70", "leave", 0)
		f.expectConsent("70", "71")
		f.expectControlWatermark("104")
	}
	f.expectRoom("20")
	f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("99", "20"))))
	f.send("VOICE_SERVER_UPDATE", json.RawMessage(`{"guild_id":"10","token":"fixture-voice","endpoint":"example.invalid"}`))
	return text
}

func (f *dispatcherCallFixture) begin() (*callFixtureSpeech, TeammateAdmission) {
	f.t.Helper()
	text := f.requestJoin()
	speech := callFixtureReceive(f.t, f.ctx, f.ack, "media and speech readiness")

	f.audio(1)
	heard := callFixtureReceive(f.t, f.ctx, f.native.accepted, "first voice admission")
	authority := AuthorityOwnerDirect
	if f.team {
		authority = AuthorityExternal
	}
	if heard.Discussion != text.Discussion || heard.Route != text.Discussion.Route ||
		heard.Authority != authority || heard.Voice.SpeakerID != f.marker.Author.ID ||
		heard.Voice.SupervisorSocket != text.Discussion.SupervisorSocket || heard.Voice.CallID == "" ||
		heard.OperationID == text.OperationID || heard.Text != "read the discussion marker 1" {
		f.t.Fatalf("voice admission did not preserve the written discussion and speaker: %+v", heard)
	}
	return speech, heard
}

func TestDispatcherCallHelperLossDuringAdmission(t *testing.T) {
	for _, name := range []string{"process exit", "stuck write"} {
		t.Run(name, func(t *testing.T) {
			f := newDispatcherCallFixture(t, false)
			finalFault := filepath.Join(t.TempDir(), "send-fault")
			drain := fmt.Sprintf(`
while [ ! -f %q ]; do sleep 0.01; done
printf '%%s\n' '{"type":"fault","code":"dave_changed"}'
`, finalFault)
			// Keep stdout open after the supervised process exits in the first case.
			if name == "process exit" {
				drain = "IFS= read -r line\n(\n" + drain + ") &\nexit 0\n"
			}
			path := mediaTestHelper(t, `
IFS= read -r line
printf '%s\n' '{"type":"gateway","data":{"op":4,"d":{"guild_id":"10","channel_id":"20"}}}'
IFS= read -r line
IFS= read -r line
printf '%s\n' '{"type":"ready"}'
`+drain)
			releaseFault := func() {
				if err := os.WriteFile(finalFault, nil, 0600); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(releaseFault)
			f.calls.mu.Lock()
			f.connector.service.Config.Calls.HelperPath = path
			f.calls.dependencies.runMedia = runCallMedia
			f.calls.mu.Unlock()
			text := f.requestJoin()
			speech := callFixtureReceive(t, f.ctx, f.ack, "real helper readiness")
			f.calls.mu.Lock()
			call := f.calls.active
			f.calls.mu.Unlock()

			f.native.mu.Lock()
			unlock := sync.OnceFunc(f.native.mu.Unlock)
			defer unlock()
			turn := callspeech.Turn{ID: "queued", Speaker: "70", Text: "pending voice request"}
			speech.cfg.Turn(speech.ctx, turn)
			turnID := digest([]string{call.id, turn.ID})
			op := callAdmissionID(text.Discussion.InstallationID, call.id, turnID)
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			var record callAdmissionRecord
			for {
				var err error
				record, err = f.connector.callAdmission(text.Discussion, op)
				if err == nil {
					break
				}
				if !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				callFixtureReceive(t, f.ctx, tick.C, "durable turn waiting for native admission")
			}
			history := f.connector.history(text.Discussion)
			query := TeammateHistoryQuery{Source: "admission", AdmissionID: op, Revision: record.Voice.BodyHash,
				AuthorID: turn.Speaker, Voice: record.Voice, Authority: record.Authority}
			if _, err := history(f.ctx, query); err != nil {
				t.Fatalf("pending receipt was invalid before helper loss: %v", err)
			}

			pcm := make([]byte, callMediaPCMBytes)
			if name == "process exit" {
				if err := speech.cfg.Play(f.ctx, pcm); err != nil {
					t.Fatal(err)
				}
			} else {
				message := map[string]any{"type": "voice_server", "data": map[string]string{"token": strings.Repeat("x", 8000)}}
				pacer := time.NewTicker(50 * time.Millisecond)
				defer pacer.Stop()
				for speech.ctx.Err() == nil {
					f.calls.mu.Lock()
					f.calls.queueLocked(call, message)
					f.calls.mu.Unlock()
					callFixtureReceive(t, f.ctx, pacer.C, "filling the helper input pipe")
				}
			}
			callFixtureReceive(t, f.ctx, speech.ctx.Done(), "authority loss before final helper fault")
			if err := speech.cfg.Play(f.ctx, pcm); !errors.Is(err, ErrNotAuthorized) {
				t.Fatalf("helper loss still permitted playback: %v", err)
			}
			if _, err := history(f.ctx, query); !errors.Is(err, ErrTeammateSourceChanged) {
				t.Fatalf("helper loss still permitted a fresh receipt: %v", err)
			}
			unlock()
			for {
				f.connector.callAdmissionMu.Lock()
				pending := f.connector.callAdmissions[text.Discussion.ID]
				f.connector.callAdmissionMu.Unlock()
				if pending == 0 {
					break
				}
				callFixtureReceive(t, f.ctx, tick.C, "blocked admission returning during drain")
			}
			select {
			case admitted := <-f.native.accepted:
				t.Fatalf("queued turn crossed helper loss: %+v", admitted)
			default:
			}
			releaseFault()
			f.expectRoom("")
			f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("99", ""))))
			f.expectRoom("20")
			f.calls.mu.Lock()
			ready, lost, recovered := call.ready, call.mediaLost, call.daveChanged
			f.calls.mu.Unlock()
			if ready || !lost || !recovered {
				t.Fatalf("retired helper ready=%v lost=%v final DAVE fault=%v", ready, lost, recovered)
			}
		})
	}
}

func TestDispatcherCallHelperLossDuringStartup(t *testing.T) {
	f := newDispatcherCallFixture(t, false)
	providerReady, lost, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finishDrain := sync.OnceFunc(func() { close(finish) })
	defer finishDrain()
	f.calls.mu.Lock()
	f.calls.dependencies.newSpeech = func(ctx context.Context, cfg callspeech.Config) dispatcherCallSpeech {
		return &callFixtureSpeech{ctx: ctx, cfg: cfg, ready: providerReady, ack: f.ack}
	}
	f.calls.dependencies.runMedia = func(ctx context.Context, path, guild, room, bot string, ids []string, input <-chan any, onLoss func(), handle func(callMediaEvent) error) error {
		return f.runMedia(ctx, path, guild, room, bot, ids, input, onLoss, func(e callMediaEvent) error {
			if err := handle(e); err != nil || e.Type != "ready" {
				return err
			}
			onLoss()
			close(providerReady)
			close(lost)
			select {
			case <-finish:
				return errors.New("test media process exited")
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}
	f.calls.mu.Unlock()
	f.requestJoin()
	callFixtureReceive(t, f.ctx, lost, "helper loss before provider readiness")
	f.calls.mu.Lock()
	call := f.calls.active
	f.calls.mu.Unlock()
	if call == nil {
		t.Fatal("call left before the helper drain completed")
	}
	finishDrain()
	f.expectRoom("")
	f.calls.mu.Lock()
	ready, mediaLost := call.ready, call.mediaLost
	f.calls.mu.Unlock()
	if ready || !mediaLost {
		t.Fatal("provider readiness restored authority after helper loss")
	}
	select {
	case <-f.ack:
		t.Fatal("provider acknowledged readiness after helper loss")
	default:
	}
}

func TestDispatcherCallsIntegration(t *testing.T) {
	personal := newDispatcherCallFixture(t, false)
	team := newDispatcherCallFixture(t, true)
	privateSpeech, privateTurn := personal.begin()
	teamSpeech, teamTurn := team.begin()
	for _, tc := range []struct {
		f              *dispatcherCallFixture
		speech         *callFixtureSpeech
		local, foreign TeammateAdmission
	}{
		{personal, privateSpeech, privateTurn, teamTurn},
		{team, teamSpeech, teamTurn, privateTurn},
	} {
		f := tc.f
		local, foreign := tc.local, tc.foreign
		query := TeammateHistoryQuery{Source: "admission", Revision: foreign.Revision, AuthorID: foreign.Author.ID, Authority: foreign.Authority, Voice: foreign.Voice,
			AdmissionID: callAdmissionID(local.Discussion.InstallationID, foreign.Voice.CallID, foreign.Voice.TurnID)}
		if _, err := f.connector.history(local.Discussion)(f.ctx, query); !errors.Is(err, ErrTeammateSourceChanged) {
			t.Fatalf("%s accepted a foreign voice receipt: %v", local.Discussion.InstallationID, err)
		}

		wrongRoute := local.Discussion
		wrongRoute.Route = foreign.Discussion.Route
		if _, err := f.connector.callPublicationOrigin(wrongRoute, TeammatePublication{AdmissionID: local.OperationID}); !errors.Is(err, ErrTeammateSourceChanged) {
			t.Fatalf("%s resolved a voice publication against the wrong audience: %v", local.Discussion.InstallationID, err)
		}

		// Replay the same transcript and then send a distinct turn as a processing barrier.
		f.audio(1)
		f.audio(2)
		next := callFixtureReceive(t, f.ctx, f.native.accepted, "distinct second voice admission")
		if next.Discussion != local.Discussion || next.OperationID == local.OperationID || next.Text != "read the discussion marker 2" {
			t.Fatalf("unexpected second voice admission: %+v", next)
		}

		body := "marker-" + local.Discussion.InstallationID
		first := TeammatePublication{ID: "voice-first", AdmissionID: local.OperationID, Body: body, Sequence: 1}
		pending := TeammatePublication{ID: "voice-pending", AdmissionID: next.OperationID, Body: body + " pending", Sequence: 2}
		foreignOutput := TeammatePublication{ID: "foreign-voice", AdmissionID: foreign.OperationID, Body: "marker-" + foreign.Discussion.InstallationID, Sequence: 3}
		f.publish(first, first, foreignOutput, pending)
		for _, want := range []callFixtureRender{{"1", first.Body}, {"2", pending.Body}} {
			if got := callFixtureReceive(t, f.ctx, f.render, "native publication reaching speech"); got != want {
				t.Fatalf("render = %+v, want %+v", got, want)
			}
		}
		pcm := bytes.Repeat([]byte{1}, callMediaPCMBytes)
		if err := tc.speech.cfg.Play(tc.speech.ctx, pcm); err != nil {
			t.Fatal(err)
		}
		if got := callFixtureReceive(t, f.ctx, f.spoken, "rendered audio reaching media"); !bytes.Equal(got, pcm) {
			t.Fatal("media received the wrong returned audio")
		}
		for _, want := range []string{first.Body, pending.Body} {
			if got := callFixtureReceive(t, f.ctx, f.written, "publication written to the bound discussion"); got != want {
				t.Fatalf("written publication = %q, want %q", got, want)
			}
		}

		f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("72", "20"))))
		f.expectRoom("")
		// Use a live context to model a provider completion racing with call cancellation.
		if err := tc.speech.cfg.Play(f.ctx, pcm); !errors.Is(err, ErrNotAuthorized) {
			t.Fatalf("pending render survived an extra join: %v", err)
		}
		select {
		case <-f.spoken:
			t.Fatal("invalidated output reached media")
		default:
		}
		f.expectNoTextIngress()
		query.AdmissionID, query.Revision, query.AuthorID = local.OperationID, local.Revision, local.Author.ID
		query.Authority, query.Voice = local.Authority, local.Voice
		if _, err := f.connector.history(local.Discussion)(f.ctx, query); !errors.Is(err, ErrTeammateSourceChanged) {
			t.Fatalf("ended call still authorized fresh native input: %v", err)
		}
		query.Source = "grant"
		if _, err := f.connector.history(local.Discussion)(f.ctx, query); err != nil {
			t.Fatalf("ended call lost the original written continuation grant: %v", err)
		}
		if count := f.postID.Load(); count != 2 {
			t.Fatalf("got %d written posts, want one per local publication", count)
		}
		replayed, err := f.connector.submitCallTurn(f.ctx, local.Discussion, local.Voice.CallID, local.Voice.TurnID,
			local.Voice.Generation, local.Author, local.Text)
		if err != nil || replayed != local.OperationID {
			t.Fatalf("replayed voice turn = %q, %v, want %q", replayed, err, local.OperationID)
		}
	}

	// A replayed command must neither change call control nor enter text ingress.
	if err := wsjson.Write(team.ctx, team.gateway.socket, map[string]int{"op": 7}); err != nil {
		t.Fatal(err)
	}
	team.gateway = callFixtureReceive(t, team.ctx, team.gateways, "gateway resume connection")
	team.expectOpcode(6)
	team.command("105", "30", "70", "leave", 0)
	team.expectConsent()
	team.expectControlWatermark("104")
	team.expectNoTextIngress()
}
