package logins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func identityDigest(provider, id string) string {
	v := sha256.Sum256([]byte(provider + "\x00" + id))
	return hex.EncodeToString(v[:])
}
func validDigest(v string) bool {
	if len(v) != 64 || strings.ToLower(v) != v {
		return false
	}
	_, e := hex.DecodeString(v)
	return e == nil
}
func CodexIdentity(raw []byte) (string, error) {
	var a struct {
		Mode   string `json:"auth_mode"`
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if len(raw) > maxCredentialBytes || validateJSON(raw) != nil || json.Unmarshal(raw, &a) != nil || (a.Mode != "" && a.Mode != "chatgpt") || a.APIKey != "" || strings.TrimSpace(a.Tokens.AccountID) == "" || len(a.Tokens.AccountID) > 4096 {
		return "", ErrInvalidCredentials
	}
	return identityDigest(ProviderCodex, a.Tokens.AccountID), nil
}
func ClaudeIdentity(raw []byte) (string, error) {
	var a struct {
		Account struct {
			ID  string `json:"accountUuid"`
			Org string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	if len(raw) > maxCredentialBytes || validateJSON(raw) != nil || json.Unmarshal(raw, &a) != nil || a.Account.ID == "" || a.Account.Org == "" || len(a.Account.ID) > 4096 || len(a.Account.Org) > 4096 {
		return "", ErrInvalidCredentials
	}
	return identityDigest(ProviderClaude, a.Account.ID+"\x00"+a.Account.Org), nil
}

func normalizeCredential(provider string, c Credential) (Credential, string, bool, time.Time, error) {
	bad := func() (Credential, string, bool, time.Time, error) {
		return Credential{}, "", false, time.Time{}, ErrInvalidCredentials
	}
	if len(c.Auth) > maxCredentialBytes || validateJSON(c.Auth) != nil {
		return bad()
	}
	var auth map[string]json.RawMessage
	if json.Unmarshal(c.Auth, &auth) != nil || auth == nil {
		return bad()
	}
	if provider == ProviderCodex {
		id, e := CodexIdentity(c.Auth)
		if e != nil {
			return bad()
		}
		var t struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
		}
		if json.Unmarshal(auth["tokens"], &t) != nil || t.Access == "" || t.Refresh == "" {
			return bad()
		}
		b, e := json.Marshal(map[string]json.RawMessage{"auth_mode": json.RawMessage(`"chatgpt"`), "tokens": auth["tokens"], "last_refresh": nonemptyJSON(auth["last_refresh"])})
		if e != nil {
			return bad()
		}
		return Credential{Auth: b}, id, true, time.Time{}, nil
	}
	if provider != ProviderClaude {
		return bad()
	}
	id, e := ClaudeIdentity(c.Identity)
	if e != nil {
		return bad()
	}
	var oauth struct {
		Access  string `json:"accessToken"`
		Refresh string `json:"refreshToken"`
		Expires int64  `json:"expiresAt"`
	}
	if json.Unmarshal(auth["claudeAiOauth"], &oauth) != nil || oauth.Access == "" {
		return bad()
	}
	var identity map[string]json.RawMessage
	if json.Unmarshal(c.Identity, &identity) != nil {
		return bad()
	}
	filtered := map[string]json.RawMessage{"oauthAccount": identity["oauthAccount"]}
	if user := identity["userID"]; len(user) > 0 {
		filtered["userID"] = user
	}
	ib, _ := json.Marshal(filtered)
	ab, _ := json.Marshal(map[string]json.RawMessage{"claudeAiOauth": auth["claudeAiOauth"]})
	var exp time.Time
	if oauth.Expires > 0 {
		exp = time.UnixMilli(oauth.Expires).UTC()
	}
	return Credential{Auth: ab, Identity: ib}, id, oauth.Refresh != "", exp, nil
}
func nonemptyJSON(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

func (s *Store) Add(expected uint64, provider, label string, c Credential) (Registry, Login, error) {
	id, e := newID()
	if e != nil {
		return Registry{}, Login{}, e
	}
	return s.Import(expected, Login{ID: id, Provider: provider, Label: label}, c)
}

// Import is idempotent for an already imported ID with identical credentials.
// It never chooses an active login; the caller performs the normal transaction.
func (s *Store) Import(expected uint64, l Login, c Credential) (Registry, Login, error) {
	if !validID(l.ID) || !validProvider(l.Provider) || !validLabel(l.Label) || (l.LastLoginJobID != "" && !validID(l.LastLoginJobID)) {
		return Registry{}, Login{}, ErrIneligible
	}
	if l.LastLoginJobID != "" {
		r, e := s.Snapshot()
		if e != nil {
			return Registry{}, Login{}, e
		}
		if old := find(&r, l.ID); old != nil && old.Provider == l.Provider && old.LastLoginJobID == l.LastLoginJobID && (l.Identity == "" || l.Identity == old.Identity) {
			return r, *old, nil
		}
	}
	r, e := s.mutate(expected, func(r *Registry) error {
		var id string
		var renewable bool
		var expires time.Time
		var e error
		c, id, renewable, expires, e = normalizeCredential(l.Provider, c)
		if e != nil {
			return e
		}
		if l.Identity != "" && l.Identity != id {
			return ErrIdentityChanged
		}
		l.Identity = id
		l.Renewable = renewable
		l.ExpiresAt = expires
		if l.CredentialRevision == 0 {
			l.CredentialRevision = 1
		}
		if l.AuthState == "" {
			l.AuthState = AuthReady
		}
		if old := find(r, l.ID); old != nil {
			stored, e := s.readSaved(*old)
			if e != nil {
				return e
			}
			a, _ := json.Marshal(stored)
			b, _ := json.Marshal(c)
			if old.Identity != id || string(a) != string(b) {
				return ErrConflict
			}
			l = *old
			return nil
		}
		for _, old := range r.Logins {
			if old.Provider == l.Provider && old.Identity == l.Identity {
				return ErrConflict
			}
		}
		if e := s.writeSaved(l, c); e != nil {
			return e
		}
		r.Logins = append(r.Logins, l)
		return nil
	})
	return r, l, e
}
func owned(r Registry, id string) bool {
	for _, p := range r.Providers {
		if p.ActiveID == id {
			return true
		}
	}
	for _, h := range r.Handovers {
		for _, held := range h.HeldLoginIDs {
			if held == id {
				return true
			}
		}
	}
	for _, op := range r.Switches {
		if op.FromID == id || op.ToID == id || op.NextID == id {
			return true
		}
	}
	return false
}
func (s *Store) Remove(expected uint64, id string) (Registry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Registry
	e := s.locked(func() error {
		var e error
		r, e = s.load()
		if e != nil {
			return e
		}
		if r.Revision != expected {
			return ErrConflict
		}
		if owned(r, id) {
			return ErrInUse
		}
		var lease *GrantLease
		if codexOwned(r, ProviderCodex) {
			for _, l := range r.Logins {
				if l.ID == id && l.Provider == ProviderCodex {
					lease, e = s.acquireCodexGrantLocked(l)
					if e != nil {
						return e
					}
					defer func() { _ = lease.lock.Close(); lease.lock = nil }()
					if _, e = s.root.Lstat(filepath.Join("saved", id, codexAuxFile)); e == nil {
						if e = lease.CleanupAuxiliary(); e != nil {
							return e
						}
					} else if !errors.Is(e, os.ErrNotExist) {
						return e
					}
				}
			}
		}
		for i, l := range r.Logins {
			if l.ID == id {
				parent, _, e := s.openSavedForRemoval(id)
				if e != nil {
					return e
				}
				if e = parent.Close(); e != nil {
					return e
				}
				r.Logins = append(r.Logins[:i], r.Logins[i+1:]...)
				r.Revision++
				if e = s.save(r); e != nil {
					return e
				}
				return s.eraseSaved(id)
			}
		}
		return ErrIneligible
	})
	return r, e
}

func (s *Store) openSavedForRemoval(id string) (*os.Root, []os.DirEntry, error) {
	if !validID(id) {
		return nil, nil, ErrIneligible
	}
	dir := filepath.Join("saved", id)
	parent, e := openPrivateDir(s.root, dir)
	if e != nil {
		return nil, nil, e
	}
	ok := false
	defer func() {
		if !ok {
			_ = parent.Close()
		}
	}()
	f, e := parent.Open(".")
	if e != nil {
		return nil, nil, e
	}
	entries, e := f.ReadDir(-1)
	_ = f.Close()
	if e != nil {
		return nil, nil, e
	}
	for _, ent := range entries {
		if ent.Name() != ".grant-owner" && ent.Name() != "auth.json" && ent.Name() != ".grant-lock" && ent.Name() != "credential.json" && ent.Name() != ".credentials.json" && ent.Name() != "relogin.json" && !validStoreTemporary(ent.Name()) {
			return nil, nil, errUnknownSavedData
		}
		if _, e = readPrivate(parent, ent.Name(), maxCredentialBytes*2); e != nil {
			return nil, nil, e
		}
	}
	ok = true
	return parent, entries, nil
}

func validStoreTemporary(name string) bool {
	id, ok := strings.CutPrefix(name, ".login-tmp-")
	return ok && validID(id)
}

func (s *Store) eraseSaved(id string) error {
	parent, entries, e := s.openSavedForRemoval(id)
	if e != nil {
		return e
	}
	defer func() { _ = parent.Close() }()
	for _, ent := range entries {
		if e = parent.Remove(ent.Name()); e != nil {
			return e
		}
	}
	if s.keychain != nil {
		if e = s.keychain.Delete(s.savedNativeHome(id)); e != nil {
			return e
		}
	}
	if len(entries) != 0 {
		if e = syncRootDir(parent, "."); e != nil {
			return ErrDurabilityUncertain
		}
	}
	if e = s.root.Remove(filepath.Join("saved", id)); e != nil {
		return e
	}
	if e = syncRootDir(s.root, "saved"); e != nil {
		return ErrDurabilityUncertain
	}
	return nil
}

// ImportUnavailable retains a known native identity after revoked credentials
// were cleared by the native CLI. It creates no usable authentication authority.
func (s *Store) ImportUnavailable(expected uint64, l Login) (Registry, Login, error) {
	if !validID(l.ID) || !validProvider(l.Provider) || !validLabel(l.Label) || !validDigest(l.Identity) || (l.LastLoginJobID != "" && !validID(l.LastLoginJobID)) || (l.AuthState != AuthNeedsLogin && l.AuthState != AuthPaused) {
		return Registry{}, Login{}, ErrIneligible
	}
	if l.CredentialRevision == 0 {
		l.CredentialRevision = 1
	}
	l.Renewable = false
	r, e := s.mutate(expected, func(r *Registry) error {
		if old := find(r, l.ID); old != nil {
			if old.Identity != l.Identity || old.Provider != l.Provider {
				return ErrConflict
			}
			l = *old
			return nil
		}
		for _, old := range r.Logins {
			if old.Identity == l.Identity && old.Provider == l.Provider {
				return ErrConflict
			}
		}
		dir := filepath.Join("saved", l.ID)
		if e := mkdirPrivate(s.root, dir, s.writeOps); e != nil {
			return e
		}
		if e := s.writeSavedMetadata(l, Credential{}); e != nil {
			return e
		}
		r.Logins = append(r.Logins, l)
		return nil
	})
	return r, l, e
}
func (s *Store) UpdateAvailability(expected uint64, id, auth string, limited bool, until time.Time) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error {
		l := find(r, id)
		if l == nil {
			return ErrIneligible
		}
		if l.Provider == ProviderCodex {
			if e := s.noGrantRelogin(l.ID); e != nil {
				return e
			}
		}
		if _, e := s.readRelogin(*l); e == nil {
			return ErrPending
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		l.AuthState = auth
		l.LimitKnown = limited
		if !limited {
			l.LimitNonRolling = false
			until = time.Time{}
		} else if l.LimitNonRolling {
			until = time.Time{}
		}
		l.ExhaustedUntil = until
		return nil
	})
}

// UpdateExhaustion retains a terminal credit/spend rejection across sparse
// rolling-window feeds; only an explicit clear or fresh grant releases it.
func (s *Store) UpdateExhaustion(expected uint64, id string, until time.Time, nonRolling bool) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error {
		l := find(r, id)
		if l == nil {
			return ErrIneligible
		}
		if l.Provider == ProviderCodex {
			if e := s.noGrantRelogin(l.ID); e != nil {
				return e
			}
		}
		if _, e := s.readRelogin(*l); e == nil {
			return ErrPending
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		l.LimitKnown = true
		l.LimitNonRolling = l.LimitNonRolling || nonRolling
		if l.LimitNonRolling {
			until = time.Time{}
		}
		l.ExhaustedUntil = until
		return nil
	})
}

type savedCredential struct {
	AuthOwner  *grantOwnership `json:"auth_owner,omitempty"`
	Revision   uint64          `json:"revision"`
	Identity   string          `json:"identity"`
	Credential Credential      `json:"credential"`
	LoginJobID string          `json:"login_job_id,omitempty"`
}

func (s *Store) savedPath(l Login) string { return filepath.Join("saved", l.ID, "credential.json") }
func (s *Store) writeSaved(l Login, c Credential) error {
	r, e := s.load()
	if e != nil {
		return e
	}
	if codexOwned(r, l.Provider) {
		return s.writeNewCodexGrant(l, c)
	}
	dir := filepath.Join("saved", l.ID)
	if e := mkdirPrivate(s.root, dir, s.writeOps); e != nil {
		return e
	}
	if l.Provider == ProviderClaude {
		if e := s.writeClaudeAuth(l.ID, c.Auth); e != nil {
			return e
		}
	}
	return s.writeSavedMetadata(l, c)
}
func (s *Store) writeSavedMetadata(l Login, c Credential) error {
	if l.Provider == ProviderCodex {
		r, e := s.load()
		if e != nil {
			return e
		}
		if codexOwned(r, l.Provider) {
			empty := savedCredential{Credential: c}
			if !credentiallessCodex(l, empty) {
				return ErrPending
			}
			if _, e := s.root.Lstat(s.savedPath(l)); e == nil {
				return ErrPending
			} else if !errors.Is(e, os.ErrNotExist) {
				return e
			}
			if e = s.verifyNoCodexGrant(l); e != nil {
				return e
			}
		}
	}
	if l.Provider == ProviderClaude {
		c.Auth = nil
	}
	b, e := json.Marshal(savedCredential{Revision: l.CredentialRevision, Identity: l.Identity, Credential: c})
	if e != nil {
		return e
	}
	if _, e = durableReplace(s.root, s.savedPath(l), b, s.writeOps); e != nil {
		return e
	}
	return syncRootDir(s.root, "saved")
}
func (s *Store) readSaved(l Login) (Credential, error) {
	if l.Provider == ProviderCodex {
		if e := s.noGrantRelogin(l.ID); e != nil {
			return Credential{}, e
		}
	}
	saved, e := s.readSavedRecord(l)
	if e != nil {
		return Credential{}, e
	}
	if l.Provider == ProviderCodex && saved.AuthOwner != nil {
		saved.Credential.Auth, e = s.readCodexGrant(l, saved)
		if e != nil {
			return Credential{}, e
		}
	}
	if l.Provider == ProviderClaude {
		saved.Credential.Auth, e = s.claudeAuth(l.ID)
		if e != nil {
			return Credential{}, e
		}
	}
	c, id, _, _, e := normalizeCredential(l.Provider, saved.Credential)
	if e != nil {
		return Credential{}, e
	}
	if id != l.Identity {
		return Credential{}, ErrIdentityChanged
	}
	return c, nil
}
func (s *Store) readSavedRecord(l Login) (savedCredential, error) {
	b, e := readPrivate(s.root, s.savedPath(l), maxCredentialBytes*2)
	if e != nil {
		return savedCredential{}, e
	}
	var saved savedCredential
	if validateJSON(b) != nil || json.Unmarshal(b, &saved) != nil || saved.Revision != l.CredentialRevision || saved.Identity != l.Identity {
		return savedCredential{}, ErrConflict
	}
	return saved, nil
}

// VerifySaved proves migration's canonical credential copy before source
// cleanup. A needs-login label does not excuse malformed or foreign auth.
func (s *Store) VerifySaved(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.safeAnchor(); e != nil {
		return e
	}
	r, e := s.load()
	if e != nil {
		return e
	}
	l := find(&r, id)
	if l == nil {
		return ErrIneligible
	}
	if l.Provider == ProviderCodex {
		if e = s.noGrantRelogin(l.ID); e != nil {
			return e
		}
	}
	if _, e = s.readSaved(*l); e == nil {
		return nil
	}
	if l.AuthState != AuthNeedsLogin && l.AuthState != AuthPaused {
		return e
	}
	record, recordErr := s.readSavedRecord(*l)
	if recordErr != nil {
		return recordErr
	}
	if (len(record.Credential.Auth) != 0 && string(record.Credential.Auth) != "null") || (len(record.Credential.Identity) != 0 && string(record.Credential.Identity) != "null") {
		return e
	}
	if l.Provider == ProviderCodex && codexOwned(r, ProviderCodex) {
		return s.verifyNoCodexGrant(*l)
	}
	if l.Provider == ProviderClaude {
		if _, authErr := s.claudeAuth(id); !errors.Is(authErr, os.ErrNotExist) {
			if authErr != nil {
				return authErr
			}
			return ErrInvalidCredentials
		}
	}
	return nil
}

func (s *Store) NextEligible(provider string, attempted []string, now time.Time) (Login, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.safeAnchor(); e != nil {
		return Login{}, e
	}
	r, e := s.load()
	if e != nil {
		return Login{}, e
	}
	tried := map[string]bool{}
	for _, id := range attempted {
		tried[id] = true
	}
	for _, l := range r.Logins {
		if l.Provider != provider || tried[l.ID] || r.Providers[provider].ActiveID == l.ID || l.AuthState != AuthReady {
			continue
		}
		if l.LimitKnown && (l.ExhaustedUntil.IsZero() || now.Before(l.ExhaustedUntil)) {
			continue
		}
		if _, e = s.readRelogin(l); e == nil {
			continue
		} else if !errors.Is(e, os.ErrNotExist) {
			return Login{}, e
		}
		return l, nil
	}
	return Login{}, ErrNoEligible
}

// PrepareActivation checks the parked authority without minting tokens. Neither
// native CLI has a characterized no-model command that guarantees refresh.
// Expired access tokens remain usable when the native runtime can renew them.
func (s *Store) PrepareActivation(id string) (Login, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.safeAnchor(); e != nil {
		return Login{}, e
	}
	r, e := s.load()
	if e != nil {
		return Login{}, e
	}
	l := find(&r, id)
	if l == nil || !eligible(*l, time.Now()) {
		return Login{}, ErrIneligible
	}
	if owned(r, id) {
		return Login{}, ErrInUse
	}
	if _, e = s.readRelogin(*l); e == nil {
		return Login{}, ErrPending
	} else if !errors.Is(e, os.ErrNotExist) {
		return Login{}, e
	}
	if e := s.validateAuthContext(l.Provider); e != nil {
		return Login{}, e
	}
	if _, e = s.readSaved(*l); e != nil {
		return Login{}, e
	}
	return *l, nil
}
