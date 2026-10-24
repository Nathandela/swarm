package logins

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/processcontain"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const CodexNativeGrantMode = "native-grant-v1"

type grantOwnership struct {
	Version int    `json:"version"`
	Owner   string `json:"owner"`
}

func codexOwned(r Registry, provider string) bool {
	return provider == ProviderCodex && r.Providers[provider].AuthOwnerMode == CodexNativeGrantMode
}

// BeginCodexOwnershipMigration fences old binaries before any credential role
// changes. The normal stopped-writer journal retains every restart obligation.
func (s *Store) BeginCodexOwnershipMigration(expected uint64, sessions []Restart) (Switch, error) {
	return s.BeginCodexOwnershipMigrationTo(expected, "", sessions)
}

func (s *Store) BeginCodexOwnershipMigrationTo(expected uint64, toID string, sessions []Restart) (Switch, error) {
	var out Switch
	_, err := s.mutate(expected, func(r *Registry) error {
		if codexOwned(*r, ProviderCodex) {
			return ErrConflict
		}
		if r.MigrationPending {
			return ErrPending
		}
		if _, ok := r.Switches[ProviderCodex]; ok {
			return ErrPending
		}
		p := r.Providers[ProviderCodex]
		if p.Epoch == ^uint64(0) || p.ActiveID == "" && toID == "" {
			return ErrIneligible
		}
		if toID == "" {
			toID = p.ActiveID
		}
		destination := find(r, toID)
		if destination == nil || destination.Provider != ProviderCodex {
			return ErrIneligible
		}
		if toID != p.ActiveID {
			if !eligible(*destination, time.Now()) {
				return ErrIneligible
			}
			if _, e := s.readSaved(*destination); e != nil {
				return e
			}
		}
		for _, l := range r.Logins {
			if l.Provider == ProviderCodex {
				if _, e := s.readRelogin(l); e == nil {
					return ErrPending
				} else if !errors.Is(e, os.ErrNotExist) {
					return e
				}
			}
		}
		id, e := newID()
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		for i := range sessions {
			row := &sessions[i]
			if row.SourceID == "" || row.ConversationID == "" || seen[row.SourceID] {
				return ErrIneligible
			}
			seen[row.SourceID] = true
			*row = Restart{SourceID: row.SourceID, ConversationID: row.ConversationID, Phase: "waiting"}
		}
		out = Switch{ID: id, Provider: ProviderCodex, FromID: p.ActiveID, ToID: toID, FromEpoch: p.Epoch, ToEpoch: p.Epoch + 1, Phase: "waiting", OwnershipMigration: true, Sessions: sessions, AttemptedIDs: []string{toID}}
		if r.SchemaVersion < 2 {
			r.SchemaVersion = 2
		}
		r.Switches[ProviderCodex] = out
		return nil
	})
	return out, err
}

