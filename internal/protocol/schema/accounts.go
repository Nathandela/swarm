package schema

import "time"

// AccountsReq is an owner-local account operation. Token is secret input and
// must never enter journal records, diagnostics or persisted launch metadata.
type AccountsReq struct {
	Action           string `json:"action"`
	ExpectedRevision uint64 `json:"expected_revision,omitempty"`
	Provider         string `json:"provider,omitempty"`
	AccountID        string `json:"account_id,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	JobID            string `json:"job_id,omitempty"`
	Label            string `json:"label,omitempty"`
	Method           string `json:"method,omitempty"`
	SourcePath       string `json:"source_path,omitempty"`
	Token            string `json:"token,omitempty"`
	Enabled          bool   `json:"enabled,omitempty"`
	Paused           bool   `json:"paused,omitempty"`
	Retire           bool   `json:"retire,omitempty"`
}

type AccountsReply struct {
	Revision uint64                         `json:"revision"`
	Accounts []AccountView                  `json:"accounts"`
	Jobs     []AccountEnrollmentView        `json:"jobs"`
	Enabled  map[string]bool                `json:"enabled"`
	Methods  map[string][]AccountMethodView `json:"methods"`
	Job      *AccountEnrollmentView         `json:"job,omitempty"`
	Coverage map[string]AccountCoverageView `json:"coverage,omitempty"`
}

// Coverage counts visible discussions, not historical resume attempts. An
// enabled pool does not imply an older, unbound discussion has been enrolled.
type AccountCoverageView struct {
	Managed          int `json:"managed"`
	Unmanaged        int `json:"unmanaged"`
	RunningUnmanaged int `json:"running_unmanaged"`
}

type AccountMethodView struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type AccountView struct {
	ID                   string             `json:"id"`
	Provider             string             `json:"provider"`
	Label                string             `json:"label"`
	Email                string             `json:"email,omitempty"`
	Plan                 string             `json:"plan,omitempty"`
	CredentialKind       string             `json:"credential_kind"`
	State                string             `json:"state"`
	CredentialGeneration uint64             `json:"credential_generation"`
	Assigned             int                `json:"assigned"`
	Quota                []AccountQuotaView `json:"quota,omitempty"`
	QuotaFetchState      string             `json:"quota_fetch_state,omitempty"`
	QuotaFetchError      string             `json:"quota_fetch_error,omitempty"`
	QuotaNextRefreshAt   *time.Time         `json:"quota_next_refresh_at,omitempty"`
	QuotaLastAttemptAt   *time.Time         `json:"quota_last_attempt_at,omitempty"`
	NextRetryAt          *time.Time         `json:"next_retry_at,omitempty"`
	RefreshSupported     bool               `json:"refresh_supported"`
	RetrySupported       bool               `json:"retry_supported"`
	CredentialsErased    bool               `json:"credentials_erased,omitempty"`
	Retiring             bool               `json:"retiring,omitempty"`
}

type AccountQuotaView struct {
	Label       string     `json:"label"`
	UsedPercent *int       `json:"used_percent,omitempty"`
	ResetAt     *time.Time `json:"reset_at,omitempty"`
	ObservedAt  time.Time  `json:"observed_at"`
}

// Authorization codes/URLs and private login socket are live presentation data;
// they are returned only on owner-local status reads and are never persisted.
type AccountEnrollmentView struct {
	ID              string     `json:"id"`
	Provider        string     `json:"provider"`
	Method          string     `json:"method"`
	State           string     `json:"state"`
	TargetAccountID string     `json:"target_account_id,omitempty"`
	VerificationURL string     `json:"verification_url,omitempty"`
	UserCode        string     `json:"user_code,omitempty"`
	LoginSocket     string     `json:"login_socket,omitempty"`
	Email           string     `json:"email,omitempty"`
	Plan            string     `json:"plan,omitempty"`
	ErrorCode       string     `json:"error_code,omitempty"`
	Message         string     `json:"message,omitempty"`
	Deadline        *time.Time `json:"deadline,omitempty"`
}
