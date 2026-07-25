// Package push implements the FCM v1 sender behind relay.PushSink (PB-PUSH-2): it turns
// a generic alert plus an opaque ciphertext envelope into an HTTP/2 request Google's
// messaging API accepts, and turns Google's answers back into the two verdicts the relay
// acts on — retry, or prune this token.
//
// SCOPE HONESTY. This package models the FCM v1 PROTOCOL: the request emitted, the OAuth
// exchange performed, which failures are retried, and which response means a dead token.
// It models NOTHING about delivery. There is no Google account in this project, every
// test request goes to a loopback httptest.Server, and PB-E2E-5 — the physical-handset
// gate covering real delivery, real Doze behaviour and a real device — remains DEFERRED.
// Nothing here may be read as evidence that a handset would receive anything.
package push

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
)

// ServiceAccount is a parsed Google service-account credential: the fields the OAuth
// assertion is built from, plus the RSA key it is signed with.
type ServiceAccount struct {
	ProjectID    string
	ClientEmail  string
	TokenURI     string
	PrivateKeyID string

	key *rsa.PrivateKey
}

// errServiceAccount prefixes every load failure. PB-PUSH-5 requires the failure to be
// legible, not merely loud: an operator reading a relay's boot error must be able to tell
// WHICH credential is broken, and "invalid JSON" on a host that holds several would not.
var errServiceAccount = errors.New("push: service account")

// LoadServiceAccount parses and VALIDATES a service-account JSON document.
//
// It validates at load rather than at first send on purpose (PB-PUSH-5). A sender that
// constructs happily from a broken credential and fails on every push is a relay that
// looks healthy while push is dead, and the operator finds out from a user who missed a
// hand-off — hours later, with nothing in any log tying the two together.
func LoadServiceAccount(doc []byte) (*ServiceAccount, error) {
	var raw struct {
		Type         string `json:"type"`
		ProjectID    string `json:"project_id"`
		PrivateKeyID string `json:"private_key_id"`
		PrivateKey   string `json:"private_key"`
		ClientEmail  string `json:"client_email"`
		TokenURI     string `json:"token_uri"`
	}
	if err := json.Unmarshal(doc, &raw); err != nil {
		return nil, fmt.Errorf("%w: not a JSON document: %w", errServiceAccount, err)
	}
	for _, f := range []struct {
		name, value string
	}{
		{"project_id", raw.ProjectID},
		{"client_email", raw.ClientEmail},
		{"token_uri", raw.TokenURI},
		{"private_key", raw.PrivateKey},
	} {
		if f.value == "" {
			return nil, fmt.Errorf("%w: missing %s", errServiceAccount, f.name)
		}
	}
	key, err := parseRSAPrivateKey([]byte(raw.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("%w: private_key: %w", errServiceAccount, err)
	}
	return &ServiceAccount{
		ProjectID:    raw.ProjectID,
		ClientEmail:  raw.ClientEmail,
		TokenURI:     raw.TokenURI,
		PrivateKeyID: raw.PrivateKeyID,
		key:          key,
	}, nil
}

// parseRSAPrivateKey accepts the PKCS#8 form Google issues, and PKCS#1 for an operator who
// converted the key themselves.
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("not a PEM block")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is %T, want an RSA key", k)
		}
		return rk, nil
	}
	rk, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("not a PKCS#8 or PKCS#1 RSA key")
	}
	return rk, nil
}