// MigrateCodexOwnership is replayable only under the durable captured journal.
// Lifecycle must prove all legacy writers stopped before CaptureOutgoing.
func (s *Store) MigrateCodexOwnership(id string) error {
	return s.switchMutation(id, func(r *Registry, op *Switch) error {
		if !op.OwnershipMigration || op.Provider != ProviderCodex || r.SchemaVersion < 2 {
			return ErrConflict
		}
		if codexOwned(*r, ProviderCodex) && (op.Phase == "installed" || op.Phase == "committed" || op.Phase == "resuming") {
			return nil
		}
		if op.Phase != "captured" {
			return ErrInUse
		}
		for _, row := range op.Sessions {
			if row.Phase != "stopped" && row.Phase != "cancelled" {
				return ErrInUse
			}
		}
		// Validate every source before changing any, so malformed parked grants keep
		// their original authority and block the migration visibly.
		for _, l := range r.Logins {
			if l.Provider == ProviderCodex {
				saved, e := s.readSavedRecord(l)
				if e != nil {
					return e
				}
				if credentiallessCodex(l, saved) {
					if e = s.verifyNoCodexGrant(l); e != nil {
						return e
					}
					continue
				}
				c, e := s.readSaved(l)
				if e != nil {
					return e
				}
				if _, e = CodexGrantOwner(c.Auth); e != nil {
					return e
				}
			}
		}
		for _, l := range r.Logins {
			if l.Provider == ProviderCodex {
				saved, e := s.readSavedRecord(l)
				if e != nil {
					return e
				}
				if credentiallessCodex(l, saved) {
					continue
				}
				if saved.AuthOwner != nil {
					if _, e = s.readCodexGrant(l, saved); e != nil {
						return e
					}
					continue
				}
				owner, e := CodexGrantOwner(saved.Credential.Auth)
				if e != nil {
					return e
				}
				existing, readErr := readPrivate(s.root, filepath.Join("saved", l.ID, "auth.json"), maxCredentialBytes)
				if readErr == nil && !bytes.Equal(existing, saved.Credential.Auth) {
					return ErrConflict
				}
				if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
					return readErr
				}
				if _, e = durableReplace(s.root, filepath.Join("saved", l.ID, "auth.json"), saved.Credential.Auth, s.writeOps); e != nil {
					return e
				}
				saved.AuthOwner = &grantOwnership{Version: 1, Owner: owner}
				saved.Credential.Auth = nil
				b, e := json.Marshal(saved)
				if e != nil {
					return e
				}
				if _, e = durableReplace(s.root, s.savedPath(l), b, s.writeOps); e != nil {
					return e
				}
			}
		}
		// Retire the common refresh authority only after every canonical grant is
		// durable. Keep unrelated auth/config fields owned by the native runtime.
		home, e := openPrivateDir(s.root, "provider/codex/native")
		if e != nil {
			return e
		}
		e = eraseNativeFields(home, "auth.json", "auth_mode", "tokens", "last_refresh", "OPENAI_API_KEY")
		_ = home.Close()
		if e != nil {
			return e
		}
		p := r.Providers[ProviderCodex]
		p.Provider = ProviderCodex
		p.AuthOwnerMode = CodexNativeGrantMode
		r.Providers[ProviderCodex] = p
		op.Phase = "installed"
		return nil
	})
}
func (s *Store) readCodexGrant(l Login, saved savedCredential) ([]byte, error) {
	if e := s.noGrantRelogin(l.ID); e != nil {
		return nil, e
	}
	return s.readCodexGrantUnchecked(l, saved)
}
func (s *Store) readCodexGrantUnchecked(l Login, saved savedCredential) ([]byte, error) {
	if saved.AuthOwner == nil || saved.AuthOwner.Version != 1 || !validDigest(saved.AuthOwner.Owner) || len(saved.Credential.Auth) != 0 && string(saved.Credential.Auth) != "null" {
		return nil, ErrInvalidCredentials
	}
	raw, e := readPrivate(s.root, filepath.Join("saved", l.ID, "auth.json"), maxCredentialBytes)
	if e != nil {
		return nil, e
	}
	owner, e := CodexGrantOwner(raw)
	if e != nil {
		return nil, e
	}
	if owner != saved.AuthOwner.Owner {
		return nil, ErrIdentityChanged
	}
	identity, e := CodexIdentity(raw)
	if e != nil {
		return nil, e
	}
	if identity != l.Identity {
		return nil, ErrIdentityChanged
	}
	return raw, nil
}
func (s *Store) validateSelected(r Registry, provider, identity, id string) error {
	if !codexOwned(r, provider) {
		return s.validateNative(provider, identity, id)
	}
	l := find(&r, id)
	if l == nil || l.Identity != identity {
		return ErrIdentityChanged
	}
	_, e := s.readSaved(*l)
	return e
}

// GrantLease owns one native refresh writer across startup, refresh and stop.
// It must remain held until the supervising worker proves native containment.
type GrantLease struct {
	store        *Store
	lock         *os.File
	login        Login
	owner        string
	homeAnchor   os.FileInfo
	nativeIntent bool
	reloginJobID string
}

