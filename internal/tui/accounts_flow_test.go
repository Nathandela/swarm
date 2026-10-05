package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Nathandela/swarm/internal/protocol"
)

func accountsFlowFixture(count int, enabled bool) accountsModel {
	a := accountsModel{available: true, loaded: true, reply: protocol.AccountsReply{Enabled: map[string]bool{"claude": enabled}, Jobs: []protocol.AccountEnrollmentView{{ID: "old-failure", Provider: "claude", State: "failed"}, {ID: "added", Provider: "claude", State: "admitted"}}}}
	for i := range count {
		a.reply.Accounts = append(a.reply.Accounts, protocol.AccountView{ID: fmt.Sprint(i), Provider: "claude", Label: fmt.Sprintf("Personal %d", i+1), CredentialKind: "native", State: "enabled"})
	}
	return a
}

func checkAccountsFlowViewport(t *testing.T, text string, width, height int) {
	t.Helper()
	if lines := strings.Split(text, "\n"); len(lines) > height {
		t.Fatalf("account body exceeds its budget: %d > %d\n%s", len(lines), height, stripANSI(text))
	}
	for _, line := range strings.Split(text, "\n") {
		if lipgloss.Width(line) > width {
			t.Fatalf("account body exceeds width %d: %q", width, line)
		}
	}
}

func TestAccountsFlow_ReadyAccountsHaveTruthfulNextStepAndUnknownQuota(t *testing.T) {
	for _, tc := range []struct {
		count   int
		enabled bool
		want    []string
	}{
		{0, true, []string{"add a verified Claude account", "No account is ready"}},
		{1, false, []string{"enable Claude rotation", "Discussion coverage is unavailable"}},
		{1, true, []string{"Ready", "Quota: unknown", "not yet observed", "no same-provider backup", "Next: start a new Claude discussion"}},
		{2, true, []string{"Ready", "Quota: unknown", "2 ready accounts configured", "Backup capacity is unconfirmed"}},
	} {
		t.Run(fmt.Sprintf("count%d-enabled%t", tc.count, tc.enabled), func(t *testing.T) {
			a := accountsFlowFixture(tc.count, tc.enabled)
			if tc.count > 0 {
				a.focus = 1
			}
			text := stripANSI(a.view(44, 12, false, true))
			checkAccountsFlowViewport(t, text, 44, 12)
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("account flow lacks %q:\n%s", want, text)
				}
			}
			if strings.Contains(text, "0%") || strings.Contains(text, "Checking") || strings.Contains(text, "Account added") {
				t.Fatal("quota absence or completed enrollment implies active checking/capacity", text)
			}
			if strings.Contains(text, "Previous sign-in") {
				t.Fatal("completed failures crowd added accounts", text)
			}
		})
	}
}

func TestAccountsFlow_HintsMatchTheSelectedRowAndWizardAction(t *testing.T) {
	a := accountsFlowFixture(1, true)
	if got := a.hint(false, true, 48); !strings.Contains(got, "enter toggle") {
		t.Fatal("provider row has a details hint", got)
	}
	a.focus = 1
	if got := a.hint(false, true, 48); !strings.Contains(got, "enter details") || !strings.Contains(got, "a add") || lipgloss.Width(got) > 46 {
		t.Fatal("account row lacks discoverable details/add action", got)
	}
	a.wizardOpen = true
	for _, tc := range []struct {
		state, want string
		attach      bool
	}{
		{"pending", "Starting sign-in", true},
		{"authenticating", "Waiting for sign-in", true},
		{"verifying", "Checking sign-in", false},
		{"admitting", "Adding account", false},
		{"cancelling", "Stopping sign-in", false},
		{"failed", "Sign-in failed", false},
		{"cancelled", "Sign-in cancelled", false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			a.wizard = accountWizard{step: accountStepAuthenticate, provider: "claude", method: "native-login", job: protocol.AccountEnrollmentView{ID: "synthetic", State: tc.state, LoginSocket: "private-socket"}}
			body := stripANSI(a.view(44, 12, false, true))
			hint := a.hint(false, true, 48)
			checkAccountsFlowViewport(t, body, 44, 12)
			if !strings.Contains(body, tc.want) || strings.Contains(hint, "enter next") {
				t.Fatal("wizard does not describe its current state", body, hint)
			}
			if strings.Contains(hint, "enter sign in") != tc.attach {
				t.Fatal("wizard advertises the wrong Enter action", hint)
			}
			if lipgloss.Width(hint) > 46 || !strings.Contains(hint, "esc") || !strings.Contains(hint, "ctrl+x cancel") {
				t.Fatal("48-column footer loses exit/cancellation", hint)
			}
			if got := a.hint(false, false, 48); strings.Contains(got, "enter sign in") {
				t.Fatal("client without a runner promises terminal attachment", got)
			}
		})
	}
	if got := a.hint(false, true, 25); lipgloss.Width(got) > 23 || !strings.Contains(got, "esc") || !strings.Contains(got, "ctrl+x") {
		t.Fatal("tiny-terminal footer loses exit/cancellation", got)
	}
}

