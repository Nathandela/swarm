package logins

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type reloginIntent struct {
	Login      Login      `json:"login"`
	Credential Credential `json:"credential"`
}

func reloginPath(id string) string { return filepath.Join("saved", id, "relogin.json") }
func (s *Store) readRelogin(l Login) (reloginIntent, error) {
	raw, e := readPrivate(s.root, reloginPath(l.ID), maxCredentialBytes*2)
	if e != nil {
		return reloginIntent{}, e
	}
	var intent reloginIntent
	if validateJSON(raw) != nil || json.Unmarshal(raw, &intent) != nil {
		return intent, ErrInvalidCredentials
	}
	target := intent.Login
	if target.ID != l.ID || target.Provider != l.Provider || target.Identity != l.Identity || target.Label != l.Label || (target.LastLoginJobID != "" && !validID(target.LastLoginJobID)) || target.AuthState != AuthReady || target.LimitKnown || target.LimitNonRolling || !target.ExhaustedUntil.IsZero() || target.CredentialRevision == 0 || (target.CredentialRevision != l.CredentialRevision && target.CredentialRevision != l.CredentialRevision+1) {
		return intent, ErrConflict
	}
	c, id, renewable, expires, e := normalizeCredential(l.Provider, intent.Credential)
	if e != nil {
		return intent, e
	}
	if id != l.Identity {
		return intent, ErrIdentityChanged
	}
	if renewable != target.Renewable || !expires.Equal(target.ExpiresAt) {
		return intent, ErrConflict
	}
	intent.Credential = c
	return intent, nil
}

func (s *Store) eraseRelogin(id string) error {
	if e := s.root.Remove(reloginPath(id)); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e := syncRootDir(s.root, filepath.Join("saved", id)); e != nil {
		return ErrDurabilityUncertain
	}
	return nil
}

