package discord

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestDiscoverTeammateIdentityRequiresUnambiguousGuild(t *testing.T) {
	for _, tc := range []struct {
		name, guilds string
		config       Config
		ok           bool
	}{
		{"one", `[{"id":"51"}]`, Config{}, true},
		{"two", `[{"id":"51"},{"id":"52"}]`, Config{}, false},
		{"mismatch", "", Config{ApplicationID: "2"}, false},
		{"chosen server", "", Config{GuildIDs: []string{"51"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := NewDiscordClient("fixture")
			client.apiBase = mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("unexpected mutation %s", r.Method)
				}
				switch r.URL.Path {
				case "/users/@me":
					_, _ = fmt.Fprint(w, `{"id":"1","username":"Workshop","bot":true}`)
				case "/users/@me/guilds":
					if tc.guilds == "" {
						t.Error("unexpected guild lookup")
					}
					_, _ = fmt.Fprint(w, tc.guilds)
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			})
			err := discoverTeammateIdentity(context.Background(), client, &tc.config)
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
			if tc.ok && (tc.config.ApplicationID != "1" || tc.config.Name != "Workshop" || len(tc.config.GuildIDs) != 1 || tc.config.GuildIDs[0] != "51") {
				t.Fatal(tc.config)
			}
		})
	}
}
