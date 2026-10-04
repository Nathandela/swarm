package accounts

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const candidateFile = ".swarm-candidate.json"

func identityDigest(provider, immutableID string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + immutableID))
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validVerification(provider, kind, identity, verification string) bool {
	if kind == KindClaudeToken {
		return provider == ProviderClaude && identity == "" && verification == VerificationUnverified
	}
	return validDigest(identity) && ((provider == ProviderCodex && verification == VerificationCodex) || (provider == ProviderClaude && verification == VerificationClaude))
}

// CodexIdentity reads only the immutable native account id. Ordinary bearer,
// refresh-token and last_refresh rewrites leave this identity unchanged.
func CodexIdentity(raw []byte) (string, error) {
	if len(raw) > maxCredentialBytes || validateJSON(raw) != nil {
		return "", ErrInvalidCredentials
	}
	var auth struct {
		AuthMode string `json:"auth_mode"`
		APIKey   string `json:"OPENAI_API_KEY"`
		Tokens   struct {
			AccountID    string `json:"account_id"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil || (auth.AuthMode != "chatgpt" && auth.AuthMode != "") || strings.TrimSpace(auth.Tokens.AccountID) == "" || len(auth.Tokens.AccountID) > 4096 {
		return "", ErrInvalidCredentials
	}
	if auth.AuthMode == "" && auth.APIKey != "" {
		return "", ErrInvalidCredentials
	}
	return identityDigest(ProviderCodex, auth.Tokens.AccountID), nil
}

// ClaudeIdentity characterizes the cached .claude.json oauthAccount fields in
// Claude Code 2.1.288. It is local cached identity, not proof of model access or
// of an unrelated opaque token's identity. Subscription scope includes org id.
func ClaudeIdentity(raw []byte) (string, error) {
	if len(raw) > maxCredentialBytes || validateJSON(raw) != nil {
		return "", ErrInvalidCredentials
	}
	var config struct {
		OAuthAccount struct {
			AccountUUID      string `json:"accountUuid"`
			OrganizationUUID string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(raw, &config) != nil || config.OAuthAccount.AccountUUID == "" || config.OAuthAccount.OrganizationUUID == "" || len(config.OAuthAccount.AccountUUID) > 4096 || len(config.OAuthAccount.OrganizationUUID) > 4096 {
		return "", ErrInvalidCredentials
	}
	return identityDigest(ProviderClaude, config.OAuthAccount.AccountUUID+"\x00"+config.OAuthAccount.OrganizationUUID), nil
}

func validateClaudeCredentials(raw []byte) error {
	var credentials struct {
		OAuth struct {
			AccessToken      string   `json:"accessToken"`
			RefreshToken     string   `json:"refreshToken"`
			Scopes           []string `json:"scopes"`
			SubscriptionType string   `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	if len(raw) > maxCredentialBytes || validateJSON(raw) != nil || json.Unmarshal(raw, &credentials) != nil {
		return ErrInvalidCredentials
	}
	oauth := credentials.OAuth
	if oauth.AccessToken == "" || oauth.RefreshToken == "" || (oauth.SubscriptionType != "pro" && oauth.SubscriptionType != "max") {
		return ErrInvalidCredentials
	}
	for _, scope := range oauth.Scopes {
		if scope == "user:inference" {
			return nil
		}
	}
	return ErrInvalidCredentials
}

func (s *Store) CreateCandidate(provider, kind string) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return Candidate{}, ErrIneligible
	}
	if err := s.safeAnchor(); err != nil {
		return Candidate{}, err
	}
	if !validProvider(provider) || !validCredentialKind(provider, kind) {
		return Candidate{}, ErrIneligible
	}
	id, err := newID()
	if err != nil {
		return Candidate{}, err
	}
	source := SourceNativeLogin
	if kind == KindClaudeToken {
		source = SourceManualToken
	}
	c := Candidate{ID: id, Provider: provider, Kind: kind, Source: source, ProfileGeneration: id, Verification: VerificationUnverified, CreatedAt: time.Now().UTC(), ProfilePath: filepath.Join(s.path, "profiles", id)}
	if err := s.root.Mkdir(filepath.Join("profiles", id), 0o700); err != nil {
		return Candidate{}, err
	}
	if err := s.saveCandidate(c); err != nil {
		return Candidate{}, err
	}
	if err := syncRootDir(s.root, "profiles"); err != nil {
		return Candidate{}, ErrDurabilityUncertain
	}
	return c, nil
}

