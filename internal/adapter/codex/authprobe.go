package codex

// The Codex half of the ADR-024 auth probe. Codex processes (the PTY TUI and the
// per-session `codex app-server`) load ~/.codex/auth.json once at startup and
// hold its tokens in memory; after a logout/login they fail every refresh with
// "Your access token could not be refreshed because you have since logged out or
// signed in to another account" until restarted. This file tells the core where
// that file lives and what identifies the account inside it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
)

// AuthCredentialsFile names codex's credentials file relative to the user home.
func (codexAdapter) AuthCredentialsFile() string {
	return filepath.Join(".codex", "auth.json")
}

// AuthIdentity derives the account identity from auth.json: auth_mode plus
// tokens.account_id (ChatGPT login), falling back to the raw API key (apikey
// mode). Codex rewrites auth.json with fresh tokens and a new last_refresh on
// every routine refresh, so the derivation deliberately reads NOTHING but the
// account fields -- a refresh yields the same identity, a re-login to another
// account yields a different one. The identity is a SHA-256 digest so it never
// carries a secret.
func (codexAdapter) AuthIdentity(raw []byte) (string, bool) {
	var f struct {
		AuthMode string `json:"auth_mode"`
		APIKey   string `json:"OPENAI_API_KEY"`
		Tokens   struct {
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", false
	}
	account := f.Tokens.AccountID
	if account == "" {
		account = f.APIKey
	}
	if account == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(f.AuthMode + "\x00" + account))
	return hex.EncodeToString(sum[:]), true
}
