// Package accountusage reads subscription usage without starting a native CLI,
// refreshing OAuth credentials or making a model request.
package accountusage

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
)

const maxBody = 1 << 20

type Result struct {
	Observations  []accounts.ScopeObservation
	Labels        map[string]string
	WindowSeconds map[string]int64
}

// Error carries only a fixed owner-facing message and a bounded retry delay.
// It deliberately does not wrap transport errors or retain provider bodies.
type Error struct {
	Class      string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	switch e.Class {
	case "auth-required":
		return "Usage authentication was refused; sign in again if retry still fails."
	case "rate-limited":
		return "Usage refresh was rate limited; wait before retrying."
	case "network":
		return "Usage refresh could not reach the provider; retry later."
	case "malformed":
		return "The provider returned an unsupported usage response."
	case "cancelled":
		return "Usage refresh was cancelled."
	case "login-expired":
		return "Sign-in expired for this account; sign in again."
	default:
		return "Usage refresh is unavailable for this account right now."
	}
}

// Fetch reads exactly one managed native credential snapshot. The caller owns
// scheduling, retirement references and fencing stale completions before merge.
func Fetch(ctx context.Context, store *accounts.Store, binding accounts.Binding) (Result, error) {
	// No ambient proxy, endpoint override, cookie jar or redirect can select an
	// alternate bearer destination. TLS uses the standard certificate verifier.
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return fetch(bounded, store, binding, client)
}

func fetch(ctx context.Context, store *accounts.Store, binding accounts.Binding, client *http.Client) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, &Error{Class: requestFailureClass(ctx)}
	}
	if store == nil {
		return Result{}, &Error{Class: "unavailable"}
	}
	credential, err := store.ReadUsageCredential(binding)
	if err != nil {
		return Result{}, credentialFailure(err)
	}
	endpoint := "https://chatgpt.com/backend-api/wham/usage"
	if binding.Provider == accounts.ProviderClaude {
		endpoint = "https://api.anthropic.com/api/oauth/usage"
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil || credential.Authorize(req) != nil {
			return Result{}, &Error{Class: "unavailable"}
		}
		response, err := client.Do(req)
		if err != nil {
			return Result{}, &Error{Class: requestFailureClass(ctx)}
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized && attempt == 0 && ctx.Err() == nil {
				// A live native CLI may have atomically renewed its bearer after
				// our snapshot. Retry once only when checked bytes changed.
				next, readErr := store.ReadUsageCredential(binding)
				if readErr != nil {
					return Result{}, credentialFailure(readErr)
				}
				if next != credential {
					credential = next
					continue
				}
				if binding.Provider == accounts.ProviderClaude {
					outcome, renewErr := store.RenewClaudeCredential(ctx, binding, credential, exchangeClaude(client))
					switch {
					case renewErr == nil && (outcome == accounts.RenewRefreshed || outcome == accounts.RenewAdopted):
						if credential, readErr = store.ReadUsageCredential(binding); readErr != nil {
							return Result{}, credentialFailure(readErr)
						}
						continue
					case renewErr == nil && outcome == accounts.RenewDead:
						return Result{}, &Error{Class: "login-expired"}
					case renewErr == nil && outcome == accounts.RenewBusy:
						return Result{}, &Error{Class: "unavailable"}
					}
					// Keep the token endpoint's own class and Retry-After.
					var safe *Error
					if errors.As(renewErr, &safe) {
						return Result{}, safe
					}
				}
			}
			class := "unavailable"
			switch response.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				class = "auth-required"
			case http.StatusTooManyRequests:
				class = "rate-limited"
			}
			return Result{}, &Error{Class: class, RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now())}
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
		_ = response.Body.Close()
		if err != nil {
			return Result{}, &Error{Class: requestFailureClass(ctx)}
		}
		if len(raw) > maxBody || uniqueJSON(raw) != nil {
			return Result{}, &Error{Class: "malformed"}
		}
		if binding.Provider == accounts.ProviderClaude {
			return parseClaude(raw)
		}
		return parseCodex(raw, credential)
	}
	return Result{}, &Error{Class: "unavailable"}
}

func credentialFailure(err error) *Error {
	if errors.Is(err, accounts.ErrInvalidCredentials) {
		return &Error{Class: "auth-required"}
	}
	return &Error{Class: "unavailable"}
}

func requestFailureClass(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	return "network"
}

func retryAfter(value string, now time.Time) time.Duration {
	const maximum = 24 * time.Hour
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds >= uint64(maximum/time.Second) {
			return maximum
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return min(when.Sub(now), maximum)
	}
	return 0
}