func (s *Store) saveCandidate(c Candidate) error {
	if s.readOnly {
		return ErrIneligible
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = durableReplace(s.root, filepath.Join("profiles", c.ProfileGeneration, candidateFile), raw, writeOps{})
	return err
}

func (s *Store) candidate(c Candidate) (Candidate, error) {
	if !validID(c.ID) || c.ID != c.ProfileGeneration || !validProvider(c.Provider) || !validCredentialKind(c.Provider, c.Kind) {
		return Candidate{}, ErrIneligible
	}
	raw, err := readPrivate(s.root, filepath.Join("profiles", c.ProfileGeneration, candidateFile), 16<<10)
	if err != nil {
		return Candidate{}, ErrUnsafePath
	}
	var stored Candidate
	if validateJSON(raw) != nil || json.Unmarshal(raw, &stored) != nil || stored.ID != c.ID || stored.ProfileGeneration != c.ProfileGeneration || stored.Provider != c.Provider || stored.Kind != c.Kind {
		return Candidate{}, ErrUnsafePath
	}
	stored.ProfilePath = filepath.Join(s.path, "profiles", stored.ProfileGeneration)
	if stored.Source != SourceNativeLogin && stored.Source != SourceCachedImport && stored.Source != SourceManualToken {
		return Candidate{}, ErrInvalidCredentials
	}
	if (stored.Kind == KindClaudeToken) != (stored.Source == SourceManualToken) {
		return Candidate{}, ErrInvalidCredentials
	}
	return stored, nil
}

func (s *Store) VerifyCandidate(c Candidate) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return Candidate{}, err
	}
	return s.verifyCandidate(c)
}

func (s *Store) verifyCandidate(c Candidate) (Candidate, error) {
	stored, err := s.candidate(c)
	if err != nil {
		return Candidate{}, err
	}
	if stored.Kind == KindClaudeToken {
		if _, err := readPrivate(s.root, filepath.Join("vault", stored.ProfileGeneration), 32<<10); err != nil {
			return Candidate{}, ErrInvalidCredentials
		}
		if stored.TokenFingerprint == "" {
			return Candidate{}, ErrInvalidCredentials
		}
		return stored, nil
	}
	identity, err := s.profileIdentity(stored.Provider, stored.ProfileGeneration)
	if err != nil {
		return Candidate{}, err
	}
	if c.Identity != "" && c.Identity != identity {
		return Candidate{}, ErrIdentityChanged
	}
	stored.Identity = identity
	if stored.Source == SourceCachedImport {
		// Imported opaque credentials and local identity caches are independent.
		// Only an isolated native login can establish their shared provenance.
		stored.Verification = VerificationUnverified
	} else if stored.Provider == ProviderCodex {
		stored.Verification = VerificationCodex
	} else if stored.Source == SourceNativeLogin {
		stored.Verification = VerificationClaude
	} else {
		// The opaque OAuth credential does not authenticate the independently
		// copied cache's UUIDs. Re-verification cannot promote this provenance.
		stored.Verification = VerificationUnverified
	}
	if err := s.saveCandidate(stored); err != nil {
		return Candidate{}, err
	}
	return stored, nil
}

func (s *Store) profileIdentity(provider, profile string) (string, error) {
	base := filepath.Join("profiles", profile)
	if provider == ProviderCodex {
		raw, err := readPrivate(s.root, filepath.Join(base, "auth.json"), maxCredentialBytes)
		if err != nil {
			return "", ErrInvalidCredentials
		}
		if err := validateCodexCredentials(raw); err != nil {
			return "", err
		}
		return CodexIdentity(raw)
	}
	credentials, err := readPrivate(s.root, filepath.Join(base, ".credentials.json"), maxCredentialBytes)
	if err != nil || validateClaudeCredentials(credentials) != nil {
		return "", ErrInvalidCredentials
	}
	raw, err := readPrivate(s.root, filepath.Join(base, ".claude.json"), maxCredentialBytes)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	return ClaudeIdentity(raw)
}

func validateCodexCredentials(raw []byte) error {
	if _, err := CodexIdentity(raw); err != nil {
		return err
	}
	var auth struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.Tokens.AccessToken == "" || auth.Tokens.RefreshToken == "" {
		return ErrInvalidCredentials
	}
	return nil
}

