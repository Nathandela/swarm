package discord

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	bolt "go.etcd.io/bbolt"
)

func TestTeammateAddressingRequiresNativeMentionAndText(t *testing.T) {
	for _, tc := range []struct {
		text          string
		mention, want bool
	}{
		{"<@99> thoughts?", true, true},
		{"<@99> thoughts?", false, false},
		{"thoughts?", true, false},
		{"> <@99> thoughts?", true, false},
		{"`<@99>` thoughts?", true, false},
	} {
		m := &discordgo.Message{Content: tc.text, Author: &discordgo.User{ID: "88"}}
		if tc.mention {
			m.Mentions = []*discordgo.User{{ID: "99"}}
		}
		if got := teammateMention(m, "99"); got != tc.want {
			t.Errorf("%q mention=%v: got %v", tc.text, tc.mention, got)
		}
	}
}

func TestTeammateIngressExceedsLegacyBurstAndKeepsDeletes(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := 1; i <= 100; i++ {
		if i == 80 {
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			c, e := s.teammateCheckpoint("installation")
			if e != nil || c.GatewaySequence != 79 {
				t.Fatalf("restart checkpoint %v: %v", c, e)
			}
		}
		id := fmt.Sprint(200 + i)
		m := teammateCandidate{Route: Route{GuildID: "10", ChannelID: "20", MessageID: id}, Author: Actor{ID: "30"}, Opening: true}
		if err = s.teammateRecord("installation", "session", int64(i), []teammateCandidate{m}, false); err != nil {
			t.Fatal(err)
		}
	}
	deletion := teammateCandidate{Route: Route{GuildID: "10", ChannelID: "20", MessageID: "250"}}
	if err = s.teammateRecord("installation", "session", 101, []teammateCandidate{deletion}, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.teammatePending("installation")
	if err != nil || len(got) != 99 || got[0].Route.MessageID != "201" || got[98].Route.MessageID != "300" {
		t.Fatalf("unexpected pending count %d: %v", len(got), err)
	}
}

type teammateTestManager struct {
	known    bool
	phase    string
	canceled bool
	admitted []TeammateAdmission
}

func (m *teammateTestManager) AdmissionState(context.Context, TeammateAdmission) (string, error) {
	if m.phase != "" {
		return m.phase, nil
	}
	if m.known || len(m.admitted) > 0 {
		return TeammateAdmissionCutover, nil
	}
	return TeammateAdmissionMissing, nil
}
func (m *teammateTestManager) CancelAdmission(context.Context, TeammateAdmission) error {
	m.canceled = true
	m.phase = TeammateAdmissionCancelled
	return nil
}

func (m *teammateTestManager) Admit(_ context.Context, a TeammateAdmission, _ TeammateHistoryReader) error {
	m.admitted = append(m.admitted, a)
	if m.phase == TeammateAdmissionSuperseded {
		m.phase = TeammateAdmissionAccepted
	}
	return nil
}
func (*teammateTestManager) ValidatePublication(context.Context, TeammateDiscussion, TeammatePublication) error {
	return nil
}

func (*teammateTestManager) Sync(context.Context, TeammateDiscussion, TeammateHistoryReader, func(TeammatePublication) error) error {
	return nil
}

func teammateTestConnector(t *testing.T, handler http.HandlerFunc) (*teammateConnector, *teammateTestManager) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client, err := NewDiscordClient("test")
	if err != nil {
		t.Fatal(err)
	}
	client.apiBase = mockEndpoints(t, handler)
	cfg := Config{Mode: "teammate", InstallationID: "install", ApplicationID: "99", GuildIDs: []string{"10"}, UserIDs: []string{"77"}}
	svc := NewService(cfg, store, nil, ScopedMessenger{Client: client, Config: cfg})
	svc.SetBotID("99")
	key, err := store.teammateCursorKey()
	if err != nil {
		t.Fatal(err)
	}
	mgr := &teammateTestManager{}
	return &teammateConnector{service: svc, client: client, manager: mgr, cursorKey: key, wake: make(chan struct{}, 1)}, mgr
}

const teammatePublic = `{"id":"201","guild_id":"10","parent_id":"20","type":11,"thread_metadata":{"archived":false,"locked":false}}`
const teammateParent = `{"id":"20","guild_id":"10","type":0}`

