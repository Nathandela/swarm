package discord

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/bwmarrin/discordgo"
)

type controlWorkers struct {
	*fakeWorkers
	launches   []protocol.LaunchReq
	killed     []string
	decisions  []protocol.NativeDecisionReq
	launchErr  error
	native     protocol.NativeRequestsReply
	launchGate <-chan struct{}
}

func (w *controlWorkers) Launch(_ context.Context, r protocol.LaunchReq) (string, string, error) {
	if w.launchGate != nil {
		<-w.launchGate
	}
	w.launches = append(w.launches, r)
	return "local/new", r.Name, w.launchErr
}
func (w *controlWorkers) Resume(context.Context, string) (string, string, error) {
	return "local/resumed", "Resumed", nil
}
func (w *controlWorkers) Kill(_ context.Context, id string) error {
	w.killed = append(w.killed, id)
	return nil
}
func (w *controlWorkers) Requests(context.Context, protocol.NativeRequestsReq) (protocol.NativeRequestsReply, error) {
	return w.native, nil
}
func (w *controlWorkers) Decide(_ context.Context, _ string, r protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, error) {
	w.decisions = append(w.decisions, r)
	return protocol.NativeDecisionReceipt{State: "admitted"}, nil
}
func controlsRig(t *testing.T) (*Bridge, *controlWorkers) {
	t.Helper()
	s, w, _ := serviceRig(t)
	s.Config.Enabled = true
	s.Config.WorkDir = t.TempDir()
	cw := &controlWorkers{fakeWorkers: w}
	s.Workers = cw
	c, _ := NewDiscordClient("fake-token")
	return NewBridge(s, c), cw
}
func ownerInteraction(id, owner string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: id, AppID: "100", User: &discordgo.User{ID: owner, Username: "Owner"}, Type: discordgo.InteractionApplicationCommand}}
}
func controlOption(name string, values map[string]string) *discordgo.ApplicationCommandInteractionDataOption {
	option := &discordgo.ApplicationCommandInteractionDataOption{Name: name}
	for key, value := range values {
		option.Options = append(option.Options, &discordgo.ApplicationCommandInteractionDataOption{Name: key, Type: discordgo.ApplicationCommandOptionString, Value: value})
	}
	return option
}
func TestOwnerControlsClaimBeforeEffectAndDeduplicate(t *testing.T) {
	b, w := controlsRig(t)
	ctx := context.Background()
	route := Route{GuildID: "10", ChannelID: "20", MessageID: "interaction"}
	option := controlOption("new", map[string]string{"cli": "codex", "name": "Worker", "prompt": "exact Unicode 🙂", "model": "luna"})
	i := ownerInteraction("create", "1")
	if _, e := b.controlCommand(ctx, i, Actor{ID: "2"}, route, option); !errors.Is(e, ErrNotAuthorized) || len(w.launches) != 0 {
		t.Fatal("nonowner created", e)
	}
	first, e := b.controlCommand(ctx, i, Actor{ID: "1"}, route, option)
	if e != nil {
		t.Fatal(e)
	}
	again, e := b.controlCommand(ctx, i, Actor{ID: "1"}, route, option)
	if e != nil || first != again || len(w.launches) != 1 {
		t.Fatal("duplicate launch", first, again, e, len(w.launches))
	}
	r := w.launches[0]
	if r.Env != nil || r.Cwd != b.Service.Config.WorkDir || !strings.Contains(r.InitialPrompt, "swarm discord reply") || r.Options["model"] != "luna" {
		t.Fatal(r)
	}
	if _, e = b.controlCommand(ctx, ownerInteraction("stop", "1"), Actor{ID: "1"}, route, controlOption("stop", map[string]string{"agent": "Reviewer"})); e == nil || len(w.killed) != 0 {
		t.Fatal("nonexact ID stopped", e)
	}
}
func TestOwnerUncertainCreateIsNeverRepeated(t *testing.T) {
	b, w := controlsRig(t)
	w.launchErr = errors.New("lost acknowledgement")
	i := ownerInteraction("unknown", "1")
	option := controlOption("new", map[string]string{"cli": "codex"})
	route := Route{ChannelID: "20", MessageID: "unknown"}
	_, _ = b.controlCommand(context.Background(), i, Actor{ID: "1"}, route, option)
	_, e := b.controlCommand(context.Background(), i, Actor{ID: "1"}, route, option)
	if !errors.Is(e, ErrDeliveryUncertain) || len(w.launches) != 1 {
		t.Fatal(e, len(w.launches))
	}
}
func TestOwnerInvalidLaunchHasNoEffects(t *testing.T) {
	b, w := controlsRig(t)
	for _, values := range []map[string]string{{"cli": "sh"}, {"cli": "codex", "model": "--flag"}, {"cli": "codex", "dir": "relative"}, {"cli": "codex", "unexpected": "value"}} {
		_, e := b.controlCommand(context.Background(), ownerInteraction("invalid", "1"), Actor{ID: "1"}, Route{ChannelID: "20", MessageID: "x"}, controlOption("new", values))
		if e == nil {
			t.Fatal(values)
		}
	}
	if len(w.launches) != 0 {
		t.Fatal(w.launches)
	}
}
func TestClaudeAliasesUseCanonicalNativeRegistry(t *testing.T) {
	b, w := controlsRig(t)
	for _, cli := range []string{"claude", "claude-code"} {
		i := ownerInteraction(cli, "1")
		if _, e := b.controlCommand(context.Background(), i, Actor{ID: "1"}, Route{ChannelID: "20", MessageID: i.ID}, controlOption("new", map[string]string{"cli": cli})); e != nil {
			t.Fatal(e)
		}
		agent := w.launches[len(w.launches)-1].Agent
		if agent != "claude" || !registry.IsProduction(agent) {
			t.Fatal("Claude alias bypassed native registry", agent)
		}
	}
}