func (s *Store) ImportCodex(path string) (Candidate, error) {
	raw, err := readImport(path)
	if err != nil {
		return Candidate{}, ErrUnsafePath
	}
	if err := validateCodexCredentials(raw); err != nil {
		return Candidate{}, err
	}
	c, err := s.CreateCandidate(ProviderCodex, KindNative)
	if err != nil {
		return Candidate{}, err
	}
	s.mu.Lock()
	c.Source = SourceCachedImport
	err = s.saveCandidate(c)
	s.mu.Unlock()
	if err != nil {
		_ = s.DiscardCandidate(c, true)
		return Candidate{}, err
	}
	if err := s.stage(c, "auth.json", raw); err != nil {
		_ = s.DiscardCandidate(c, true)
		return Candidate{}, err
	}
	return s.VerifyCandidate(c)
}

// ImportClaude copies only characterized credential/identity files. Effective
// settings, trust and history are independently projected by the caller.
func (s *Store) ImportClaude(credentialsPath, identityPath string) (Candidate, error) {
	credentials, err := readImport(credentialsPath)
	if err != nil {
		return Candidate{}, ErrUnsafePath
	}
	identity, err := readImport(identityPath)
	if err != nil {
		return Candidate{}, ErrUnsafePath
	}
	if validateClaudeCredentials(credentials) != nil {
		return Candidate{}, ErrInvalidCredentials
	}
	if _, err := ClaudeIdentity(identity); err != nil {
		return Candidate{}, err
	}
	// Carry only the characterized nonsecret identity fields. The original
	// global config can contain API keys, provider selectors and trust settings.
	var source struct {
		OAuthAccount struct {
			AccountUUID      string `json:"accountUuid"`
			OrganizationUUID string `json:"organizationUuid"`
			EmailAddress     string `json:"emailAddress,omitempty"`
			DisplayName      string `json:"displayName,omitempty"`
			FullName         string `json:"fullName,omitempty"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(identity, &source) != nil {
		return Candidate{}, ErrInvalidCredentials
	}
	identity, err = json.Marshal(source)
	if err != nil {
		return Candidate{}, ErrInvalidCredentials
	}
	c, err := s.CreateCandidate(ProviderClaude, KindNative)
	if err != nil {
		return Candidate{}, err
	}
	// Persist provenance before either independent native file is copied.
	s.mu.Lock()
	c.Source = SourceCachedImport
	err = s.saveCandidate(c)
	s.mu.Unlock()
	if err != nil {
		_ = s.DiscardCandidate(c, true)
		return Candidate{}, err
	}
	if err := s.stage(c, ".credentials.json", credentials); err != nil {
		_ = s.DiscardCandidate(c, true)
		return Candidate{}, err
	}
	if err := s.stage(c, ".claude.json", identity); err != nil {
		_ = s.DiscardCandidate(c, true)
		return Candidate{}, err
	}
	return s.VerifyCandidate(c)
}

func (s *Store) stage(c Candidate, name string, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return err
	}
	if _, err := s.candidate(c); err != nil {
		return err
	}
	_, err := durableReplace(s.root, filepath.Join("profiles", c.ProfileGeneration, name), raw, writeOps{})
	return err
}

func (s *Store) ImportClaudeToken(token string) (Candidate, error) {
	token = strings.TrimSpace(token)
	if len(token) == 0 || len(token) > 32<<10 || !utf8.ValidString(token) {
		return Candidate{}, ErrInvalidCredentials
	}
	for _, r := range token {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return Candidate{}, ErrInvalidCredentials
		}
	}
	c, err := s.CreateCandidate(ProviderClaude, KindClaudeToken)
	if err != nil {
		return Candidate{}, err
	}
	s.mu.Lock()
	err = s.withFileLock(func() error {
		key, err := readPrivate(s.root, ".fingerprint-key", 32)
		if errors.Is(err, os.ErrNotExist) {
			key = make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return err
			}
			if _, err := durableReplace(s.root, ".fingerprint-key", key, writeOps{}); err != nil {
				return err
			}
		} else if err != nil || len(key) != 32 {
			return ErrUnsafePath
		}
		fingerprint := hmac.New(sha256.New, key)
		_, _ = fingerprint.Write([]byte(token))
		c.TokenFingerprint = hex.EncodeToString(fingerprint.Sum(nil))
		if _, err := durableReplace(s.root, filepath.Join("vault", c.ProfileGeneration), []byte(token), writeOps{}); err != nil {
			return err
		}
		return s.saveCandidate(c)
	})
	s.mu.Unlock()
	if err != nil {
		_ = s.DiscardCandidate(c, true)
		return Candidate{}, err
	}
	return c, nil
}

// Admit is called only after the enrollment authority has stopped its native
// writers and confirmed native account/status under this selected directory.
func (s *Store) Admit(expected uint64, c Candidate, label string) (Registry, Account, error) {
	var admitted Account
	r, err := s.mutate(expected, func(r *Registry) error {
		if !validLabel(label) {
			return ErrIneligible
		}
		verified, err := s.verifyCandidate(c)
		if err != nil {
			return err
		}
		if !validVerification(verified.Provider, verified.Kind, verified.Identity, verified.Verification) || (verified.Kind == KindNative && verified.Source != SourceNativeLogin) {
			return ErrInvalidCredentials
		}
		for _, existing := range r.Accounts {
			for _, generation := range existing.Generations {
				if generation.ProfileGeneration == c.ProfileGeneration || (existing.Provider == verified.Provider && ((verified.Identity != "" && generation.Identity == verified.Identity) || (verified.TokenFingerprint != "" && generation.TokenFingerprint == verified.TokenFingerprint))) {
					admitted = existing
					return ErrDuplicate
				}
			}
		}
		id, err := newID()
		if err != nil {
			return err
		}
		admitted = Account{ID: id, Provider: verified.Provider, Label: label, Lifecycle: LifecycleEnabled, Auth: AuthValid, CurrentGeneration: 1, Generations: map[uint64]Generation{1: generationFromCandidate(verified, 1)}}
		r.Accounts[id] = admitted
		return nil
	})
	return r, admitted, err
}

func (s *Store) Reauthenticate(expected uint64, id string, c Candidate) (Registry, Account, error) {
	var promoted Account
	r, err := s.mutate(expected, func(r *Registry) error {
		account, ok := r.Accounts[id]
		if !ok || account.Lifecycle == LifecycleRetiring || account.CurrentGeneration == ^uint64(0) {
			return ErrIneligible
		}
		verified, err := s.verifyCandidate(c)
		if err != nil {
			return err
		}
		if !validVerification(verified.Provider, verified.Kind, verified.Identity, verified.Verification) || (verified.Kind == KindNative && verified.Source != SourceNativeLogin) {
			return ErrInvalidCredentials
		}
		current := account.Generations[account.CurrentGeneration]
		if verified.Provider != account.Provider || verified.Identity != current.Identity || verified.Kind != current.Kind {
			return ErrIdentityChanged
		}
		for _, existing := range r.Accounts {
			for _, gen := range existing.Generations {
				if gen.ProfileGeneration == verified.ProfileGeneration {
					return ErrDuplicate
				}
			}
		}
		account.CurrentGeneration++
		account.Generations[account.CurrentGeneration] = generationFromCandidate(verified, account.CurrentGeneration)
		account.Auth = AuthValid
		r.Accounts[id] = account
		promoted = account
		return nil
	})
	return r, promoted, err
}

// DiscardCandidate removes only an unadmitted private generation, after the
// caller has verified its enrollment writer's death. Admission wins a race.
func (s *Store) DiscardCandidate(c Candidate, writersStopped bool) error {
	if !writersStopped {
		return ErrInUse
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withFileLock(func() error {
		if !validID(c.ID) || c.ID != c.ProfileGeneration || !validProvider(c.Provider) || !validCredentialKind(c.Provider, c.Kind) {
			return ErrIneligible
		}
		r, err := s.load()
		if err != nil {
			return err
		}
		for _, a := range r.Accounts {
			for _, g := range a.Generations {
				if g.ProfileGeneration == c.ProfileGeneration {
					return ErrInUse
				}
			}
		}
		profile := filepath.Join("profiles", c.ProfileGeneration)
		if _, err := s.root.Lstat(profile); err == nil {
			if _, err := checkRelative(s.root, profile, true); err != nil {
				return ErrUnsafePath
			}
			if _, err := s.root.Lstat(filepath.Join(profile, candidateFile)); err == nil {
				if _, err := s.candidate(c); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return ErrUnsafePath
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrUnsafePath
		}
		vault := filepath.Join("vault", c.ProfileGeneration)
		if _, err := s.root.Lstat(vault); err == nil {
			if _, err := readPrivate(s.root, vault, 32<<10); err != nil {
				return ErrUnsafePath
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrUnsafePath
		}
		// OpenRoot confines recursive cleanup; a malicious symlink is removed as
		// a link by RemoveAll and never traversed outside the private anchor.
		if err := s.root.RemoveAll(filepath.Join("profiles", c.ProfileGeneration)); err != nil {
			return err
		}
		if err := s.root.Remove(filepath.Join("vault", c.ProfileGeneration)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncRootDir(s.root, "profiles"); err != nil {
			return ErrDurabilityUncertain
		}
		if err := syncRootDir(s.root, "vault"); err != nil {
			return ErrDurabilityUncertain
		}
		return nil
	})
}
