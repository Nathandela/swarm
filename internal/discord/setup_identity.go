package discord

import (
	"context"
	"errors"
	"net/http"
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
