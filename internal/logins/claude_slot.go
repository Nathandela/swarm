package logins

import (
	"encoding/json"
	"strings"
)

const claudeSlotSchemaVersion = 11
const claudeNativeSlotMode = "claude-native-slot-v1"

// These records retain refresh ownership, never native readiness or adoption.
// Only the future qualified publisher may establish or change actual authority.
type claudeSlotState struct {
	Actual    claudeSlotAuthority  `json:"actual"`
	Promotion *claudeSlotPromotion `json:"promotion,omitempty"`
}

type claudeSlotAuthority struct {
	LoginID            string `json:"login_id"`
	Identity           string `json:"identity"`
	CredentialRevision uint64 `json:"credential_revision"`
	// GrantHash hashes raw claudeAiOauth value bytes, not the native document or a later encoded publication.
	// It is historical: native renewal can change the grant.
	GrantHash string `json:"grant_hash"`
}

type claudeSlotPromotion struct {
	ID string `json:"id"`
	// Empty only on retained unbound holds, never inferred from today's helper.
	AuthNonce    string              `json:"auth_nonce,omitempty"`
	DesiredEpoch uint64              `json:"desired_epoch"`
	Source       claudeSlotAuthority `json:"source"`
	Target       claudeSlotAuthority `json:"target"`
}

func (slot *claudeSlotState) UnmarshalJSON(raw []byte) error {
	if len(raw) > 4096 {
		return ErrCapacity
	}
	doc, err := claudeAuthObject(raw, "actual", "promotion")
	if err != nil {
		return err
	}
	authority := func(raw []byte) error {
		_, err := claudeAuthObject(raw, "login_id", "identity", "credential_revision", "grant_hash")
		return err
	}
	if err := authority(doc["actual"]); err != nil {
		return err
	}
	if raw, present := doc["promotion"]; present {
		promotion, err := claudeAuthObject(raw, "id", "auth_nonce", "desired_epoch", "source", "target")
		if err != nil {
			return err
		}
		if nonce, present := promotion["auth_nonce"]; present {
			var value string
			if json.Unmarshal(nonce, &value) != nil || !validID(value) {
				return ErrIneligible
			}
		}
		for _, name := range []string{"source", "target"} {
			if err := authority(promotion[name]); err != nil {
				return err
			}
		}
	}
	type state claudeSlotState
	var decoded state
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ErrIneligible
	}
	*slot = claudeSlotState(decoded)
	return nil
}

func claudeSlotOwned(r Registry, provider string) bool {
	return provider == ProviderClaude && r.Providers[provider].AuthOwnerMode == claudeNativeSlotMode
}

func validClaudeSlot(r Registry) bool {
	if r.ClaudeSlot == nil {
		return !claudeSlotOwned(r, ProviderClaude)
	}
	selected := r.Providers[ProviderClaude]
	if r.SchemaVersion < claudeSlotSchemaVersion || !claudeSlotOwned(r, ProviderClaude) || r.MigrationPending || selected.ActiveID == "" || selected.Epoch == 0 {
		return false
	}
	if _, pending := r.Switches[ProviderClaude]; pending {
		return false
	}
	validAuthority := func(a claudeSlotAuthority) bool {
		login := find(&r, a.LoginID)
		return login != nil && login.Provider == ProviderClaude && login.Identity == a.Identity && login.CredentialRevision == a.CredentialRevision && validDigest(a.GrantHash)
	}
	actual := r.ClaudeSlot.Actual
	if !validAuthority(actual) {
		return false
	}
	if promotion := r.ClaudeSlot.Promotion; promotion != nil {
		// A newer desired C cannot release unresolved A→B ownership.
		// Cross-account current-revision custody only; staged same-account relogin needs a separate representation.
		if !validID(promotion.ID) || promotion.AuthNonce != "" && !validID(promotion.AuthNonce) || promotion.DesiredEpoch == 0 || promotion.DesiredEpoch > selected.Epoch || !validAuthority(promotion.Source) || !validAuthority(promotion.Target) || promotion.Source.LoginID == promotion.Target.LoginID || actual.LoginID != promotion.Source.LoginID && actual.LoginID != promotion.Target.LoginID {
			return false
		}
	}
	return true
}

// Reject ownership fields under an old schema, including null and case aliases.
func validateClaudeSlotEncoding(raw []byte, r Registry) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ErrIneligible
	}
	for name := range doc {
		if strings.EqualFold(name, "claude_slot") && (name != "claude_slot" || r.SchemaVersion < claudeSlotSchemaVersion || r.ClaudeSlot == nil) {
			return ErrIneligible
		}
	}
	return nil
}

func claudeSlotHolds(r Registry, id string) bool {
	if r.ClaudeSlot == nil {
		return false
	}
	if r.ClaudeSlot.Actual.LoginID == id {
		return true
	}
	promotion := r.ClaudeSlot.Promotion
	return promotion != nil && (promotion.Source.LoginID == id || promotion.Target.LoginID == id)
}

func claudeParkedAuthAllowed(r Registry, id string) error {
	if claudeSlotHolds(r, id) {
		return ErrInUse
	}
	return nil
}
