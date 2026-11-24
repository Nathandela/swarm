package discord

import "context"

func (t *teammateConnector) verifyTeammateRecipient(ctx context.Context, d TeammateDiscussion, q TeammateHistoryQuery) error {
	if d.Route.GuildID == "" || !snowflake(q.AuthorID) || !snowflake(q.MessageID) || q.Cursor != "" || q.Anchor != "" {
		return ErrNotAuthorized
	}
	if err := t.checkDiscussion(ctx, d); err != nil {
		return err
	}
	route := d.Route
	route.MessageID = q.MessageID
	if q.MessageID == d.Starter.MessageID && d.Starter.ChannelID == d.Route.ParentID {
		route = d.Starter
	}
	m, err := t.client.teammateMessage(ctx, route)
	if err != nil {
		return err
	}
	if !teammateHuman(m, t.service.botUserID()) || m.ID != q.MessageID || m.ChannelID != route.ChannelID || m.Author.ID != q.AuthorID {
		return ErrNotAuthorized
	}
	return nil
}
