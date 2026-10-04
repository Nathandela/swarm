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

func quotaTestAccount(t *testing.T, m *accountManager, provider string) accounts.Account {
	t.Helper()
	c := accountTestCandidate(t, m, provider, "quota-"+provider)
	r := accountTestRegistry(t, m)
	_, a, err := m.store.Admit(r.Revision, c, provider)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func quotaWait(t *testing.T, m *accountManager, id, state string) protocol.AccountView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := m.Accounts(protocol.AccountsReq{Action: "list"})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range r.Accounts {
			if a.ID == id && a.QuotaFetchState == state {
				return a
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("quota state did not reach %s", state)
	return protocol.AccountView{}
}

func TestIdleQuotaFetchBothProvidersCoalescesAndDoesNotBlockList(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	codex := quotaTestAccount(t, m, accounts.ProviderCodex)
	claude := quotaTestAccount(t, m, accounts.ProviderClaude)
	started := make(chan string, 2)
	release := make(chan struct{})
	source := func(ctx context.Context, _ *accounts.Store, b accounts.Binding) (accountusage.Result, error) {
		started <- b.Provider
		select {
		case <-release:
		case <-ctx.Done():
			return accountusage.Result{}, ctx.Err()
		}
		used := 46
		return accountusage.Result{Observations: []accounts.ScopeObservation{{Scope: "bucket:" + b.Provider + ":primary", UsedPercent: &used}}}, nil
	}
	m.startQuotaFetchingWith(source)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("idle provider did not fetch")
		}
	}
	for _, a := range []accounts.Account{codex, claude} {
		view := quotaWait(t, m, a.ID, "loading")
		if !view.RefreshSupported {
			t.Fatal("idle account cannot refresh")
		}
		// Passive refresh must not depend on an old UI registry revision.
		if _, err := m.Accounts(protocol.AccountsReq{Action: "refresh", AccountID: a.ID}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	for _, a := range []accounts.Account{codex, claude} {
		view := quotaWait(t, m, a.ID, "ready")
		if len(view.Quota) != 1 || view.Quota[0].UsedPercent == nil || *view.Quota[0].UsedPercent != 46 {
			t.Fatal("idle account quota missing")
		}
	}
	select {
	case <-started:
		t.Fatal("duplicate refresh escaped coalescing")
	default:
	}
}

func TestQuotaRetirementCancelsAndShutdownJoinsRead(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	a := quotaTestAccount(t, m, accounts.ProviderClaude)
	started, stopped := make(chan struct{}), make(chan struct{})
	m.startQuotaFetchingWith(func(ctx context.Context, _ *accounts.Store, _ accounts.Binding) (accountusage.Result, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return accountusage.Result{}, ctx.Err()
	})
	<-started
	r := accountTestRegistry(t, m)
	if _, err := m.Accounts(protocol.AccountsReq{Action: "remove", ExpectedRevision: r.Revision, AccountID: a.ID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("retiring account left a read running")
	}
	m.close()
	store, err := accounts.OpenReadOnly(m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	snapshot, err := store.Snapshot()
	if err != nil || len(snapshot.Accounts[a.ID].Quota.Scopes) != 0 {
		t.Fatal("retired read published telemetry", err)
	}
}

func TestQuotaThrottleRetainsUsageAndManualCannotBypassRetry(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	a := quotaTestAccount(t, m, accounts.ProviderCodex)
	r := accountTestRegistry(t, m)
	b, err := m.store.CurrentBinding(a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	used := 32
	old := time.Now().Add(-10 * time.Minute)
	if _, err := m.store.RecordUsage(r.Revision, b, a.Quota, []accounts.ScopeObservation{{Scope: "bucket:codex:primary", UsedPercent: &used}}, old); err != nil {
		t.Fatal(err)
	}
	calls := make(chan struct{}, 3)
	m.startQuotaFetchingWith(func(context.Context, *accounts.Store, accounts.Binding) (accountusage.Result, error) {
		calls <- struct{}{}
		return accountusage.Result{}, &accountusage.Error{Class: "rate-limited", RetryAfter: 10 * time.Minute}
	})
	view := quotaWait(t, m, a.ID, "error")
	if view.QuotaFetchError == "" || view.QuotaNextRefreshAt == nil || view.QuotaNextRefreshAt.Before(time.Now().Add(9*time.Minute)) {
		t.Fatal("throttle retry not visible")
	}
	if len(view.Quota) != 1 || view.Quota[0].UsedPercent == nil || *view.Quota[0].UsedPercent != used || !view.Quota[0].ObservedAt.Equal(old) {
		t.Fatal("failed refresh lost or refreshed old usage")
	}
	for i := 0; i < 4; i++ {
		if _, err := m.Accounts(protocol.AccountsReq{Action: "refresh", AccountID: a.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 1 {
		t.Fatal("manual refresh bypassed retry delay")
	}
}

func TestQuotaRetirementWaitsForCopiedCredentialToDrain(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	a := quotaTestAccount(t, m, accounts.ProviderClaude)
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m.startQuotaFetchingWith(func(ctx context.Context, _ *accounts.Store, _ accounts.Binding) (accountusage.Result, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return accountusage.Result{}, ctx.Err()
	})
	<-started
	r := accountTestRegistry(t, m)
	if _, err := m.Accounts(protocol.AccountsReq{Action: "remove", ExpectedRevision: r.Revision, AccountID: a.ID}); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	b := accounts.Binding{SchemaVersion: accounts.SchemaVersion, Provider: a.Provider, AccountID: a.ID, CredentialGeneration: a.CurrentGeneration, Identity: a.Generations[a.CurrentGeneration].Identity, ConfigurationGeneration: 1}
	m.mu.Lock()
	_, err := (&accountRotationManager{}).retirementReferences(m, b, a.Generations[a.CurrentGeneration])
	m.mu.Unlock()
	if err != accounts.ErrInUse {
		t.Fatal("cancelled but unfinished read was considered drained", err)
	}
	close(release)
	m.close()
	if len(m.quota.active) != 0 {
		t.Fatal("finished quota read leaked custody")
	}
}

func TestQuotaReauthenticationFetchesImmediatelyDespiteFreshOldUsage(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	a := quotaTestAccount(t, m, accounts.ProviderCodex)
	calls := make(chan uint64, 4)
	m.startQuotaFetchingWith(func(_ context.Context, _ *accounts.Store, b accounts.Binding) (accountusage.Result, error) {
		calls <- b.CredentialGeneration
		used := int(b.CredentialGeneration)
		return accountusage.Result{Observations: []accounts.ScopeObservation{{Scope: "bucket:codex:primary", UsedPercent: &used}}}, nil
	})
	quotaWait(t, m, a.ID, "ready")
	c := accountTestCandidate(t, m, accounts.ProviderCodex, "quota-codex")
	r := accountTestRegistry(t, m)
	if _, _, err := m.store.Reauthenticate(r.Revision, a.ID, c); err != nil {
		t.Fatal(err)
	}
	// List observes the new generation and requests its own fetch immediately.
	if _, err := m.Accounts(protocol.AccountsReq{Action: "list"}); err != nil {
		t.Fatal(err)
	}
	view := quotaWait(t, m, a.ID, "ready")
	if view.CredentialGeneration != 2 || len(view.Quota) != 1 || *view.Quota[0].UsedPercent != 2 {
		t.Fatal("reauthentication reused old generation freshness")
	}
	if len(calls) != 2 {
		t.Fatal("reauthentication did not fetch exactly once")
	}
}
