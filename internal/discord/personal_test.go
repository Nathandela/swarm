package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

const personalFixture = `{"id":"20","type":1,"recipients":[{"id":"30","username":"Owner"}]}`
const personalMessage = `{"id":"200","channel_id":"20","content":"Please check this checkout.","author":{"id":"30","username":"Owner"},"type":0}`

func TestPersonalDMDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, pin, response string
		ok                  bool
	}{
		{"private", "", personalFixture, true},
		{"same pin", "20", personalFixture, true},
		{"changed pin", "21", personalFixture, false},
		{"group", "", `{"id":"20","type":3,"recipients":[{"id":"30"}]}`, false},
		{"different recipient", "", `{"id":"20","type":1,"recipients":[{"id":"31"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := NewDiscordClient("fixture")
			client.apiBase = mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /users/@me":
					_, _ = fmt.Fprint(w, `{"id":"99","username":"Mac","bot":true}`)
				case "POST /users/@me/channels":
					var req map[string]string
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req["recipient_id"] != "30" {
						t.Errorf("Create DM request %v: %v", req, err)
					}
					_, _ = fmt.Fprint(w, tc.response)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			cfg := Config{Mode: "personal", OwnerID: "30", DMChannelID: tc.pin}
			err := discoverTeammateIdentity(context.Background(), client, &cfg)
			if (err == nil) != tc.ok || tc.ok && (cfg.ApplicationID != "99" || cfg.Name != "Mac" || cfg.DMChannelID != "20" || len(cfg.GuildIDs) != 0) {
				t.Fatalf("discovery: %+v, %v", cfg, err)
			}
		})
	}
}

func TestPersonalDMScopeAdmissionHistoryAndPublication(t *testing.T) {
	var group atomic.Bool
	var sent atomic.Int32
	conn, manager := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /channels/20":
			if group.Load() {
				_, _ = fmt.Fprint(w, `{"id":"20","type":3,"recipients":[{"id":"30"},{"id":"31"}]}`)
			} else {
				_, _ = fmt.Fprint(w, personalFixture)
			}
		case "GET /channels/20/messages/200":
			_, _ = fmt.Fprint(w, personalMessage)
		case "GET /channels/20/messages":
			_, _ = fmt.Fprint(w, "["+personalMessage+"]")
		case "POST /channels/20/messages":
			var req struct {
				Content string
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Content != "Done." {
				t.Errorf("publication %+v: %v", req, err)
			}
			sent.Add(1)
			_, _ = fmt.Fprint(w, `{"id":"300","channel_id":"20","content":"Done.","author":{"id":"99","bot":true}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	cfg := &conn.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID = "personal", "30", "20"
	cfg.GuildIDs, cfg.UserIDs = nil, nil
	for n, msg := range []string{
		`{"id":"198","channel_id":"21","author":{"id":"30"}}`,
		`{"id":"199","channel_id":"20","guild_id":"10","author":{"id":"30"}}`,
		`{"id":"201","channel_id":"20","author":{"id":"31"}}`,
		`{"id":"202","channel_id":"20","author":{"id":"30","bot":true}}`,
		`{"id":"203","channel_id":"20","author":{"id":"30"},"webhook_id":"31"}`,
		personalMessage,
	} {
		seq := int64(n + 1)
		if err := conn.gateway(gatewayFrame{Type: "MESSAGE_CREATE", Data: json.RawMessage(msg), Sequence: &seq}, "session"); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	pending, err := conn.service.Store.teammatePending(cfg.InstallationID)
	if err != nil || len(pending) != 1 || pending[0].Route.MessageID != "200" {
		t.Fatalf("pending: %+v, %v", pending, err)
	}
	if err = conn.process(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(manager.admitted) != 1 || manager.admitted[0].Authority != AuthorityOwnerDirect || manager.admitted[0].RequireMention {
		t.Fatalf("admissions: %+v", manager.admitted)
	}
	a := manager.admitted[0]
	d := a.Discussion
	if d.Route != (Route{ChannelID: "20"}) || d.Starter != (Route{}) {
		t.Fatalf("discussion route: %+v", d)
	}
	page, err := conn.history(d)(ctx, TeammateHistoryQuery{})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Author.ID != "30" {
		t.Fatalf("history: %+v, %v", page, err)
	}
	if err = conn.service.Store.teammateQueueOutput(d, TeammatePublication{ID: "done", AdmissionID: a.OperationID, Body: "Done."}, conn.service.Now()); err != nil {
		t.Fatal(err)
	}
	group.Store(true)
	if err = conn.sendPending(ctx, d); !errors.Is(err, ErrNotAuthorized) || sent.Load() != 0 {
		t.Fatalf("publication reached group DM: %d sends, %v", sent.Load(), err)
	}
	group.Store(false)
	if err = conn.sendPending(ctx, d); err != nil || sent.Load() != 1 {
		t.Fatalf("owner publication: %d sends, %v", sent.Load(), err)
	}
}

func TestPersonalDMConfigRejectsMixedScope(t *testing.T) {
	cfg := Config{
		Version: 1, Mode: "personal", ManagerCLI: "codex", Name: "Mac", InstallationID: "place",
		ApplicationID: "99", OwnerID: "30", DMChannelID: "20",
		TokenFile: "/private/token", WorkDir: "/checkout", DaemonSocket: "/private/swarm.sock",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.OwnerID = "" },
		func(c *Config) { c.OwnerID = c.ApplicationID },
		func(c *Config) { c.DMChannelID = "" },
		func(c *Config) { c.GuildIDs = []string{"10"} },
		func(c *Config) { c.ChannelIDs = []string{"20"} },
		func(c *Config) { c.UserIDs = []string{"31"} },
		func(c *Config) { c.Mode = "legacy" },
	} {
		bad := cfg
		change(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("accepted invalid DM config %+v", bad)
		}
	}
}
