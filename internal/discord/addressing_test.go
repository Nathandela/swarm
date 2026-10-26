package discord

import (
	"context"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestExplicitAgentNamesAreDirectAndAmbiguousNamesStayChoices(t *testing.T) {
	rows := []Worker{{Target: Target{ID: "a", Name: "API"}}, {Target: Target{ID: "b", Name: "API reviewer"}}, {Target: Target{ID: "c", Name: "夜の群れ 🙂"}}}
	for text, want := range map[string]string{"API reviewer: explain": "b", "api explain": "a", "夜の群れ 🙂 explain": "c", "APIary is slow": ""} {
		if got := explicitAgent(text, rows); got != want {
			t.Fatalf("%q resolved%q want%q", text, got, want)
		}
	}
	rows = append(rows, Worker{Target: Target{ID: "d", Name: "API reviewer"}})
	if explicitAgent("API reviewer: task", rows) != "" {
		t.Fatal("duplicate display names silently chose an agent")
	}
}
func TestMentioningNamedAgentBypassesRoutingModel(t *testing.T) {
	s, w, _ := serviceRig(t)
	w.rows[0].Name = "API reviewer"
	w.rows = append(w.rows, Worker{Target: Target{ID: "local/other", Instance: "other", Name: "Frontend", Agent: "claude"}, State: "running"})
	c, _ := NewDiscordClient("fake")
	b := NewBridge(s, c)
	b.Message(context.Background(), &discordgo.MessageCreate{Message: &discordgo.Message{ID: "name-event", GuildID: "10", ChannelID: "20", Content: "<@100> API reviewer: explain the retry", Author: &discordgo.User{ID: "2", Username: "Alice"}, Mentions: []*discordgo.User{{ID: "100"}}, Timestamp: time.Now()}})
	if len(w.sends) != 1 || w.sends[0].Session != "local/one" {
		t.Fatal("explicit agent name did not route directly")
	}
}
