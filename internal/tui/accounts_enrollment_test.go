package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestAccountsEnrollmentFailedAttemptsDoNotCrowdLiveSignIn(t *testing.T) {
	c := newAccountUIClient()
	c.reply.Accounts = accountsFlowFixture(2, true).reply.Accounts
	for i := range 12 {
		for _, provider := range []string{"claude", "codex"} {
			c.reply.Jobs = append(c.reply.Jobs, protocol.AccountEnrollmentView{ID: fmt.Sprintf("failed-%s-%d", provider, i), Provider: provider, State: "failed", Method: "device-code", Message: "Sign-in failed. Start a new attempt."})
		}
	}
	c.reply.Jobs = append(c.reply.Jobs, protocol.AccountEnrollmentView{ID: "live-codex", Provider: "codex", Method: "device-code", State: "authenticating", VerificationURL: "https://example.invalid/device", UserCode: "TEST-CODE"})
	m := accountUIOpen(t, c).(rootModel)
	m.width, m.height = 80, 24
	body := stripANSI(view(m))
	if strings.Contains(body, "Previous sign-in") {
		t.Fatal("completed failures crowd the account list:\n" + body)
	}
	if len(m.accounts.rows()) != 5 || len(m.accounts.reply.Jobs) != 25 {
		t.Fatal("list filtering lost a live row or changed retained jobs")
	}
	m.accounts.restoreSelection("job:live-codex")
	body = stripANSI(view(m))
	checkAccountsFlowViewport(t, body, 80, 24)
	for _, want := range []string{"Personal 1", "Personal 2", "▌ Waiting for sign-in"} {
		if !strings.Contains(body, want) {
			t.Fatalf("list hides %q:\n%s", want, body)
		}
	}
	reopened, status := m.Update(keyEnter)
	if status == nil || !reopened.(rootModel).accounts.wizardOpen || reopened.(rootModel).accounts.wizard.job.ID != "live-codex" {
		t.Fatal("live sign-in cannot be reopened")
	}
	status()
	if req := c.requests[len(c.requests)-1]; req.Action != "status" || req.JobID != "live-codex" {
		t.Fatal("Enter selected a filtered failure instead of the live attempt", req)
	}
}

func TestAccountsEnrollmentActiveStatesStayReachable(t *testing.T) {
	for _, state := range []string{"pending", "authenticating", "verifying", "ready", "admitting", "cancelling"} {
		t.Run(state, func(t *testing.T) {
			c := newAccountUIClient()
			c.reply.Jobs = []protocol.AccountEnrollmentView{
				{ID: "old", Provider: "codex", Method: "device-code", State: "failed"},
				{ID: "live", Provider: "codex", Method: "device-code", State: state},
				{ID: "added", Provider: "codex", State: "admitted"},
				{ID: "cancelled", Provider: "codex", State: "cancelled"},
			}
			m := accountUIOpen(t, c).(rootModel)
			if len(m.accounts.rows()) != 3 {
				t.Fatal("active sign-in or terminal filtering changed", m.accounts.rows())
			}
			m.accounts.restoreSelection("job:live")
			if m.accounts.selected().id != "live" || !strings.Contains(m.accounts.hint(false, false, 80), "enter reopen") {
				t.Fatal("active attempt is unreachable")
			}
			reopened, cmd := m.Update(keyEnter)
			if cmd == nil || reopened.(rootModel).accounts.wizard.job.ID != "live" {
				t.Fatal("active attempt did not reopen")
			}
			cmd()
			if req := c.requests[len(c.requests)-1]; req.Action != "status" || req.JobID != "live" {
				t.Fatal(req)
			}
		})
	}
}

func TestAccountsEnrollmentFailureKeepsCurrentWizardRetry(t *testing.T) {
	c := newAccountUIClient()
	live := protocol.AccountEnrollmentView{ID: "current", Provider: "codex", Method: "device-code", State: "authenticating", TargetAccountID: "existing-account"}
	c.reply.Jobs = []protocol.AccountEnrollmentView{live}
	m := accountUIOpen(t, c).(rootModel)
	m.accounts.restoreSelection("job:current")
	m.accounts.wizardOpen = true
	m.accounts.wizard = accountWizard{step: accountStepAuthenticate, provider: "codex", method: "device-code", targetID: live.TargetAccountID, job: live}
	failed := live
	failed.State, failed.Message = "failed", "Codex sign-in failed."
	reply := c.reply
	reply.Jobs = []protocol.AccountEnrollmentView{failed}
	settled, _ := m.applyAccountsReply(accountsReplyMsg{generation: m.accounts.generation, clientGeneration: m.accountsClientGeneration, action: "list", reply: reply})
	m = settled.(rootModel)
	if len(m.accounts.rows()) != 2 || m.accounts.selected().kind != "provider" || len(m.accounts.reply.Jobs) != 1 {
		t.Fatal("failure projection lost durable job or valid selection")
	}
	if !m.accounts.wizardOpen || !strings.Contains(view(m), "Sign-in failed") || !strings.Contains(m.accounts.hint(false, false, 80), "r retry") {
		t.Fatal("current failure lost its error or retry action")
	}
	_, retry := m.Update(keyRune('r'))
	if retry == nil {
		t.Fatal("failed current wizard cannot retry")
	}
	retry()
	if req := c.requests[len(c.requests)-1]; req.Action != "start" || req.Provider != "codex" || req.Method != "device-code" || req.AccountID != "existing-account" || req.JobID != "" {
		t.Fatal("retry changed the logical account or reused the failed job", req)
	}
}

