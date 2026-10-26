package discord

import (
	"context"
	"net/http"
	"net/url"
)

// ScopedMessenger checks current channel placement before any public publication.
type ScopedMessenger struct {
	Client *DiscordClient
	Config Config
}

func (m ScopedMessenger) Resolve(ctx context.Context, route Route) (Route, error) {
	if route.GuildID == "" {
		return route, nil
	}
	if !containsID(m.Config.GuildIDs, route.GuildID) {
		return Route{}, ErrNotAuthorized
	}
	if len(m.Config.ChannelIDs) == 0 || containsID(m.Config.ChannelIDs, route.ChannelID) {
		return route, nil
	}
	var channel struct {
		ID       string `json:"id"`
		GuildID  string `json:"guild_id"`
		ParentID string `json:"parent_id"`
	}
	if err := m.Client.REST(ctx, http.MethodGet, "/channels/"+url.PathEscape(route.ChannelID), nil, &channel); err != nil {
		return Route{}, err
	}
	if channel.ID != route.ChannelID || channel.GuildID != route.GuildID || !containsID(m.Config.ChannelIDs, channel.ParentID) {
		return Route{}, ErrNotAuthorized
	}
	route.ParentID = channel.ParentID
	return route, nil
}
func (m ScopedMessenger) Post(ctx context.Context, out Outbound) (string, error) {
	route, err := m.Resolve(ctx, out.Route)
	if err != nil {
		return "", err
	}
	out.Route = route
	return m.Client.Post(ctx, out)
}
func (m ScopedMessenger) Message(ctx context.Context, route Route) (Input, error) {
	checked, err := m.Resolve(ctx, route)
	if err != nil {
		return Input{}, err
	}
	return m.Client.Message(ctx, checked)
}
func (b *Bridge) resolveRoute(ctx context.Context, route Route) (Route, error) {
	if resolver, ok := b.Service.Messenger.(interface {
		Resolve(context.Context, Route) (Route, error)
	}); ok {
		return resolver.Resolve(ctx, route)
	}
	return route, nil
}
