package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestAccountsQuotaStatesRetainObservedUsage(t *testing.T) {
	used := 42
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name, state, err string
		observed         time.Time
		reset            *time.Time
		want             []string
	}{
		{"ready", "ready", "", time.Now(), &future, []string{"5h: 42% used", "58% remaining", "resets", "last observed"}},
		{"loading", "loading", "", time.Now(), &future, []string{"42% used", "refreshing"}},
		{"failure", "error", "Provider unavailable", time.Now(), &future, []string{"42% used", "Quota refresh failed", "Provider unavailable", "r to retry"}},
		{"old", "ready", "", time.Now().Add(-6 * time.Minute), &future, []string{"42% used", "stale"}},
		{"reset passed", "ready", "", time.Now(), &past, []string{"42% used", "stale", "reset passed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := protocol.AccountView{RefreshSupported: true, QuotaFetchState: tc.state, QuotaFetchError: tc.err, Quota: []protocol.AccountQuotaView{{Label: "five_hour", UsedPercent: &used, ObservedAt: tc.observed, ResetAt: tc.reset}}}
			text := strings.Join(accountQuotaLines(a), "\n")
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q: %s", want, text)
				}
			}
		})
	}
}

func TestAccountsQuotaUnknownAndOlderDaemon(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  string
	}{
		{"", "not yet observed"}, {"loading", "fetching"}, {"ready", "provider reported no usage windows"}, {"unsupported", "unavailable for this credential"}, {"error", "Quota refresh failed"},
	} {
		text := strings.Join(accountQuotaLines(protocol.AccountView{QuotaFetchState: tc.state}), "\n")
		if !strings.Contains(text, "unknown") || !strings.Contains(text, tc.want) || strings.Contains(text, "0%") {
			t.Fatalf("%s: %s", tc.state, text)
		}
	}
	var older protocol.AccountView
	if err := json.Unmarshal([]byte(`{"id":"old","quota":[{"label":"weekly","used_percent":17,"observed_at":"2026-01-01T00:00:00Z"}]}`), &older); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(accountQuotaLines(older), "\n")
	if !strings.Contains(text, "17% used") || strings.Contains(text, "fetching") || strings.Contains(text, "failed") {
		t.Fatal(text)
	}
	unknown := strings.Join(accountQuotaLines(protocol.AccountView{Quota: []protocol.AccountQuotaView{{Label: "weekly", ObservedAt: time.Now()}}}), "\n")
	if strings.Contains(unknown, "0%") || strings.Contains(unknown, "100%") {
		t.Fatal(unknown)
	}
}

func TestAccountsQuotaNavigationWhileRequestPending(t *testing.T) {
	m := accountUIOpen(t, newAccountUIClient()).(rootModel)
	m.accounts = accountsFlowFixture(5, true)
	m.accounts.focus, m.accounts.busy = 1, true
	next := send(m, keyDown).(rootModel)
	if next.accounts.focus != 2 {
		t.Fatal("pending quota read blocked account navigation")
	}
	next = send(m, keyEnter).(rootModel)
	if !next.accounts.detail {
		t.Fatal("pending quota read blocked details")
	}
	next.accounts.detailFocus = 6
	next = send(next, keyEnter).(rootModel)
	if next.accounts.detail {
		t.Fatal("pending quota read blocked Back")
	}
}

func TestAccountsQuotaPollCompletesAndRenderingDoesNotFetch(t *testing.T) {
	c := newAccountUIClient()
	c.reply.Accounts = []protocol.AccountView{{ID: "personal", Provider: "claude", State: "enabled", RefreshSupported: true, QuotaFetchState: "loading"}}
	m := accountUIOpen(t, c).(rootModel)
	m.accounts.focus = 1
	before := len(c.requests)
	if text := stripANSI(m.accounts.view(76, 20, false, true)); !strings.Contains(text, "fetching") {
		t.Fatal(text)
	}
	_ = m.accounts.view(76, 20, false, true)
	if len(c.requests) != before {
		t.Fatal("rendering fetched quota")
	}
	used := 12
	c.reply.Accounts = []protocol.AccountView{{ID: "personal", Provider: "claude", State: "enabled", RefreshSupported: true, QuotaFetchState: "ready", Quota: []protocol.AccountQuotaView{{Label: "five_hour", UsedPercent: &used, ObservedAt: time.Now()}}}}
	polling, request := m.Update(accountsPollMsg{generation: m.accounts.generation, clientGeneration: m.accountsClientGeneration})
	if request == nil {
		t.Fatal("fetching accounts did not poll")
	}
	completed, nextPoll := polling.Update(request())
	if nextPoll == nil || completed.(rootModel).accounts.reply.Accounts[0].QuotaFetchState != "ready" {
		t.Fatal("poll completion lost its result or future refresh timer")
	}
	if text := stripANSI(completed.(rootModel).accounts.view(76, 20, false, true)); !strings.Contains(text, "5h 12%") || strings.Contains(text, "fetching") {
		t.Fatal(text)
	}
}

func TestAccountsQuotaRefreshErrorIsBoundedAndObservationAgeStaysDistinct(t *testing.T) {
	used := 9
	observation, attempt, next := time.Now().Add(-10*time.Minute), time.Now(), time.Now().Add(time.Minute)
	a := protocol.AccountView{RefreshSupported: true, QuotaFetchState: "error", QuotaFetchError: strings.Repeat("x", 300) + "\x1b\n", QuotaLastAttemptAt: &attempt, QuotaNextRefreshAt: &next, Quota: []protocol.AccountQuotaView{{Label: "weekly", UsedPercent: &used, ObservedAt: observation}}}
	text := strings.Join(accountQuotaLines(a), "\n")
	if strings.ContainsAny(text, "\x1b") || strings.Contains(text, strings.Repeat("x", 161)) || !strings.Contains(text, "last observed: 10m ago") || !strings.Contains(text, "Last refresh attempt: just now") || !strings.Contains(text, "Next quota refresh:") {
		t.Fatal(text)
	}
}

func TestAccountsQuotaSummaryAndViewport(t *testing.T) {
	used, week := 42, 73
	for _, state := range []string{"ready", "loading", "error"} {
		a := accountsFlowFixture(3, true)
		a.focus = 1
		a.reply.Accounts[0].RefreshSupported = true
		a.reply.Accounts[0].QuotaFetchState = state
		a.reply.Accounts[0].QuotaFetchError = "Provider unavailable"
		a.reply.Accounts[0].Quota = []protocol.AccountQuotaView{{Label: "five_hour", UsedPercent: &used, ObservedAt: time.Now()}, {Label: "seven_day", UsedPercent: &week, ObservedAt: time.Now()}}
		text := stripANSI(a.view(80, 24, false, true))
		if !strings.Contains(text, "5h 42%") || !strings.Contains(text, "weekly 73%") {
			t.Fatal(text)
		}
		for _, size := range [][2]int{{44, 12}, {76, 20}, {116, 28}, {23, 3}} {
			for _, detail := range []bool{false, true} {
				a.detail = detail
				checkAccountsFlowViewport(t, a.view(size[0], size[1], false, true), size[0], size[1])
			}
		}
	}
}
