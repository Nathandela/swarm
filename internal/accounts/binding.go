package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func (s *Store) CurrentBinding(id string, configurationGeneration uint64) (Binding, error) {
	r, err := s.Snapshot()
	if err != nil {
		return Binding{}, err
	}
	a, ok := r.Accounts[id]
	if !ok || a.Lifecycle != LifecycleEnabled || a.Auth != AuthValid || configurationGeneration == 0 {
		return Binding{}, ErrIneligible
	}
	g := a.Generations[a.CurrentGeneration]
	if g.CredentialErased || g.CredentialErasing {
		return Binding{}, ErrIneligible
	}
	b := Binding{SchemaVersion: SchemaVersion, Provider: a.Provider, AccountID: id, CredentialGeneration: g.Number, Identity: g.Identity, ConfigurationGeneration: configurationGeneration}
	if err := s.ValidateBinding(b); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func bindingRecord(r Registry, b Binding) (Account, Generation, error) {
	if b.SchemaVersion != SchemaVersion || !validProvider(b.Provider) || !validID(b.AccountID) || b.CredentialGeneration == 0 || b.ConfigurationGeneration == 0 {
		return Account{}, Generation{}, ErrIneligible
	}
	a, ok := r.Accounts[b.AccountID]
	if !ok || a.Provider != b.Provider || (a.Auth != AuthValid && b.CredentialGeneration == a.CurrentGeneration) {
		return Account{}, Generation{}, ErrIneligible
	}
	g, ok := a.Generations[b.CredentialGeneration]
	if !ok || g.CredentialErased || g.CredentialErasing || g.Identity != b.Identity || !validVerification(b.Provider, g.Kind, g.Identity, g.Verification) || (g.Kind == KindNative && g.Source != SourceNativeLogin) {
		return Account{}, Generation{}, ErrIneligible
	}
	return a, g, nil
}

func (s *Store) validateBinding(b Binding) (Generation, error) {
	r, err := s.load()
	if err != nil {
		return Generation{}, err
	}
	_, g, err := bindingRecord(r, b)
	if err != nil {
		return Generation{}, err
	}
	if _, err := checkRelative(s.root, filepath.Join("profiles", g.ProfileGeneration), true); err != nil {
		return Generation{}, ErrUnsafePath
	}
	if g.Kind == KindClaudeToken {
		if _, err := readPrivate(s.root, filepath.Join("vault", g.ProfileGeneration), 32<<10); err != nil {
			return Generation{}, ErrInvalidCredentials
		}
	} else {
		identity, err := s.profileIdentity(b.Provider, g.ProfileGeneration)
		if err != nil {
			return Generation{}, err
		}
		if identity != b.Identity {
			return Generation{}, ErrIdentityChanged
		}
	}
	return g, nil
}

func (s *Store) ValidateBinding(b Binding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return err
	}
	_, err := s.validateBinding(b)
	return err
}

func (s *Store) ProfilePath(b Binding) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return "", err
	}
	g, err := s.validateBinding(b)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.path, "profiles", g.ProfileGeneration), nil
}

// HistoryProfilePath resolves retained history only. It deliberately allows
// erased or invalid credentials; its result must never select a child process.
func (s *Store) HistoryProfilePath(b Binding) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return "", err
	}
	r, err := s.load()
	if err != nil {
		return "", err
	}
	a, ok := r.Accounts[b.AccountID]
	if !ok || b.SchemaVersion != SchemaVersion || a.Provider != b.Provider || b.CredentialGeneration == 0 {
		return "", ErrIneligible
	}
	g, ok := a.Generations[b.CredentialGeneration]
	if !ok || g.Identity != b.Identity {
		return "", ErrIneligible
	}
	if _, err := checkRelative(s.root, filepath.Join("profiles", g.ProfileGeneration), true); err != nil {
		return "", ErrUnsafePath
	}
	return filepath.Join(s.path, "profiles", g.ProfileGeneration), nil
}

// NativeIdentity observes exactly the frozen binding's profile. It never falls
// back to HOME or ambient credentials, and never returns a bearer fingerprint.
func (s *Store) NativeIdentity(b Binding) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return "", err
	}
	r, err := s.load()
	if err != nil {
		return "", err
	}
	a, ok := r.Accounts[b.AccountID]
	g, found := a.Generations[b.CredentialGeneration]
	if !ok || !found || b.SchemaVersion != SchemaVersion || !validID(b.AccountID) || b.ConfigurationGeneration == 0 || a.Provider != b.Provider || g.Identity != b.Identity || g.CredentialErased || g.CredentialErasing {
		return "", ErrIneligible
	}
	if g.Kind != KindNative {
		return "", ErrIneligible
	}
	return s.profileIdentity(b.Provider, g.ProfileGeneration)
}

// ScrubEnvironment removes alternate provider credentials and profile/host
// selectors. The caller's ordinary HOME and unrelated tool configuration stay
// intact. Effective native settings/projection validation remains the launch
// authority's responsibility; this helper does not claim to parse those files.
func ScrubEnvironment(inherited []string) ([]string, error) {
	out := make([]string, 0, len(inherited)+2)
	for _, value := range inherited {
		key, _, ok := strings.Cut(value, "=")
		if !ok || key == "" || strings.ContainsAny(value, "\x00\r\n") {
			return nil, ErrConfigurationConflict
		}
		if alternateAuthEnvironment(key) {
			continue
		}
		out = append(out, value)
	}
	return out, nil
}

