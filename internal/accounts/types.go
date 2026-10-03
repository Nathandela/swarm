// Package accounts owns private native account profiles and their nonsecret
// registry. It performs no native login, network request or model invocation.
package accounts

import (
	"errors"
	"time"
)

const (
	SchemaVersion          = 1
	ProviderCodex          = "codex"
	ProviderClaude         = "claude"
	KindNative             = "native"
	KindClaudeToken        = "claude-token"
	LifecycleEnabled       = "enabled"
	LifecyclePaused        = "paused"
	LifecycleRetiring      = "retiring"
	AuthValid              = "valid"
	AuthNeedsLogin         = "needs-login"
	VerificationCodex      = "codex-account-id"
	VerificationClaude     = "claude-cached-personal"
	VerificationUnverified = "unverified"
	SourceNativeLogin      = "native-login"
	SourceCachedImport     = "cached-import"
	SourceManualToken      = "manual-token"
)

var (
	ErrRevisionConflict      = errors.New("account registry revision changed")
	ErrDurabilityUncertain   = errors.New("account registry durability uncertain; reconcile before retry")
	ErrUnsafePath            = errors.New("unsafe account storage path or permissions")
	ErrInvalidCredentials    = errors.New("invalid personal subscription credentials")
	ErrIdentityChanged       = errors.New("native account identity changed")
	ErrDuplicate             = errors.New("account is already registered")
	ErrIneligible            = errors.New("account is not eligible")
	ErrInUse                 = errors.New("account credentials are still in use")
	ErrConfigurationConflict = errors.New("native authentication configuration conflicts with account binding")
)

// Binding is a frozen, nonsecret execution selector. ConfigurationGeneration
// names the caller's immutable launch projection; the store never authors it.
type Binding struct {
	SchemaVersion           int    `json:"schema_version"`
	Provider                string `json:"provider"`
	AccountID               string `json:"account_id"`
	CredentialGeneration    uint64 `json:"credential_generation"`
	Identity                string `json:"identity"`
	ConfigurationGeneration uint64 `json:"configuration_generation"`
}

// Candidate's native directory is final from creation onward. ProfilePath is
// worker-local execution data and must never be included in protocol DTOs.
type Candidate struct {
	ID                string    `json:"id"`
	Provider          string    `json:"provider"`
	Kind              string    `json:"kind"`
	Source            string    `json:"source"`
	ProfileGeneration string    `json:"profile_generation"`
	Identity          string    `json:"identity,omitempty"`
	Verification      string    `json:"verification"`
	CreatedAt         time.Time `json:"created_at"`
	ProfilePath       string    `json:"-"`
	TokenFingerprint  string    `json:"token_fingerprint,omitempty"`
}

type Generation struct {
	Number            uint64    `json:"number"`
	ProfileGeneration string    `json:"profile_generation"`
	Kind              string    `json:"kind"`
	Source            string    `json:"source"`
	Identity          string    `json:"identity,omitempty"`
	Verification      string    `json:"verification"`
	TokenFingerprint  string    `json:"token_fingerprint,omitempty"`
	CredentialErased  bool      `json:"credential_erased,omitempty"`
	CredentialErasing bool      `json:"credential_erasing,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type Account struct {
	ID                string                `json:"id"`
	Provider          string                `json:"provider"`
	Label             string                `json:"label"`
	Lifecycle         string                `json:"lifecycle"`
	Auth              string                `json:"auth"`
	CurrentGeneration uint64                `json:"current_generation"`
	Generations       map[uint64]Generation `json:"generations"`
	Quota             QuotaState            `json:"quota"`
}

type Registry struct {
	SchemaVersion int                `json:"schema_version"`
	Revision      uint64             `json:"revision"`
	Enabled       map[string]bool    `json:"enabled"`
	Accounts      map[string]Account `json:"accounts"`
}

// ErasureProof is supplied by the lifecycle authority after it has fenced all
// live writers and references. Ended discussion history is not a live reference.
// CompleteInventory requires native-version characterization of every local
// credential cache; absence of that proof refuses erasure.
type ErasureProof struct {
	WritersStopped    bool
	LiveReferences    int
	CompleteInventory bool
	CredentialFiles   []string // relative to this generation's native directory
}
