package schema

import (
	"errors"
	"unicode"
	"unicode/utf8"
)

// MessageOrigin records human provenance independently of the transport's source.
// External context grants no owner authority; endorsement names the owner who accepted it.
type MessageOrigin struct {
	Kind           string `json:"kind"`
	InstallationID string `json:"installation_id"`
	RequestID      string `json:"request_id"`
	ActorID        string `json:"actor_id"`
	RequesterID    string `json:"requester_id,omitempty"`
	ActorName      string `json:"actor_name,omitempty"`
	GuildID        string `json:"guild_id,omitempty"`
	ChannelID      string `json:"channel_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	EndorserID     string `json:"endorser_id,omitempty"`
	DiscussionID   string `json:"discussion_id,omitempty"`
	Revision       string `json:"revision,omitempty"`
}

func (o MessageOrigin) fields() []string {
	fields := []string{o.Kind, o.InstallationID, o.RequestID, o.ActorID, o.ActorName, o.GuildID, o.ChannelID, o.MessageID, o.EndorserID, o.RequesterID}
	if o.DiscussionID != "" || o.Revision != "" {
		fields = append(fields, o.DiscussionID, o.Revision)
	}
	return fields
}

func (o MessageOrigin) Validate() error {
	if o.Kind != "external" && o.Kind != "owner_direct" && o.Kind != "owner_endorsed" {
		return errors.New("message origin has an unknown kind")
	}
	if o.InstallationID == "" || o.RequestID == "" || o.ActorID == "" {
		return errors.New("message origin requires installation, request and actor IDs")
	}
	if (o.Kind == "owner_endorsed") != (o.EndorserID != "") {
		return errors.New("message origin requires an endorser only for owner_endorsed")
	}
	if o.DiscussionID != "" || o.Revision != "" {
		personal := o.Kind == "owner_direct" && o.RequesterID == o.ActorID
		if o.DiscussionID == "" || o.Revision == "" || o.RequesterID == "" || o.MessageID == "" || (o.GuildID == "" && !personal) || o.ChannelID == "" || o.Kind == "owner_endorsed" {
			return errors.New("message origin requires the complete discussion coordinate")
		}
	}
	for _, field := range o.fields() {
		if len(field) > 512 || !utf8.ValidString(field) {
			return errors.New("message origin field exceeds its bound or is invalid UTF-8")
		}
		for _, r := range field {
			if unicode.IsControl(r) {
				return errors.New("message origin contains a control character")
			}
		}
	}
	return nil
}
