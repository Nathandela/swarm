package discord

import (
	"context"
	"github.com/bwmarrin/discordgo"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInteractionAcknowledgementDoesNotWaitForMessageRateLimit(t *testing.T) {
	limited := make(chan struct{})
	var messages atomic.Int32
	base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/interactions/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if messages.Add(1) == 1 {
			close(limited)
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"retry_after":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"message"}`))
	})
	c, _ := NewDiscordClient("fake")
	c.apiBase = base
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Post(ctx, Outbound{Route: Route{ChannelID: "20"}, Text: "progress"}); done <- err }()
	<-limited
	ackCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if err := c.REST(ackCtx, http.MethodPost, "/interactions/1/token/callback", map[string]int{"type": 5}, nil); err != nil {
		t.Fatal("interactive acknowledgement blocked behind progress:", err)
	}
	cancel()
	<-done
}

func TestInteractionAcknowledgementPrecedesThreadScopeLookup(t *testing.T) {
	s, _, _ := serviceRig(t)
	s.Config.ChannelIDs = []string{"40"}
	limited, acknowledged := make(chan struct{}), make(chan struct{})
	base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/interactions/") {
			close(acknowledged)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		close(limited)
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"retry_after":1}`))
	})
	c, _ := NewDiscordClient("fake")
	c.apiBase = base
	s.Messenger = ScopedMessenger{Client: c, Config: s.Config}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Post(ctx, Outbound{Route: Route{ChannelID: "20"}, Text: "progress"})
	}()
	<-limited
	b := NewBridge(s, c)
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "123", AppID: "100", Token: "token", Type: discordgo.InteractionApplicationCommand, GuildID: "10", ChannelID: "20", Member: &discordgo.Member{User: &discordgo.User{ID: "2"}}, Data: discordgo.ApplicationCommandInteractionData{Name: "swarm", Options: []*discordgo.ApplicationCommandInteractionDataOption{{Name: "agents"}}}}}
	finished := make(chan struct{})
	go func() { defer close(finished); b.Interaction(ctx, i) }()
	select {
	case <-acknowledged:
	case <-time.After(200 * time.Millisecond):
		t.Error("button acknowledgement waited for channel lookup")
	}
	cancel()
	<-finished
	<-done
}
