package skeleton

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/appserver"
)

const backendAuthRefreshMethod = "account/chatgptAuthTokens/refresh"

var errBackendAuthUnavailable = errors.New("backend_auth_unavailable")

// Access credentials live only on the authenticated app-server connection.
type backendAuthTokens struct {
	AccessToken string `json:"-"`
	AccountID   string `json:"-"`
	PlanType    string `json:"-"`
	current     func() bool
}

type backendAuthNativeTokens struct {
	AccessToken string `json:"accessToken"`
	AccountID   string `json:"chatgptAccountId"`
	PlanType    string `json:"chatgptPlanType,omitempty"`
}

func (t backendAuthTokens) native() backendAuthNativeTokens {
	return backendAuthNativeTokens{t.AccessToken, t.AccountID, t.PlanType}
}
func (backendAuthTokens) String() string { return "[redacted backend auth]" }
func (backendAuthTokens) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[redacted backend auth]")
}
func (backendAuthNativeTokens) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[redacted backend auth]")
}

type backendAuthRequest struct {
	id    json.RawMessage
	valid bool
}
type backendAuthCoordinator struct {
	access   func(context.Context, bool) (backendAuthTokens, error)
	current  func() bool
	bound    chan struct{}
	once     sync.Once
	conn     *appserver.Client
	requests chan backendAuthRequest
	stopOnce sync.Once
}

func newBackendAuthCoordinator(access func(context.Context, bool) (backendAuthTokens, error), current func() bool) *backendAuthCoordinator {
	a := &backendAuthCoordinator{access: access, current: current, bound: make(chan struct{}), requests: make(chan backendAuthRequest, 8)}
	go a.run()
	return a
}
func (a *backendAuthCoordinator) bind(conn *appserver.Client) {
	a.once.Do(func() { a.conn = conn; close(a.bound) })
}

// Consume even malformed auth envelopes before they can enter the generic event pump.
func (a *backendAuthCoordinator) intercept(method string, raw json.RawMessage) bool {
	if method != backendAuthRefreshMethod {
		return false
	}
	var fr struct {
		ID     json.RawMessage `json:"id"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &fr) != nil || !backendAuthRequestID(fr.ID) {
		return true
	}
	valid := validBackendAuthParams(fr.Params) && len(raw) <= 4096
	select {
	case a.requests <- backendAuthRequest{id: fr.ID, valid: valid}:
	default:
		a.stopOnce.Do(func() {
			go func() {
				<-a.bound
				if a.conn != nil {
					_ = a.conn.Close()
				}
			}()
		})
	}
	return true
}
func validBackendAuthParams(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		switch key {
		case "reason":
			var reason string
			if dec.Decode(&reason) != nil || reason != "unauthorized" {
				return false
			}
		case "previousAccountId":
			var account *string
			if dec.Decode(&account) != nil || (account != nil && len(*account) > 1024) {
				return false
			}
		default:
			return false
		}
	}
	if _, err = dec.Token(); err != nil {
		return false
	}
	var extra any
	return seen["reason"] && dec.Decode(&extra) == io.EOF
}

func backendAuthRequestID(id json.RawMessage) bool {
	if len(id) == 0 || len(id) > 256 || bytes.Equal(id, []byte("null")) {
		return false
	}
	var value any
	if json.Unmarshal(id, &value) != nil {
		return false
	}
	switch v := value.(type) {
	case string:
		return v != ""
	case float64:
		return true
	}
	return false
}
func (a *backendAuthCoordinator) run() {
	<-a.bound
	if a.conn == nil {
		return
	}
	base, stop := context.WithCancel(context.Background())
	defer stop()
	go func() {
		select {
		case <-a.conn.Done():
			stop()
		case <-base.Done():
		}
	}()
	for {
		if base.Err() != nil {
			return
		}
		select {
		case <-a.conn.Done():
			return
		case req := <-a.requests:
			ctx, cancel := context.WithTimeout(base, 30*time.Second)
			code := "backend_auth_invalid_request"
			var token backendAuthTokens
			if req.valid {
				code = "backend_auth_unavailable"
			}
			if req.valid && a.access != nil && a.current() {
				var err error
				token, err = a.access(ctx, true)
				code = "backend_auth_unavailable"
				if err == nil && ctx.Err() == nil && token.AccessToken != "" && token.AccountID != "" && a.current() && (token.current == nil || token.current()) {
					code = ""
				}
			}
			if code == "" {
				if err := a.conn.RespondAuthTokens(ctx, req.id, token.native(), func() bool { return ctx.Err() == nil && a.current() && (token.current == nil || token.current()) }); err != nil {
					if a.conn.RespondAuthError(ctx, req.id, "backend_auth_unavailable") != nil {
						_ = a.conn.Close()
					}
				}
			} else {
				if a.conn.RespondAuthError(ctx, req.id, code) != nil {
					_ = a.conn.Close()
				}
			}
			cancel()
		}
	}
}
func (a *backendAuthCoordinator) bootstrap(ctx context.Context) error {
	if a.access == nil || !a.current() {
		return errBackendAuthUnavailable
	}
	token, err := a.access(ctx, false)
	if err != nil || token.AccessToken == "" || token.AccountID == "" || !a.current() || (token.current != nil && !token.current()) {
		return errBackendAuthUnavailable
	}
	params := struct {
		Type string `json:"type"`
		backendAuthNativeTokens
	}{"chatgptAuthTokens", token.native()}
	var result struct {
		Type string `json:"type"`
	}
	if a.conn.CallAtWriteBoundary(ctx, "account/login/start", params, &result, func() error {
		if ctx.Err() != nil || !a.current() || (token.current != nil && !token.current()) {
			return errBackendAuthUnavailable
		}
		return nil
	}, nil) != nil || result.Type != "chatgptAuthTokens" || !a.current() || (token.current != nil && !token.current()) {
		return errBackendAuthUnavailable
	}
	return nil
}
