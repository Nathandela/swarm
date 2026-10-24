package logins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const grantReloginFile = "grant-relogin.json"

type grantReloginIntent struct {
	Version       int             `json:"version"`
	LoginID       string          `json:"login_id"`
	FromRevision  uint64          `json:"from_revision"`
	PreviousOwner string          `json:"previous_owner,omitempty"`
	Generation    string          `json:"generation"`
	Target        savedCredential `json:"target"`
}

func grantReloginPath(id string) string { return filepath.Join("saved", id, grantReloginFile) }
func (s *Store) noGrantRelogin(id string) error {
	_, e := s.root.Lstat(grantReloginPath(id))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	return ErrPending
}
func (l *GrantLease) tokensAllowed() error {
	if l.reloginJobID != "" {
		return ErrPending
	}
	return l.store.noGrantRelogin(l.login.ID)
}
func codexCredentialGeneration(raw []byte) ([32]byte, error) {
	if _, e := CodexGrantOwner(raw); e != nil {
		return [32]byte{}, e
	}
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(raw, &doc)
	var tokens map[string]json.RawMessage
	_ = json.Unmarshal(doc["tokens"], &tokens)
	var values [3]string
	for i, key := range []string{"access_token", "id_token", "refresh_token"} {
		if json.Unmarshal(tokens[key], &values[i]) != nil {
			return [32]byte{}, ErrInvalidCredentials
		}
	}
	canonical, _ := json.Marshal(values)
	return sha256.Sum256(canonical), nil
}
func (s *Store) readGrantIntent(l Login) (grantReloginIntent, error) {
	var intent grantReloginIntent
	raw, e := readPrivate(s.root, grantReloginPath(l.ID), 16<<10)
	if e != nil {
		return intent, e
	}
	if validateJSON(raw) != nil || json.Unmarshal(raw, &intent) != nil || intent.Version != 1 || intent.LoginID != l.ID || intent.FromRevision == 0 || intent.FromRevision == ^uint64(0) || intent.Target.Revision != intent.FromRevision+1 || intent.Target.Identity != l.Identity || !validID(intent.Target.LoginJobID) || !validDigest(intent.Generation) || intent.Target.AuthOwner == nil || intent.Target.AuthOwner.Version != 1 || !validDigest(intent.Target.AuthOwner.Owner) || !emptyCredentialPart(intent.Target.Credential.Auth) || !emptyCredentialPart(intent.Target.Credential.Identity) || (intent.PreviousOwner != "" && (!validDigest(intent.PreviousOwner) || intent.PreviousOwner != intent.Target.AuthOwner.Owner)) {
		return intent, ErrInvalidCredentials
	}
	if l.CredentialRevision != intent.FromRevision && (l.CredentialRevision != intent.Target.Revision || l.LastLoginJobID != intent.Target.LoginJobID) {
		return intent, ErrConflict
	}
	return intent, nil
}
func (s *Store) intentMetadata(l Login, intent grantReloginIntent) (savedCredential, error) {
	var saved savedCredential
	raw, e := readPrivate(s.root, s.savedPath(l), maxCredentialBytes*2)
	if e != nil {
		return saved, e
	}
	if validateJSON(raw) != nil || json.Unmarshal(raw, &saved) != nil || saved.Identity != l.Identity || !emptyCredentialPart(saved.Credential.Auth) || !emptyCredentialPart(saved.Credential.Identity) {
		return saved, ErrInvalidCredentials
	}
	if saved.Revision == intent.Target.Revision {
		if saved.AuthOwner == nil || saved.AuthOwner.Version != 1 || saved.AuthOwner.Owner != intent.Target.AuthOwner.Owner || saved.LoginJobID != intent.Target.LoginJobID {
			return saved, ErrConflict
		}
	} else if saved.Revision == intent.FromRevision && l.CredentialRevision == intent.FromRevision {
		if intent.PreviousOwner == "" {
			if !credentiallessCodex(l, saved) {
				return saved, ErrConflict
			}
		} else if saved.AuthOwner == nil || saved.AuthOwner.Version != 1 || saved.AuthOwner.Owner != intent.PreviousOwner {
			return saved, ErrIdentityChanged
		}
	} else {
		return saved, ErrConflict
	}
	return saved, nil
}
func (s *Store) eraseGrantIntent(id string) error {
	name := grantReloginPath(id)
	remove := s.root.Remove
	if s.writeOps.remove != nil {
		remove = s.writeOps.remove
	}
	if e := remove(name); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e := syncRootDir(s.root, filepath.Join("saved", id)); e != nil {
		s.uncertain = true
		return ErrDurabilityUncertain
	}
	return nil
}
func (s *Store) syncCodexGrant(id string) error {
	f, e := s.root.OpenFile(filepath.Join("saved", id, "auth.json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	e = f.Sync()
	_ = f.Close()
	if e != nil {
		return ErrDurabilityUncertain
	}
	if e = syncRootDir(s.root, filepath.Join("saved", id)); e != nil {
		return ErrDurabilityUncertain
	}
	return nil
}

// reconcileGrantIntents never reinstalls enrollment credentials. Only canonical
// bytes matching a durable intent can advance an uncommitted job acknowledgement.
func (s *Store) reconcileGrantIntents(r *Registry) (map[string]bool, []string, bool, error) {
	pending := make(map[string]bool)
	var retire []string
	baselineChanged := false
	if !codexOwned(*r, ProviderCodex) {
		return pending, retire, baselineChanged, nil
	}
	for _, l := range r.Logins {
		if l.Provider != ProviderCodex {
			continue
		}
		intent, e := s.readGrantIntent(l)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, nil, false, e
		}
		if r.SchemaVersion < 3 {
			return nil, nil, false, ErrIneligible
		}
		if _, e = s.intentMetadata(l, intent); e != nil {
			return nil, nil, false, e
		}
		selected := r.Providers[ProviderCodex]
		if selected.ActiveID == l.ID && selected.DesiredCredentialRevision == 0 {
			selected.DesiredCredentialRevision = intent.FromRevision
			r.Providers[ProviderCodex] = selected
			baselineChanged = true
		}
		if l.CredentialRevision == intent.Target.Revision && l.LastLoginJobID == intent.Target.LoginJobID {
			retire = append(retire, l.ID)
			continue
		}
		raw, e := readPrivate(s.root, filepath.Join("saved", l.ID, "auth.json"), maxCredentialBytes)
		if e != nil {
			pending[l.ID] = true
			continue
		}
		owner, e := CodexGrantOwner(raw)
		if e != nil || owner != intent.Target.AuthOwner.Owner {
			pending[l.ID] = true
			continue
		}
		identity, e := CodexIdentity(raw)
		if e != nil || identity != l.Identity {
			pending[l.ID] = true
			continue
		}
		generation, e := codexCredentialGeneration(raw)
		if e != nil || hex.EncodeToString(generation[:]) != intent.Generation {
			pending[l.ID] = true
			continue
		}
		if e = s.syncCodexGrant(l.ID); e != nil {
			return nil, nil, false, e
		}
		encoded, e := json.Marshal(intent.Target)
		if e != nil {
			return nil, nil, false, e
		}
		if _, e = durableReplace(s.root, s.savedPath(l), encoded, s.writeOps); e != nil {
			return nil, nil, false, e
		}
		retire = append(retire, l.ID)
	}
	return pending, retire, baselineChanged, nil
}

// AcquireCodexRelogin is restricted to an owner-authorized, completed native
// enrollment job. It cannot provide tokens or start a background refresher.
func (s *Store) AcquireCodexRelogin(id, jobID string) (*GrantLease, error) {
	if !validID(jobID) {
		return nil, ErrIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var lease *GrantLease
	e := s.locked(func() error {
		r, e := s.load()
		if e != nil {
			return e
		}
		if !codexOwned(r, ProviderCodex) {
			return ErrPending
		}
		l := find(&r, id)
		if l == nil || l.Provider != ProviderCodex {
			return ErrIneligible
		}
		lease, e = s.acquireCodexCustodyLocked(*l, jobID)
		return e
	})
	return lease, e
}

// CheckCodexGrantPending fences cached or already-returned access credentials
// without reading tokens or imposing the legacy switch restart barrier.
func (s *Store) CheckCodexGrantPending(id string) error {
	if !validID(id) {
		return ErrIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.safeAnchor(); e != nil {
		return e
	}
	if _, e := checkRelative(s.root, filepath.Join("saved", id), true); e != nil {
		return e
	}
	return s.noGrantRelogin(id)
}