func TestAccountsFlow_RevokedReadyCannotAdmitAndRetryKeepsTarget(t *testing.T) {
	for _, state := range []string{"failed", "cancelled", "pending", "authenticating", "verifying"} {
		for _, step := range []int{accountStepVerify, accountStepLabel, accountStepReview} {
			t.Run(fmt.Sprintf("%s-step%d", state, step), func(t *testing.T) {
				c := newAccountUIClient()
				m := accountUIOpen(t, c).(rootModel)
				m.accounts.wizardOpen = true
				m.accounts.wizard = accountWizard{step: step, scroll: 100, provider: "claude", method: "native-login", targetID: "existing", label: newLineEditor("Retained label"), job: protocol.AccountEnrollmentView{ID: "synthetic", State: "ready", TargetAccountID: "existing"}}
				job := protocol.AccountEnrollmentView{ID: "synthetic", Provider: "claude", Method: "native-login", State: state, TargetAccountID: "existing", LoginSocket: "stale-socket"}
				next, _ := m.applyAccountsReply(accountsReplyMsg{generation: m.accounts.generation, clientGeneration: m.accountsClientGeneration, action: "status", reply: protocol.AccountsReply{Job: &job}})
				m = next.(rootModel)
				w := m.accounts.wizard
				if w.step != accountStepAuthenticate || w.scroll != 0 || w.targetID != "existing" || w.label.text != "Retained label" {
					t.Fatal("readiness revocation lost reauthentication context", w)
				}
				if body := stripANSI(w.view(80, false, false, true, nil)); strings.Contains(body, "Enter adds") || strings.Contains(body, "Sign-in checked") {
					t.Fatal("revoked readiness retains add/review instructions", body)
				}
				attached := false
				m.accountLoginRunner = func(protocol.AccountEnrollmentView) error { attached = true; return nil }
				_, cmd := m.updateAccountWizard(keyEnter)
				if cmd != nil {
					cmd()
				}
				for _, req := range c.requests {
					if req.Action == "admit" || req.Action == "start" && req.AccountID != "existing" {
						t.Fatal("revoked ready admitted or retried a different logical account", req)
					}
				}
				if state != "pending" && state != "authenticating" && attached {
					t.Fatal("checking/terminal state attached a stale login socket")
				}
			})
		}
	}
}

func TestAccountsFlow_PagingAndFocusStayVisible(t *testing.T) {
	a := accountsFlowFixture(60, true)
	a.focus = 60
	a.notice = "Account added. Automatic rotation is enabled separately for each provider."
	text := a.view(44, 12, false, true)
	checkAccountsFlowViewport(t, text, 44, 12)
	if !strings.Contains(stripANSI(text), "▌ Personal 60") || !strings.Contains(text, "PgUp/PgDn") || !strings.Contains(text, "Next:") {
		t.Fatal("narrow list hid focused row/paging/next action", stripANSI(text))
	}
	m := rootModel{height: 14, accounts: a}
	m.accounts.focus = 1
	next, _ := m.updateAccounts(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if next.(rootModel).accounts.focus <= 1 {
		t.Fatal("list paging changed unused scroll instead of visible selection")
	}
	w := accountWizard{step: accountStepAuthenticate, scroll: 100, provider: "codex", method: "device-code", job: protocol.AccountEnrollmentView{ID: "synthetic", State: "authenticating"}}
	m.accounts.wizardOpen, m.accounts.wizard = true, w
	m.updateAccountJob(protocol.AccountEnrollmentView{ID: "synthetic", State: "ready"})
	if m.accounts.wizard.step != accountStepVerify || m.accounts.wizard.scroll != 0 {
		t.Fatal("ready transition retained long-URL scroll")
	}
	m.updateAccountJob(protocol.AccountEnrollmentView{ID: "synthetic", State: "failed", Message: strings.Repeat("Explanation ", 40)})
	m.accounts.wizard.scroll = 3
	m.updateAccountJob(m.accounts.wizard.job)
	if m.accounts.wizard.scroll != 3 {
		t.Fatal("unchanged terminal poll moved the user's viewport")
	}
	terminal := m.accounts.view(44, 12, false, true)
	checkAccountsFlowViewport(t, terminal, 44, 12)
	if !strings.Contains(terminal, "PgUp/PgDn scroll") {
		t.Fatal("long terminal explanation has no paging reminder")
	}
}
