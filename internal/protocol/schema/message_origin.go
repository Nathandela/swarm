package schema

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"strconv"
	"unicode"
	"unicode/utf8"
)

// MessageOrigin records human provenance independently of the transport's source.
// External context grants no owner authority; endorsement names the owner who accepted it.
type MessageOrigin struct {
	Kind           string      `json:"kind"`
	InstallationID string      `json:"installation_id"`
	RequestID      string      `json:"request_id"`
	ActorID        string      `json:"actor_id"`
	RequesterID    string      `json:"requester_id,omitempty"`
	ActorName      string      `json:"actor_name,omitempty"`
	GuildID        string      `json:"guild_id,omitempty"`
	ChannelID      string      `json:"channel_id,omitempty"`
	MessageID      string      `json:"message_id,omitempty"`
	EndorserID     string      `json:"endorser_id,omitempty"`
	DiscussionID   string      `json:"discussion_id,omitempty"`
	Revision       string      `json:"revision,omitempty"`
	Voice          VoiceOrigin `json:"voice,omitzero"`
}

// VoiceOrigin pins a finalized call turn independently of a Discord message.
type VoiceOrigin struct {
	CallID           string `json:"call_id"`
	TurnID           string `json:"turn_id"`
	SpeakerID        string `json:"speaker_id"`
	SupervisorSocket string `json:"supervisor_socket"`
	BodyHash         string `json:"body_hash"`
	Generation       uint64 `json:"generation"`
}

func (o MessageOrigin) fields() []string {
	fields := []string{o.Kind, o.InstallationID, o.RequestID, o.ActorID, o.ActorName, o.GuildID, o.ChannelID, o.MessageID, o.EndorserID, o.RequesterID}
	if o.DiscussionID != "" || o.Revision != "" {
		fields = append(fields, o.DiscussionID, o.Revision)
	}
	if v := o.Voice; v != (VoiceOrigin{}) {
		fields = append(fields, "voice", v.CallID, v.TurnID, v.SpeakerID, v.SupervisorSocket, v.BodyHash, strconv.FormatUint(v.Generation, 10))
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
	if v := o.Voice; v != (VoiceOrigin{}) {
		_, hashErr := hex.DecodeString(v.BodyHash)
		personal := o.Kind == "owner_direct" && o.RequesterID == o.ActorID
		if o.DiscussionID == "" || o.Revision != v.BodyHash || o.RequesterID != o.ActorID || o.ChannelID == "" ||
			o.MessageID != "" || o.Kind == "owner_endorsed" || o.GuildID == "" && !personal ||
			v.CallID == "" || v.TurnID == "" || v.SpeakerID != o.ActorID || v.Generation == 0 ||
			!filepath.IsAbs(v.SupervisorSocket) || len(v.BodyHash) != 64 || hashErr != nil {
			return errors.New("message origin requires the complete voice coordinate")
		}
	} else if o.DiscussionID != "" || o.Revision != "" {
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
