package schema

import "time"

// LoginsReq is owner-local. Credentials never cross this protocol.
type LoginsReq struct {
	Action           string `json:"action"`
	ExpectedRevision uint64 `json:"expected_revision,omitempty"`
	Provider         string `json:"provider,omitempty"`
	LoginID          string `json:"login_id,omitempty"`
	Label            string `json:"label,omitempty"`
	JobID            string `json:"job_id,omitempty"`
}

type LoginsReply struct {
	Revision  uint64              `json:"revision"`
	Logins    []LoginView         `json:"logins"`
	Providers []ProviderLoginView `json:"providers"`
	Jobs      []LoginJobView      `json:"jobs,omitempty"`
	Job       *LoginJobView       `json:"job,omitempty"`
}

type LoginView struct {
	ID             string     `json:"id"`
	Provider       string     `json:"provider"`
	Label          string     `json:"label"`
	State          string     `json:"state"`
	Active         bool       `json:"active"`
	ExhaustedUntil *time.Time `json:"exhausted_until,omitempty"`
}

type ProviderLoginView struct {
	Provider string `json:"provider"`
	ActiveID string `json:"active_id,omitempty"`
	State    string `json:"state"`
	Message  string `json:"message,omitempty"`
}

// Sign-in presentation is live owner-local data, never journal content.
type LoginJobView struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	LoginID         string `json:"login_id,omitempty"`
	Label           string `json:"label,omitempty"`
	State           string `json:"state"`
	VerificationURL string `json:"verification_url,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	LoginSocket     string `json:"login_socket,omitempty"`
	Message         string `json:"message,omitempty"`
}
