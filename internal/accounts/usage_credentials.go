package accounts

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
)

// UsageCredential is a short-lived, read-only snapshot of one managed native
// login. It has no token accessor and must never be persisted.
type UsageCredential struct {
	provider  string
	token     string
	accountID string
}

func (UsageCredential) String() string   { return "managed usage credential [redacted]" }
func (UsageCredential) GoString() string { return "managed usage credential [redacted]" }

// Authorize attaches this snapshot only to the pinned provider's usage GET.
// It cannot authorize an arbitrary host, native refresh or model request.
func (c UsageCredential) Authorize(req *http.Request) error {
	if req == nil || req.URL == nil || req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Opaque != "" || req.URL.RawQuery != "" || req.URL.Fragment != "" || req.URL.RawPath != "" || (req.Body != nil && req.Body != http.NoBody) || (req.Host != "" && req.Host != req.URL.Host) || !usageHeaderValue(c.token) {
		return ErrIneligible
	}
	switch c.provider {
	case ProviderCodex:
		if req.URL.Host != "chatgpt.com" || req.URL.Path != "/backend-api/wham/usage" || !usageHeaderValue(c.accountID) {
			return ErrIneligible
		}
	case ProviderClaude:
		if req.URL.Host != "api.anthropic.com" || req.URL.Path != "/api/oauth/usage" {
			return ErrIneligible
		}
	default:
		return ErrIneligible
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if c.provider == ProviderCodex {
		req.Header.Del("x-openai-codex-luna-reserve")
		req.Header.Set("ChatGPT-Account-Id", c.accountID)
		req.Header.Set("User-Agent", "codex-cli")
	} else {
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	}
	return nil
}

// MatchesAccountID checks optional Codex response identity without exposing it.
func (c UsageCredential) MatchesAccountID(id string) bool {
	return c.provider == ProviderCodex && c.accountID != "" && c.accountID == id
}

func usageHeaderValue(value string) bool {
	if value == "" || len(value) > 32<<10 {
		return false
	}
	for _, ch := range value {
		if ch < 0x21 || ch > 0x7e {
			return false
		}
	}
	return true
}

// ReadUsageCredential uses only the exact current, verified native profile.
// Paused accounts can be inspected; retirement, erasure and old generations
// cannot acquire another credential snapshot. Nothing is refreshed or written.
func (s *Store) ReadUsageCredential(b Binding) (UsageCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return UsageCredential{}, err
	}
	r, err := s.load()
	if err != nil {
		return UsageCredential{}, err
	}
	a, g, err := usageBindingRecord(r, b)
	if err != nil {
		return UsageCredential{}, err
	}
	profile := filepath.Join("profiles", g.ProfileGeneration)
	c := UsageCredential{provider: a.Provider}
	if b.Provider == ProviderCodex {
		raw, err := readPrivate(s.root, filepath.Join(profile, "auth.json"), maxCredentialBytes)
		if err != nil {
			return UsageCredential{}, ErrUnsafePath
		}
		// Identity and bearer come from the same checked bytes, never a reopen.
		identity, err := CodexIdentity(raw)
		if err != nil || validateCodexCredentials(raw) != nil {
			return UsageCredential{}, ErrInvalidCredentials
		}
		if identity != b.Identity {
			return UsageCredential{}, ErrIdentityChanged
		}
		var auth struct {
			Tokens struct {
				AccessToken string `json:"access_token"`
				AccountID   string `json:"account_id"`
			} `json:"tokens"`
		}
		if json.Unmarshal(raw, &auth) != nil {
			return UsageCredential{}, ErrInvalidCredentials
		}
		c.token, c.accountID = auth.Tokens.AccessToken, auth.Tokens.AccountID
	} else {
		identityRaw, err := readPrivate(s.root, filepath.Join(profile, ".claude.json"), maxCredentialBytes)
		if err != nil {
			return UsageCredential{}, ErrUnsafePath
		}
		identity, err := ClaudeIdentity(identityRaw)
		if err != nil || identity != b.Identity {
			return UsageCredential{}, ErrIdentityChanged
		}
		raw, err := readPrivate(s.root, filepath.Join(profile, ".credentials.json"), maxCredentialBytes)
		if err != nil {
			return UsageCredential{}, ErrUnsafePath
		}
		if validateClaudeCredentials(raw) != nil {
			return UsageCredential{}, ErrInvalidCredentials
		}
		var credentials struct {
			OAuth struct {
				AccessToken string `json:"accessToken"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(raw, &credentials) != nil {
			return UsageCredential{}, ErrInvalidCredentials
		}
		c.token = credentials.OAuth.AccessToken
		// Native token refresh may rewrite the credential file. Cached identity
		// is checked again to refuse an observed concurrent account change.
		identityRaw, err = readPrivate(s.root, filepath.Join(profile, ".claude.json"), maxCredentialBytes)
		if err != nil {
			return UsageCredential{}, ErrUnsafePath
		}
		if identity, err = ClaudeIdentity(identityRaw); err != nil || identity != b.Identity {
			return UsageCredential{}, ErrIdentityChanged
		}
	}
	if !usageHeaderValue(c.token) || (c.provider == ProviderCodex && (!usageHeaderValue(c.accountID) || strings.TrimSpace(c.accountID) != c.accountID)) {
		return UsageCredential{}, ErrInvalidCredentials
	}
	// Another Store handle can change lifecycle/generation while bytes are read.
	r, err = s.load()
	if err != nil {
		return UsageCredential{}, err
	}
	if _, _, err = usageBindingRecord(r, b); err != nil {
		return UsageCredential{}, err
	}
	return c, nil
}

func usageBindingRecord(r Registry, b Binding) (Account, Generation, error) {
	a, g, err := bindingRecord(r, b)
	if err != nil || a.Auth != AuthValid || a.CurrentGeneration != b.CredentialGeneration || (a.Lifecycle != LifecycleEnabled && a.Lifecycle != LifecyclePaused) || g.Kind != KindNative || g.Source != SourceNativeLogin {
		return Account{}, Generation{}, ErrIneligible
	}
	return a, g, nil
}
