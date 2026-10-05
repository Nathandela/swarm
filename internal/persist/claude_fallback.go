package persist

// ClaudeFallbackPolicy records Swarm's recovery-only environment override.
// A nil OwnerValue means the owner had no corresponding environment variable.
type ClaudeFallbackPolicy struct {
	OwnerValue     *string `json:"owner_value,omitempty"`
	RecoveryPinned bool    `json:"recovery_pinned,omitempty"`
}
