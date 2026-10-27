package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/bwmarrin/discordgo"
)

type autocompleteWorkers struct {
	rows             []Worker
	rosters, submits int
}

func (w *autocompleteWorkers) Roster(context.Context) ([]Worker, error) {
	w.rosters++
	return w.rows, nil
}
func (w *autocompleteWorkers) Submit(context.Context, string, protocol.ComposerSendReq) (protocol.SemanticMessageReceipt, error) {
	w.submits++
	return protocol.SemanticMessageReceipt{}, nil
}
func TestAutocompleteRoster(t *testing.T) {
	s, _, _ := serviceRig(t)
	workers := &autocompleteWorkers{}
	s.Workers = workers
	for n := 0; n < 55; n++ {
		workers.rows = append(workers.rows, Worker{Target: Target{ID: fmt.Sprintf("id-%02d", n), Instance: "i", Name: "Atlas", Agent: "codex"}, Project: "project"})
	}
	workers.rows[50].Name = "星 renamed"
	var response struct {
		Type int `json:"type"`
		Data struct {
			Choices []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"choices"`
		} `json:"data"`
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/interactions/123/token/callback" {
			t.Errorf("path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&response); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, _ := NewDiscordClient("secret")
	client.apiBase = server.URL
	b := NewBridge(s, client)
	option := &discordgo.ApplicationCommandInteractionDataOption{Name: "agent", Focused: true, Value: ""}
	in := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "123", Token: "token", Type: discordgo.InteractionApplicationCommandAutocomplete, Data: discordgo.ApplicationCommandInteractionData{Name: "swarm", Options: []*discordgo.ApplicationCommandInteractionDataOption{{Name: "ask", Options: []*discordgo.ApplicationCommandInteractionDataOption{option}}}}}}
	route := Route{GuildID: "10", ChannelID: "20"}
	if err := b.autocomplete(context.Background(), in, Actor{ID: ""}, route); err == nil || workers.rosters != 0 || calls != 0 {
		t.Fatalf("unauthorized: %v roster %d calls %d", err, workers.rosters, calls)
	}
	if err := b.autocomplete(context.Background(), in, Actor{ID: "2"}, route); err != nil {
		t.Fatal(err)
	}
	if response.Type != 8 || len(response.Data.Choices) != 25 {
		t.Fatalf("response %+v", response)
	}
	if response.Data.Choices[0].Name == response.Data.Choices[1].Name || response.Data.Choices[0].Value != "id-00" {
		t.Fatalf("duplicate labels %+v", response.Data.Choices[:2])
	}
	option.Value = "星 RENAMED"
	if err := b.autocomplete(context.Background(), in, Actor{ID: "2"}, route); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Choices) != 1 || response.Data.Choices[0].Value != "id-50" {
		t.Fatalf("filtered %+v", response)
	}
	workers.rows[50].Name = strings.Repeat("🙂", 200)
	option.Value = "id-50"
	if err := b.autocomplete(context.Background(), in, Actor{ID: "2"}, route); err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(response.Data.Choices[0].Name) > 100 {
		t.Fatal("choice label too long")
	}
	if len(utf16.Encode([]rune(response.Data.Choices[0].Name))) > 100 {
		t.Fatal("emoji choice exceeds Discord UTF16 limit")
	}

	if err := s.Store.SetOptOut(workers.rows[50].Target, true); err != nil {
		t.Fatal(err)
	}
	option.Value = "id-50"
	if err := b.autocomplete(context.Background(), in, Actor{ID: "2"}, route); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Choices) != 0 {
		t.Fatal("opted-out agent exposed")
	}

	if workers.submits != 0 {
		t.Fatal("submitted a worker request")
	}
}
