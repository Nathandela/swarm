package discord

import (
	"context"
	"errors"
	"net/http"

	"github.com/bwmarrin/discordgo"
)

// DiscoverTeammateIdentity resolves missing setup fields from the bot's own account.
func DiscoverTeammateIdentity(ctx context.Context, cfg *Config) error {
	token, err := LoadToken(cfg.TokenFile)
	if err != nil {
		return err
	}
	client, err := NewDiscordClient(token)
	if err != nil {
		return err
	}
	return discoverTeammateIdentity(ctx, client, cfg)
}

func discoverTeammateIdentity(ctx context.Context, client *DiscordClient, cfg *Config) error {
	var me struct {
		ID, Username string
		Bot          bool
	}
	if err := client.REST(ctx, http.MethodGet, "/users/@me", nil, &me); err != nil {
		return err
	}
	if !me.Bot || me.ID == "" || (cfg.ApplicationID != "" && cfg.ApplicationID != me.ID) {
		return errors.New("discord: token does not match the configured bot identity")
	}
	cfg.ApplicationID = me.ID
	if cfg.Name == "" {
		cfg.Name = me.Username
	}
	if cfg.Mode == "personal" {
		if !snowflake(cfg.OwnerID) || cfg.OwnerID == me.ID {
			return errors.New("discord: personal mode requires a valid human --owner-id")
		}
		var dm discordgo.Channel
		if err := client.REST(ctx, http.MethodPost, "/users/@me/channels", map[string]string{"recipient_id": cfg.OwnerID}, &dm); err != nil {
			return err
		}
		if !personalChannel(dm, cfg.OwnerID) || cfg.DMChannelID != "" && cfg.DMChannelID != dm.ID {
			return ErrNotAuthorized
		}
		cfg.DMChannelID = dm.ID
		return nil
	}
	if len(cfg.GuildIDs) == 0 {
		var guilds []struct{ ID string }
		if err := client.REST(ctx, http.MethodGet, "/users/@me/guilds", nil, &guilds); err != nil {
			return err
		}
		if len(guilds) != 1 {
			return errors.New("discord: select an installed server using --guild")
		}
		cfg.GuildIDs = []string{guilds[0].ID}
	}
	return nil
}

func personalChannel(c discordgo.Channel, owner string) bool {
	return snowflake(c.ID) && snowflake(owner) && c.Type == discordgo.ChannelTypeDM && c.GuildID == "" && c.ParentID == "" &&
		len(c.Recipients) == 1 && c.Recipients[0] != nil && c.Recipients[0].ID == owner && !c.Recipients[0].Bot && !c.Recipients[0].System
}
