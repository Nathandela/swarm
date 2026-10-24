package skeleton

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestCodexAccessCoalescesAndKeepsOtherWaiter(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	b := testCodexAccessBroker(t)
	b.refresh = func(ctx context.Context, _ string, _ persist.CLIIdentity) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := b.tokens(ctx, persist.CLIIdentity{}, true); first <- err }()
	<-started
	second := make(chan error, 1)
	go func() { _, err := b.tokens(context.Background(), persist.CLIIdentity{}, true); second <- err }()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("native renewals = %d, want one", calls.Load())
	}
}

func TestCodexAccessRejectsObsoleteSelection(t *testing.T) {
	for _, changed := range []string{"login", "epoch", "credential", "paused", "ownership"} {
		t.Run(changed, func(t *testing.T) {
			b := testCodexAccessBroker(t)
			var mu sync.Mutex
			r, _ := b.snapshot()
			b.snapshot = func() (logins.Registry, error) { mu.Lock(); defer mu.Unlock(); return cloneAccessRegistry(r), nil }
			token, err := b.tokens(context.Background(), persist.CLIIdentity{}, false)
			if err != nil || !token.current() {
				t.Fatalf("initial token: %v", err)
			}
			mu.Lock()
			p := r.Providers[logins.ProviderCodex]
			switch changed {
			case "login":
				p.ActiveID = "other"
			case "epoch":
				p.Epoch++
			case "credential":
				r.Logins[0].CredentialRevision++
			case "paused":
				r.Logins[0].AuthState = logins.AuthPaused
			case "ownership":
				p.AuthOwnerMode = ""
			}
			r.Providers[logins.ProviderCodex] = p
			mu.Unlock()
			if token.current() {
				t.Fatal("obsolete credential remains writable")
			}
		})
	}
}

func TestCodexAccessRevisionChangeDoesNotInvalidateSelection(t *testing.T) {
	b := testCodexAccessBroker(t)
	r, _ := b.snapshot()
	b.snapshot = func() (logins.Registry, error) { return r, nil }
	token, err := b.tokens(context.Background(), persist.CLIIdentity{}, false)
	if err != nil {
		t.Fatal(err)
	}
	r.Revision++
	if !token.current() {
		t.Fatal("unrelated registry write invalidates credential")
	}
}

func TestCodexAccessWaitsForReloginSelectionIntent(t *testing.T) {
	b := testCodexAccessBroker(t)
	r, _ := b.snapshot()
	p := r.Providers[logins.ProviderCodex]
	p.DesiredCredentialRevision = 1
	r.Providers[logins.ProviderCodex] = p
	r.Logins[0].CredentialRevision = 2
	b.snapshot = func() (logins.Registry, error) { return r, nil }
	if _, err := b.tokens(context.Background(), persist.CLIIdentity{}, false); err == nil {
		t.Fatal("new re-login credentials were distributed under the previous desired revision")
	}
}

func TestCodexAccessRejectsZeroDesiredRevision(t *testing.T) {
	b := testCodexAccessBroker(t)
	r, _ := b.snapshot()
	p := r.Providers[logins.ProviderCodex]
	p.DesiredCredentialRevision = 0
	r.Providers[logins.ProviderCodex] = p
	b.snapshot = func() (logins.Registry, error) { return r, nil }
	if _, err := b.tokens(context.Background(), persist.CLIIdentity{}, false); err == nil {
		t.Fatal("unbootstrapped selection returned credentials")
	}
}

func TestCodexAccessPendingReloginRevokesOutstandingToken(t *testing.T) {
	b := testCodexAccessBroker(t)
	var pending atomic.Bool
	b.pending = func(string) error {
		if pending.Load() {
			return logins.ErrPending
		}
		return nil
	}
	token, err := b.tokens(context.Background(), persist.CLIIdentity{}, false)
	if err != nil {
		t.Fatal(err)
	}
	pending.Store(true)
	if token.current() {
		t.Fatal("credential stayed writable during relogin intent-to-registry gap")
	}
	if _, err := b.tokens(context.Background(), persist.CLIIdentity{}, false); err == nil {
		t.Fatal("pending relogin admitted a new read")
	}
}

func TestCodexAccessCloseCancelsRefreshWait(t *testing.T) {
	b := testCodexAccessBroker(t)
	started := make(chan struct{})
	b.refresh = func(ctx context.Context, _ string, _ persist.CLIIdentity) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	result := make(chan error, 1)
	go func() { _, err := b.tokens(context.Background(), persist.CLIIdentity{}, true); result <- err }()
	<-started
	b.close()
	if err := <-result; err == nil {
		t.Fatal("closed source returned credentials")
	}
	if _, err := b.tokens(context.Background(), persist.CLIIdentity{}, false); err == nil {
		t.Fatal("closed source admitted a new read")
	}
}

func TestCodexAccessExpiredCredentialRequiresSuccessfulRenewal(t *testing.T) {
	b := testCodexAccessBroker(t)
	var fresh atomic.Bool
	b.read = func(string) (logins.AccessCredential, error) {
		if !fresh.Load() {
			return logins.AccessCredential{}, logins.ErrInvalidCredentials
		}
		return logins.AccessCredential{Token: "synthetic", AccountID: "synthetic-account", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	b.refresh = func(context.Context, string, persist.CLIIdentity) error { fresh.Store(true); return nil }
	if _, err := b.tokens(context.Background(), persist.CLIIdentity{}, false); err != nil {
		t.Fatal(err)
	}
}

func TestCodexAccessExpiresAtNativeWriteBoundary(t *testing.T) {
	b := testCodexAccessBroker(t)
	expires := time.Now().Add(200 * time.Millisecond)
	b.read = func(string) (logins.AccessCredential, error) {
		return logins.AccessCredential{Token: "synthetic", AccountID: "synthetic-account", ExpiresAt: expires}, nil
	}
	token, err := b.tokens(context.Background(), persist.CLIIdentity{}, false)
	if err != nil || !token.current() {
		t.Fatalf("fresh token: %v", err)
	}
	<-time.After(time.Until(expires))
	if token.current() {
		t.Fatal("expired token remains eligible for native write")
	}
}

func cloneAccessRegistry(r logins.Registry) logins.Registry {
	r.Logins = append([]logins.Login(nil), r.Logins...)
	p := r.Providers[logins.ProviderCodex]
	r.Providers = map[string]logins.ProviderState{logins.ProviderCodex: p}
	return r
}

func testCodexAccessBroker(t *testing.T) *codexAccessBroker {
	t.Helper()
	b := newCodexAccessBroker(context.Background())
	b.pending = func(string) error { return nil }
	b.snapshot = func() (logins.Registry, error) {
		return logins.Registry{
			Providers: map[string]logins.ProviderState{logins.ProviderCodex: {Provider: logins.ProviderCodex, ActiveID: "saved", Epoch: 1, AuthOwnerMode: logins.CodexNativeGrantMode, DesiredCredentialRevision: 1}},
			Logins:    []logins.Login{{ID: "saved", Provider: logins.ProviderCodex, AuthState: logins.AuthReady, CredentialRevision: 1}},
		}, nil
	}
	b.read = func(string) (logins.AccessCredential, error) {
		return logins.AccessCredential{Token: "synthetic", AccountID: "synthetic-account", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	b.refresh = func(context.Context, string, persist.CLIIdentity) error { return nil }
	t.Cleanup(b.close)
	return b
}
