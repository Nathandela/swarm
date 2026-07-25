package push

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// FCM is the shipped relay.PushSink.
var _ relay.PushSink = (*FCM)(nil)

// DefaultFCMBaseURL is Google's messaging endpoint. Tests point BaseURL at a loopback
// httptest.Server instead; nothing in this package ever reaches Google.
const DefaultFCMBaseURL = "https://fcm.googleapis.com"

// DefaultMaxAttempts bounds how many times ONE push is sent before it is reported failed.
// A provider outage must end in an error rather than an unbounded loop: the relay calls
// this synchronously on its push path, so a sender that retried forever would hold the
// connection that triggered it.
const DefaultMaxAttempts = 3

// DefaultRetryDelay is the spacing a caller should ask for between retries. It is NOT the
// zero-value default: FCMConfig.RetryDelay of 0 means no delay at all, so a test can drive
// the retry path without putting a real sleep in the suite. What bounds a retry storm is
// DefaultMaxAttempts (and, above it, the relay's own per-target push quota), never the
// delay — so a caller that forgets this gets three back-to-back HTTP round trips, not a
// spin. cmd/swarm-relay passes it.
const DefaultRetryDelay = 500 * time.Millisecond

// defaultRequestTimeout bounds one HTTP round trip.
const defaultRequestTimeout = 10 * time.Second

// maxResponseBytes caps how much of a provider response is read. The relay is
// network-facing; an unbounded read is a memory-exhaustion lever handed to whatever
// answers the configured URL.
const maxResponseBytes = 1 << 20

// FCMConfig configures an FCM sender.
type FCMConfig struct {
	Account    *ServiceAccount // parsed credential; required
	BaseURL    string          // messaging endpoint (nil/"" => DefaultFCMBaseURL)
	Now        func() time.Time
	RetryDelay time.Duration // spacing between retries; <= 0 means none (see DefaultRetryDelay)
	HTTPClient *http.Client
}

// FCM sends wakes through Firebase Cloud Messaging v1.
type FCM struct {
	acct       *ServiceAccount
	baseURL    string
	now        func() time.Time
	retryDelay time.Duration
	http       *http.Client

	mu    sync.Mutex
	token accessToken
}

// NewFCM builds a sender over cfg, REFUSING an invalid credential rather than deferring
// the failure to the first wake (PB-PUSH-5). LoadServiceAccount does the parsing; this
// catches the assembly mistake — a nil or half-built account reaching the constructor.
func NewFCM(cfg FCMConfig) (*FCM, error) {
	if cfg.Account == nil || cfg.Account.key == nil {
		return nil, fmt.Errorf("%w: no parsed credential (load one with LoadServiceAccount)", errServiceAccount)
	}
	if cfg.Account.ProjectID == "" || cfg.Account.ClientEmail == "" || cfg.Account.TokenURI == "" {
		return nil, fmt.Errorf("%w: incomplete credential", errServiceAccount)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultFCMBaseURL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	delay := cfg.RetryDelay
	if delay < 0 {
		delay = 0
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultRequestTimeout}
	}
	return &FCM{acct: cfg.Account, baseURL: base, now: now, retryDelay: delay, http: client}, nil
}

// Push delivers one wake to token.
//
// It retries only what is worth retrying: a 5xx or a transport failure is the provider
// having a bad moment, while a 4xx is a request Google will refuse identically forever and
// retrying it is quota spent to reproduce a refusal. The one 4xx with a consequence is
// UNREGISTERED, which is reported as relay.ErrPushUnregistered so the relay can prune.
func (f *FCM) Push(ctx context.Context, token string, p relay.PushPayload) error {
	body, err := f.marshalMessage(token, p)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= DefaultMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 1 && f.retryDelay > 0 {
			t := time.NewTimer(f.retryDelay)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		retryable, err := f.send(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable {
			return err
		}
	}
	return fmt.Errorf("push: giving up after %d attempts: %w", DefaultMaxAttempts, lastErr)
}

// marshalMessage builds the FCM v1 request body.
//
// Three properties are load-bearing rather than stylistic:
//   - DATA-ONLY. A `notification` block is rendered by the system, on the lock screen,
//     from text the PROVIDER composed — precisely the rendering PB-PUSH-4 puts under the
//     app's control and PB-KEY-2 gates on authentication. Data-only also hands the message
//     to the app's own handler, which is what a content-free wake needs.
//   - android.priority HIGH. ADR-007 B16 makes high-priority FCM the sole background wake
//     path; a normal-priority message is deferred under Doze, which is the exact state the
//     wake exists to escape.
//   - the device token, never a topic. A topic is a fan-out channel anyone who learns its
//     name can subscribe to.
//
// The data block carries ONE key. Every additional key is metadata handed to the provider,
// and PB-PUSH-3 concedes only token, timing and size. p.Alert in particular is NEVER sent:
// it is a constant the app renders locally, and shipping it would be provider-visible text
// describing why the phone is being woken.
func (f *FCM) marshalMessage(token string, p relay.PushPayload) ([]byte, error) {
	return json.Marshal(map[string]any{
		"message": map[string]any{
			"token":   token,
			"android": map[string]any{"priority": "high"},
			"data":    map[string]any{"e": base64.StdEncoding.EncodeToString(p.Ciphertext)},
		},
	})
}

// send performs one attempt and reports whether the failure is worth retrying.
func (f *FCM) send(ctx context.Context, body []byte) (retryable bool, err error) {
	tok, err := f.accessTokenFor(ctx)
	if err != nil {
		// A refused/unreachable token endpoint is as transient as a refused send.
		return true, err
	}
	url := fmt.Sprintf("%s/v1/projects/%s/messages:send", f.baseURL, f.acct.ProjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return true, fmt.Errorf("push: send: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode == http.StatusOK {
		return false, nil
	}
	if readErr != nil {
		return resp.StatusCode >= 500, fmt.Errorf("push: send returned %d and its body was unreadable: %w", resp.StatusCode, readErr)
	}
	return classify(resp.StatusCode, respBody)
}

// classify turns one non-200 provider response into (retryable, error).
//
// UNREGISTERED is matched on the structured errorCode, NOT on the 404 status. Treating
// every 404 as a dead token would let one misconfigured project id prune every handset the
// relay holds — a self-inflicted total push outage that looks, from the relay's side,
// exactly like a fleet that quietly uninstalled.
func classify(status int, body []byte) (retryable bool, err error) {
	var envelope struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope) // a body we cannot parse simply yields no errorCode
	for _, d := range envelope.Error.Details {
		if d.ErrorCode == "UNREGISTERED" {
			return false, fmt.Errorf("push: send returned %d: %w", status, relay.ErrPushUnregistered)
		}
	}
	if status >= 500 {
		return true, fmt.Errorf("push: provider returned %d (%s)", status, envelope.Error.Status)
	}
	return false, fmt.Errorf("push: provider refused with %d (%s)", status, envelope.Error.Status)
}

// accessTokenFor returns a usable bearer, exchanging for a fresh one when the cached one
// is absent or within the refresh skew of expiry. An exchange per push would be a second
// round trip on every wake and a self-inflicted rate limit.
func (f *FCM) accessTokenFor(ctx context.Context) (string, error) {
	now := f.now()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token.usableAt(now) {
		return f.token.value, nil
	}
	tok, err := f.fetchAccessToken(ctx, now)
	if err != nil {
		return "", err
	}
	f.token = tok
	return tok.value, nil
}
