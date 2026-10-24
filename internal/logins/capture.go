package logins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// captureCredential distinguishes native signout from corruption. Absent or
// explicitly cleared grants cannot overwrite the last good saved snapshot.
func (s *Store) captureCredential(l Login) (Credential, bool, error) {
	if l.Provider == ProviderClaude {
		if err := s.claudeAuthMutationAllowed(l.ID); err != nil {
			return Credential{}, false, err
		}
	}
	if _, e := checkRelative(s.root, filepath.Join("provider", l.Provider, "native"), true); e != nil {
		return Credential{}, false, e
	}
	if l.Provider == ProviderClaude {
		if _, e := checkRelative(s.root, filepath.Join("saved", l.ID), true); e != nil {
			return Credential{}, false, e
		}
	}
	c, e := s.nativeCredential(l.Provider, l.ID)
	missing := errors.Is(e, os.ErrNotExist)
	if e != nil && !missing {
		return Credential{}, false, e
	}
	if l.Provider == ProviderClaude {
		// Missing identity is not evidence of a missing grant: it may be a
		// damaged execution context, and must not hide a foreign account.
		if len(c.Identity) == 0 {
			return Credential{}, false, ErrInvalidCredentials
		}
		identity, identityErr := ClaudeIdentity(c.Identity)
		if identityErr == nil && identity != l.Identity {
			return Credential{}, false, ErrIdentityChanged
		}
		if identityErr != nil && !clearedClaudeIdentity(c.Identity) {
			return Credential{}, false, identityErr
		}
		if missing {
			return Credential{}, true, nil
		}
		cleared := clearedGrant(l.Provider, c.Auth)
		if identityErr != nil && !cleared {
			return Credential{}, false, identityErr
		}
		if cleared {
			return Credential{}, true, nil
		}
	} else {
		if missing {
			return Credential{}, true, nil
		}
		identity, identityErr := CodexIdentity(c.Auth)
		if identityErr == nil && identity != l.Identity {
			return Credential{}, false, ErrIdentityChanged
		}
		cleared := clearedGrant(l.Provider, c.Auth)
		if identityErr != nil && !cleared {
			return Credential{}, false, identityErr
		}
		if cleared {
			return Credential{}, true, nil
		}
	}
	c, identity, _, _, e := normalizeCredential(l.Provider, c)
	if e != nil {
		return Credential{}, false, e
	}
	if identity != l.Identity {
		return Credential{}, false, ErrIdentityChanged
	}
	return c, false, nil
}

func clearedClaudeIdentity(raw []byte) bool {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return false
	}
	var v struct {
		Account *struct {
			ID  string `json:"accountUuid"`
			Org string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	return validateJSON(raw) == nil && json.Unmarshal(raw, &v) == nil && (v.Account == nil || (v.Account.ID == "" && v.Account.Org == ""))
}
func clearedGrant(provider string, raw []byte) bool {
	var doc map[string]json.RawMessage
	if validateJSON(raw) != nil || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return false
	}
	if provider == ProviderClaude {
		var oauth *struct {
			Access  string `json:"accessToken"`
			Refresh string `json:"refreshToken"`
		}
		v, ok := doc["claudeAiOauth"]
		if !ok {
			return true
		}
		return json.Unmarshal(v, &oauth) == nil && (oauth == nil || (oauth.Access == "" && oauth.Refresh == ""))
	}
	var auth struct {
		Mode   string `json:"auth_mode"`
		Key    string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			ID      string `json:"account_id"`
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.Key != "" || (auth.Mode != "" && auth.Mode != "chatgpt") {
		return false
	}
	if auth.Tokens == nil {
		return true
	}
	// A nonempty account id must remain parseable even after grant removal.
	if auth.Tokens.ID != "" {
		if _, e := CodexIdentity(raw); e != nil {
			return false
		}
	}
	return auth.Tokens.Access == "" && auth.Tokens.Refresh == ""
}
