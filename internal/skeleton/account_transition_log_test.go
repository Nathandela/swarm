//go:build linux

package skeleton

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/accountusage"
	"github.com/Nathandela/swarm/internal/protocol"
)

type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureTransitionLog redirects the standard logger for the test.
func captureTransitionLog(t *testing.T) *lockedBuf {
	t.Helper()
	b := &lockedBuf{}
	oldOut := log.Writer()
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(oldOut) })
	return b
}

// refetch forces one more fetch of the account, bypassing the retry delay.
func refetch(m *accountManager, a accounts.Account, serial uint64) {
	m.mu.Lock()
	s := m.quota.items[a.ID]
	s.state, s.serial = "loading", serial
	m.mu.Unlock()
	m.quota.fetch(accountQuotaRequest{id: a.ID, generation: a.CurrentGeneration, serial: serial})
}

func countLines(out, sub string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "accounts:") && strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func TestQuotaFetch_LogsAuthTransitionAndFetchError(t *testing.T) {
	out := captureTransitionLog(t)
	m := accountTestManager(t, accountTestState(t))
	net := quotaTestAccount(t, m, accounts.ProviderCodex)
	exp := quotaTestAccount(t, m, accounts.ProviderClaude)
	m.startQuotaFetchingWith(func(_ context.Context, _ *accounts.Store, b accounts.Binding) (accountusage.Result, error) {
		if b.Provider == accounts.ProviderClaude {
			return accountusage.Result{}, &accountusage.Error{Class: "login-expired"}
		}
		return accountusage.Result{}, &accountusage.Error{Class: "network"}
	})
	quotaWait(t, m, net.ID, "error")
	for i := 0; i < 3000 && accountTestRegistry(t, m).Accounts[exp.ID].Auth != accounts.AuthNeedsLogin; i++ {
		time.Sleep(time.Millisecond)
	}
	text := out.String()
	if !strings.Contains(text, "accounts: auth "+exp.Label+": valid -> needs-login (login-expired)") {
		t.Fatalf("auth transition not logged:\n%s", text)
	}
	if !strings.Contains(text, "accounts: usage fetch "+net.Label+": network") {
		t.Fatalf("fetch error not logged:\n%s", text)
	}
}

func TestQuotaFetch_RepeatedErrorLogsOnce(t *testing.T) {
	out := captureTransitionLog(t)
	m := accountTestManager(t, accountTestState(t))
	a := quotaTestAccount(t, m, accounts.ProviderCodex)
	var mu sync.Mutex
	class := "network"
	m.startQuotaFetchingWith(func(context.Context, *accounts.Store, accounts.Binding) (accountusage.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		if class == "" {
			used := 10
			return accountusage.Result{Observations: []accounts.ScopeObservation{{Scope: "bucket:codex:primary", UsedPercent: &used}}}, nil
		}
		return accountusage.Result{}, &accountusage.Error{Class: class}
	})
	setClass := func(c string) { mu.Lock(); class = c; mu.Unlock() }
	quotaWait(t, m, a.ID, "error")
	refetch(m, a, 101)
	refetch(m, a, 102)
	if n := countLines(out.String(), "usage fetch "+a.Label); n != 1 {
		t.Fatalf("repeated error logged %d lines, want 1:\n%s", n, out.String())
	}
	setClass("rate-limited")
	refetch(m, a, 103)
	if n := countLines(out.String(), "usage fetch "+a.Label); n != 2 {
		t.Fatalf("changed error not logged (%d lines):\n%s", n, out.String())
	}
	setClass("")
	refetch(m, a, 104)
	if !strings.Contains(out.String(), "usage fetch "+a.Label+": recovered") {
		t.Fatalf("recovery not logged:\n%s", out.String())
	}
}

func TestRotationToggle_IsLogged(t *testing.T) {
	out := captureTransitionLog(t)
	m := accountTestManager(t, accountTestState(t))
	r := accountTestRegistry(t, m)
	if _, err := m.Accounts(protocol.AccountsReq{Action: "enable", Provider: accounts.ProviderClaude, Enabled: true, ExpectedRevision: r.Revision}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "accounts: rotation claude: false -> true") {
		t.Fatalf("toggle not logged:\n%s", out.String())
	}
}
