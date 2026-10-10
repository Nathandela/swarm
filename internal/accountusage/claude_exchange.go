package accountusage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
)

// Native refresh endpoint pinned to Claude Code 2.1.296.
const claudeTokenEndpoint = "https://platform.claude.com/v1/oauth/token"

// exchangeClaude posts one native refresh through the hardened usage client.
// Errors are fixed classes; provider bodies and tokens are never retained.
func exchangeClaude(client *http.Client) accounts.ClaudeExchange {
	return func(ctx context.Context, refreshToken, clientID string, scopes []string) (accounts.ClaudeTokens, error) {
		body, err := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refreshToken, "client_id": clientID, "scope": strings.Join(scopes, " ")})
		if err != nil {
			return accounts.ClaudeTokens{}, &Error{Class: "unavailable"}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeTokenEndpoint, bytes.NewReader(body))
		if err != nil {
			return accounts.ClaudeTokens{}, &Error{Class: "unavailable"}
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return accounts.ClaudeTokens{}, &Error{Class: requestFailureClass(ctx)}
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
		_ = response.Body.Close()
		if err != nil {
			return accounts.ClaudeTokens{}, &Error{Class: requestFailureClass(ctx)}
		}
		switch response.StatusCode {
		case http.StatusOK:
		case http.StatusBadRequest, http.StatusUnauthorized:
			var failure struct {
				Error string `json:"error"`
			}
			if len(raw) <= maxBody && uniqueJSON(raw) == nil && json.Unmarshal(raw, &failure) == nil && failure.Error == "invalid_grant" {
				return accounts.ClaudeTokens{}, accounts.ErrRefreshRejected
			}
			return accounts.ClaudeTokens{}, &Error{Class: "unavailable"}
		case http.StatusTooManyRequests:
			return accounts.ClaudeTokens{}, &Error{Class: "rate-limited", RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now())}
		default:
			return accounts.ClaudeTokens{}, &Error{Class: "unavailable"}
		}
		var payload struct {
			AccessToken           string `json:"access_token"`
			RefreshToken          string `json:"refresh_token"`
			ExpiresIn             int64  `json:"expires_in"`
			RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
			Scope                 string `json:"scope"`
			Account               struct {
				UUID string `json:"uuid"`
			} `json:"account"`
			Organization struct {
				UUID string `json:"uuid"`
			} `json:"organization"`
		}
		if len(raw) > maxBody || uniqueJSON(raw) != nil || json.Unmarshal(raw, &payload) != nil {
			return accounts.ClaudeTokens{}, &Error{Class: "malformed"}
		}
		tokens := accounts.ClaudeTokens{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, ExpiresIn: payload.ExpiresIn, RefreshTokenExpiresIn: payload.RefreshTokenExpiresIn, AccountUUID: payload.Account.UUID, OrganizationUUID: payload.Organization.UUID}
		if payload.Scope != "" {
			tokens.Scopes = strings.Fields(payload.Scope)
		}
		return tokens, nil
	}
}
