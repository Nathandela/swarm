package discord

import (
	"context"
	"github.com/bwmarrin/discordgo"
)

// Direct fixtures capture the same arrival epoch as the production Gateway callbacks.
func (b *Bridge) Message(ctx context.Context, msg *discordgo.MessageCreate) {
	if msg != nil && msg.Message != nil {
		b.message(ctx, msg, b.Service.contextEpoch(msg.ChannelID))
	}
}
func (b *Bridge) MessageUpdate(ctx context.Context, msg *discordgo.MessageUpdate) {
	if msg != nil && msg.Message != nil {
		b.messageUpdate(ctx, msg, b.Service.invalidateContext(msg.ChannelID))
	}
}
