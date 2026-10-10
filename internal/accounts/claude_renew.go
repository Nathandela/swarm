package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Native refresh protocol pinned to Claude Code 2.1.296.
const (
	claudeDefaultClientID  = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeRefreshLockStale = 60 * time.Second
	claudeMaxExpiresIn     = 30 * 24 * 3600
)

// ErrRefreshRejected is returned, possibly wrapped, by a ClaudeExchange for invalid_grant.
var ErrRefreshRejected = errors.New("claude refresh token rejected")

// ClaudeTokens is one refresh response. Expiries are in seconds.
type ClaudeTokens struct {
	AccessToken           string
	RefreshToken          string
	ExpiresIn             int64
	RefreshTokenExpiresIn int64
	Scopes                []string
	AccountUUID           string
	OrganizationUUID      string
}

func (ClaudeTokens) String() string   { return "claude oauth tokens [redacted]" }
func (ClaudeTokens) GoString() string { return "claude oauth tokens [redacted]" }

// ClaudeExchange performs the network refresh; accounts stays network-free.
type ClaudeExchange func(ctx context.Context, refreshToken, clientID string, scopes []string) (ClaudeTokens, error)

type RenewOutcome int

const (
	RenewRefreshed RenewOutcome = iota + 1 // swarm wrote new tokens
	RenewAdopted                           // a sibling already renewed; retry the read
	RenewBusy                              // the native lock is held fresh by someone else
	RenewDead                              // the refresh token is rejected and unchanged on disk
)

type claudeOAuth struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	Scopes       []string `json:"scopes"`
	ClientID     string   `json:"clientId"`
}

func (c UsageCredential) sameToken(token string) bool {
	return c.provider == ProviderClaude && c.token == token
}

// RenewClaudeCredential renews a managed Claude profile under both native refresh
// locks and a refresh-token CAS. s.mu is never held across the exchange.
func (s *Store) RenewClaudeCredential(ctx context.Context, b Binding, rejected UsageCredential, exchange ClaudeExchange) (RenewOutcome, error) {
	if s.readOnly || b.Provider != ProviderClaude || rejected.provider != ProviderClaude || exchange == nil {
		return 0, ErrIneligible
	}
	profile, _, _, err := s.readClaudeRenewal(b)
	if err != nil {
		return 0, err
	}
	for _, lock := range []string{filepath.Join(profile, ".oauth_refresh.lock"), profile + ".lock"} {
		held, err := s.lockClaudeRefresh(lock)
		if err != nil {
			return 0, err
		}
		if !held {
			return RenewBusy, nil
		}
		defer func() { _ = s.root.Remove(lock) }()
	}
	_, _, oauth, err := s.readClaudeRenewal(b)
	if err != nil {
		return 0, err
	}
	if !rejected.sameToken(oauth.AccessToken) {
		return RenewAdopted, nil
	}
	clientID := oauth.ClientID
	if clientID == "" {
		clientID = claudeDefaultClientID
	}
	tokens, err := exchange(ctx, oauth.RefreshToken, clientID, oauth.Scopes)
	if errors.Is(err, ErrRefreshRejected) {
		_, _, current, err := s.readClaudeRenewal(b)
		if err != nil {
			return 0, err
		}
		if current.RefreshToken != oauth.RefreshToken {
			return RenewAdopted, nil
		}
		return RenewDead, nil
	}
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !usageHeaderValue(tokens.AccessToken) || (tokens.RefreshToken != "" && !usageHeaderValue(tokens.RefreshToken)) || tokens.ExpiresIn <= 0 || tokens.ExpiresIn > claudeMaxExpiresIn {
		return 0, ErrInvalidCredentials
	}
	if tokens.AccountUUID != "" && tokens.OrganizationUUID != "" && identityDigest(ProviderClaude, tokens.AccountUUID+"\x00"+tokens.OrganizationUUID) != b.Identity {
		return 0, ErrIdentityChanged
	}
	return s.saveClaudeRenewal(b, oauth.RefreshToken, tokens)
}

