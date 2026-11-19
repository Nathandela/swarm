package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"net/http"
	"testing"
)

func TestParentHistoryIncludesInterveningHumanContext(t *testing.T) {
	root := teammateTestMessage("100", "20", "88", "first", false)
	middle := teammateTestMessage("150", "20", "77", "violet needs work", false)
	opener := teammateTestMessage("201", "20", "88", "thoughts?", true)
	opener.MessageReference = &discordgo.MessageReference{MessageID: "100", ChannelID: "20"}
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/20/messages/100":
			_ = json.NewEncoder(w).Encode(root)
		case "/channels/20/messages/201":
			_ = json.NewEncoder(w).Encode(opener)
		case "/channels/20/messages":
			if r.URL.Query().Get("before") == "201" {
				_ = json.NewEncoder(w).Encode([]*discordgo.Message{middle, root})
			} else {
				_, _ = fmt.Fprint(w, "[]")
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	d := TeammateDiscussion{InstallationID: "install", ID: "discussion", ParentAnchor: "201", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}, Starter: Route{GuildID: "10", ChannelID: "20", MessageID: "201"}}
	if err := bridge.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d}); err != nil {
		t.Fatal(err)
	}
	page, err := bridge.history(d)(context.Background(), TeammateHistoryQuery{Source: "parent"})
	if err != nil || len(page.Messages) != 3 || page.Messages[1].ID != "150" || len(page.ReplyChain) != 2 {
		t.Fatalf("%+v, %v", page, err)
	}
}
