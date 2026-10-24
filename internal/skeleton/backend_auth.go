package skeleton

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
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
	selection   codexAuthSelection
	acquire     func() (func(), bool)
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
	access         func(context.Context, bool) (backendAuthTokens, error)
	current        func() bool
	bound          chan struct{}
	once           sync.Once
	conn           *appserver.Client
	requests       chan backendAuthRequest
	stopOnce       sync.Once
	initialized    chan struct{}
	initOnce       sync.Once
	onClosed       func()
	ctx            context.Context
	cancel         context.CancelFunc
	done, syncDone chan struct{}
	syncWake       chan struct{}
	synchronize    func(context.Context) error
	onTokenChange  func()
	revision       atomic.Uint64
	handover       *codexHandoverFeed
}

func newBackendAuthCoordinator(access func(context.Context, bool) (backendAuthTokens, error), current func() bool) *backendAuthCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	a := &backendAuthCoordinator{access: access, current: current, bound: make(chan struct{}), requests: make(chan backendAuthRequest, 8), ctx: ctx, cancel: cancel, done: make(chan struct{}), syncDone: make(chan struct{}), syncWake: make(chan struct{}, 1), initialized: make(chan struct{})}
	go a.run()
	go a.syncLoop()
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
	if valid && a.onTokenChange != nil {
		a.onTokenChange()
	}
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
	defer close(a.done)
	defer a.cancel()
	select {
	case <-a.bound:
	case <-a.ctx.Done():
		return
	}
	if a.conn == nil {
		return
	}
	base, stop := context.WithCancel(a.ctx)
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
				if err := a.respondToken(ctx, req.id, token); err != nil {
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
	if a.handover != nil {
		return a.handover.synchronize(ctx)
	}
	if a.access == nil || !a.current() {
		return errBackendAuthUnavailable
	}
	revision := a.revision.Load()
	token, err := a.access(ctx, false)
	if err != nil {
		return errBackendAuthUnavailable
	}
	return a.install(ctx, token, revision)
}
func (a *backendAuthCoordinator) install(ctx context.Context, token backendAuthTokens, revision uint64) error {
	params := struct {
		Type string `json:"type"`
		backendAuthNativeTokens
	}{"chatgptAuthTokens", token.native()}
	var result struct {
		Type string `json:"type"`
	}
	var release func()
	err := a.conn.CallAtWriteBoundary(ctx, "account/login/start", params, &result, func() error {
		if a.revision.Load() != revision {
			return errBackendAuthUnavailable
		}
		var ok bool
		release, ok = a.tokenGuard(ctx, token)
		if !ok {
			return errBackendAuthUnavailable
		}
		return nil
	}, func() {
		if release != nil {
			release()
			release = nil
		}
	})
	if release != nil {
		release()
	}
	if err != nil || result.Type != "chatgptAuthTokens" || !a.tokenCurrent(ctx, token) || a.revision.Load() != revision {
		return errBackendAuthUnavailable
	}
	return nil
}
func (a *backendAuthCoordinator) tokenCurrent(ctx context.Context, token backendAuthTokens) bool {
	return ctx.Err() == nil && a.ctx.Err() == nil && a.current() && token.AccessToken != "" && token.AccountID != "" && (token.current == nil || token.current())
}
func (a *backendAuthCoordinator) tokenGuard(ctx context.Context, token backendAuthTokens) (func(), bool) {
	var release func()
	if token.acquire != nil {
		var ok bool
		release, ok = token.acquire()
		if !ok {
			return nil, false
		}
	}
	if !a.tokenCurrent(ctx, token) {
		if release != nil {
			release()
		}
		return nil, false
	}
	return release, true
}
func (a *backendAuthCoordinator) respondToken(ctx context.Context, id json.RawMessage, token backendAuthTokens) error {
	var release func()
	err := a.conn.RespondAuthTokens(ctx, id, token.native(), func() bool {
		var ok bool
		release, ok = a.tokenGuard(ctx, token)
		if ok {
			a.revision.Add(1)
		}
		return ok
	})
	if release != nil {
		release()
	}
	if err == nil && a.onTokenChange != nil {
		a.onTokenChange()
	}
	return err
}
func (a *backendAuthCoordinator) requestSync() {
	select {
	case a.syncWake <- struct{}{}:
	default:
	}
}
func (a *backendAuthCoordinator) syncLoop() {
	defer close(a.syncDone)
	defer func() {
		<-a.done
		if a.onClosed != nil {
			a.onClosed()
		}
	}()
	select {
	case <-a.bound:
	case <-a.ctx.Done():
		return
	}
	if a.conn == nil {
		return
	}
	select {
	case <-a.initialized:
	case <-a.ctx.Done():
		return
	case <-a.conn.Done():
		return
	}
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.conn.Done():
			return
		case <-a.syncWake:
			if a.synchronize != nil {
				ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
				_ = a.synchronize(ctx)
				cancel()
			}
		}
	}
}
func (a *backendAuthCoordinator) close() {
	a.cancel()
	a.bind(nil)
	if a.conn != nil {
		_ = a.conn.Close()
	}
	<-a.done
	<-a.syncDone
}

func (a *backendAuthCoordinator) startSync() { a.initOnce.Do(func() { close(a.initialized) }) }
func (a *backendAuthCoordinator) connection() *appserver.Client {
	select {
	case <-a.bound:
		return a.conn
	default:
		return nil
	}
}
