package logins

import (
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
		intent, e := s.readRelogin(*l)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
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