func (s *Store) AcquireCodexGrant(id string) (*GrantLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lease *GrantLease
	err := s.locked(func() error {
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
		if op, ok := r.Switches[ProviderCodex]; ok && op.Phase != "committed" && op.Phase != "resuming" {
			return ErrPending
		}
		lease, e = s.acquireCodexGrantLocked(*l)
		return e
	})
	return lease, err
}
func (l *GrantLease) Home() string { return l.store.savedNativeHome(l.login.ID) }
func (l *GrantLease) Close() error {
	if l.lock == nil {
		return nil
	}
	if l.nativeIntent {
		if e := l.ProveStopped(); e != nil {
			return e
		}
	}
	e := l.lock.Close()
	l.lock = nil
	return e
}
func (l *GrantLease) Access(now time.Time) (AccessCredential, error) {
	if e := l.tokensAllowed(); e != nil {
		return AccessCredential{}, e
	}
	if l.login.AuthState != AuthReady {
		return AccessCredential{}, ErrIneligible
	}
	if e := l.validate(); e != nil {
		return AccessCredential{}, e
	}
	raw, e := readPrivate(l.store.root, filepath.Join("saved", l.login.ID, "auth.json"), maxCredentialBytes)
	if e != nil {
		return AccessCredential{}, e
	}
	return CodexAccess(raw, l.owner, now)
}

