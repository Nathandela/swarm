package logins

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
)

// IsClaudeProfileRuntime classifies only an exact deterministic address/shape.
// It is not ownership, authentication, admission, or writer-stop evidence.
func IsClaudeProfileRuntime(stateRoot string, rt Runtime) bool {
	return rt.Provider == ProviderClaude && !rt.ExternalAuth && rt.Epoch != 0 && validDigest(rt.Identity) && filepath.IsAbs(stateRoot) && filepath.Clean(stateRoot) == stateRoot && rt.Home == filepath.Join(stateRoot, "logins", "provider", "claude", "profiles", rt.Identity)
}

// ClaimClaudeProfileCustody fences an exclusive zero-PID cleanup claim only.
// The callback must validate the exact live reservation, recorded CLI and shim,
// durably claim once, and neither start a child nor reenter Store.
func (s *Store) ClaimClaudeProfileCustody(rt Runtime, cwd string, claim func() error) error {
	if claim == nil || !IsClaudeProfileRuntime(s.StateRoot(), rt) || !validClaudeCWDs([]string{cwd}) {
		return ErrIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		if r.SchemaVersion < ClaudeProfileCustodySchemaVersion {
			return ErrPending
		}
		if rt.Epoch > r.Providers[ProviderClaude].Epoch {
			return ErrConflict
		}
		if _, known := r.ClaudeProfiles[rt.Identity]; !known {
			return ErrPending
		}
		for _, login := range r.Logins {
			if login.Provider == ProviderClaude && login.Identity == rt.Identity {
				if _, err := s.readSavedRecord(login); err != nil {
					return err
				}
				if err := s.settleClaudeRegistry(); err != nil {
					return err
				}
				return claim()
			}
		}
		return ErrConflict
	})
}

// AdmitClaudeProfileBirth holds the interprocess registry fence through the
// caller's one exact reservation check, native start and durable publication.
// The callback must not reacquire Store locks or perform slow preparation.
func (s *Store) AdmitClaudeProfileBirth(rt Runtime, cwd string, startAndPublish func() error) error {
	if startAndPublish == nil || !IsClaudeProfileRuntime(s.StateRoot(), rt) || !validClaudeCWDs([]string{cwd}) {
		return ErrIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		if r.MigrationPending || r.SchemaVersion < ClaudeProfileCustodySchemaVersion {
			return ErrPending
		}
		if _, pending := r.Switches[ProviderClaude]; pending {
			return ErrPending
		}
		selected := r.Providers[ProviderClaude]
		if !claudeProfilesOwned(r, ProviderClaude) || selected.Epoch != rt.Epoch {
			return ErrConflict
		}
		l := find(&r, selected.ActiveID)
		if l == nil || l.Provider != ProviderClaude || l.Identity != rt.Identity || l.AuthState != AuthReady {
			return ErrConflict
		}
		if _, err := s.readRelogin(*l); err == nil {
			return ErrPending
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		profile, known := r.ClaudeProfiles[l.Identity]
		if !known || !profile.Provisioned || !slices.Contains(profile.CWDs, cwd) {
			return ErrPending
		}
		if _, err := s.claudeProfileReady(r, l.Identity); err != nil {
			return err
		}
		intent, err := s.readClaudeAuthIntent(l.ID)
		if err != nil {
			return err
		}
		if err := s.validateClaudeAuthReceipt(r, intent.Ref, rt.Epoch); err != nil {
			return err
		}
		if err := s.settleClaudeRegistry(); err != nil {
			return err
		}
		return startAndPublish()
	})
}

func (s *Store) claudeProfileReady(r Registry, identity string) (string, error) {
	profile, known := r.ClaudeProfiles[identity]
	if !known || !profile.Provisioned {
		return "", ErrPending
	}
	base := filepath.Join("provider", "claude", "profiles", identity)
	if err := s.verifyClaudeAliases(base); err != nil {
		return "", err
	}
	if err := s.validateClaudeAuthSettings(); err != nil {
		return "", err
	}
	// Shared settings.json is checked at its verified source; these two files
	// are private native profile inputs and may independently select authority.
	for _, name := range []string{".claude.json", "settings.local.json"} {
		raw, err := s.readOptional(filepath.Join(base, name))
		if err != nil {
			return "", err
		}
		if len(raw) > 0 {
			if err := ValidateAuthSettings(ProviderClaude, raw); err != nil {
				return "", err
			}
		}
	}
	return s.ClaudeProfileHome(identity)
}
