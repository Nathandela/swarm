package discord

import (
	"context"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestRoutingChoiceIsAvailableToItsActualRequester(t *testing.T) {
	s, w, _ := serviceRig(t)
	w.rows = append(w.rows, Worker{Target: Target{ID: "local/two", Instance: "two", Name: "Other", Agent: "claude"}, State: "running"})
	c, _ := NewDiscordClient("fake")
	b := NewBridge(s, c)
	in := incoming()
	in.TargetID = ""
	b.pick(context.Background(), in, w.rows)
	r, err := s.Store.ExistingEvent("routing:"+in.EventID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.RequesterID != in.Actor.ID {
		t.Fatal("routing lost requester attribution")
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "choice-click", AppID: "100", Type: discordgo.InteractionMessageComponent, User: &discordgo.User{ID: "2", Username: "Alice"}, Data: discordgo.MessageComponentInteractionData{CustomID: "choose:" + r.Ref, Values: []string{"local/two"}}}}
	if _, err = b.interactionAction(context.Background(), i, Actor{ID: "2", Name: "Alice"}, in.Route); err != nil {
		t.Fatal(err)
	}
	if len(w.sends) != 1 || w.sends[0].Session != "local/two" || w.sends[0].Origin.Kind != "external" {
		t.Fatal("requester couldn't select an agent with original authority")
	}
}