func TestAccountsEnrollmentHistoryTogglePreservesSelection(t *testing.T) {
	c := newAccountUIClient()
	c.reply.Accounts = accountsFlowFixture(2, true).reply.Accounts
	c.reply.Jobs = []protocol.AccountEnrollmentView{{ID: "old", Provider: "claude", State: "failed"}, {ID: "live", Provider: "codex", State: "authenticating", Method: "device-code"}}
	m := accountUIOpen(t, c).(rootModel)
	m.width, m.height = 80, 24
	m.accounts.restoreSelection("job:live")
	before := len(c.requests)
	shown, cmd := m.Update(keyRune('h'))
	m = shown.(rootModel)
	if cmd != nil || len(c.requests) != before || len(m.accounts.rows()) != 6 || m.accounts.selected().id != "live" {
		t.Fatal("history toggle fetched, lost selection or hid retained failure")
	}
	for _, size := range [][2]int{{80, 24}, {48, 14}} {
		m.width, m.height = size[0], size[1]
		checkAccountsFlowViewport(t, stripANSI(view(m)), size[0], size[1])
		checkAccountsFlowViewport(t, m.accounts.hint(false, false, size[0]), size[0]-4, 1)
		if !strings.Contains(m.accounts.hint(false, false, size[0]), "h hide history") {
			t.Fatal("history has no hide control")
		}
	}
	m.accounts.restoreSelection("job:old")
	hidden, cmd := m.Update(keyRune('h'))
	m = hidden.(rootModel)
	if cmd != nil || len(m.accounts.rows()) != 5 || m.accounts.selected().kind == "" || m.accounts.selected().id == "old" || strings.Contains(view(m), "Previous sign-in") {
		t.Fatal("hiding selected failure left invalid focus or clutter")
	}
	if !strings.Contains(m.accounts.hint(false, false, 80), "h history") {
		t.Fatal("retained failures have no history control")
	}
	empty := accountUIOpen(t, newAccountUIClient()).(rootModel)
	if strings.Contains(empty.accounts.hint(false, false, 80), "history") {
		t.Fatal("history control appears without failed attempts")
	}
}

func TestAccountsEnrollmentHistoricalFailureCanRequestFencedCancellation(t *testing.T) {
	c := newAccountUIClient()
	failed := protocol.AccountEnrollmentView{ID: "retained-failed-candidate", Provider: "codex", Method: "device-code", State: "failed", TargetAccountID: "existing-account", Message: "Sign-in failed; stopped writer proof is unavailable."}
	c.reply.Jobs, c.reply.Job = []protocol.AccountEnrollmentView{failed}, &failed
	m := accountUIOpen(t, c).(rootModel)
	m = send(m, keyRune('h')).(rootModel)
	m.accounts.restoreSelection("job:" + failed.ID)
	reopened, status := m.Update(keyEnter)
	if status == nil || !reopened.(rootModel).accounts.wizardOpen || reopened.(rootModel).accounts.wizard.job.ID != failed.ID {
		t.Fatal("historical failure cannot reopen")
	}
	settled, _ := reopened.Update(status())
	m = settled.(rootModel)
	if !strings.Contains(m.accounts.hint(false, false, 80), "r retry") || !strings.Contains(m.accounts.hint(false, false, 80), "ctrl+x cancel") {
		t.Fatal("historical attempt lost retry or cancellation")
	}
	if req := c.requests[len(c.requests)-1]; req.Action != "status" || req.JobID != failed.ID {
		t.Fatal("history reopened the wrong candidate", req)
	}
	// Cancellation intent must still reach the daemon. A nonterminal response
	// means writer proof is pending; the UI cannot turn it into completed cleanup.
	cancelling := failed
	cancelling.State, cancelling.Message = "cancelling", "Waiting for stopped writer proof."
	c.reply.Jobs, c.reply.Job = []protocol.AccountEnrollmentView{cancelling, {ID: "another-failure", Provider: "codex", State: "failed"}}, &cancelling
	requested, cancel := m.Update(keyCtrlX)
	if cancel == nil {
		t.Fatal("historical candidate cannot request explicit cancellation")
	}
	settled, _ = requested.Update(cancel())
	m = settled.(rootModel)
	if req := c.requests[len(c.requests)-1]; req.Action != "cancel" || req.JobID != failed.ID {
		t.Fatal("explicit cancel targeted another candidate", req)
	}
	if len(m.accounts.reply.Jobs) != 2 || m.accounts.reply.Jobs[0].State != "cancelling" || m.accounts.reply.Jobs[0].TargetAccountID != failed.TargetAccountID || strings.Contains(m.accounts.notice, "Sign-in cancelled") {
		t.Fatal("UI claimed cleanup without stopped writer proof")
	}
	m = send(m, keyRune('h')).(rootModel) // Hide history while cancellation stays active.
	m.accounts.restoreSelection("job:" + failed.ID)
	if m.accounts.showFailedAttempts || m.accounts.selected().id != failed.ID || !strings.Contains(view(m), "Stopping sign-in") {
		t.Fatal("active cancellation disappeared with history hidden")
	}
}
