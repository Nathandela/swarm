package logins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
)

// ReserveClaudePromotion reserves a settled helper's target; it never publishes auth.
func (s *Store) ReserveClaudePromotion(expected uint64, ready ClaudeAuthRef) (Registry, error) {
	if !validClaudeAuthRef(ready) {
		return Registry{}, ErrIneligible
	}
	return s.mutate(expected, func(r *Registry) error {
		if !claudeSlotOwned(*r, ProviderClaude) || r.ClaudeSlot == nil || r.ClaudeSlot.Promotion != nil {
			return ErrPending
		}
		selected := r.Providers[ProviderClaude]
		if selected.ActiveID != ready.LoginID || selected.Epoch == 0 {
			return ErrConflict
		}
		if r.ClaudeSlot.Actual.LoginID == ready.LoginID {
			return ErrInUse
		}
		if err := claudeAuthTarget(*r, ready); err != nil {
			return err
		}
		if _, err := s.readRelogin(*find(r, ready.LoginID)); err == nil {
			return ErrPending
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		intent, err := s.readClaudeAuthIntent(ready.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ready {
			return ErrConflict
		}
		if intent.Phase != "ready" {
			return ErrPending
		}
		if !intent.UnitRetired || !validClaudeAuthScope(ready, intent.Scope) || intent.Worker.Alive() {
			return ErrInUse
		}
		if err := s.proveClaudeAuthStopped(intent); err != nil {
			return err
		}
		if err := s.claudeAuthPayloadEmpty(ready.LoginID); err != nil {
			return err
		}
		if err := s.claudeAuthorityMutationAllowed(*r, *find(r, ready.LoginID)); err != nil {
			return err
		}
		// Ready can outlive its attempt deadline and historical selection.
		if err := s.validateClaudeAuthReceipt(*r, ready, selected.Epoch); err != nil {
			return err
		}
		generation, grant, err := s.claudeAuthGeneration(ready.LoginID)
		if err != nil {
			return err
		}
		if generation != intent.Generation {
			return ErrConflict
		}
		hash, err := claudeProviderGrantHash(grant)
		if err != nil {
			return err
		}
		id, err := newID()
		if err != nil {
			return err
		}
		r.ClaudeSlot.Promotion = &claudeSlotPromotion{ID: id, AuthNonce: ready.Nonce, DesiredEpoch: selected.Epoch, Source: r.ClaudeSlot.Actual, Target: claudeSlotAuthority{LoginID: ready.LoginID, Identity: ready.Identity, CredentialRevision: ready.CredentialRevision, GrantHash: hash}}
		return nil
	})
}

func claudeProviderGrantHash(raw []byte) (string, error) {
	if len(raw) > maxCredentialBytes {
		return "", ErrInvalidCredentials
	}
	doc, err := jsonObject(raw)
	if err != nil {
		return "", err
	}
	provider := doc["claudeAiOauth"]
	if _, err := jsonObject(provider); err != nil {
		return "", err
	}
	hash := sha256.Sum256(provider)
	return hex.EncodeToString(hash[:]), nil
}

func claudePromotionPinsAuth(r Registry, ref ClaudeAuthRef) bool {
	if r.ClaudeSlot == nil || r.ClaudeSlot.Promotion == nil {
		return false
	}
	p := r.ClaudeSlot.Promotion
	return p.AuthNonce != "" && p.AuthNonce == ref.Nonce && p.Target.LoginID == ref.LoginID && p.Target.Identity == ref.Identity && p.Target.CredentialRevision == ref.CredentialRevision
}