func TestConcurrentOwnerCreateHasOneEffect(t *testing.T) {
	b, w := controlsRig(t)
	i := ownerInteraction("concurrent", "1")
	option := controlOption("new", map[string]string{"cli": "codex"})
	route := Route{ChannelID: "20", MessageID: i.ID}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			_, e := b.controlCommand(context.Background(), i, Actor{ID: "1"}, route, option)
			if e != nil && !errors.Is(e, ErrDeliveryUncertain) {
				t.Error(e)
			}
		})
	}
	group.Wait()
	if len(w.launches) != 1 {
		t.Fatal("concurrent create duplicated", len(w.launches))
	}
}
func TestUnsupportedNativeRequestsNeverUseRawInput(t *testing.T) {
	b, w := controlsRig(t)
	w.native = protocol.NativeRequestsReply{Reason: "adapter has no structured native requests"}
	if _, e := b.ownerInbox(context.Background(), w.rows[0]); !errors.Is(e, ErrControlUnsupported) || len(w.sends) != 0 || len(w.decisions) != 0 {
		t.Fatal(e, w.sends, w.decisions)
	}
}
func TestContextMenusPreserveOriginalAuthorAndInvoker(t *testing.T) {
	b, workers := controlsRig(t)
	i := ownerInteraction("quote", "2")
	i.Data = discordgo.ApplicationCommandInteractionData{Name: "Ask Swarm", CommandType: discordgo.MessageApplicationCommand, TargetID: "source", Resolved: &discordgo.ApplicationCommandInteractionDataResolved{Messages: map[string]*discordgo.Message{"source": {ID: "source", ChannelID: "20", Content: "original owner text", Author: &discordgo.User{ID: "1", Username: "Original owner"}}}}}
	route := Route{GuildID: "10", ChannelID: "20", MessageID: i.ID}
	b.Service.Config.ReceptionistCLI = ""
	_, e := b.contextCommand(context.Background(), i, Actor{ID: "2"}, route)
	if e != nil {
		t.Fatal(e)
	}
	r, e := b.Service.Store.ExistingEvent("context:"+i.ID, b.Service.Now())
	if e != nil || r.Author.ID != "1" || r.Authority != AuthorityExternal || r.RequesterID != "2" || r.Text != "original owner text" {
		t.Fatal(r, e)
	}
	if _, e = b.selectRequest(context.Background(), Actor{ID: "2"}, r, "local/one", "choice"); e != nil {
		t.Fatal(e)
	}
	if len(workers.sends) != 1 || workers.sends[0].Origin.Kind != "external" || workers.sends[0].Origin.ActorID != "1" || workers.sends[0].Origin.RequesterID != "2" {
		t.Fatal("quoted owner text gained authority", workers.sends)
	}
	i.Data = discordgo.ApplicationCommandInteractionData{Name: "Treat as my instruction", CommandType: discordgo.MessageApplicationCommand}
	if _, e = b.contextCommand(context.Background(), i, Actor{ID: "2"}, route); !errors.Is(e, ErrNotAuthorized) {
		t.Fatal("external quoted text endorsed", e)
	}
	if strings.Contains(r.Text, filepath.Base(b.Service.Config.TokenFile)) && b.Service.Config.TokenFile != "" {
		t.Fatal("token path leaked")
	}
}

