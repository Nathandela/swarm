package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func webhookMessage(webhookID string) *discordgo.Message {
	return &discordgo.Message{ID: "300", ChannelID: "20", Type: discordgo.MessageTypeDefault, WebhookID: webhookID,
		Author: &discordgo.User{ID: webhookID, Bot: true}}
}

func TestTeammateAdmitsOnlyTrustedWebhooks(t *testing.T) {
	c, _ := teammateTestConnector(t, nil)
	if c.admissibleHuman(webhookMessage("555")) {
		t.Fatal("admitted a webhook without opt-in")
	}
	c.service.Config.TrustedWebhookIDs = []string{"555"}
	if !c.admissibleHuman(webhookMessage("555")) {
		t.Fatal("rejected a trusted webhook")
	}
	if c.admissibleHuman(webhookMessage("556")) {
		t.Fatal("admitted an untrusted webhook")
	}
	bot := &discordgo.Message{ID: "301", ChannelID: "20", Type: discordgo.MessageTypeDefault, Author: &discordgo.User{ID: "555", Bot: true}}
	if c.admissibleHuman(bot) {
		t.Fatal("admitted a bot account sharing a trusted webhook ID")
	}
	appOwned := webhookMessage("555")
	appOwned.Author.ID = "556"
	if c.admissibleHuman(appOwned) {
		t.Fatal("admitted a webhook message authored by someone else")
	}
	crossposted := webhookMessage("555")
	crossposted.Flags = discordgo.MessageFlagsIsCrossPosted
	if c.admissibleHuman(crossposted) {
		t.Fatal("admitted a message followed in from another channel")
	}
	followUp := webhookMessage("555")
	followUp.InteractionMetadata = &discordgo.MessageInteractionMetadata{ID: "400"}
	if c.admissibleHuman(followUp) {
		t.Fatal("admitted an interaction follow-up")
	}
	legacyFollowUp := webhookMessage("555")
	legacyFollowUp.Interaction = &discordgo.MessageInteraction{ID: "401"}
	if c.admissibleHuman(legacyFollowUp) {
		t.Fatal("admitted an interaction follow-up")
	}
	c.service.Config.TrustedWebhookIDs = []string{"99"}
	if c.admissibleHuman(webhookMessage("99")) {
		t.Fatal("admitted the teammate's own application webhook")
	}
}

func TestConfigRejectsInvalidTrustedWebhookID(t *testing.T) {
	c := sampleConfig(t.TempDir())
	c.TrustedWebhookIDs = []string{"not-a-snowflake"}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted invalid trusted webhook ID")
	}
	c.TrustedWebhookIDs = []string{c.ApplicationID}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted the teammate's own application as a trusted webhook")
	}
}

func TestTrustedWebhookMentionOpensDiscussionForPeopleToFollow(t *testing.T) {
	opener := teammateTestMessage("201", "20", "555", "<@99> New ticket", true)
	opener.WebhookID, opener.Author.Bot = "555", true
	follow := teammateTestMessage("202", "201", "88", "Please also check mobile", false)
	bridge, mgr := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/20/messages/201":
			_ = json.NewEncoder(w).Encode(opener)
		case "/channels/201/messages/202":
			_ = json.NewEncoder(w).Encode(follow)
		case "/channels/20/messages/201/threads", "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	bridge.service.Config.TrustedWebhookIDs = []string{"555"}
	teammateTestEvent(t, bridge, 1, "MESSAGE_CREATE", opener)
	teammateTestEvent(t, bridge, 2, "MESSAGE_CREATE", follow)
	pending, err := bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 2 {
		t.Fatalf("%d candidates: %v", len(pending), err)
	}
	for _, item := range pending {
		if err = bridge.process(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	if len(mgr.admitted) != 2 || mgr.admitted[0].Author.ID != "555" || mgr.admitted[0].Discussion.Route.ChannelID != "201" ||
		mgr.admitted[1].Author.ID != "88" || mgr.admitted[1].Discussion.ID != mgr.admitted[0].Discussion.ID {
		t.Fatalf("admissions %+v", mgr.admitted)
	}
}
