// Package logins owns provider-wide native authentication. Native processes own
// active refresh; saved login files contain credentials only.
package logins

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	SchemaVersion  = 1
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
	AuthReady      = "ready"
	AuthNeedsLogin = "needs-login"
	AuthPaused     = "paused"
)

var (
	ErrUnsafePath          = errors.New("unsafe login storage path")
	ErrIneligible          = errors.New("login is not eligible")
	ErrInvalidCredentials  = errors.New("invalid native login credentials")
	ErrIdentityChanged     = errors.New("native login identity changed")
	ErrConflict            = errors.New("login registry revision changed")
	ErrPending             = errors.New("provider login switch pending")
	ErrInUse               = errors.New("login is active or owned by a switch")
	ErrNoEligible          = errors.New("no eligible saved login")
	ErrDurabilityUncertain = errors.New("login storage durability uncertain")
	errUnknownSavedData    = errors.New("login directory contains unrecognized data")
)

type Login struct {
	ID                 string    `json:"id"`
	Provider           string    `json:"provider"`
	Label              string    `json:"label"`
	Identity           string    `json:"identity"`
	CredentialRevision uint64    `json:"credential_revision"`
	AuthState          string    `json:"auth_state"`
	Renewable          bool      `json:"renewable"`
	ExpiresAt          time.Time `json:"expires_at,omitempty"`
	ExhaustedUntil     time.Time `json:"exhausted_until,omitempty"`
	LimitKnown         bool      `json:"limit_known,omitempty"`
	LimitNonRolling    bool      `json:"limit_non_rolling,omitempty"`
	LastLoginJobID     string    `json:"last_login_job_id,omitempty"`
}

type ProviderState struct {
	Provider string `json:"provider"`
	ActiveID string `json:"active_id,omitempty"`
	Epoch    uint64 `json:"epoch"`
}

type Runtime struct {
	Provider string `json:"provider"`
	Epoch    uint64 `json:"epoch"`
	Identity string `json:"identity"`
	Home     string `json:"home"`
}

type Restart struct {
	SourceID         string `json:"source_id"`
	ConversationID   string `json:"conversation_id"`
	ChildID          string `json:"child_id,omitempty"`
	ChildStopClaimed bool   `json:"child_stop_claimed,omitempty"`
	Phase            string `json:"phase"`
	LastError        string `json:"last_error,omitempty"`
}

type Switch struct {
	ID              string    `json:"id"`
	Provider        string    `json:"provider"`
	FromID          string    `json:"from_id,omitempty"`
	ToID            string    `json:"to_id"`
	NextID          string    `json:"next_id,omitempty"`
	LoginJobID      string    `json:"login_job_id,omitempty"`
	FromEpoch       uint64    `json:"from_epoch"`
	ToEpoch         uint64    `json:"to_epoch"`
	Phase           string    `json:"phase"`
	AttemptedIDs    []string  `json:"attempted_ids,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	CancelRequested bool      `json:"cancel_requested,omitempty"`
	Relogin         bool      `json:"relogin,omitempty"`
	Sessions        []Restart `json:"sessions,omitempty"`
}

type Registry struct {
	SchemaVersion    int                      `json:"schema_version"`
	Revision         uint64                   `json:"revision"`
	Logins           []Login                  `json:"logins"`
	Providers        map[string]ProviderState `json:"providers"`
	Switches         map[string]Switch        `json:"switches"`
	MigrationPending bool                     `json:"migration_pending,omitempty"`
}

// Credential is never protocol metadata. Auth and Identity contain only native
// authentication fields; unrelated settings and MCP credentials are excluded.
type Credential struct {
	Auth     json.RawMessage `json:"auth"`
	Identity json.RawMessage `json:"identity,omitempty"`
}
