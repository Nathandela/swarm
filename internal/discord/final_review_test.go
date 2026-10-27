package discord

import (
	"context"
	"github.com/bwmarrin/discordgo"
	"strings"
	"testing"
	"time"
)

type reviewDelayedScope struct {
	*fakeMessenger
	entered, release chan struct{}
}

func (m reviewDelayedScope) Resolve(ctx context.Context, r Route) (Route, error) {
	close(m.entered)
	select {
	case <-m.release:
		return r, nil
	case <-ctx.Done():
		return Route{}, ctx.Err()
	}
}
func TestReviewDeleteCannotBeUndoneByPendingCreate(t *testing.T) {
	s, w, m := serviceRig(t)
	in := incoming()
	if e := s.Join(in.Route, w.rows[0].Target); e != nil {
		t.Fatal(e)
	}
	gate := reviewDelayedScope{m, make(chan struct{}), make(chan struct{})}
	s.Messenger = gate
	c, _ := NewDiscordClient("test")
	b := NewBridge(s, c)
	done := make(chan struct{})
	msg := &discordgo.MessageCreate{Message: &discordgo.Message{ID: "30", GuildID: "10", ChannelID: "20", Content: "deleted private text", Timestamp: time.Now(), Author: &discordgo.User{ID: "2"}}}
	go func() { defer close(done); b.Message(context.Background(), msg) }()
	<-gate.entered
	b.MessageDelete(&discordgo.MessageDelete{Message: &discordgo.Message{ID: "30", ChannelID: "20"}})
	close(gate.release)
	<-done
	if rows := s.threadContext("20"); len(rows) != 0 {
		t.Fatalf("deleted text restored after delayed create: %+v", rows)
	}
}
func TestReviewNewWorkerReplyResolvesReceiver(t *testing.T) {
	b, w := controlsRig(t)
	i := ownerInteraction("new-reply", "1")
	route := Route{GuildID: "10", ChannelID: "20", MessageID: i.ID}
	if _, e := b.controlCommand(context.Background(), i, Actor{ID: "1"}, route, controlOption("new", map[string]string{"cli": "codex", "name": "New", "prompt": "task"})); e != nil {
		t.Fatal(e)
	}
	record, e := b.Service.Store.ExistingEvent("control:"+i.ID, b.Service.Now())
	if e != nil {
		t.Fatal(e)
	}
	w.rows = append(w.rows, Worker{Target: Target{ID: "local/new", Instance: "real", Name: "New", Agent: "codex"}, State: "running"})
	if _, e = b.Service.Reply(context.Background(), record.Ref, "", "done"); e != nil {
		t.Fatal(e)
	}
	target, e := b.Service.TargetForReply(context.Background(), "20", "posted-1")
	if e != nil || target.ID != "local/new" {
		t.Fatal(target, e)
	}
}
func TestReviewResumedReceptionistCanBeReset(t *testing.T) {
	b, w := controlsRig(t)
	old := receptionistLookPath
	defer func() { receptionistLookPath = old }()
	receptionistLookPath = func(string) (string, error) { return "/mock/codex", nil }
	b.Service.Config.ReceptionistCLI = "codex"
	b.Service.Config.ReceptionistModel = "luna"
	b.Service.Now = func() time.Time { return testNow }
	input := sampleRequest()
	input.Target.ID = "routing"
	first := mustAdmit(t, b.Service.Store, input)
	if _, e := b.receptionist(context.Background(), first); e != nil {
		t.Fatal(e)
	}
	w.rows = []Worker{{Target: Target{ID: "local/resumed", Instance: "resumed", Name: "Role", Agent: "codex"}, Supersedes: []string{"local/new"}, State: "exited"}}
	if _, e := b.Service.Roster(context.Background()); e != nil {
		t.Fatal(e)
	}
	i := ownerInteraction("reset", "1")
	_, e := b.controlCommand(context.Background(), i, Actor{ID: "1"}, Route{ChannelID: "20", MessageID: i.ID}, controlOption("receptionist-reset", map[string]string{"agent": "local/resumed"}))
	if e != nil {
		t.Fatalf("confirmed stopped explicit successor cannot reset role: %v", e)
	}
}

