// Package discord owns the local Discord request and publication ledger.
package discord

import "time"

type Actor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Route struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
}
type Target struct {
	ID       string `json:"id"`
	Instance string `json:"instance"`
	Name     string `json:"name"`
	Agent    string `json:"agent"`
}
type Authority string

const (
	AuthorityExternal      Authority = "external"
	AuthorityOwnerDirect   Authority = "owner_direct"
	AuthorityOwnerEndorsed Authority = "owner_endorsed"
)

type ContextMessage struct {
	ID        string    `json:"id"`
	Author    Actor     `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}
type Request struct {
	Ref             string           `json:"ref"`
	EventID         string           `json:"event_id"`
	MessageRevision string           `json:"message_revision"`
	EventAt         time.Time        `json:"event_at"`
	Author          Actor            `json:"author"`
	EndorserID      string           `json:"endorser_id,omitempty"`
	Authority       Authority        `json:"authority"`
	Route           Route            `json:"route"`
	Target          Target           `json:"target"`
	Text            string           `json:"text"`
	Snapshot        bool             `json:"snapshot,omitempty"`
	Context         []ContextMessage `json:"context,omitempty"`
	OperationID     string           `json:"operation_id"`
	CreatedAt       time.Time        `json:"created_at"`
	ExpiresAt       time.Time        `json:"expires_at"`
	DeliveryState   string           `json:"delivery_state"`
	NativeEvidence  string           `json:"native_evidence,omitempty"`
}
type ContextPage struct {
	Ref        string           `json:"ref"`
	Messages   []ContextMessage `json:"messages"`
	NextOffset int              `json:"next_offset"`
}
type Chunk struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}
type Publication struct {
	RequestRef        string    `json:"request_ref"`
	ID                string    `json:"id"`
	Body              string    `json:"body"`
	BodyHash          string    `json:"body_hash"`
	Version           int       `json:"version"`
	Chunks            []Chunk   `json:"chunks"`
	State             string    `json:"state"`
	DiscordMessageIDs []string  `json:"discord_message_ids,omitempty"`
	DiscordEvidence   string    `json:"discord_evidence,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

const (
	StatePrepared       = "prepared"
	StateSending        = "sending"
	StateSent           = "sent"
	StateUncertain      = "uncertain"
	StateFailed         = "failed"
	MaxRequests         = 2048
	MaxPublications     = 2048
	MaxStoreBytes       = 32 << 20
	MaxRequestBytes     = 256 << 10
	MaxPublicationBytes = 64 << 10
	MaxContextPageSize  = 32
	MaxContextPageBytes = 32 << 10
	EventMaxAge         = 15 * time.Minute
	EventFutureSkew     = 2 * time.Minute
	RequestTTL          = 24 * time.Hour
	GuardTTL            = 48 * time.Hour
)
