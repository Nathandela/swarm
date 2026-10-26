package discord

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func (b *Bridge) autocomplete(ctx context.Context, i *discordgo.InteractionCreate, actor Actor, route Route) error {
	if !b.Service.Allowed(actor, route) {
		return ErrNotAuthorized
	}
	if i == nil || i.Interaction == nil || i.Type != discordgo.InteractionApplicationCommandAutocomplete {
		return ErrInvalid
	}
	data, ok := i.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok || data.Name != "swarm" {
		return ErrInvalid
	}
	var focused func([]*discordgo.ApplicationCommandInteractionDataOption) (string, bool)
	focused = func(options []*discordgo.ApplicationCommandInteractionDataOption) (string, bool) {
		for _, option := range options {
			if option == nil {
				continue
			}
			if option.Focused && option.Name == "agent" {
				value, ok := option.Value.(string)
				return value, ok
			}
			if value, ok := focused(option.Options); ok {
				return value, true
			}
		}
		return "", false
	}
	query, ok := focused(data.Options)
	if !ok {
		return ErrInvalid
	}
	rows, err := b.Service.Roster(ctx)
	if err != nil {
		return err
	}
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, 25)
	for _, row := range rows {
		if !foldContains(row.Name, query) && !foldContains(row.Agent, query) && !foldContains(row.Project, query) && !foldContains(row.ID, query) {
			continue
		}
		suffix := clipUTF16(" · "+row.ID, 99)
		label := clipUTF16(row.Name, 100-lengthUTF16(suffix)) + suffix
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: label, Value: row.ID})
		if len(choices) == 25 {
			break
		}
	}
	payload := struct {
		Type discordgo.InteractionResponseType `json:"type"`
		Data struct {
			Choices []*discordgo.ApplicationCommandOptionChoice `json:"choices"`
		} `json:"data"`
	}{Type: discordgo.InteractionApplicationCommandAutocompleteResult}
	payload.Data.Choices = choices
	return b.Client.REST(ctx, http.MethodPost, "/interactions/"+url.PathEscape(i.ID)+"/"+url.PathEscape(i.Token)+"/callback", payload, nil)
}

func foldContains(s, query string) bool {
	text, needle := []rune(s), []rune(query)
	for n := 0; n+len(needle) <= len(text); n++ {
		if strings.EqualFold(string(text[n:n+len(needle)]), query) {
			return true
		}
	}
	return false
}