func (s *Store) acquireCodexGrantLocked(l Login) (*GrantLease, error) {
	return s.acquireCodexCustodyLocked(l, "")
}
func (s *Store) acquireCodexCustodyLocked(l Login, jobID string) (*GrantLease, error) {
	owner := ""
	if jobID == "" {
		if e := s.noGrantRelogin(l.ID); e != nil {
			return nil, e
		}
		saved, e := s.readSavedRecord(l)
		if e != nil {
			return nil, e
		}
		if credentiallessCodex(l, saved) {
			if e = s.verifyNoCodexGrant(l); e != nil {
				return nil, e
			}
		} else {
			if _, e = s.readCodexGrant(l, saved); e != nil {
				return nil, e
			}
			owner = saved.AuthOwner.Owner
		}
	} else {
		intent, e := s.readGrantIntent(l)
		if e == nil {
			if intent.Target.LoginJobID != jobID {
				return nil, ErrPending
			}
			if _, e = s.intentMetadata(l, intent); e != nil {
				return nil, e
			}
			owner = intent.PreviousOwner
			if l.CredentialRevision == intent.Target.Revision {
				owner = intent.Target.AuthOwner.Owner
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, e
		} else {
			saved, e := s.readSavedRecord(l)
			if e != nil {
				return nil, e
			}
			if credentiallessCodex(l, saved) {
				if e = s.verifyNoCodexGrant(l); e != nil {
					return nil, e
				}
			} else {
				if saved.AuthOwner == nil || saved.AuthOwner.Version != 1 || !validDigest(saved.AuthOwner.Owner) || !emptyCredentialPart(saved.Credential.Auth) || !emptyCredentialPart(saved.Credential.Identity) {
					return nil, ErrInvalidCredentials
				}
				owner = saved.AuthOwner.Owner
				if _, e = s.readCodexGrantUnchecked(l, saved); e != nil && !errors.Is(e, os.ErrNotExist) {
					return nil, e
				}
			}
		}
	}

	name := filepath.Join("saved", l.ID, ".grant-lock")
	f, e := s.root.OpenFile(name, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*GrantLease, error) { _ = f.Close(); return nil, e }
	info, e := f.Stat()
	if e != nil || !privateInfo(info, false) {
		return fail(ErrUnsafePath)
	}
	pathInfo, e := checkRelative(s.root, name, false)
	if e != nil || !os.SameFile(info, pathInfo) {
		return fail(ErrUnsafePath)
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		if errors.Is(e, syscall.EWOULDBLOCK) {
			e = ErrInUse
		}
		return fail(e)
	}
	raw, ownerErr := readPrivate(s.root, filepath.Join("saved", l.ID, ".grant-owner"), 1024)
	if ownerErr == nil {
		var native processcontain.Identity
		if validateJSON(raw) != nil || json.Unmarshal(raw, &native) != nil || native.PID <= 0 || native.StartTime <= 0 {
			return fail(ErrInvalidCredentials)
		}
		if native.Alive() {
			return fail(ErrInUse)
		}
	} else if !errors.Is(ownerErr, os.ErrNotExist) {
		return fail(ownerErr)
	}
	// flock disappears after a crashed supervisor; its native process may not.
	scan := s.grantWriters
	if scan == nil {
		scan = processcontain.EnvironmentWriters
	}
	writers, e := scan([]string{s.savedNativeHome(l.ID)}, nil)
	if e != nil || len(writers) != 0 {
		return fail(ErrInUse)
	}
	anchor, e := checkRelative(s.root, filepath.Join("saved", l.ID), true)
	if e != nil {
		return fail(e)
	}
	return &GrantLease{store: s, lock: f, login: l, owner: owner, homeAnchor: anchor, reloginJobID: jobID}, nil
}
func (s *Store) writeNewCodexGrant(l Login, c Credential) error {
	// Existing grants require lease-owned replacement; import may create only a
	// new saved identity, never overwrite an existing native refresh authority.
	if _, e := s.root.Lstat(s.savedPath(l)); e == nil {
		return ErrPending
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	owner, e := CodexGrantOwner(c.Auth)
	if e != nil {
		return e
	}
	existing, readErr := readPrivate(s.root, filepath.Join("saved", l.ID, "auth.json"), maxCredentialBytes)
	if readErr == nil && !bytes.Equal(existing, c.Auth) {
		return ErrConflict
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if e = mkdirPrivate(s.root, filepath.Join("saved", l.ID), s.writeOps); e != nil {
		return e
	}
	if _, e = durableReplace(s.root, filepath.Join("saved", l.ID, "auth.json"), c.Auth, s.writeOps); e != nil {
		return e
	}
	b, e := json.Marshal(savedCredential{Revision: l.CredentialRevision, Identity: l.Identity, AuthOwner: &grantOwnership{Version: 1, Owner: owner}})
	if e != nil {
		return e
	}
	_, e = durableReplace(s.root, s.savedPath(l), b, s.writeOps)
	return e
}

// Generation is an opaque change proof, never a credential or public metadata.
func (l *GrantLease) Generation() ([32]byte, error) {
	if e := l.tokensAllowed(); e != nil {
		return [32]byte{}, e
	}
	if l.owner == "" {
		return [32]byte{}, ErrIneligible
	}
	if e := l.validate(); e != nil {
		return [32]byte{}, e
	}
	raw, e := readPrivate(l.store.root, filepath.Join("saved", l.login.ID, "auth.json"), maxCredentialBytes)
	if e != nil {
		return [32]byte{}, e
	}
	owner, e := CodexGrantOwner(raw)
	if e != nil {
		return [32]byte{}, e
	}
	if owner != l.owner {
		return [32]byte{}, ErrIdentityChanged
	}
	return codexCredentialGeneration(raw)
}

// ValidateRenewal runs after native containment. It establishes changed bytes,
// exact owner, unexpired access, and file+directory durability.
func (l *GrantLease) ValidateRenewal(before [32]byte, now time.Time) error {
	if e := l.ProveStopped(); e != nil {
		return e
	}
	after, e := l.Generation()
	if e != nil {
		return e
	}
	if after == before {
		return ErrInvalidCredentials
	}
	if _, e = l.Access(now); e != nil {
		return e
	}
	f, e := l.store.root.OpenFile(filepath.Join("saved", l.login.ID, "auth.json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	e = f.Sync()
	_ = f.Close()
	if e != nil {
		return ErrDurabilityUncertain
	}
	if e = syncRootDir(l.store.root, filepath.Join("saved", l.login.ID)); e != nil {
		return ErrDurabilityUncertain
	}
	return nil
}

func (l *GrantLease) validate() error {
	if l.lock == nil {
		return ErrInUse
	}
	if e := l.store.safeAnchor(); e != nil {
		return e
	}
	dir, e := checkRelative(l.store.root, filepath.Join("saved", l.login.ID), true)
	if e != nil || !os.SameFile(dir, l.homeAnchor) {
		return ErrUnsafePath
	}
	info, e := l.lock.Stat()
	if e != nil {
		return ErrUnsafePath
	}
	pathInfo, e := checkRelative(l.store.root, filepath.Join("saved", l.login.ID, ".grant-lock"), false)
	if e != nil || !os.SameFile(info, pathInfo) {
		return ErrUnsafePath
	}
	return nil
}
func (l *GrantLease) ProveStopped() error {
	if e := l.validate(); e != nil {
		return e
	}
	scan := l.store.grantWriters
	if scan == nil {
		scan = processcontain.EnvironmentWriters
	}
	writers, e := scan([]string{l.Home()}, nil)
	if e != nil || len(writers) != 0 {
		return ErrInUse
	}
	return nil
}

// RecordNative publishes the process creation identity before any initialization
// RPC; startup-gap recovery additionally checks the exact home environment.
func (l *GrantLease) RecordNative(identity processcontain.Identity) error {
	if e := l.tokensAllowed(); e != nil {
		return e
	}
	if e := l.validate(); e != nil {
		return e
	}
	l.nativeIntent = true
	if !identity.Alive() {
		return ErrInUse
	}
	b, e := json.Marshal(identity)
	if e != nil {
		return e
	}
	_, e = durableReplace(l.store.root, filepath.Join("saved", l.login.ID, ".grant-owner"), b, l.store.writeOps)
	return e
}

// ReplaceCredential installs a stopped enrollment's same-owner grant under
// exclusive custody. Canonical auth is the only retained refresh authority.
func (l *GrantLease) ReplaceCredential(expected uint64, c Credential, jobID string) (Registry, Login, error) {
	if e := l.ProveStopped(); e != nil {
		return Registry{}, Login{}, e
	}
	if !validID(jobID) || l.reloginJobID != "" && l.reloginJobID != jobID {
		return Registry{}, Login{}, ErrIneligible
	}
	s := l.store
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
		current := find(&r, l.login.ID)
		if current == nil || !codexOwned(r, ProviderCodex) {
			return ErrIneligible
		}
		intent, intentErr := s.readGrantIntent(*current)
		if intentErr != nil && !errors.Is(intentErr, os.ErrNotExist) {
			return intentErr
		}
		if intentErr == nil && intent.Target.LoginJobID != jobID {
			return ErrPending
		}
		if intentErr == nil {
			if e = validateGrantSelection(r, intent); e != nil {
				return e
			}
		}
		if current.LastLoginJobID == jobID {
			if intentErr == nil {
				selected := r.Providers[ProviderCodex]
				if selected.ActiveID == current.ID && selected.DesiredCredentialRevision == 0 {
					if r.Revision == ^uint64(0) {
						return ErrIneligible
					}
					schemaForHandovers(&r)
					selected.DesiredCredentialRevision = intent.FromRevision
					r.Providers[ProviderCodex] = selected
					r.Revision++
					if e = s.save(r); e != nil {
						return e
					}
				}
				if _, e = s.intentMetadata(*current, intent); e != nil {
					return e
				}
				if e = s.eraseGrantIntent(current.ID); e != nil {
					return e
				}
			}
			saved, e := s.readSavedRecord(*current)
			if e != nil {
				return e
			}
			if saved.AuthOwner == nil {
				return ErrInvalidCredentials
			}
			out = *current
			l.login = *current
			l.owner = saved.AuthOwner.Owner
			l.reloginJobID = ""
			return nil
		}
		if r.Revision != expected || current.CredentialRevision != l.login.CredentialRevision {
			return ErrConflict
		}
		selection, e := reloginSelection(r, *current)
		if e != nil {
			return e
		}
		if selection != nil && l.reloginJobID != jobID {
			return ErrPending
		}
		if r.Revision == ^uint64(0) || current.CredentialRevision == ^uint64(0) {
			return ErrIneligible
		}
		normalized, identity, renewable, expires, e := normalizeCredential(ProviderCodex, c)
		if e != nil {
			return e
		}
		owner, e := CodexGrantOwner(normalized.Auth)
		if e != nil {
			return e
		}
		if identity != current.Identity || l.owner != "" && owner != l.owner {
			return ErrIdentityChanged
		}
		if e = l.validate(); e != nil {
			return e
		}
		if intentErr == nil {
			if _, e = s.intentMetadata(*current, intent); e != nil {
				return e
			}
		} else {
			saved, e := s.readSavedRecord(*current)
			if e != nil {
				return e
			}
			if l.owner == "" {
				if !credentiallessCodex(*current, saved) {
					return ErrConflict
				}
				if e = s.verifyNoCodexGrant(*current); e != nil {
					return e
				}
			} else if saved.AuthOwner == nil || saved.AuthOwner.Owner != l.owner {
				return ErrIdentityChanged
			}
		}
		raw, e := readPrivate(s.root, filepath.Join("saved", current.ID, "auth.json"), maxCredentialBytes)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if len(raw) > 0 {
			actualOwner, e := CodexGrantOwner(raw)
			if e != nil {
				return e
			}
			if actualOwner != owner {
				return ErrIdentityChanged
			}
		}
		previousGeneration := ""
		if len(raw) > 0 {
			previous, e := codexCredentialGeneration(raw)
			if e != nil {
				return e
			}
			previousGeneration = hex.EncodeToString(previous[:])
		}
		if intentErr == nil && intent.Version == 2 && previousGeneration != intent.PreviousGeneration && previousGeneration != intent.Generation {
			return ErrConflict
		}
		merged, e := mergeFields(raw, normalized.Auth, "auth_mode", "tokens", "last_refresh", "OPENAI_API_KEY")
		if e != nil {
			return e
		}
		generation, e := codexCredentialGeneration(merged)
		if e != nil {
			return e
		}
		target := savedCredential{Revision: current.CredentialRevision + 1, Identity: current.Identity, AuthOwner: &grantOwnership{Version: 1, Owner: owner}, LoginJobID: jobID}
		selected := r.Providers[ProviderCodex]
		missingBaseline := selected.ActiveID == current.ID && selected.DesiredCredentialRevision == 0
		if r.SchemaVersion < 3 || missingBaseline || selection != nil && r.SchemaVersion < 4 {
			if r.Revision >= ^uint64(0)-1 {
				return ErrIneligible
			}
			schemaForHandovers(&r)
			if selection != nil {
				r.SchemaVersion = 4
			}
			if missingBaseline {
				selected.DesiredCredentialRevision = current.CredentialRevision
				if intentErr == nil {
					selected.DesiredCredentialRevision = intent.FromRevision
				}
				r.Providers[ProviderCodex] = selected
			}
			r.Revision++
			if e = s.save(r); e != nil {
				return e
			}
		}
		if intentErr == nil {
			if intent.Target.Revision != target.Revision || intent.Target.AuthOwner.Owner != owner || intent.Generation != hex.EncodeToString(generation[:]) {
				return ErrConflict
			}
			if l.owner == "" && len(raw) > 0 {
				actual, e := codexCredentialGeneration(raw)
				if e != nil || hex.EncodeToString(actual[:]) != intent.Generation {
					return ErrConflict
				}
			}
		} else {
			intent = grantReloginIntent{Version: 1, LoginID: current.ID, FromRevision: current.CredentialRevision, PreviousOwner: l.owner, Generation: hex.EncodeToString(generation[:]), Target: target}
			if selection != nil {
				intent.Version = 2
				intent.Selection = selection
				intent.PreviousGeneration = previousGeneration
			}
			encoded, e := json.Marshal(intent)
			if e != nil {
				return e
			}
			if _, e = durableReplace(s.root, grantReloginPath(current.ID), encoded, s.writeOps); e != nil {
				return e
			}
		}
		l.reloginJobID = jobID
		if _, e = durableReplace(s.root, filepath.Join("saved", current.ID, "auth.json"), merged, s.writeOps); e != nil {
			return e
		}
		encoded, e := json.Marshal(target)
		if e != nil {
			return e
		}
		if _, e = durableReplace(s.root, s.savedPath(*current), encoded, s.writeOps); e != nil {
			return e
		}
		current.CredentialRevision = target.Revision
		current.LastLoginJobID = jobID
		current.AuthState = AuthReady
		current.Renewable = renewable
		current.ExpiresAt = expires
		current.LimitKnown = false
		current.LimitNonRolling = false
		current.ExhaustedUntil = time.Time{}
		if e = applyGrantSelection(&r, intent); e != nil {
			return e
		}
		r.Revision++
		if e = s.save(r); e != nil {
			return e
		}
		out = *current
		l.login = *current
		l.owner = owner
		if e = s.eraseGrantIntent(current.ID); e != nil {
			return e
		}
		l.reloginJobID = ""
		return nil
	})
	return r, out, e
}
