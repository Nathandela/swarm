//go:build linux

package skeleton

import (
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/accountusage"
	"github.com/Nathandela/swarm/internal/protocol"
)

func quotaWaitAccountState(t *testing.T, m *accountManager, id, state string) protocol.AccountView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		r, err := m.Accounts(protocol.AccountsReq{Action: "list"})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range r.Accounts {
			if a.ID == id {
				last = a.State
				if a.State == state {
					return a
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("account state did not reach %s (last %q)", state, last)
	return protocol.AccountView{}
}

func loginExpired() error {
	return &accountusage.Error{Class: "login-expired"}
}

func TestQuotaLoginExpiredMarksNeedsLoginKeepsUsageAndStopsFetching(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	a := quotaTestAccount(t, m, accounts.ProviderClaude)
	r := accountTestRegistry(t, m)
	b, err := m.store.CurrentBinding(a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	used, old := 41, time.Now().Add(-20*time.Minute)
	if _, err := m.store.RecordUsage(r.Revision, b, a.Quota, []accounts.ScopeObservation{{Scope: "bucket:claude:five_hour", UsedPercent: &used}}, old); err != nil {
		t.Fatal(err)
	}
	calls := make(chan struct{}, 8)
	m.startQuotaFetchingWith(func(context.Context, *accounts.Store, accounts.Binding) (accountusage.Result, error) {
		calls <- struct{}{}
		return accountusage.Result{}, loginExpired()
	})
	view := quotaWaitAccountState(t, m, a.ID, accounts.AuthNeedsLogin)
	if len(view.Quota) != 1 || view.Quota[0].UsedPercent == nil || *view.Quota[0].UsedPercent != used || !view.Quota[0].ObservedAt.Equal(old) {
		t.Fatal("login-expired fetch lost the last quota reading")
	}
	if snap := accountTestRegistry(t, m); snap.Accounts[a.ID].Auth != accounts.AuthNeedsLogin || snap.Accounts[a.ID].CurrentGeneration != 1 {
		t.Fatal("login-expired fetch did not persist needs-login on the current generation")
	}
	// Neither a manual refresh nor a later scheduler tick may fetch again.
	_, _ = m.Accounts(protocol.AccountsReq{Action: "refresh", AccountID: a.ID})
	m.mu.Lock()
	if snap, err := m.store.Snapshot(); err == nil {
		m.quota.scheduleLocked(snap, time.Now().Add(2*time.Hour))
	}
	m.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	if len(calls) != 1 {
		t.Fatal("needs-login account kept fetching", len(calls))
	}
}

func TestQuotaLoginExpiredDoesNotOverwriteReauthenticationDuringFetch(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	a := quotaTestAccount(t, m, accounts.ProviderClaude)
	started, release := make(chan struct{}), make(chan struct{})
	m.startQuotaFetchingWith(func(ctx context.Context, _ *accounts.Store, b accounts.Binding) (accountusage.Result, error) {
		if b.CredentialGeneration == 1 {
			close(started)
			<-release
			return accountusage.Result{}, loginExpired()
		}
		used := 7
		return accountusage.Result{Observations: []accounts.ScopeObservation{{Scope: "bucket:claude:five_hour", UsedPercent: &used}}}, nil
	})
	<-started
	// Reauthenticate behind the in-flight fetch without letting list or the
	// scheduler observe the new generation, so only the generation fence can
	// stop the stale login-expired result.
	c := accountTestCandidate(t, m, accounts.ProviderClaude, "quota-claude")
	r := accountTestRegistry(t, m)
	if _, _, err := m.store.Reauthenticate(r.Revision, a.ID, c); err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		s := m.quota.items[a.ID]
		done := s == nil || s.state != "loading"
		m.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("in-flight fetch did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	snap := accountTestRegistry(t, m)
	if snap.Accounts[a.ID].Auth != accounts.AuthValid || snap.Accounts[a.ID].CurrentGeneration != 2 {
		t.Fatal("stale login-expired result overwrote the reauthenticated generation")
	}
	view := quotaWait(t, m, a.ID, "ready")
	if view.State == accounts.AuthNeedsLogin || view.CredentialGeneration != 2 {
		t.Fatal("reauthenticated account shown as needs-login", view.State)
	}
}

func TestQuotaNeedsLoginAccountFetchesAgainAfterReauthenticate(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	a := quotaTestAccount(t, m, accounts.ProviderClaude)
	calls := make(chan uint64, 8)
	m.startQuotaFetchingWith(func(_ context.Context, _ *accounts.Store, b accounts.Binding) (accountusage.Result, error) {
		calls <- b.CredentialGeneration
		if b.CredentialGeneration == 1 {
			return accountusage.Result{}, loginExpired()
		}
		used := 12
		return accountusage.Result{Observations: []accounts.ScopeObservation{{Scope: "bucket:claude:five_hour", UsedPercent: &used}}}, nil
	})
	quotaWaitAccountState(t, m, a.ID, accounts.AuthNeedsLogin)
	c := accountTestCandidate(t, m, accounts.ProviderClaude, "quota-claude")
	r := accountTestRegistry(t, m)
	if _, _, err := m.store.Reauthenticate(r.Revision, a.ID, c); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Accounts(protocol.AccountsReq{Action: "list"}); err != nil {
		t.Fatal(err)
	}
	view := quotaWait(t, m, a.ID, "ready")
	if view.State == accounts.AuthNeedsLogin || view.CredentialGeneration != 2 || len(view.Quota) != 1 || view.Quota[0].UsedPercent == nil || *view.Quota[0].UsedPercent != 12 {
		t.Fatal("reauthenticated account did not fetch fresh quota", view.State)
	}
	if len(calls) != 2 {
		t.Fatal("expected one fetch per generation", len(calls))
	}
}
