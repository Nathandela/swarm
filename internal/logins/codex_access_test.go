package logins

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func grantJWT(user, account string, expiry int64) string {
	b, _ := json.Marshal(map[string]any{"exp": expiry, "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": user, "chatgpt_account_id": account}})
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".signature"
}
func grantAuth(user, account string, expiry int64) []byte {
	token := grantJWT(user, account, expiry)
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": account, "access_token": token, "id_token": token, "refresh_token": "never-return-this"}})
	return b
}
func TestCodexAccessIdentityExpiryAndSecretBoundary(t *testing.T) {
	now := time.Unix(2000000000, 0)
	raw := grantAuth("user-a", "workspace", now.Add(time.Hour).Unix())
	owner, err := CodexGrantOwner(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CodexAccess(raw, owner, now)
	if err != nil || got.AccountID != "workspace" || got.Token == "" || !got.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("access proof failed: %v", err)
	}
	other, _ := CodexGrantOwner(grantAuth("user-b", "workspace", now.Add(time.Hour).Unix()))
	if owner == other {
		t.Fatal("same workspace collapsed distinct users")
	}
	if _, err = CodexAccess(raw, other, now); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("foreign owner: %v", err)
	}
	if _, err = CodexAccess(raw, owner, now.Add(time.Hour)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expired: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v %s", got, got, got), got.Token) {
		t.Fatal("diagnostics leaked token")
	}
	// No token may be serialized into durable metadata or protocol output.
	b, err := json.Marshal(got)
	if err == nil && string(b) != "{}" {
		t.Fatalf("access credential serializable: %s", b)
	}
}
func TestCodexAccessRejectsMismatchedOrMalformedClaims(t *testing.T) {
	now := time.Unix(2000000000, 0)
	for _, field := range []string{"access_token", "id_token", "account_id"} {
		var auth map[string]any
		_ = json.Unmarshal(grantAuth("user-a", "workspace", now.Add(time.Hour).Unix()), &auth)
		tokens := auth["tokens"].(map[string]any)
		tokens[field] = grantJWT("user-b", "other-workspace", now.Add(time.Hour).Unix())
		raw, _ := json.Marshal(auth)
		if _, err := CodexGrantOwner(raw); err == nil {
			t.Fatalf("accepted mismatched %s", field)
		}
	}
	for _, token := range []string{"opaque", "a.b.c", grantJWT("", "workspace", now.Unix()), grantJWT("user-a", "", now.Unix())} {
		b, _ := json.Marshal(map[string]any{"tokens": map[string]string{"account_id": "workspace", "access_token": token, "id_token": token, "refresh_token": "r"}})
		if _, err := CodexGrantOwner(b); err == nil {
			t.Fatal("accepted malformed identity")
		}
	}
}

func TestCodexClaimsExactKeysAndTupleBoundary(t *testing.T) {
	raw := []byte(`{"exp":1,"EXP":4102444800,"https://api.openai.com/auth":{"chatgpt_user_id":"u","chatgpt_account_id":"a"}}`)
	token := "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
	_, _, expiry, err := codexTokenClaims(token)
	if err != nil || expiry.Unix() != 1 {
		t.Fatal("case variant overrode expiration")
	}
	if _, err := CodexGrantOwner(grantAuth("a\x00b", "c", 4102444800)); err == nil {
		t.Fatal("accepted tuple delimiter in identity")
	}
}
func TestCodexAccessUsesExactNativeTokenKeys(t *testing.T) {
	now := time.Unix(2000000000, 0)
	var doc map[string]any
	_ = json.Unmarshal(grantAuth("u", "a", now.Add(-time.Hour).Unix()), &doc)
	doc["tokens"].(map[string]any)["ACCESS_TOKEN"] = grantJWT("u", "a", now.Add(time.Hour).Unix())
	raw, _ := json.Marshal(doc)
	owner, e := CodexGrantOwner(raw)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = CodexAccess(raw, owner, now); !errors.Is(e, ErrInvalidCredentials) {
		t.Fatal("case alias replaced the native expired access token")
	}
}
