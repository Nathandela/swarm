package discord

import (
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
