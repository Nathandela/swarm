package logins

import (
	"errors"
	"maps"
	"os"
)

// SelectClaudeProfile records the desired account for future owned births. It
// does not start authentication, stop discussions, or assert authentication.
func (s *Store) SelectClaudeProfile(expected uint64, loginID string) (Registry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Registry
	err := s.locked(func() error {
		var err error
		r, err = s.load()
		if err != nil {
			return err
		}
		if r.Revision != expected {
			return ErrConflict
		}
		if r.MigrationPending {
			return ErrPending
		}
		if _, pending := r.Switches[ProviderClaude]; pending {
			return ErrPending
		}
		l := find(&r, loginID)
		if l == nil || l.Provider != ProviderClaude || l.AuthState != AuthReady {
			return ErrIneligible
		}
		if _, err := s.readRelogin(*l); err == nil {
			return ErrPending
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		selected := r.Providers[ProviderClaude]
		if r.Revision == ^uint64(0) || selected.Epoch == ^uint64(0) {
			return ErrIneligible
		}
		selected.Provider, selected.ActiveID, selected.AuthOwnerMode = ProviderClaude, loginID, ClaudeNativeProfileMode
		selected.Epoch++
		selected.DesiredCredentialRevision = 0
		prospective := r
		prospective.Providers = maps.Clone(r.Providers)
		if prospective.Providers == nil {
			prospective.Providers = map[string]ProviderState{}
		}
		prospective.Providers[ProviderClaude] = selected
		if intent, err := s.readClaudeAuthIntent(loginID); err == nil {
			if intent.Phase == "ready" {
				if err := s.validateClaudeAuthReceipt(prospective, intent.Ref, selected.Epoch); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if prospective.SchemaVersion < ClaudeProfileCustodySchemaVersion {
			prospective.SchemaVersion = ClaudeProfileCustodySchemaVersion
		}
		prospective.Revision++
		if err := s.save(prospective); err != nil {
			return err
		}
		r = prospective
		return nil
	})
	return r, err
}