func TestNativeQuestionSelectionUsesOnlyObservedChoices(t *testing.T) {
	b, w := controlsRig(t)
	view := nativeView(b.Service.Now())
	view.Kind = "question"
	view.Decisions = []protocol.NativeDecisionOption{{ID: "question_reply", Label: "Send answers"}, {ID: "question_reject", Label: "Reject"}}
	view.Questions = []protocol.NativeQuestionView{{Question: "Choose", Options: []protocol.NativeQuestionOption{{Label: "Exact A"}, {Label: "Exact B"}}}}
	w.native.Request = &view
	card, e := b.saveNativeCard(view)
	if e != nil {
		t.Fatal(e)
	}
	card.ChannelID = "dm"
	if e = b.markNativeCard(card, StateSent); e != nil {
		t.Fatal(e)
	}
	i := ownerInteraction("answer", "1")
	i.Type = discordgo.InteractionMessageComponent
	i.Data = discordgo.MessageComponentInteractionData{CustomID: "native:" + card.Ref + ":question_reply", Values: []string{"99"}}
	route := Route{ChannelID: "dm", MessageID: i.ID}
	if _, e = b.nativeAction(context.Background(), i, Actor{ID: "1"}, route); !errors.Is(e, ErrInvalid) {
		t.Fatal("arbitrary answer accepted", e)
	}
	i.Data = discordgo.MessageComponentInteractionData{CustomID: "native:" + card.Ref + ":question_reply", Values: []string{"1"}}
	if _, e = b.nativeAction(context.Background(), i, Actor{ID: "1"}, route); e != nil {
		t.Fatal(e)
	}
	if len(w.decisions) != 1 || w.decisions[0].Answers[0][0] != "Exact B" || w.decisions[0].Kind != "question_reply" {
		t.Fatal(w.decisions)
	}
}

func TestContextEndorsementDuplicateInteractionsShareOneInstruction(t *testing.T) {
	b, w := controlsRig(t)
	b.Service.Config.ReceptionistCLI = ""
	route := Route{GuildID: "10", ChannelID: "20"}
	source := &discordgo.Message{ID: "source", ChannelID: "20", Content: "external task", Author: &discordgo.User{ID: "2", Username: "Original"}}
	for _, id := range []string{"endorse-one", "endorse-two"} {
		i := ownerInteraction(id, "1")
		i.Data = discordgo.ApplicationCommandInteractionData{Name: "Treat as my instruction", CommandType: discordgo.MessageApplicationCommand, TargetID: source.ID, Resolved: &discordgo.ApplicationCommandInteractionDataResolved{Messages: map[string]*discordgo.Message{source.ID: source}}}
		if _, e := b.contextCommand(context.Background(), i, Actor{ID: "1"}, route); e != nil {
			t.Fatal(e)
		}
	}
	messenger := b.Service.Messenger.(*fakeMessenger)
	if len(messenger.posts) != 1 {
		t.Fatal("duplicate context endorsement made multiple routing claims", len(messenger.posts))
	}
	ref := strings.TrimPrefix(messenger.posts[0].Actions[0].ID, "choose:")
	r, e := b.Service.Store.Request(ref, b.Service.Now())
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"choose-one", "choose-two"} {
		if _, e = b.selectRequest(context.Background(), Actor{ID: "1"}, r, "local/one", id); e != nil {
			t.Fatal(e)
		}
	}
	if len(w.sends) != 1 || w.sends[0].Origin.Kind != "owner_endorsed" || w.sends[0].Origin.ActorID != "2" || w.sends[0].Origin.EndorserID != "1" {
		t.Fatal(w.sends)
	}
}

