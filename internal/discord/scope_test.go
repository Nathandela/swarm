package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestConfiguredParentAllowsThreadAndPublicationRechecksItsScope(t *testing.T) {
	var moved atomic.Bool
	var posts atomic.Int32
	base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			parent := "20"
			if moved.Load() {
				parent = "99"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "21", "guild_id": "10", "parent_id": parent, "type": 11})
			return
		}
		posts.Add(1)
		_, _ = w.Write([]byte(`{"id":"posted"}`))
	})
	client, _ := NewDiscordClient("fake")
	client.apiBase = base
	messenger := ScopedMessenger{Client: client, Config: Config{GuildIDs: []string{"10"}, ChannelIDs: []string{"20"}}}
	route, err := messenger.Resolve(context.Background(), Route{GuildID: "10", ChannelID: "21", MessageID: "30"})
	if err != nil {
		t.Fatal(err)
	}
	if route.ParentID != "20" {
		t.Fatal(route)
	}
	if _, err = messenger.Post(context.Background(), Outbound{Route: route, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	moved.Store(true)
	if _, err = messenger.Post(context.Background(), Outbound{Route: route, Text: "should not post"}); !errors.Is(err, ErrNotAuthorized) {
		t.Fatal(err)
	}
	if posts.Load() != 1 {
		t.Fatal("publication escaped configured scope")
	}
}

func TestParentScopeRequiresAnActualDiscordThread(t *testing.T) {
	for _, kind := range []int{0, 2, 4, 10, 11, 12, 15, 16, 99} {
		t.Run(strconv.Itoa(kind), func(t *testing.T) {
			base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "21", "guild_id": "10", "parent_id": "20", "type": kind})
			})
			client, _ := NewDiscordClient("fake")
			client.apiBase = base
			messenger := ScopedMessenger{Client: client, Config: Config{GuildIDs: []string{"10"}, ChannelIDs: []string{"20"}}}
			_, err := messenger.Resolve(context.Background(), Route{GuildID: "10", ChannelID: "21"})
			if kind >= 10 && kind <= 12 {
				if err != nil {
					t.Fatal("actual thread was refused", kind, err)
				}
			} else if !errors.Is(err, ErrNotAuthorized) {
				t.Fatal("non-thread inherited access from a category", kind, err)
			}
		})
	}
}