// lockClaudeRefresh takes one proper-lockfile mkdir lock, replacing it once if stale.
func (s *Store) lockClaudeRefresh(name string) (bool, error) {
	err := s.root.Mkdir(name, 0o700)
	if errors.Is(err, os.ErrExist) {
		if info, statErr := s.root.Lstat(name); statErr == nil && info.IsDir() && time.Since(info.ModTime()) > claudeRefreshLockStale && s.root.Remove(name) == nil {
			err = s.root.Mkdir(name, 0o700)
		}
	}
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) readClaudeRenewal(b Binding) (string, []byte, claudeOAuth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readClaudeRenewalLocked(b)
}

func (s *Store) readClaudeRenewalLocked(b Binding) (string, []byte, claudeOAuth, error) {
	if err := s.safeAnchor(); err != nil {
		return "", nil, claudeOAuth{}, err
	}
	r, err := s.load()
	if err != nil {
		return "", nil, claudeOAuth{}, err
	}
	_, g, err := usageBindingRecord(r, b)
	if err != nil {
		return "", nil, claudeOAuth{}, err
	}
	profile := filepath.Join("profiles", g.ProfileGeneration)
	if err := s.claudeIdentityMatches(profile, b); err != nil {
		return "", nil, claudeOAuth{}, err
	}
	raw, err := readPrivate(s.root, filepath.Join(profile, ".credentials.json"), maxCredentialBytes)
	if err != nil {
		return "", nil, claudeOAuth{}, ErrUnsafePath
	}
	var file struct {
		OAuth claudeOAuth `json:"claudeAiOauth"`
	}
	if validateClaudeCredentials(raw) != nil || json.Unmarshal(raw, &file) != nil {
		return "", nil, claudeOAuth{}, ErrInvalidCredentials
	}
	return profile, raw, file.OAuth, nil
}

func (s *Store) claudeIdentityMatches(profile string, b Binding) error {
	raw, err := readPrivate(s.root, filepath.Join(profile, ".claude.json"), maxCredentialBytes)
	if err != nil {
		return ErrUnsafePath
	}
	if identity, err := ClaudeIdentity(raw); err != nil || identity != b.Identity {
		return ErrIdentityChanged
	}
	return nil
}

// saveClaudeRenewal writes only if the on-disk refresh token is still the posted one.
func (s *Store) saveClaudeRenewal(b Binding, posted string, tokens ClaudeTokens) (RenewOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, raw, oauth, err := s.readClaudeRenewalLocked(b)
	if err != nil {
		return 0, err
	}
	if oauth.RefreshToken != posted {
		return RenewAdopted, nil
	}
	merged, err := mergeClaudeTokens(raw, tokens, time.Now())
	if err != nil {
		return 0, err
	}
	if _, err := durableReplace(s.root, filepath.Join(profile, ".credentials.json"), merged, s.writeOps); err != nil {
		return 0, err
	}
	if err := s.claudeIdentityMatches(profile, b); err != nil {
		return 0, err
	}
	return RenewRefreshed, nil
}

// mergeClaudeTokens replaces only the renewed claudeAiOauth fields, keeping every other key.
func mergeClaudeTokens(raw []byte, tokens ClaudeTokens, now time.Time) ([]byte, error) {
	var file, oauth map[string]json.RawMessage
	if json.Unmarshal(raw, &file) != nil || json.Unmarshal(file["claudeAiOauth"], &oauth) != nil || oauth == nil {
		return nil, ErrInvalidCredentials
	}
	updates := map[string]any{"accessToken": tokens.AccessToken, "expiresAt": now.Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli()}
	if tokens.RefreshToken != "" {
		updates["refreshToken"] = tokens.RefreshToken
	}
	if tokens.RefreshTokenExpiresIn > 0 {
		updates["refreshTokenExpiresAt"] = now.Add(time.Duration(tokens.RefreshTokenExpiresIn) * time.Second).UnixMilli()
	}
	if len(tokens.Scopes) > 0 {
		updates["scopes"] = tokens.Scopes
	}
	for key, value := range updates {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, ErrInvalidCredentials
		}
		oauth[key] = encoded
	}
	encoded, err := json.Marshal(oauth)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	file["claudeAiOauth"] = encoded
	merged, err := json.Marshal(file)
	if err != nil || validateClaudeCredentials(merged) != nil {
		return nil, ErrInvalidCredentials
	}
	return merged, nil
}
