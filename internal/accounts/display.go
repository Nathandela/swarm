package accounts

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DisplayIdentity returns sanitized, cached presentation fields. These fields
// never establish credential identity or subscription/model access.
func (s *Store) DisplayIdentity(binding Binding) (email, plan string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.safeAnchor(); err != nil {
		return
	}
	registry, err := s.load()
	if err != nil {
		return "", "", err
	}
	account, ok := registry.Accounts[binding.AccountID]
	generation, found := account.Generations[binding.CredentialGeneration]
	if !ok || !found || binding.SchemaVersion != SchemaVersion || account.Provider != binding.Provider || generation.Identity != binding.Identity {
		return "", "", ErrIneligible
	}
	return s.profileDisplay(binding.Provider, generation.Kind, generation.ProfileGeneration)
}

func (s *Store) CandidateDisplayIdentity(candidate Candidate) (email, plan string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.safeAnchor(); err != nil {
		return
	}
	stored, err := s.candidate(candidate)
	if err != nil {
		return "", "", err
	}
	return s.profileDisplay(stored.Provider, stored.Kind, stored.ProfileGeneration)
}

func displayField(value string) string {
	if len(value) > 256 || !utf8.ValidString(value) {
		return ""
	}
	for _, c := range value {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return ""
		}
	}
	return strings.TrimSpace(value)
}

func (s *Store) profileDisplay(provider, kind, profile string) (string, string, error) {
	if kind == KindClaudeToken {
		return "", "", nil
	}
	if provider == ProviderClaude {
		raw, err := readPrivate(s.root, filepath.Join("profiles", profile, ".claude.json"), maxCredentialBytes)
		if err != nil {
			return "", "", err
		}
		var config struct {
			OAuthAccount struct {
				Email string `json:"emailAddress"`
			} `json:"oauthAccount"`
		}
		if json.Unmarshal(raw, &config) != nil {
			return "", "", ErrInvalidCredentials
		}
		raw, err = readPrivate(s.root, filepath.Join("profiles", profile, ".credentials.json"), maxCredentialBytes)
		if err != nil {
			return "", "", err
		}
		var credentials struct {
			OAuth struct {
				Plan string `json:"subscriptionType"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(raw, &credentials) != nil {
			return "", "", ErrInvalidCredentials
		}
		plan := ""
		if credentials.OAuth.Plan == "pro" || credentials.OAuth.Plan == "max" {
			plan = credentials.OAuth.Plan
		}
		return displayField(config.OAuthAccount.Email), plan, nil
	}
	raw, err := readPrivate(s.root, filepath.Join("profiles", profile, "auth.json"), maxCredentialBytes)
	if err != nil {
		return "", "", err
	}
	var auth struct {
		Tokens struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return "", "", ErrInvalidCredentials
	}
	parts := strings.Split(auth.Tokens.IDToken, ".")
	if len(parts) != 3 || len(parts[1]) > 32<<10 {
		return "", "", nil
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", nil
	}
	var presentation struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(claims, &presentation) != nil {
		return "", "", nil
	}
	return displayField(presentation.Email), "", nil
}
