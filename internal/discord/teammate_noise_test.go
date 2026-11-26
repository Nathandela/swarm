package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func gapNotices(t *testing.T, c *teammateConnector, discussion string) int {
	t.Helper()
	out, err := c.service.Store.teammateOutputs("install", discussion)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, o := range out {
		if strings.HasPrefix(o.Publication.ID, "gap_") {
			n++
		}
	}
	return n
}

// A restart whose repair accounts for every human message must not ask the thread to restate.
func TestTeammateGapNoticeOnlyWhenRepairFindsAMissedMessage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		missed bool
	}{{"nothing missed", false}, {"human message missed", true}} {
		t.Run(tc.name, func(t *testing.T) {
			live := teammateTestMessage("261", "201", "77", "Deploy it in a playground please", false)
			bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/channels/20":
					_, _ = fmt.Fprint(w, teammateParent)
				case "/channels/201":
					_, _ = fmt.Fprint(w, teammatePublic)
				case "/channels/20/permissions/member/99":
					teammateTestPermissions(w)
				case "/channels/201/messages/261":
					_ = json.NewEncoder(w).Encode(live)
				case "/channels/201/messages":
					own := teammateTestMessage("250", "201", "99", "On it", false)
					own.Author.Bot = true
					rows := []*discordgo.Message{live, own, teammateTestMessage("210", "201", "88", "before the gap", false)}
					if tc.missed {
						rows = append([]*discordgo.Message{teammateTestMessage("240", "201", "88", "<@99> missed request", true)}, rows...)
					}
					_ = json.NewEncoder(w).Encode(rows)
				default:
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
			// The live message arrives through the Gateway after the reconnect.
			teammateTestEvent(t, bridge, 3, "MESSAGE_CREATE", live)
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
			want := 0
			if tc.missed {
				want = 1
			}
			if got := gapNotices(t, bridge, "d1"); got != want {
				t.Fatalf("gap notices after repair: %d, want %d", got, want)
			}
			// Processing the live message never posts the restate notice in reply to it.
			pending, err := bridge.service.Store.teammatePending("install")
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending %+v %v", pending, err)
			}
			_ = bridge.process(ctx, pending[0])
			if got := gapNotices(t, bridge, "d1"); got != want {
				t.Fatalf("gap notices after a received message: %d, want %d", got, want)
			}
		})
	}
}
