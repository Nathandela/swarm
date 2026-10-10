package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// needsLoginFixture focuses a needs-login account with week-old quota next to
// one ready account, so the guidance path renders the selected account.
func needsLoginFixture(older, newest time.Time) accountsModel {
	used, week := 42, 73
	a := accountsFlowFixture(2, true)
	a.focus = 1
	a.reply.Accounts[0].State = "needs-login"
	a.reply.Accounts[0].RefreshSupported = true
	a.reply.Accounts[0].QuotaFetchState = "error"
	a.reply.Accounts[0].QuotaFetchError = "Sign-in expired for this account; sign in again."
	a.reply.Accounts[0].Quota = []protocol.AccountQuotaView{{Label: "five_hour", UsedPercent: &used, ObservedAt: older}, {Label: "seven_day", UsedPercent: &week, ObservedAt: newest}}
	return a
}

func TestAccountsNeedsLoginSummaryNamesSignInAndNewestReading(t *testing.T) {
	older, newest := time.Now().Add(-9*24*time.Hour), time.Now().Add(-7*24*time.Hour)
	a := needsLoginFixture(older, newest)
	want := "sign in again · last reading " + newest.Local().Format("02 Jan")
	if got := accountQuotaSummary(a.reply.Accounts[0]); got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestAccountsNeedsLoginListRowAndGuidanceShowSignInAgain(t *testing.T) {
	older, newest := time.Now().Add(-9*24*time.Hour), time.Now().Add(-7*24*time.Hour)
	a := needsLoginFixture(older, newest)
	want := "sign in again · last reading " + newest.Local().Format("02 Jan")
	text := stripANSI(a.view(140, 40, false, true))
	row := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "Personal 1") {
			row = line
		}
	}
	if !strings.Contains(row, want) {
		t.Fatalf("list row missing %q: %q\n%s", want, row, text)
	}
	if !strings.Contains(text, "Quota: "+want) {
		t.Fatalf("guidance missing %q:\n%s", "Quota: "+want, text)
	}
	checkAccountsFlowViewport(t, a.view(80, 24, false, true), 80, 24)
}

func TestAccountsNeedsLoginDetailExplainsExpiryBeforeBars(t *testing.T) {
	older, newest := time.Now().Add(-9*24*time.Hour), time.Now().Add(-7*24*time.Hour)
	a := needsLoginFixture(older, newest)
	a.detail = true
	text := stripANSI(a.view(140, 60, false, true))
	want := `Sign-in expired. Last reading ` + newest.Local().Format("02 Jan 15:04") + `; use "Sign in again".`
	at := strings.Index(text, want)
	if at < 0 {
		t.Fatalf("detail missing %q:\n%s", want, text)
	}
	bar := strings.IndexAny(text, "█░")
	if bar < 0 || at > bar {
		t.Fatalf("expiry line must precede the usage bars (line %d, bar %d):\n%s", at, bar, text)
	}
}

func TestAccountsNeedsLoginWordingLeavesOtherStatesUnchanged(t *testing.T) {
	old := time.Now().Add(-7 * 24 * time.Hour)
	enabled := needsLoginFixture(old, old)
	enabled.reply.Accounts[0].State = "enabled"
	paused := needsLoginFixture(old, old)
	paused.reply.Accounts[0].State = "paused"
	noQuota := needsLoginFixture(old, old)
	noQuota.reply.Accounts[0].Quota = nil
	for name, a := range map[string]accountsModel{"enabled": enabled, "paused": paused, "needs-login without quota": noQuota} {
		if summary := accountQuotaSummary(a.reply.Accounts[0]); strings.Contains(summary, "sign in again") || strings.Contains(summary, "last reading") {
			t.Fatalf("%s: summary changed: %q", name, summary)
		}
		a.detail = true
		if text := stripANSI(a.view(140, 60, false, true)); strings.Contains(text, "Sign-in expired. Last reading") {
			t.Fatalf("%s: detail shows expiry line:\n%s", name, text)
		}
	}
	if summary := accountQuotaSummary(enabled.reply.Accounts[0]); !strings.Contains(summary, "refresh failed") || !strings.Contains(summary, "stale") {
		t.Fatalf("enabled summary lost its existing wording: %q", summary)
	}
}

func TestAccountsNeedsLoginWithoutReadingTimeKeepsUsageSummary(t *testing.T) {
	used := 40
	a := protocol.AccountView{State: "needs-login", Quota: []protocol.AccountQuotaView{{Label: "five_hour", UsedPercent: &used}}}
	if text := accountQuotaSummary(a); strings.Contains(text, "last reading") {
		t.Fatal(text)
	}
}
