package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestGatewayBridgeRoutesHumanMentionAndNeverMirrorsWorkerHistory(t *testing.T) {
	s, w, m := serviceRig(t)
	c, _ := NewDiscordClient("test-token")
	b := NewBridge(s, c)
	message := &discordgo.MessageCreate{Message: &discordgo.Message{ID: "30", GuildID: "10", ChannelID: "20", Content: "<@100> why does reconnect fail?", Timestamp: time.Now(), Author: &discordgo.User{ID: "2", Username: "Alice"}, Mentions: []*discordgo.User{{ID: "100"}}}}
	b.Message(context.Background(), message)
	if len(w.sends) != 1 || len(m.posts) != 1 || len(m.posts[0].Actions) != 1 {
		t.Fatalf("requests=%d posts=%+v", len(w.sends), m.posts)
	}
	message.Author.Bot = true
	message.ID = "bot-response"
	b.Message(context.Background(), message)
	if len(w.sends) != 1 || len(m.posts) != 1 {
		t.Fatal("bot response started a loop or mirrored output")
	}
}
func TestImmutableCommandSnapshotCanBeEndorsedWithoutFetchingAMessage(t *testing.T) {
	s, w, _ := serviceRig(t)
	in := incoming()
	in.Snapshot = true
	r, err := s.Handle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Endorse(context.Background(), Actor{ID: "1"}, r.Ref, "endorse-command", time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(w.sends) != 2 || w.sends[1].Origin.Kind != "owner_endorsed" {
		t.Fatal("command snapshot wasn't endorsed")
	}
}
func TestPostWithoutNonceDoesNotEnableEmptyNonceDeduplication(t *testing.T) {
	base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["enforce_nonce"] == true {
			t.Error("empty nonce requested deduplication")
		}
		_, _ = w.Write([]byte(`{"id":"reply"}`))
	})
	c, _ := NewDiscordClient("test-token")
	c.apiBase = base
	if _, err := c.Post(context.Background(), Outbound{Route: Route{ChannelID: "20"}, Text: "status"}); err != nil {
		t.Fatal(err)
	}
}