func alternateAuthEnvironment(key string) bool {
	switch key {
	case "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_ORG_ID", "OPENAI_ORGANIZATION", "OPENAI_PROJECT", "CODEX_HOME", "CODEX_API_KEY", "CODEX_PROFILE", "CODEX_CLI_AUTH_CREDENTIALS_STORE", "CODEX_DISABLE_KEYRING",
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS", "AWS_BEARER_TOKEN_BEDROCK", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CLAUDE_CODE_ACCOUNT_UUID", "CLAUDE_CODE_API_KEY", "CLAUDE_CODE_API_KEY_HELPER_TTL_MS", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_HOST_CREDS_FILE", "CLAUDE_CODE_HOST_GATEWAY_LINEAGE", "CLAUDE_CODE_GATEWAY_URL", "CLAUDE_CODE_USE_GATEWAY", "CLAUDE_CODE_FEDERATION_PROFILE", "CLAUDE_CODE_PROFILE", "CLAUDE_CODE_DEFAULT_PROFILE":
		return true
	}
	return strings.HasPrefix(key, "CODEX_AUTH_") || strings.HasPrefix(key, "CODEX_REMOTE_") || strings.HasPrefix(key, "CLAUDE_CODE_HOST_") || strings.HasPrefix(key, "CLAUDE_CODE_GATEWAY_")
}

// ResolveEnvironment must run once before either child exec. Its output is
// child-only secret custody: reuse it for CLI and BackendEnv, never serialize
// it into launch metadata, saved environments, argv or diagnostics.
func (s *Store) ResolveEnvironment(b Binding, inherited []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return nil, err
	}
	g, err := s.validateBinding(b)
	if err != nil {
		return nil, err
	}
	out, err := ScrubEnvironment(inherited)
	if err != nil {
		return nil, err
	}
	profile := filepath.Join(s.path, "profiles", g.ProfileGeneration)
	if b.Provider == ProviderCodex {
		return append(out, "CODEX_HOME="+profile), nil
	}
	out = append(out, "CLAUDE_CONFIG_DIR="+profile, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+profile)
	if g.Kind == KindClaudeToken {
		token, err := readPrivate(s.root, filepath.Join("vault", g.ProfileGeneration), 32<<10)
		if err != nil {
			return nil, ErrInvalidCredentials
		}
		out = append(out, "CLAUDE_CODE_OAUTH_TOKEN="+string(token))
	}
	return out, nil
}

func ResolveBoundEnvironment(stateRoot string, binding Binding, inherited []string) ([]string, error) {
	s, err := OpenReadOnly(stateRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	return s.ResolveEnvironment(binding, inherited)
}

// EraseCredentials persists an embargo before deleting only the characterized
// credential inventory. Native history and all generation references survive.
// A crash leaves CredentialErasing set; retrying with the visible revision
// completes the same idempotent erasure after the authority renews its proof.
func (s *Store) EraseCredentials(expected uint64, id string, number uint64, proof ErasureProof) (Registry, error) {
	if !proof.WritersStopped || proof.LiveReferences != 0 {
		return Registry{}, ErrInUse
	}
	if !proof.CompleteInventory {
		return Registry{}, ErrIneligible
	}
	for _, file := range proof.CredentialFiles {
		if !filepath.IsLocal(file) || filepath.Clean(file) != file || file == "." || file == candidateFile || strings.Contains(file, string(filepath.Separator)) {
			return Registry{}, ErrUnsafePath
		}
	}
	r, err := s.mutate(expected, func(r *Registry) error {
		a, ok := r.Accounts[id]
		if !ok {
			return ErrIneligible
		}
		g, ok := a.Generations[number]
		if !ok || g.CredentialErased {
			return ErrIneligible
		}
		if g.Kind == KindNative {
			required := "auth.json"
			if a.Provider == ProviderClaude {
				required = ".credentials.json"
			}
			found := false
			for _, name := range proof.CredentialFiles {
				if name != required {
					return ErrIneligible
				}
				found = found || name == required
			}
			if !found {
				return ErrIneligible
			}
		} else if len(proof.CredentialFiles) != 0 {
			return ErrIneligible
		}
		g.CredentialErasing = true
		a.Generations[number] = g
		if number == a.CurrentGeneration {
			a.Auth = AuthNeedsLogin
		}
		r.Accounts[id] = a
		return nil
	})
	if err != nil {
		return r, err
	}
	s.mu.Lock()
	err = s.withFileLock(func() error {
		latest, err := s.load()
		if err != nil {
			return err
		}
		a, ok := latest.Accounts[id]
		if !ok {
			return ErrIneligible
		}
		g, ok := a.Generations[number]
		if !ok || !g.CredentialErasing {
			return ErrIneligible
		}
		base := filepath.Join("profiles", g.ProfileGeneration)
		for _, name := range proof.CredentialFiles {
			path := filepath.Join(base, name)
			if _, err := checkRelative(s.root, path, false); err != nil && !errors.Is(err, os.ErrNotExist) {
				return ErrUnsafePath
			}
			if err := s.root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if g.Kind == KindClaudeToken {
			if err := s.root.Remove(filepath.Join("vault", g.ProfileGeneration)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := syncRootDir(s.root, "vault"); err != nil {
				return ErrDurabilityUncertain
			}
		}
		return syncRootDir(s.root, base)
	})
	s.mu.Unlock()
	if err != nil {
		return r, err
	}
	return s.mutate(r.Revision, func(r *Registry) error {
		a := r.Accounts[id]
		g := a.Generations[number]
		if !g.CredentialErasing {
			return ErrIneligible
		}
		g.CredentialErasing = false
		g.CredentialErased = true
		a.Generations[number] = g
		r.Accounts[id] = a
		return nil
	})
}
