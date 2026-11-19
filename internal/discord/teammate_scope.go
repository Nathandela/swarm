package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/bwmarrin/discordgo"
)

var errTeammateUnsupported = errors.New("discord: the teammate can join server text threads and forum posts, or start a public thread from a server text message")

func teammateAPIStatus(err error, status int) bool {
	var apiErr discordAPIError
	return errors.As(err, &apiErr) && int(apiErr) == status
}

func (c *DiscordClient) teammateChannel(ctx context.Context, guild, id string) (discordgo.Channel, error) {
	var channel discordgo.Channel
	if !snowflake(guild) || !snowflake(id) {
		return channel, ErrInvalid
	}
	err := c.REST(ctx, http.MethodGet, "/channels/"+id, nil, &channel)
	if err == nil && (channel.ID != id || channel.GuildID != guild) {
		err = ErrNotAuthorized
	}
	return channel, err
}

func teammateChannelScope(ctx context.Context, client *DiscordClient, cfg Config, route Route, botID string) (discordgo.Channel, discordgo.Channel, int64, error) {
	var channel, parent discordgo.Channel
	if !containsID(cfg.GuildIDs, route.GuildID) || !snowflake(botID) {
		return channel, parent, 0, ErrNotAuthorized
	}
	channel, err := client.teammateChannel(ctx, route.GuildID, route.ChannelID)
	if err != nil {
		return channel, parent, 0, err
	}
	parent = channel
	if channel.IsThread() {
		if channel.ThreadMetadata == nil || !snowflake(channel.ParentID) || channel.ParentID == channel.ID ||
			route.ParentID != "" && channel.ParentID != route.ParentID {
			return channel, parent, 0, ErrNotAuthorized
		}
		parent, err = client.teammateChannel(ctx, route.GuildID, channel.ParentID)
		if err != nil {
			return channel, parent, 0, err
		}
		if parent.IsThread() {
			return channel, parent, 0, ErrNotAuthorized
		}
	}
	if route.ParentID != "" && !channel.IsThread() || len(cfg.ChannelIDs) > 0 && !containsID(cfg.ChannelIDs, channel.ID) && !containsID(cfg.ChannelIDs, parent.ID) {
		return channel, parent, 0, ErrNotAuthorized
	}
	var response struct{ Permissions string }
	if err = client.REST(ctx, http.MethodGet, "/channels/"+parent.ID+"/permissions/member/"+botID, nil, &response); err != nil {
		return channel, parent, 0, err
	}
	bits, err := strconv.ParseInt(response.Permissions, 10, 64)
	if err != nil || bits < 0 {
		return channel, parent, 0, ErrInvalid
	}
	if bits&discordgo.PermissionAdministrator != 0 {
		bits = int64(^uint64(0) >> 1)
	}
	if bits&discordgo.PermissionViewChannel == 0 {
		return channel, parent, 0, ErrNotAuthorized
	}
	if channel.Type == discordgo.ChannelTypeGuildPrivateThread && bits&discordgo.PermissionManageThreads == 0 {
		var member discordgo.ThreadMember
		err = client.REST(ctx, http.MethodGet, "/channels/"+channel.ID+"/thread-members/"+botID, nil, &member)
		if err != nil {
			return channel, parent, 0, err
		}
		if member.ID != channel.ID || member.UserID != botID {
			return channel, parent, 0, ErrNotAuthorized
		}
	}
	return channel, parent, bits, nil
}

// TeammatePermissions rechecks actual channel placement, current bot visibility and private thread membership.
func TeammatePermissions(ctx context.Context, client *DiscordClient, cfg Config, route Route, botID string) (int64, error) {
	_, _, bits, err := teammateChannelScope(ctx, client, cfg, route, botID)
	return bits, err
}

func (c *DiscordClient) teammateMessage(ctx context.Context, route Route) (*discordgo.Message, error) {
	if !snowflake(route.ChannelID) || !snowflake(route.MessageID) || !snowflake(route.GuildID) {
		return nil, ErrInvalid
	}
	var m discordgo.Message
	if err := c.REST(ctx, http.MethodGet, "/channels/"+route.ChannelID+"/messages/"+route.MessageID, nil, &m); err != nil {
		return nil, err
	}
	if m.ID != route.MessageID || m.ChannelID != route.ChannelID || m.GuildID != "" && m.GuildID != route.GuildID || m.Author == nil || !snowflake(m.Author.ID) {
		return nil, ErrConflict
	}
	m.GuildID = route.GuildID
	return &m, nil
}

func (c *DiscordClient) teammateMessages(ctx context.Context, route Route, before string, limit int) ([]*discordgo.Message, error) {
	if !snowflake(route.ChannelID) || !snowflake(route.GuildID) || before != "" && !snowflake(before) || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if before != "" {
		query.Set("before", before)
	}
	var messages []*discordgo.Message
	err := c.rest(ctx, http.MethodGet, "/channels/"+route.ChannelID+"/messages", query, nil, &messages)
	if err != nil {
		return nil, err
	}
	if len(messages) > limit {
		return nil, ErrConflict
	}
	for _, m := range messages {
		if m == nil || !snowflake(m.ID) || m.ChannelID != route.ChannelID || m.GuildID != "" && m.GuildID != route.GuildID ||
			before != "" && !snowflakeLess(m.ID, before) {
			return nil, ErrConflict
		}
		m.GuildID = route.GuildID
	}
	return messages, nil
}

func (c *DiscordClient) teammateThread(ctx context.Context, opener Route, name string, canCreate bool) (Route, error) {
	route := Route{GuildID: opener.GuildID, ChannelID: opener.MessageID, ParentID: opener.ChannelID}
	current, err := c.teammateChannel(ctx, route.GuildID, route.ChannelID)
	if teammateAPIStatus(err, http.StatusNotFound) {
		if !canCreate {
			return Route{}, errTeammateUnsupported
		}
		var created discordgo.Channel
		postErr := c.REST(ctx, http.MethodPost, fmt.Sprintf("/channels/%s/messages/%s/threads", opener.ChannelID, opener.MessageID), map[string]string{"name": name}, &created)
		if postErr == nil && (created.ID != route.ChannelID || created.ParentID != route.ParentID || created.GuildID != route.GuildID) {
			return Route{}, ErrConflict
		}
		current, err = c.teammateChannel(ctx, route.GuildID, route.ChannelID)
		if err != nil && postErr != nil {
			return Route{}, postErr
		}
	}
	if err != nil {
		return Route{}, err
	}
	if current.Type != discordgo.ChannelTypeGuildPublicThread || current.ParentID != route.ParentID {
		return Route{}, ErrNotAuthorized
	}
	return route, nil
}

func (t *teammateConnector) checkDiscussion(ctx context.Context, d TeammateDiscussion) error {
	c, _, bits, err := teammateChannelScope(ctx, t.client, t.service.Config, d.Route, t.service.botUserID())
	if err != nil {
		return err
	}
	required := int64(discordgo.PermissionReadMessageHistory | discordgo.PermissionSendMessagesInThreads)
	if !c.IsThread() || c.ThreadMetadata.Locked || bits&required != required {
		return ErrNotAuthorized
	}
	return nil
}