func nativeView(now time.Time) protocol.NativeRequestView {
	expiry := now.Add(time.Minute)
	return protocol.NativeRequestView{Session: "local/one", SessionInstance: "instance", RootID: "root", Generation: "generation", NativeSessionID: "native", RequestID: "permission", RequestHash: strings.Repeat("a", 64), ExpiresAt: &expiry, Kind: "permission_reply", Permission: "private command", Decisions: []protocol.NativeDecisionOption{{ID: "once", Label: "Allow once"}, {ID: "reject", Label: "Reject"}}}
}
func TestNativeCardsArePrivateExactAndSeparateFromEndorsement(t *testing.T) {
	b, w := controlsRig(t)
	view := nativeView(b.Service.Now())
	w.native = protocol.NativeRequestsReply{Request: &view}
	card, e := b.saveNativeCard(view)
	if e != nil {
		t.Fatal(e)
	}
	card.ChannelID = "dm"
	if e = b.markNativeCard(card, StateSent); e != nil {
		t.Fatal(e)
	}
	i := ownerInteraction("approve", "1")
	i.Type = discordgo.InteractionMessageComponent
	i.Data = discordgo.MessageComponentInteractionData{CustomID: "native:" + card.Ref + ":once"}
	route := Route{ChannelID: "dm", MessageID: i.ID}
	if _, e = b.nativeAction(context.Background(), i, Actor{ID: "2"}, route); !errors.Is(e, ErrNotAuthorized) {
		t.Fatal(e)
	}
	if _, e = b.nativeAction(context.Background(), i, Actor{ID: "1"}, Route{GuildID: "10", ChannelID: "20"}); !errors.Is(e, ErrNotAuthorized) {
		t.Fatal("public approval accepted", e)
	}
	stale := view
	stale.Generation = "other"
	w.native.Request = &stale
	if _, e = b.nativeAction(context.Background(), i, Actor{ID: "1"}, route); !errors.Is(e, ErrConflict) || len(w.decisions) != 0 {
		t.Fatal("stale native request approved", e)
	}
	w.native.Request = &view
	for range 2 {
		if _, e = b.nativeAction(context.Background(), i, Actor{ID: "1"}, route); e != nil {
			t.Fatal(e)
		}
	}
	if len(w.decisions) != 1 || len(w.sends) != 0 || w.decisions[0].RequestHash != view.RequestHash {
		t.Fatal(w.decisions, w.sends)
	}
	b.Service.Now = func() time.Time { return view.ExpiresAt.Add(time.Second) }
	if _, e = b.nativeAction(context.Background(), i, Actor{ID: "1"}, route); !errors.Is(e, ErrExpired) {
		t.Fatal("expired control admitted", e)
	}
}
func TestOwnerInboxFailureKeepsNativePendingWithoutPublicDetails(t *testing.T) {
	b, w := controlsRig(t)
	view := nativeView(b.Service.Now())
	w.native = protocol.NativeRequestsReply{Request: &view}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	b.Client.apiBase = server.URL
	b.Client.http = server.Client()
	message, e := b.ownerInbox(context.Background(), w.rows[0])
	if e != nil || strings.Contains(message, "private command") || !strings.Contains(message, "Waiting for owner") || len(w.decisions) != 0 {
		t.Fatal(message, e)
	}
	card, e := b.saveNativeCard(view)
	if e != nil || card.State != StateSending {
		t.Fatal("pending card lost", card, e)
	}
}
