package push

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// messagingScope is the ONLY scope the assertion requests. A service account asserted
// with a broad scope is a credential sitting on a network-facing relay host that can do
// more than push; narrowing it costs nothing and bounds what a compromised relay gets.
const messagingScope = "https://www.googleapis.com/auth/firebase.messaging"

// jwtBearerGrant is the RFC 7523 grant type Google's token endpoint expects for a
// service-account assertion.
const jwtBearerGrant = "urn:ietf:params:oauth:grant-type:jwt-bearer"

// assertionValidity is how long the signed assertion claims to be good for. It is the
// assertion's lifetime, not the access token's — Google decides the latter and reports it
// in expires_in.
const assertionValidity = time.Hour

// tokenRefreshSkew refreshes the access token this long BEFORE it expires. A token used
// until the exact instant of expiry is a token that is in flight when it expires, so every
// clock difference between this host and Google's becomes an intermittent 401 that reads
// like a delivery bug.
const tokenRefreshSkew = time.Minute

// accessToken caches one OAuth bearer and the instant it stops being usable.
type accessToken struct {
	value     string
	expiresAt time.Time
}

// usableAt reports whether the cached token can still be used at now, allowing for the
// refresh skew.
func (t accessToken) usableAt(now time.Time) bool {
	return t.value != "" && now.Before(t.expiresAt.Add(-tokenRefreshSkew))
}

// fetchAccessToken performs one JWT-bearer exchange against the account's token_uri.
func (f *FCM) fetchAccessToken(ctx context.Context, now time.Time) (accessToken, error) {
	assertion, err := f.acct.SignJWT(messagingScope, now)
	if err != nil {
		return accessToken{}, err
	}
	form := url.Values{"grant_type": {jwtBearerGrant}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.acct.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return accessToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.http.Do(req)
	if err != nil {
		return accessToken{}, fmt.Errorf("push: OAuth exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return accessToken{}, fmt.Errorf("push: OAuth exchange: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Deliberately body-free: a token-endpoint error body can echo the assertion.
		return accessToken{}, fmt.Errorf("push: OAuth exchange refused with status %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return accessToken{}, fmt.Errorf("push: OAuth response: %w", err)
	}
	if tok.AccessToken == "" {
		return accessToken{}, fmt.Errorf("push: OAuth response carried no access_token")
	}
	return accessToken{value: tok.AccessToken, expiresAt: now.Add(time.Duration(tok.ExpiresIn) * time.Second)}, nil
}

// SignJWT builds the RS256 assertion the token endpoint verifies against the service
// account's registered public key, requesting exactly scope and nothing more.
//
// WHY IT TAKES THE SCOPE. Two APIs in this tree are asserted against with the same
// credential SHAPE and different scopes -- messaging here, androidpublisher in
// internal/play. Copying a signer to vary one string would put the credential crypto in
// two places, and a signing bug fixed in one copy is a signing bug still shipped in the
// other. The narrow-scope discipline messagingScope documents is preserved by each caller
// passing its own single scope, not by fixing one here.
func (a *ServiceAccount) SignJWT(scope string, now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if a.PrivateKeyID != "" {
		header["kid"] = a.PrivateKeyID
	}
	claims := map[string]any{
		"iss":   a.ClientEmail,
		"scope": scope,
		"aud":   a.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(assertionValidity).Unix(),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}
