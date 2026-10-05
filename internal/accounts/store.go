package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

type Store struct {
	mu        sync.Mutex
	root      *os.Root
	path      string
	anchor    os.FileInfo
	lock      *os.File
	uncertain bool
	readOnly  bool
	writeOps  writeOps // fault injection only; production uses anchored native ops
}

// StateRoot identifies the owner's anchored Swarm state, never a credential.
func (s *Store) StateRoot() string { return filepath.Dir(s.path) }

// Open uses the existing owner-local state directory. Concurrent handles may
// read it; a native file lock serializes every registry CAS across handles.
func Open(stateRoot string) (*Store, error) {
	if err := checkAbsolute(stateRoot, true); err != nil {
		return nil, err
	}
	stateInfo, err := os.Lstat(stateRoot)
	if err != nil {
		return nil, ErrUnsafePath
	}
	stat, ok := stateInfo.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stateInfo.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("state root ownership: %w", ErrUnsafePath)
	}
	state, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = state.Close() }()
	if err := state.Mkdir("accounts", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	anchor, err := checkRelative(state, "accounts", true)
	if err != nil {
		return nil, fmt.Errorf("accounts anchor: %w", ErrUnsafePath)
	}
	root, err := state.OpenRoot("accounts")
	if err != nil {
		return nil, ErrUnsafePath
	}
	s := &Store{root: root, path: filepath.Join(stateRoot, "accounts"), anchor: anchor}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	for _, name := range []string{"profiles", "vault", "jobs", "history-transfer"} {
		if err = root.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if _, err = checkRelative(root, name, true); err != nil {
			return nil, fmt.Errorf("account subtree permissions: %w", ErrUnsafePath)
		}
	}
	s.lock, err = root.OpenFile(".registry-lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, ErrUnsafePath
	}
	info, err := s.lock.Stat()
	if err != nil || !privateInfo(info, false) {
		err = ErrUnsafePath
		return nil, fmt.Errorf("account lock permissions: %w", err)
	}
	if err = s.withFileLock(func() error {
		_, readErr := s.load()
		if errors.Is(readErr, os.ErrNotExist) {
			_, saveErr := s.save(Registry{SchemaVersion: SchemaVersion, Enabled: map[string]bool{ProviderCodex: false, ProviderClaude: false}, Accounts: map[string]Account{}})
			return saveErr
		}
		return readErr
	}); err != nil {
		return nil, fmt.Errorf("open account registry: %w", err)
	}
	if err = syncRootDir(root, "."); err != nil {
		return nil, err
	}
	parent, err := state.Open(".")
	if err != nil {
		return nil, err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return nil, err
	}
	return s, nil
}

// OpenReadOnly validates an existing registry without creating files,
// directories or acquiring the daemon's exclusive mutation lock.
func OpenReadOnly(stateRoot string) (*Store, error) {
	path := filepath.Join(stateRoot, "accounts")
	if err := checkAbsolute(path, true); err != nil {
		return nil, err
	}
	anchor, err := os.Lstat(path)
	if err != nil || !privateInfo(anchor, true) {
		return nil, ErrUnsafePath
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrUnsafePath
	}
	s := &Store{root: root, path: path, anchor: anchor, readOnly: true}
	s.lock, err = root.OpenFile(".registry-lock", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		_ = root.Close()
		return nil, ErrUnsafePath
	}
	info, err := s.lock.Stat()
	if err != nil || !privateInfo(info, false) {
		_ = s.Close()
		return nil, ErrUnsafePath
	}
	if _, err := s.Snapshot(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.lock != nil {
		err = s.lock.Close()
		s.lock = nil
	}
	if s.root != nil {
		closeErr := s.root.Close()
		s.root = nil
		if err == nil {
			err = closeErr
		}
	}
	return err
}

func (s *Store) safeAnchor() error {
	if s.root == nil || s.lock == nil {
		return ErrUnsafePath
	}
	if err := checkAbsolute(s.path, true); err != nil {
		return err
	}
	info, err := os.Lstat(s.path)
	if err != nil || !privateInfo(info, true) || !os.SameFile(info, s.anchor) {
		return ErrUnsafePath
	}
	return nil
}

func (s *Store) withFileLock(fn func() error) error {
	if s.readOnly {
		return ErrIneligible
	}
	if err := s.safeAnchor(); err != nil {
		return err
	}
	info, err := checkRelative(s.root, ".registry-lock", false)
	if err != nil {
		return ErrUnsafePath
	}
	opened, err := s.lock.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return ErrUnsafePath
	}
	if err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN) }()
	return fn()
}

func (s *Store) Snapshot() (Registry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return Registry{}, err
	}
	return s.load()
}