func (s *Store) Relogin(expected uint64, id string, c Credential) (Registry, Login, error) {
	return s.reloginJob(expected, id, c, "")
}
func (s *Store) ReloginJob(expected uint64, id string, c Credential, jobID string) (Registry, Login, error) {
	if !validID(jobID) {
		return Registry{}, Login{}, ErrIneligible
	}
	return s.reloginJob(expected, id, c, jobID)
}
func (s *Store) reloginJob(expected uint64, id string, c Credential, jobID string) (Registry, Login, error) {
	observed, err := s.Snapshot()
	if err != nil {
		return Registry{}, Login{}, err
	}
	if login := find(&observed, id); login != nil && claudeProfilesOwned(observed, login.Provider) {
		return s.reloginClaudeProfile(expected, id, c, jobID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Registry
	var out Login
	e := s.locked(func() error {
		var e error
		r, e = s.load()
		if e != nil {
			return e
		}
		l := find(&r, id)
		if l == nil {
			return ErrIneligible
		}
		if claudeProfilesOwned(r, l.Provider) {
			return ErrPending
		}
		if codexOwned(r, l.Provider) {
			return ErrPending
		}
		if l.Provider == ProviderClaude {
			if err := s.claudeAuthMutationAllowed(l.ID); err != nil {
				return err
			}
		}
		if jobID != "" && l.LastLoginJobID == jobID {
			out = *l
			return nil
		}
		if r.Revision != expected {
			return ErrConflict
		}
		if r.Revision == ^uint64(0) {
			return ErrIneligible
		}
		if owned(r, id) {
			return ErrInUse
		}
		if _, e = s.readRelogin(*l); e == nil {
			return ErrPending
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		c, identity, renewable, expires, e := normalizeCredential(l.Provider, c)
		if e != nil {
			return e
		}
		if identity != l.Identity {
			return ErrIdentityChanged
		}
		if l.CredentialRevision == ^uint64(0) {
			return ErrIneligible
		}
		out = *l
		if jobID != "" {
			out.LastLoginJobID = jobID
		}
		out.CredentialRevision++
		out.AuthState = AuthReady
		out.Renewable = renewable
		out.ExpiresAt = expires
		out.LimitKnown = false
		out.LimitNonRolling = false
		out.ExhaustedUntil = time.Time{}
		raw, e := json.Marshal(reloginIntent{Login: out, Credential: c})
		if e != nil {
			return e
		}
		// This intent precedes both native Keychain mutation and metadata rename.
		if _, e = durableReplace(s.root, reloginPath(id), raw, s.writeOps); e != nil {
			return e
		}
		if e = s.writeSaved(out, c); e != nil {
			return e
		}
		*l = out
		r.Revision++
		if e = s.save(r); e != nil {
			return e
		}
		return s.eraseRelogin(id)
	})
	return r, out, e
}

func (s *Store) reconcileRelogins(r *Registry) ([]string, error) {
	var completed []string
	for i := range r.Logins {
		l := &r.Logins[i]
		if codexOwned(*r, l.Provider) {
			continue
		}
		intent, e := s.readRelogin(*l)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}

		if l.Provider == ProviderClaude {
			if claudeProfilesOwned(*r, l.Provider) {
				if r.SchemaVersion < ClaudeProfileCustodySchemaVersion {
					continue
				}
				_, err := s.applyClaudeProfileRelogin(r, l, intent)
				if errors.Is(err, ErrPending) || errors.Is(err, ErrInUse) {
					continue
				}
				if err != nil {
					return nil, err
				}
				completed = append(completed, l.ID)
				continue
			}
			if err := s.claudeAuthorityMutationAllowed(*r, *l); errors.Is(err, ErrPending) || errors.Is(err, ErrInUse) {
				continue
			} else if err != nil {
				return nil, err
			}
			if err := s.claudeAuthMutationAllowed(l.ID); err != nil {
				return nil, err
			}
		}

		if owned(*r, l.ID) {
			return nil, ErrInUse
		}
		if intent.Login.LastLoginJobID != "" && l.LastLoginJobID == intent.Login.LastLoginJobID && l.CredentialRevision == intent.Login.CredentialRevision {
			completed = append(completed, l.ID)
			continue
		}
		if e = s.writeSaved(intent.Login, intent.Credential); e != nil {
			return nil, e
		}
		*l = intent.Login
		completed = append(completed, l.ID)
	}
	return completed, nil
}

// reloginClaudeProfile retains the existing intent before resolving either
// native custody or the helper's spent budget; no provider-wide restart exists.
func (s *Store) reloginClaudeProfile(expected uint64, id string, c Credential, jobID string) (Registry, Login, error) {
	s.mu.Lock()
	var r Registry
	var out Login
	var helper ClaudeAuthRef
	complete := false
	err := s.locked(func() error {
		var err error
		r, err = s.load()
		if err != nil {
			return err
		}
		l := find(&r, id)
		if l == nil || l.Provider != ProviderClaude || !claudeProfilesOwned(r, ProviderClaude) || r.SchemaVersion < ClaudeProfileCustodySchemaVersion {
			return ErrPending
		}
		if r.MigrationPending {
			return ErrPending
		}
		if _, pending := r.Switches[ProviderClaude]; pending {
			return ErrPending
		}
		intent, intentErr := s.readRelogin(*l)
		if errors.Is(intentErr, os.ErrNotExist) && jobID != "" && l.LastLoginJobID == jobID {
			out = *l
			complete = true
			return nil
		}
		if r.Revision != expected {
			return ErrConflict
		}
		if r.Revision == ^uint64(0) || l.CredentialRevision == ^uint64(0) {
			return ErrIneligible
		}
		normalized, identity, renewable, expires, err := normalizeCredential(ProviderClaude, c)
		if err != nil {
			return err
		}
		if identity != l.Identity {
			return ErrIdentityChanged
		}
		if intentErr == nil {
			if intent.Login.LastLoginJobID != jobID || !bytes.Equal(intent.Credential.Auth, normalized.Auth) || !bytes.Equal(intent.Credential.Identity, normalized.Identity) {
				return ErrPending
			}
			out = intent.Login
		} else if !errors.Is(intentErr, os.ErrNotExist) {
			return intentErr
		} else {
			if selected := r.Providers[ProviderClaude]; selected.ActiveID == id && selected.Epoch == ^uint64(0) {
				return ErrIneligible
			}
			out = *l
			out.CredentialRevision++
			out.AuthState = AuthReady
			out.Renewable = renewable
			out.ExpiresAt = expires
			out.LastLoginJobID = jobID
			out.LimitKnown = false
			out.LimitNonRolling = false
			out.ExhaustedUntil = time.Time{}
			intent = reloginIntent{Login: out, Credential: normalized}
			raw, err := json.Marshal(intent)
			if err != nil {
				return err
			}
			if len(raw) > maxCredentialBytes*2 {
				return ErrInvalidCredentials
			}
			if err := s.settleClaudeRegistry(); err != nil {
				return err
			}
			if _, err := durableReplace(s.root, reloginPath(id), raw, s.writeOps); err != nil {
				if errors.Is(err, ErrDurabilityUncertain) {
					s.uncertain = true
				}
				return err
			}
		}
		if err := s.claudeAuthorityMutationAllowed(r, *l); errors.Is(err, ErrPending) || errors.Is(err, ErrInUse) {
			return ErrPending
		} else if err != nil {
			return err
		}
		helper, err = s.claudeSettledRemovalRef(r, *l)
		if errors.Is(err, ErrInUse) {
			return ErrPending
		}
		if err == nil && intentErr == nil {
			if settleErr := s.settleClaudeRegistry(); settleErr != nil {
				return settleErr
			}
		}
		return err
	})
	s.mu.Unlock()
	if err != nil || complete {
		return r, out, err
	}
	if helper.Nonce != "" {
		if err := s.RetireClaudeAuth(helper); err != nil {
			return r, out, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.locked(func() error {
		var err error
		r, err = s.load()
		if err != nil {
			return err
		}
		l := find(&r, id)
		if l == nil {
			return ErrConflict
		}
		if !claudeProfilesOwned(r, ProviderClaude) || r.SchemaVersion < ClaudeProfileCustodySchemaVersion {
			return ErrPending
		}
		intent, err := s.readRelogin(*l)
		if err != nil {
			return err
		}
		if intent.Login.LastLoginJobID != jobID || intent.Login.CredentialRevision != out.CredentialRevision {
			return ErrConflict
		}
		changed, err := s.applyClaudeProfileRelogin(&r, l, intent)
		if err != nil {
			return err
		}
		out = *l
		if changed {
			if r.Revision == ^uint64(0) {
				return ErrIneligible
			}
			r.Revision++
			if err := s.save(r); err != nil {
				return err
			}
		}
		return s.eraseRelogin(id)
	})
	return r, out, err
}

func (s *Store) applyClaudeProfileRelogin(r *Registry, l *Login, intent reloginIntent) (bool, error) {
	if r.MigrationPending {
		return false, ErrPending
	}
	if _, pending := r.Switches[ProviderClaude]; pending {
		return false, ErrPending
	}
	if err := s.claudeAuthNamespaceAbsent(l.ID); errors.Is(err, ErrInUse) {
		return false, ErrPending
	} else if err != nil {
		return false, err
	}
	if err := s.settleClaudeRegistry(); err != nil {
		return false, err
	}
	if l.CredentialRevision == intent.Login.CredentialRevision {
		if l.LastLoginJobID != intent.Login.LastLoginJobID {
			return false, ErrConflict
		}
		return false, nil
	}
	if l.CredentialRevision == ^uint64(0) || intent.Login.CredentialRevision != l.CredentialRevision+1 {
		return false, ErrConflict
	}
	selected := r.Providers[ProviderClaude]
	if selected.ActiveID == l.ID && selected.Epoch == ^uint64(0) {
		return false, ErrIneligible
	}
	if err := s.writeSaved(intent.Login, intent.Credential); err != nil {
		return false, err
	}
	*l = intent.Login
	if selected.ActiveID == l.ID {
		selected.Epoch++
		selected.DesiredCredentialRevision = 0
		r.Providers[ProviderClaude] = selected
	}
	return true, nil
}
