package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bwmarrin/discordgo"
)

// DiscordClient is the Discord REST and Gateway transport.
type DiscordClient struct{ session *discordgo.Session }

func NewDiscordClient(token string) (*DiscordClient, error) {
	if token == "" {
		return nil, errors.New("discord: empty bot token")
	}
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, errors.New("discord: initialize client failed")
	}
	s.LogLevel = -1
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	return &DiscordClient{session: s}, nil
}
func (c *DiscordClient) Session() *discordgo.Session { return c.session }
func (c *DiscordClient) Open(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.session.Open(); err != nil {
		return errors.New("discord: gateway connection failed")
	}
	if err := ctx.Err(); err != nil {
		c.session.Close()
		return err
	}
	return nil
}
func (c *DiscordClient) Close() error {
	if err := c.session.Close(); err != nil {
		return errors.New("discord: gateway close failed")
	}
	return nil
}
func components(actions []Action) ([]discordgo.MessageComponent, error) {
	if len(actions) > 5 {
		return nil, errors.New("discord: too many action rows")
	}
	rows := make([]discordgo.MessageComponent, 0, len(actions))
	for _, action := range actions {
		var component discordgo.MessageComponent
		if len(action.Choices) == 0 {
			component = discordgo.Button{Label: action.Label, CustomID: action.ID, Style: discordgo.PrimaryButton}
		} else {
			if len(action.Choices) > 25 {
				return nil, errors.New("discord: too many choices")
			}
			options := make([]discordgo.SelectMenuOption, 0, len(action.Choices))
			for _, choice := range action.Choices {
				options = append(options, discordgo.SelectMenuOption{Label: choice.Label, Value: choice.ID, Description: choice.Description})
			}
			component = discordgo.SelectMenu{CustomID: action.ID, Placeholder: action.Label, Options: options}
		}
		rows = append(rows, discordgo.ActionsRow{Components: []discordgo.MessageComponent{component}})
	}
	return rows, nil
}
func (c *DiscordClient) Post(ctx context.Context, out Outbound) (string, error) {
	rows, err := components(out.Actions)
	if err != nil {
		return "", err
	}
	payload := struct {
		Content         string                            `json:"content"`
		Components      []discordgo.MessageComponent      `json:"components"`
		AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions"`
		Nonce           string                            `json:"nonce"`
		EnforceNonce    bool                              `json:"enforce_nonce"`
	}{out.Text, rows, &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}, out.Nonce, true}
	endpoint := discordgo.EndpointChannelMessages(out.Route.ChannelID)
	raw, err := c.session.RequestWithBucketID(http.MethodPost, endpoint, payload, endpoint, discordgo.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("discord: post failed")
	}
	var msg discordgo.Message
	if json.Unmarshal(raw, &msg) != nil || msg.ID == "" {
		return "", errors.New("discord: invalid post response")
	}
	return msg.ID, nil
}
func (c *DiscordClient) Edit(ctx context.Context, route Route, text string, actions []Action) (string, error) {
	rows, err := components(actions)
	if err != nil {
		return "", err
	}
	edit := discordgo.NewMessageEdit(route.ChannelID, route.MessageID)
	edit.Content = &text
	edit.Components = &rows
	edit.AllowedMentions = &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
	msg, err := c.session.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("discord: edit failed")
	}
	if msg.ID == "" {
		return "", errors.New("discord: invalid edit response")
	}
	return msg.ID, nil
}
func (c *DiscordClient) Message(ctx context.Context, route Route) (Input, error) {
	msg, err := c.session.ChannelMessage(route.ChannelID, route.MessageID, discordgo.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return Input{}, ctx.Err()
		}
		return Input{}, errors.New("discord: fetch message failed")
	}
	if msg.Author == nil || msg.ID != route.MessageID || msg.ChannelID != route.ChannelID || (msg.GuildID != "" && msg.GuildID != route.GuildID) {
		return Input{}, errors.New("discord: invalid message response")
	}
	return Input{EventID: msg.ID, Actor: Actor{ID: msg.Author.ID, Name: msg.Author.Username}, Route: route, Text: msg.Content, Bot: msg.Author.Bot, At: msg.Timestamp}, nil
}