func teammateTestPermissions(w http.ResponseWriter) {
	bits := discordgo.PermissionViewChannel | discordgo.PermissionReadMessageHistory | discordgo.PermissionSendMessages | discordgo.PermissionSendMessagesInThreads | discordgo.PermissionCreatePublicThreads
	_, _ = fmt.Fprintf(w, `{"permissions":"%d"}`, bits)
}
func teammateTestEvent(t *testing.T, c *teammateConnector, seq int64, kind string, m any) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.gateway(gatewayFrame{Type: kind, Sequence: &seq, Data: raw}, "session"); err != nil {
		t.Fatal(err)
	}
}
func teammateTestMessage(id, channel, author, text string, mention bool) *discordgo.Message {
	m := &discordgo.Message{ID: id, ChannelID: channel, GuildID: "10", Author: &discordgo.User{ID: author, Username: "Human " + author}, Content: text}
	if mention {
		m.Mentions = []*discordgo.User{{ID: "99"}}
	}
	return m
}

func TestTeammateTopLevelRecoversLostThreadCreateAndAdmitsOtherParticipant(t *testing.T) {
	var created bool
	opener := teammateTestMessage("201", "20", "77", "<@99> thoughts?", true)
	follow := teammateTestMessage("202", "201", "88", "Please check the build", false)
	repeated := teammateTestMessage("206", "201", "77", follow.Content, false)
	bridge, mgr := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/20/messages/201":
			_ = json.NewEncoder(w).Encode(opener)
		case "/channels/201/messages/202":
			_ = json.NewEncoder(w).Encode(follow)
		case "/channels/201/messages/206":
			_ = json.NewEncoder(w).Encode(repeated)
		case "/channels/20/messages/201/threads":
			if r.Method != "POST" || created {
				t.Error("unexpected duplicate thread creation")
			}
			created = true
			w.WriteHeader(500)
		case "/channels/201":
			if created {
				_, _ = fmt.Fprint(w, teammatePublic)
			} else {
				w.WriteHeader(404)
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	teammateTestEvent(t, bridge, 1, "MESSAGE_CREATE", teammateTestMessage("199", "20", "88", "ordinary chatter", false))
	teammateTestEvent(t, bridge, 2, "MESSAGE_CREATE", opener)
	teammateTestEvent(t, bridge, 3, "MESSAGE_CREATE", follow)
	pending, err := bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 2 {
		t.Fatalf("%d candidates: %v", len(pending), err)
	}
	for _, item := range pending {
		if err = bridge.process(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if len(mgr.admitted) != 2 || mgr.admitted[0].Discussion.Route.ChannelID != "201" || mgr.admitted[0].Discussion.ID != mgr.admitted[1].Discussion.ID ||
		mgr.admitted[1].Author.ID != "88" || mgr.admitted[1].InvokerID != "88" || mgr.admitted[0].Discussion.ParentAnchor != "201" {
		t.Fatalf("admissions %+v", mgr.admitted)
	}
	teammateTestEvent(t, bridge, 4, "MESSAGE_CREATE", opener)
	pending, err = bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 0 {
		t.Fatalf("replay created %d candidates: %v", len(pending), err)
	}

	// Other agents may speak in the thread. Their messages must never start an echo loop.
	for i, author := range []string{"99", "98"} {
		post := teammateTestMessage(strconv.Itoa(204+i), "201", author, "<@99> checking", true)
		post.Author.Bot = true
		teammateTestEvent(t, bridge, int64(5+i), "MESSAGE_CREATE", post)
	}
	teammateTestEvent(t, bridge, 7, "MESSAGE_CREATE", repeated)
	pending, err = bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 1 || pending[0].Route.MessageID != repeated.ID {
		t.Fatalf("expected only the new human request, got %+v: %v", pending, err)
	}
	if err := bridge.process(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(mgr.admitted) != 3 || mgr.admitted[2].Author.ID != repeated.Author.ID || mgr.admitted[2].Discussion.ID != mgr.admitted[0].Discussion.ID {
		t.Fatalf("lost new speaker or discussion: %+v", mgr.admitted)
	}
}

func TestTeammateSourceEditBeforeAcknowledgment(t *testing.T) {
	original := teammateTestMessage("202", "201", "77", "<@99> old", true)
	current := *original
	bridge, mgr := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/201/messages/202":
			_ = json.NewEncoder(w).Encode(&current)
		default:
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	teammateTestEvent(t, bridge, 1, "MESSAGE_CREATE", original)
	pending, _ := bridge.service.Store.teammatePending("install")
	b, _, err := bridge.resolve(ctx, pending[0])
	if err != nil {
		t.Fatal(err)
	}
	a := TeammateAdmission{Discussion: b.TeammateDiscussion, OperationID: "pending", Revision: teammateRevision(original), Author: Actor{ID: "77"}, Route: pending[0].Route}
	if err = bridge.service.Store.teammateCandidateChange("install", pending[0], StateSending, "", &a); err != nil {
		t.Fatal(err)
	}
	current.Content = "<@99> corrected"
	teammateTestEvent(t, bridge, 2, "MESSAGE_UPDATE", map[string]string{"id": "202", "channel_id": "201", "guild_id": "10"})
	pending, _ = bridge.service.Store.teammatePending("install")
	if err = bridge.process(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(mgr.admitted) != 0 {
		t.Fatalf("admitted old revision %+v", mgr.admitted)
	}
	pending, _ = bridge.service.Store.teammatePending("install")
	if err = bridge.process(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(mgr.admitted) != 1 || mgr.admitted[0].Text != current.Content {
		t.Fatalf("admissions %+v", mgr.admitted)
	}
}

func TestTeammatePagesAllHistoryWithoutAllowingCrossDiscussionCursor(t *testing.T) {
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages":
			before, _ := strconv.Atoi(r.URL.Query().Get("before"))
			if before == 0 {
				before = 400
			}
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			rows := []*discordgo.Message{}
			for id := before - 1; id >= 250 && len(rows) < limit; id-- {
				rows = append(rows, teammateTestMessage(strconv.Itoa(id), "201", "88", strings.Repeat("海", 2000), false))
			}
			_ = json.NewEncoder(w).Encode(rows)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	d := TeammateDiscussion{InstallationID: "install", ID: "d1", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	q := TeammateHistoryQuery{}
	bytes, messages := 0, 0
	for {
		p, err := bridge.history(d)(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if n := historyJSONSize(p); n >= 26<<10 {
			t.Fatalf("history response is %d bytes", n)
		}
		if p.HighWater != "399" {
			t.Fatalf("high water %q", p.HighWater)
		}
		for i, m := range p.Messages {
			if i > 0 && !snowflakeLess(p.Messages[i-1].ID, m.ID) {
				t.Fatal("page order")
			}
			bytes += len(m.Text)
			messages++
		}
		if p.Cursor == "" {
			break
		}
		other := d
		other.ID = "d2"
		if _, err = bridge.history(other)(ctx, TeammateHistoryQuery{Cursor: p.Cursor}); !errors.Is(err, ErrNotAuthorized) {
			t.Fatalf("cross discussion: %v", err)
		}
		q.Cursor = p.Cursor
	}
	if messages != 150 || bytes != 900000 {
		t.Fatalf("history %d messages, %d bytes", messages, bytes)
	}
}

func TestTeammatePublicationHoldsOnRevocationAndUsesOriginalAudience(t *testing.T) {
	allowed := false
	post := 0
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/20/permissions/member/99":
			if allowed {
				teammateTestPermissions(w)
			} else {
				_, _ = fmt.Fprint(w, `{"permissions":"0"}`)
			}
		case "/channels/201/messages":
			post++
			var body struct {
				Nonce, Content string
				Enforce        bool                     `json:"enforce_nonce"`
				Mentions       struct{ Parse []string } `json:"allowed_mentions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Nonce == "" || !body.Enforce || len(body.Mentions.Parse) != 0 || body.Content != "@everyone hello" {
				t.Errorf("payload %+v", body)
			}
			_, _ = fmt.Fprint(w, `{"id":"209","channel_id":"201","content":"@everyone hello","author":{"id":"99","bot":true}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	d := TeammateDiscussion{ID: "d1", InstallationID: "install", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := bridge.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d}); err != nil {
		t.Fatal(err)
	}
	p := TeammatePublication{ID: "out", AdmissionID: "input", Body: "@everyone hello", QuestionID: "q", QuestionHash: "hash"}
	if err := bridge.service.Store.teammateQueueOutput(d, p, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := bridge.sendPending(ctx, d); !errors.Is(err, ErrNotAuthorized) || post != 0 {
		t.Fatalf("posts %d, error %v", post, err)
	}
	allowed = true
	if err := bridge.sendPending(ctx, d); err != nil || post != 1 {
		t.Fatalf("posts %d, error %v", post, err)
	}
	if err := bridge.sendPending(ctx, d); err != nil || post != 1 {
		t.Fatalf("duplicate posts %d, error %v", post, err)
	}
	q, hash, err := bridge.service.Store.teammateQuestion(d, "209")
	if err != nil || q != "q" || hash != "hash" {
		t.Fatalf("question %s %s %v", q, hash, err)
	}
	d.ID = "d2"
	q, _, err = bridge.service.Store.teammateQuestion(d, "209")
	if err != nil || q != "" {
		t.Fatalf("question leaked %s %v", q, err)
	}
}

func TestTeammateNonResumableGapRepairsContextWithoutAdmittingHistoricalMentions(t *testing.T) {
	bridge, mgr := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages":
			current := teammateTestMessage("260", "201", "88", "<@99> missed request", true)
			_ = json.NewEncoder(w).Encode([]*discordgo.Message{current})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	d := TeammateDiscussion{ID: "d1", InstallationID: "install", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := bridge.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true, Boundary: "220"}); err != nil {
		t.Fatal(err)
	}
	ready := map[string]any{"session_id": "old", "application": map[string]string{"id": "99"}, "user": map[string]string{"id": "99"}}
	teammateTestEvent(t, bridge, 1, "READY", ready)
	ready["session_id"] = "new"
	teammateTestEvent(t, bridge, 2, "READY", ready)
	check, err := bridge.service.Store.teammateCheckpoint("install")
	if err != nil || check.GapEpoch != 1 {
		t.Fatalf("checkpoint %+v %v", check, err)
	}
	bindings, err := bridge.service.Store.teammateBindings("install")
	if err != nil || len(bindings) != 1 {
		t.Fatal(bindings, err)
	}
	if err = bridge.repair(ctx, bindings[0], check.GapEpoch); err != nil {
		t.Fatal(err)
	}
	bindings, err = bridge.service.Store.teammateBindings("install")
	if err != nil || bindings[0].Boundary != "260" || bindings[0].RepairEpoch != 1 {
		t.Fatalf("repair %+v %v", bindings, err)
	}
	pending, err := bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 0 || len(mgr.admitted) != 0 {
		t.Fatalf("backfill admitted work %+v %+v %v", pending, mgr.admitted, err)
	}
	// Fresh Gateway posts after non-resumable recovery still become ordinary admissions.
	teammateTestEvent(t, bridge, 3, "MESSAGE_CREATE", teammateTestMessage("261", "201", "88", "restating now", false))
	pending, err = bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 1 || pending[0].Opening {
		t.Fatalf("fresh intake %+v %v", pending, err)
	}
	teammateTestEvent(t, bridge, 4, "MESSAGE_DELETE_BULK", map[string]any{"ids": []string{"261", "262"}, "channel_id": "201"})
	pending, err = bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 0 {
		t.Fatalf("bulk deletion %+v %v", pending, err)
	}
}

func TestTeammatePrivateThreadNeedsBotMembership(t *testing.T) {
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, strings.Replace(teammatePublic, `"type":11`, `"type":12`, 1))
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/thread-members/99":
			w.WriteHeader(404)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	_, err := TeammatePermissions(context.Background(), bridge.client, bridge.service.Config, Route{GuildID: "10", ChannelID: "201"}, "99")
	if !teammateAPIStatus(err, http.StatusNotFound) {
		t.Fatalf("private membership: %v", err)
	}
}

func TestTeammateUnconfirmedPublicationRequiresStableNonceAndStopsRetrying(t *testing.T) {
	var nonce string
	var posts atomic.Int32
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages":
			n := posts.Add(1)
			var input struct{ Nonce string }
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if n == 1 {
				nonce = input.Nonce
			} else if input.Nonce != nonce {
				t.Errorf("nonce changed %s -> %s", nonce, input.Nonce)
			}
			w.WriteHeader(500)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	now := time.Now()
	bridge.service.Now = func() time.Time { return now }
	d := TeammateDiscussion{ID: "d1", InstallationID: "install", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := bridge.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d}); err != nil {
		t.Fatal(err)
	}
	p := TeammatePublication{ID: "result", AdmissionID: "input", Body: "verified result"}
	if err := bridge.service.Store.teammateQueueOutput(d, p, now); err != nil {
		t.Fatal(err)
	}
	for i := int32(1); i <= 2; i++ {
		if err := bridge.sendPending(ctx, d); !teammateAPIStatus(err, http.StatusInternalServerError) || posts.Load() != i {
			t.Fatalf("attempt %d: %d %v", i, posts.Load(), err)
		}
	}
	now = now.Add(teammateNonceWindow + time.Second)
	if err := bridge.sendPending(ctx, d); err != nil || posts.Load() != 2 {
		t.Fatalf("late replay: %d %v", posts.Load(), err)
	}
	pending, err := bridge.service.Store.teammateOutputs("install", d.ID)
	if err != nil || len(pending) != 1 || pending[0].Chunks[0].State != StateUncertain {
		t.Fatalf("uncertain %+v %v", pending, err)
	}
	if err = bridge.service.Store.teammateObserve("install", d.Route, "333", pending[0].Chunks[0].ID, p.Body, time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err = bridge.service.Store.teammateOutputs("install", d.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("observed %+v %v", pending, err)
	}
}

func TestTeammateGatewayResumesFromLastCommittedEvent(t *testing.T) {
	var attempts atomic.Int32
	var server *httptest.Server
	resumed := make(chan int64, 1)
	delivered := make(chan struct{}, 1)
	upstream := websocket.Upgrader{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"})
			return
		}
		conn, err := upstream.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if err = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 5000}}); err != nil {
			return
		}
		var request struct {
			Op   int
			Data struct {
				Sequence int64 `json:"seq"`
			} `json:"d"`
		}
		if err = conn.ReadJSON(&request); err != nil {
			return
		}
		if request.Op == 2 {
			_ = conn.WriteJSON(map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]string{"session_id": "session"}})
		} else {
			if request.Op != 6 {
				t.Errorf("unexpected gateway op %d", request.Op)
			}
			resumed <- request.Data.Sequence
			_ = conn.WriteJSON(map[string]any{"op": 0, "t": "RESUMED", "s": 2, "d": nil})
		}
		_ = conn.WriteJSON(map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 3, "d": map[string]string{"id": "33"}})
		for {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	client, err := NewDiscordClient("test")
	if err != nil {
		t.Fatal(err)
	}
	client.apiBase = server.URL
	client.onDispatch = func(f gatewayFrame, _ string) error {
		if f.Type == "MESSAGE_CREATE" {
			if attempts.Add(1) == 1 {
				return ErrCapacity
			}
			delivered <- struct{}{}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err = client.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	select {
	case seq := <-resumed:
		if seq != 1 {
			t.Fatalf("resume sequence %d, want 1", seq)
		}
	case <-ctx.Done():
		t.Fatal("missing resume")
	}
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("uncommitted event was not redelivered")
	}
}

func TestTeammateOutboxCapacityRotatesAndLateReplayUsesGuard(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	d := TeammateDiscussion{ID: "d", InstallationID: "install", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err = store.teammateBind(teammateBinding{TeammateDiscussion: d}); err != nil {
		t.Fatal(err)
	}
	if err = store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(teammateMetaBucket)).Put(teammateKey(d.InstallationID, "outbox_count"), binary.BigEndian.AppendUint64(nil, MaxPublications-1))
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p := TeammatePublication{ID: "first", AdmissionID: "input", Body: "original", QuestionID: "question", QuestionHash: "hash"}
	if err = store.teammateQueueOutput(d, p, now); err != nil {
		t.Fatal(err)
	}
	next := TeammatePublication{ID: "next", AdmissionID: "input", Body: "next"}
	if err = store.teammateQueueOutput(d, next, now); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected cap %v", err)
	}
	out, err := store.teammateOutputs(d.InstallationID, d.ID)
	if err != nil || len(out) != 1 {
		t.Fatalf("outbox %+v %v", out, err)
	}
	if err = store.teammateChangeOutput(d.InstallationID, out[0], func(tx *bolt.Tx, k []byte, p *teammateOutput) error {
		return teammateFinishChunk(tx, k, d.InstallationID, p, 0, "301", false, now)
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.teammateQueueOutput(d, next, now); err != nil {
		t.Fatal(err)
	}
	after := now.Add(GuardTTL + time.Second)
	if err = store.teammateCompactOutbox(d, after, nil); err != nil {
		t.Fatal(err)
	}
	id, _, err := store.teammateQuestion(d, "301")
	if err != nil || id != "question" {
		t.Fatalf("compacted without native acknowledgment: %q %v", id, err)
	}
	if err = store.teammateAckOutputs(d, []string{p.ID}); err != nil {
		t.Fatal(err)
	}
	if err = store.teammateCompactOutbox(d, now.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	id, _, err = store.teammateQuestion(d, "301")
	if err != nil || id != "question" {
		t.Fatalf("compacted before grace: %q %v", id, err)
	}
	if err = store.teammateCompactOutbox(d, after, nil); err != nil {
		t.Fatal(err)
	}
	id, _, err = store.teammateQuestion(d, "301")
	if err != nil || id != "" {
		t.Fatalf("question index retained: %q %v", id, err)
	}
	if err = store.teammateQueueOutput(d, p, after); err != nil {
		t.Fatalf("late replay %v", err)
	}
	p.Body = "different"
	if err = store.teammateQueueOutput(d, p, after); !errors.Is(err, ErrConflict) {
		t.Fatalf("different late replay %v", err)
	}
	out, err = store.teammateOutputs(d.InstallationID, d.ID)
	if err != nil || len(out) != 1 || out[0].Publication.ID != "next" {
		t.Fatalf("outbox %+v %v", out, err)
	}
}

func TestTeammateHistoryOversizedEvidenceCanBeReadInFragments(t *testing.T) {
	message := teammateTestMessage("250", "201", "88", strings.Repeat("海\n", 14000), false)
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages":
			rows := []*discordgo.Message{}
			if r.URL.Query().Get("before") == "" {
				rows = append(rows, message)
			}
			_ = json.NewEncoder(w).Encode(rows)
		case "/channels/201/messages/250":
			_ = json.NewEncoder(w).Encode(message)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	d := TeammateDiscussion{ID: "d", InstallationID: "install", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	q := TeammateHistoryQuery{}
	var evidence strings.Builder
	for i := 0; i < 20; i++ {
		out, err := bridge.history(d)(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if n := historyJSONSize(out); n >= 26<<10 {
			t.Fatalf("fragment response %d bytes", n)
		}
		evidence.WriteString(out.EvidenceFragment)
		if out.Cursor == "" {
			break
		}
		q.Cursor = out.Cursor
	}
	var derived TeammateHistoryMessage
	if err := json.Unmarshal([]byte(evidence.String()), &derived); err != nil || derived.Text != message.Content {
		t.Fatalf("fragment reconstruction length %d: %v", len(derived.Text), err)
	}
}

func TestTeammateLastMileRechecksOriginalParentMessage(t *testing.T) {
	opener := teammateTestMessage("201", "20", "77", "<@99> original", true)
	changed := false
	bridge, mgr := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/20/messages/201":
			current := *opener
			if changed {
				current.Content = "edited"
			}
			_ = json.NewEncoder(w).Encode(current)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	mgr.phase = TeammateAdmissionAccepted
	teammateTestEvent(t, bridge, 1, "MESSAGE_CREATE", opener)
	pending, err := bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	if err = bridge.process(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	a := mgr.admitted[0]
	q := TeammateHistoryQuery{Source: "admission", MessageID: a.Route.MessageID, AdmissionID: a.OperationID, Revision: a.Revision, AuthorID: a.Author.ID}
	if _, err = bridge.history(a.Discussion)(ctx, q); err != nil {
		t.Fatal(err)
	}
	changed = true
	if _, err = bridge.history(a.Discussion)(ctx, q); !errors.Is(err, ErrTeammateSourceChanged) {
		t.Fatalf("changed source: %v", err)
	}
}

func TestTeammateSupersedesQueuedOpeningWithoutOpeningBeforeCutover(t *testing.T) {
	original := teammateTestMessage("202", "201", "77", "<@99> old scope", true)
	content := original.Content
	c, m := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages/202":
			current := *original
			current.Content = content
			_ = json.NewEncoder(w).Encode(current)
		default:
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	m.phase = TeammateAdmissionAccepted
	teammateTestEvent(t, c, 1, "MESSAGE_CREATE", original)
	pending, _ := c.service.Store.teammatePending("install")
	if err := c.process(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	rows, _ := c.service.Store.teammateBindings("install")
	if len(rows) != 1 || rows[0].Opened {
		t.Fatalf("premature discussion open: %+v", rows)
	}
	content = "<@99> revised scope"
	m.phase = TeammateAdmissionSuperseded
	pending, _ = c.service.Store.teammatePending("install")
	if err := c.reconcileAccepted(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(m.admitted) != 2 || m.admitted[0].OperationID != m.admitted[1].OperationID || m.admitted[0].Sequence != m.admitted[1].Sequence ||
		m.admitted[0].Revision == m.admitted[1].Revision || m.admitted[1].Text != content {
		t.Fatalf("replacement %+v", m.admitted)
	}
	rows, _ = c.service.Store.teammateBindings("install")
	if rows[0].Opened {
		t.Fatal("replacement opened prematurely")
	}
	m.phase = TeammateAdmissionCutover
	pending, _ = c.service.Store.teammatePending("install")
	if err := c.reconcileAccepted(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	rows, _ = c.service.Store.teammateBindings("install")
	if !rows[0].Opened {
		t.Fatal("verified cutover did not open discussion")
	}
	pending, _ = c.service.Store.teammatePending("install")
	if len(pending) != 0 {
		t.Fatal(pending)
	}
}

func TestTeammateWithdrawnQueuedTagCancelsProvisionalBinding(t *testing.T) {
	original := teammateTestMessage("202", "201", "77", "<@99> check this", true)
	content := original.Content
	c, m := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages/202":
			current := *original
			current.Content = content
			_ = json.NewEncoder(w).Encode(current)
		default:
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	m.phase = TeammateAdmissionAccepted
	teammateTestEvent(t, c, 1, "MESSAGE_CREATE", original)
	pending, _ := c.service.Store.teammatePending("install")
	if err := c.process(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	content = "Never mind"
	m.phase = TeammateAdmissionSuperseded
	pending, _ = c.service.Store.teammatePending("install")
	if err := c.reconcileAccepted(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	if !m.canceled {
		t.Fatal("native FIFO was left occupied")
	}
	rows, err := c.service.Store.teammateBindings("install")
	if err != nil || len(rows) != 1 || rows[0].Opened || !rows[0].ProvisionalCancelled {
		t.Fatalf("cancelled binding %+v %v", rows, err)
	}
	teammateTestEvent(t, c, 2, "MESSAGE_CREATE", teammateTestMessage("203", "201", "88", "untagged", false))
	pending, err = c.service.Store.teammatePending("install")
	if err != nil || len(pending) != 0 {
		t.Fatalf("cancelled binding accepts chatter %+v %v", pending, err)
	}
}

func TestTeammateIngressGuardsDoNotConsumeActiveCapacity(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const installation = "install"
	candidate := teammateCandidate{Route: Route{GuildID: "10", ChannelID: "20", MessageID: "201"}, Author: Actor{ID: "77"}, Opening: true}
	if err = s.teammateRecord(installation, "session", 1, []teammateCandidate{candidate}, false); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.teammatePending(installation)
	if err = s.teammateCandidateChange(installation, rows[0], StateFailed, "withdrawn", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte(teammateIngressBucket)).Get(teammateKey(installation, candidate.Route.MessageID)) != nil {
			return ErrConflict
		}
		guards := tx.Bucket([]byte(teammateIngressGuardBucket))
		guard := guards.Get(teammateKey(installation, candidate.Route.MessageID))
		for i := range teammateIngressLimit + 1 {
			if err := guards.Put(teammateKey(installation, strconv.Itoa(i+1000)), guard); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.teammateRecord(installation, "session", 2, []teammateCandidate{candidate}, false); err != nil {
		t.Fatal(err)
	}
	candidate.Route.MessageID = "202"
	if err = s.teammateRecord(installation, "session", 3, []teammateCandidate{candidate}, false); err != nil {
		t.Fatal(err)
	}
	rows, err = s.teammatePending(installation)
	if err != nil || len(rows) != 1 || rows[0].Route.MessageID != "202" {
		t.Fatalf("replay or quota %+v %v", rows, err)
	}
}

func (s *Store) teammateBindings(installation string) (out []teammateBinding, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		return teammateEach(tx.Bucket([]byte(teammateDiscussionBucket)), installation, func(_ []byte, raw []byte) error {
			var binding teammateBinding
			if e := json.Unmarshal(raw, &binding); e != nil {
				return e
			}
			out = append(out, binding)
			return nil
		})
	})
	return
}
