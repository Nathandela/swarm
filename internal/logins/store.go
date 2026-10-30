package logins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unicode"
)

type Store struct {
	mu        sync.Mutex
	root      *os.Root
	path      string
	anchor    os.FileInfo
	lock      *os.File
	readOnly  bool
	uncertain bool
	keychain  credentialKeychain
	writeOps  writeOps
}

func Open(stateRoot string) (*Store, error)         { return open(stateRoot, false) }
func OpenReadOnly(stateRoot string) (*Store, error) { return open(stateRoot, true) }

func open(stateRoot string, readOnly bool) (_ *Store, err error) {
	if checkAbsolute(stateRoot, true) != nil {
		return nil, ErrUnsafePath
	}
	info, err := os.Lstat(stateRoot)
	if err != nil {
		return nil, ErrUnsafePath
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
		return nil, ErrUnsafePath
	}
	state, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = state.Close() }()
	if !readOnly {
		if err = state.Mkdir("logins", 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	anchor, err := checkRelative(state, "logins", true)
	if err != nil {
		return nil, err
	}
	root, err := state.OpenRoot("logins")
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, path: filepath.Join(stateRoot, "logins"), anchor: anchor, readOnly: readOnly, keychain: nil}
	if runtime.GOOS == "darwin" {
		s.keychain = nativeKeychain{}
	}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	if !readOnly {
		for _, dir := range []string{"saved", "pending", "provider", "enrollment", "provider/claude", "provider/codex", "provider/claude/native", "provider/codex/native"} {
			if err = mkdirPrivate(root, dir, writeOps{}); err != nil {
				return nil, err
			}
		}
	}
	flags := os.O_RDONLY
	if !readOnly {
		flags = os.O_CREATE | os.O_RDWR
	}
	s.lock, err = root.OpenFile(".registry-lock", flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err = s.lock.Stat()
	if err != nil || !privateInfo(info, false) {
		return nil, ErrUnsafePath
	}
	if !readOnly {
		err = s.locked(func() error {
			_, e := s.load()
			if errors.Is(e, os.ErrNotExist) {
				return s.save(Registry{SchemaVersion: SchemaVersion, Providers: map[string]ProviderState{}, Switches: map[string]Switch{}})
			}
			return e
		})
		if err != nil {
			return nil, err
		}
		if err = syncRootDir(root, "."); err != nil {
			return nil, err
		}
		f, e := state.Open(".")
		if e != nil {
			return nil, e
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	if _, err = s.Snapshot(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) StateRoot() string { return filepath.Dir(s.path) }
func (s *Store) Home(provider string) string {
	if !validProvider(provider) {
		return ""
	}
	return filepath.Join(s.path, "provider", provider, "native")
}

// ProtectedHomes identifies native writers without exposing credentials. A
// pending switch retains both outgoing and incoming canonical Claude homes.
func (s *Store) ProtectedHomes(provider string) ([]string, error) {
	if !validProvider(provider) {
		return nil, ErrIneligible
	}
	r, e := s.Snapshot()
	if e != nil {
		return nil, e
	}
	homes := []string{s.Home(provider)}
	if provider == ProviderCodex {
		return homes, nil
	}
	ids := []string{r.Providers[provider].ActiveID}
	if op, ok := r.Switches[provider]; ok {
		ids = append(ids, op.FromID, op.ToID, op.NextID)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		l := find(&r, id)
		if l == nil || l.Provider != provider {
			return nil, ErrIneligible
		}
		seen[id] = true
		homes = append(homes, s.savedNativeHome(id))
	}
	return homes, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var e error
	if s.lock != nil {
		e = s.lock.Close()
		s.lock = nil
	}
	if s.root != nil {
		ee := s.root.Close()
		s.root = nil
		if e == nil {
			e = ee
		}
	}
	return e
}
func (s *Store) safeAnchor() error {
	if s.root == nil || s.lock == nil || checkAbsolute(s.path, true) != nil {
		return ErrUnsafePath
	}
	i, e := os.Lstat(s.path)
	if e != nil || !privateInfo(i, true) || !os.SameFile(i, s.anchor) {
		return ErrUnsafePath
	}
	return nil
}
func (s *Store) locked(fn func() error) error {
	if s.readOnly {
		return ErrIneligible
	}
	if s.uncertain {
		return ErrDurabilityUncertain
	}
	if e := s.safeAnchor(); e != nil {
		return e
	}
	i, e := checkRelative(s.root, ".registry-lock", false)
	if e != nil {
		return e
	}
	o, e := s.lock.Stat()
	if e != nil || !os.SameFile(i, o) {
		return ErrUnsafePath
	}
	if e = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer func() { _ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN) }()
	return fn()
}
func (s *Store) Snapshot() (Registry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.safeAnchor(); e != nil {
		return Registry{}, e
	}
	return s.load()
}
func (s *Store) load() (Registry, error) {
	var r Registry
	b, e := readPrivate(s.root, "registry.json", maxRegistryBytes)
	if e != nil {
		return r, e
	}
	if validateJSON(b) != nil || json.Unmarshal(b, &r) != nil || validateRegistry(r) != nil {
		return Registry{}, ErrIneligible
	}
	return r, nil
}
func (s *Store) save(r Registry) error {
	if validateRegistry(r) != nil {
		return ErrIneligible
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	if len(b) > maxRegistryBytes {
		return ErrIneligible
	}
	_, e = durableReplace(s.root, "registry.json", b, s.writeOps)
	if errors.Is(e, ErrDurabilityUncertain) {
		s.uncertain = true
	}
	return e
}
func (s *Store) mutate(expected uint64, fn func(*Registry) error) (Registry, error) {
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
		if r.Revision == ^uint64(0) {
			return ErrIneligible
		}
		if e = fn(&r); e != nil {
			return e
		}
		r.Revision++
		return s.save(r)
	})
	return r, e
}
func validProvider(p string) bool { return p == ProviderClaude || p == ProviderCodex }
func validLabel(s string) bool {
	if strings.TrimSpace(s) == "" || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func find(r *Registry, id string) *Login {
	for i := range r.Logins {
		if r.Logins[i].ID == id {
			return &r.Logins[i]
		}
	}
	return nil
}
func validateRegistry(r Registry) error {
	if r.SchemaVersion != SchemaVersion || r.Providers == nil || r.Switches == nil {
		return ErrIneligible
	}
	seen := map[string]bool{}
	for _, l := range r.Logins {
		if l.LimitNonRolling && (!l.LimitKnown || !l.ExhaustedUntil.IsZero()) {
			return ErrIneligible
		}
		if !validID(l.ID) || !validProvider(l.Provider) || !validLabel(l.Label) || !validDigest(l.Identity) || l.CredentialRevision == 0 || seen[l.ID] || (l.LastLoginJobID != "" && !validID(l.LastLoginJobID)) || (l.AuthState != AuthReady && l.AuthState != AuthNeedsLogin && l.AuthState != AuthPaused) {
			return ErrIneligible
		}
		seen[l.ID] = true
	}
	for p, st := range r.Providers {
		if !validProvider(p) || st.Provider != p {
			return ErrIneligible
		}
		if st.ActiveID != "" {
			l := find(&r, st.ActiveID)
			if l == nil || l.Provider != p || st.Epoch == 0 {
				return ErrIneligible
			}
		}
	}
	for p, op := range r.Switches {
		if op.LoginJobID != "" && !validID(op.LoginJobID) {
			return ErrIneligible
		}
		if op.Phase == "retry-stopping" {
			l := find(&r, op.NextID)
			if l == nil || l.Provider != p {
				return ErrIneligible
			}
		} else if op.NextID != "" {
			return ErrIneligible
		}
		if op.Deactivate {
			// Returning to native sign-in has no destination and resumes nothing.
			if op.ToID != "" || op.FromID == "" || op.Relogin || op.NextID != "" || op.LoginJobID != "" || len(op.AttemptedIDs) != 0 || op.Phase == "resuming" || op.Phase == "retry-stopping" {
				return ErrIneligible
			}
		} else if find(&r, op.ToID) == nil || find(&r, op.ToID).Provider != p {
			return ErrIneligible
		}
		if p != op.Provider || !validProvider(p) || !validID(op.ID) || op.ToEpoch == 0 || op.ToEpoch != op.FromEpoch+1 || op.Relogin != (op.FromID == op.ToID) {
			return ErrIneligible
		}
		if op.FromID != "" && (find(&r, op.FromID) == nil || find(&r, op.FromID).Provider != p) {
			return ErrIneligible
		}
		if !validPhase(op.Phase) {
			return ErrIneligible
		}
		current := r.Providers[p]
		if op.Phase == "committed" || op.Phase == "resuming" || op.Phase == "retry-stopping" {
			if current.ActiveID != op.ToID || current.Epoch != op.ToEpoch {
				return ErrIneligible
			}
		} else if current.ActiveID != op.FromID || current.Epoch != op.FromEpoch {
			return ErrIneligible
		}
		seenRows := map[string]bool{}
		for _, row := range op.Sessions {
			if row.SourceID == "" || (row.ConversationID == "" && !op.Deactivate) || rowRank(row.Phase) < 0 || seenRows[row.SourceID] || (row.ChildStopClaimed && row.ChildID == "") {
				return ErrIneligible
			}
			seenRows[row.SourceID] = true
		}
	}
	return nil
}

// SetMigrationPending is used by the offline importer before rewriting state.
func (s *Store) SetMigrationPending(expected uint64, pending bool) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error { r.MigrationPending = pending; return nil })
}