func (s *Store) load() (Registry, error) {
	var registry Registry
	raw, err := readPrivate(s.root, "registry.json", maxRegistryBytes)
	if err != nil {
		return registry, err
	}
	if err := validateJSON(raw); err != nil {
		return Registry{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return Registry{}, ErrUnsafePath
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Registry{}, ErrUnsafePath
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

func validateRegistry(r Registry) error {
	if r.SchemaVersion != SchemaVersion || r.Accounts == nil || r.Enabled == nil {
		return ErrUnsafePath
	}
	for provider := range r.Enabled {
		if !validProvider(provider) {
			return ErrUnsafePath
		}
	}
	profiles := make(map[string]bool)
	for id, account := range r.Accounts {
		if id != account.ID || !validID(id) || !validProvider(account.Provider) || !validLifecycle(account.Lifecycle) || (account.Auth != AuthValid && account.Auth != AuthNeedsLogin) || !validLabel(account.Label) || account.CurrentGeneration == 0 {
			return ErrUnsafePath
		}
		if _, ok := account.Generations[account.CurrentGeneration]; !ok {
			return ErrUnsafePath
		}
		for number, generation := range account.Generations {
			if generation.Kind == KindNative && generation.Source != SourceNativeLogin {
				return ErrInvalidCredentials
			}
			if number == 0 || number != generation.Number || number > account.CurrentGeneration || !validID(generation.ProfileGeneration) || profiles[generation.ProfileGeneration] || !validCredentialKind(account.Provider, generation.Kind) || !validVerification(account.Provider, generation.Kind, generation.Identity, generation.Verification) {
				return ErrUnsafePath
			}
			profiles[generation.ProfileGeneration] = true
		}
		if err := account.Quota.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) save(registry Registry) (bool, error) {
	if err := validateRegistry(registry); err != nil {
		return false, err
	}
	raw, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return false, err
	}
	visible, err := durableReplace(s.root, "registry.json", raw, s.writeOps)
	if visible && err != nil {
		s.uncertain = true
	}
	return visible, err
}

func (s *Store) mutate(expected uint64, change func(*Registry) error) (Registry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest Registry
	err := s.withFileLock(func() error {
		var err error
		latest, err = s.load()
		if err != nil {
			return err
		}
		if s.uncertain {
			return ErrDurabilityUncertain
		}
		if latest.Revision != expected {
			return ErrRevisionConflict
		}
		if latest.Revision == ^uint64(0) {
			return ErrIneligible
		}
		if err := change(&latest); err != nil {
			return err
		}
		latest.Revision++
		visible, err := s.save(latest)
		if err != nil && !visible {
			latest, _ = s.load()
		}
		return err
	})
	return latest, err
}

// Reconcile makes the visible document durable before allowing another CAS.
// A caller must reconcile its operation revision too, never repeat admission
// or another destructive action solely because its earlier write returned error.
func (s *Store) Reconcile() (Registry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var registry Registry
	err := s.withFileLock(func() error {
		var err error
		registry, err = s.load()
		if err != nil {
			return err
		}
		if err := syncRootDir(s.root, "."); err != nil {
			return ErrDurabilityUncertain
		}
		s.uncertain = false
		return nil
	})
	return registry, err
}

func validProvider(provider string) bool {
	return provider == ProviderCodex || provider == ProviderClaude
}
func validCredentialKind(provider, kind string) bool {
	return kind == KindNative || (provider == ProviderClaude && kind == KindClaudeToken)
}
func validLifecycle(state string) bool {
	return state == LifecycleEnabled || state == LifecyclePaused || state == LifecycleRetiring
}
func validLabel(label string) bool {
	if strings.TrimSpace(label) != label || len(label) == 0 || len(label) > 128 {
		return false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Store) SetEnabled(expected uint64, provider string, enabled bool) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error {
		if !validProvider(provider) {
			return ErrIneligible
		}
		r.Enabled[provider] = enabled
		return nil
	})
}

func (s *Store) SetLifecycle(expected uint64, id, lifecycle string) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error {
		account, ok := r.Accounts[id]
		if !ok || !validLifecycle(lifecycle) || (account.Lifecycle == LifecycleRetiring && lifecycle != LifecycleRetiring) {
			return ErrIneligible
		}
		account.Lifecycle = lifecycle
		r.Accounts[id] = account
		return nil
	})
}

func (s *Store) SetAuth(expected uint64, id, auth string) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error {
		account, ok := r.Accounts[id]
		if !ok || (auth != AuthValid && auth != AuthNeedsLogin) {
			return ErrIneligible
		}
		account.Auth = auth
		r.Accounts[id] = account
		return nil
	})
}

func (s *Store) SetLabel(expected uint64, id, label string) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error {
		account, ok := r.Accounts[id]
		if !ok || !validLabel(label) {
			return ErrIneligible
		}
		account.Label = label
		r.Accounts[id] = account
		return nil
	})
}

func generationFromCandidate(c Candidate, number uint64) Generation {
	return Generation{Number: number, ProfileGeneration: c.ProfileGeneration, Kind: c.Kind, Source: c.Source, Identity: c.Identity, Verification: c.Verification, TokenFingerprint: c.TokenFingerprint, CreatedAt: time.Now().UTC()}
}