func TestReviewPickerReplayDoesNotResubmitAfterResume(t *testing.T) {
	b, w := controlsRig(t)
	b.Service.Now = func() time.Time { return testNow }
	input := sampleRequest()
	input.Target = Target{ID: "routing", Instance: b.Service.Config.InstallationID}
	r := mustAdmit(t, b.Service.Store, input)
	actor := r.Author
	first, e := b.selectRequest(context.Background(), actor, r, "local/one", "same-interaction")
	if e != nil {
		t.Fatal(e)
	}
	w.rows[0].ID = "local/resumed"
	w.rows[0].Instance = "new-instance"
	w.rows[0].Supersedes = []string{"local/one"}
	again, e := b.selectRequest(context.Background(), actor, r, "local/one", "same-interaction")
	if e != nil || len(w.sends) != 1 || again.Ref != first.Ref {
		t.Fatalf("selection replay after resume sent again: sends=%d old=%s new=%s err=%v", len(w.sends), first.Ref, again.Ref, e)
	}
}

func TestReviewSourceAuthorCannotSpendOwnerEndorsement(t *testing.T) {
	b, w := controlsRig(t)
	b.Service.Config.ReceptionistCLI = ""
	source := &discordgo.Message{ID: "source", ChannelID: "20", Content: "do this", Author: &discordgo.User{ID: "2", Username: "Colleague"}}
	i := ownerInteraction("endorse-owner", "1")
	i.Data = discordgo.ApplicationCommandInteractionData{Name: "Treat as my instruction", CommandType: discordgo.MessageApplicationCommand, TargetID: source.ID, Resolved: &discordgo.ApplicationCommandInteractionDataResolved{Messages: map[string]*discordgo.Message{source.ID: source}}}
	route := Route{GuildID: "10", ChannelID: "20", MessageID: i.ID}
	if _, e := b.contextCommand(context.Background(), i, Actor{ID: "1"}, route); e != nil {
		t.Fatal(e)
	}
	posts := b.Service.Messenger.(*fakeMessenger).posts
	ref := strings.TrimPrefix(posts[0].Actions[0].ID, "choose:")
	r, e := b.Service.Store.Request(ref, b.Service.Now())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.selectRequest(context.Background(), Actor{ID: "2"}, r, "local/one", "colleague-click"); e == nil || len(w.sends) != 0 {
		t.Fatalf("source author used owner's endorsement to submit: err=%v sends=%+v", e, w.sends)
	}
}

func TestReviewExpiredBotReplyCannotChooseUnrelatedWorker(t *testing.T) {
	s, w, _ := serviceRig(t)
	r, e := s.Handle(context.Background(), incoming())
	if e != nil {
		t.Fatal(e)
	}
	pub, e := s.Reply(context.Background(), r.Ref, "", "old answer")
	if e != nil {
		t.Fatal(e)
	}
	id := pub.DiscordMessageIDs[0]
	later := r.CreatedAt.Add(GuardTTL + time.Hour)
	if e = s.Store.Prune(later); e != nil {
		t.Fatal(e)
	}
	s.Now = func() time.Time { return later }
	w.rows = []Worker{{Target: Target{ID: "local/unrelated", Instance: "other", Name: "B", Agent: "codex"}, State: "running"}}
	c, _ := NewDiscordClient("test")
	b := NewBridge(s, c)
	msg := &discordgo.MessageCreate{Message: &discordgo.Message{ID: "follow-up", GuildID: "10", ChannelID: "20", Content: "continue", Timestamp: later, Author: &discordgo.User{ID: "2"}, MessageReference: &discordgo.MessageReference{MessageID: id, ChannelID: "20"}, ReferencedMessage: &discordgo.Message{ID: id, Author: &discordgo.User{ID: "100", Bot: true}}}}
	b.Message(context.Background(), msg)
	if len(w.sends) != 1 {
		t.Fatalf("reply to expired agent A silently sent to unrelated worker B: %+v", w.sends)
	}
}
