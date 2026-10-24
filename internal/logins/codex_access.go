package logins

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AccessCredential lives only in memory. It cannot be encoded into metadata or
// protocol JSON; only the authenticated native connection receives Token.
type AccessCredential struct {
	Token     string    `json:"-"`
	AccountID string    `json:"-"`
	ExpiresAt time.Time `json:"-"`
}

// Format keeps accidental diagnostic formatting from revealing bearer tokens.
func (AccessCredential) Format(state fmt.State, verb rune) {
	_, _ = state.Write([]byte("[native access credential]"))
}

// CodexGrantOwner preserves the exact native user/workspace pair separately
// from CodexIdentity, whose legacy workspace digest remains stable.
func CodexGrantOwner(raw []byte) (string, error) {
	_, owner, err := codexAccessProof(raw)
	return owner, err
}

// CodexAccess checks native-produced token claims, not JWT signatures. Native
// OAuth remains the authority; these checks prevent stale/foreign distribution.
func CodexAccess(raw []byte, expectedOwner string, now time.Time) (AccessCredential, error) {
	access, owner, err := codexAccessProof(raw)
	if err != nil {
		return AccessCredential{}, err
	}
	if !validDigest(expectedOwner) || owner != expectedOwner {
		return AccessCredential{}, ErrIdentityChanged
	}
	// Native renewal may retain an expired ID token; only access authorizes requests.
	if !now.Before(access.ExpiresAt) {
		return AccessCredential{}, ErrInvalidCredentials
	}
	return access, nil
}

func codexAccessProof(raw []byte) (AccessCredential, string, error) {
	bad := func() (AccessCredential, string, error) {
		return AccessCredential{}, "", ErrInvalidCredentials
	}
	if _, err := CodexIdentity(raw); err != nil {
		return bad()
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return bad()
	}
	var tokens map[string]json.RawMessage
	if json.Unmarshal(doc["tokens"], &tokens) != nil {
		return bad()
	}
	var account, accessToken, idToken, refresh string
	if json.Unmarshal(tokens["account_id"], &account) != nil || json.Unmarshal(tokens["access_token"], &accessToken) != nil || json.Unmarshal(tokens["id_token"], &idToken) != nil || json.Unmarshal(tokens["refresh_token"], &refresh) != nil || refresh == "" {
		return bad()
	}
	accessOwner, accessAccount, accessExpiry, err := codexTokenClaims(accessToken)
	if err != nil {
		return bad()
	}
	idOwner, idAccount, _, err := codexTokenClaims(idToken)
	if err != nil || accessOwner != idOwner || accessAccount != idAccount || accessAccount != account {
		return bad()
	}
	return AccessCredential{Token: accessToken, AccountID: accessAccount, ExpiresAt: accessExpiry}, accessOwner, nil
}
func codexTokenClaims(token string) (string, string, time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return "", "", time.Time{}, ErrInvalidCredentials
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(raw) > maxCredentialBytes || validateJSON(raw) != nil {
		return "", "", time.Time{}, ErrInvalidCredentials
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(raw, &claims) != nil {
		return "", "", time.Time{}, ErrInvalidCredentials
	}
	var expiry int64
	var auth map[string]json.RawMessage
	if json.Unmarshal(claims["exp"], &expiry) != nil || json.Unmarshal(claims["https://api.openai.com/auth"], &auth) != nil {
		return "", "", time.Time{}, ErrInvalidCredentials
	}
	var user, account string
	if json.Unmarshal(auth["chatgpt_user_id"], &user) != nil || json.Unmarshal(auth["chatgpt_account_id"], &account) != nil || expiry <= 0 || strings.TrimSpace(user) == "" || strings.TrimSpace(account) == "" || len(user) > 4096 || len(account) > 4096 || strings.ContainsRune(user, 0) || strings.ContainsRune(account, 0) {
		return "", "", time.Time{}, ErrInvalidCredentials
	}
	return identityDigest(ProviderCodex, user+"\x00"+account), account, time.Unix(expiry, 0).UTC(), nil
}
