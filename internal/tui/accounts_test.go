package tui

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Nathandela/swarm/internal/protocol"
)

type accountUIClient struct {
	*contextGuardOptionsClient
	accountMu  sync.Mutex
	reply      protocol.AccountsReply
	accountErr error
	requests   []protocol.AccountsReq
	endpointID string
}

func (c *accountUIClient) EndpointID() string { return c.endpointID }

func newAccountUIClient() *accountUIClient {
	c := &accountUIClient{contextGuardOptionsClient: newContextGuardOptionsClient()}
	c.caps = append(c.caps, protocol.CapAccountsManage)
	c.reply = protocol.AccountsReply{Revision: 7, Enabled: map[string]bool{"claude": false, "codex": false}, Methods: map[string][]protocol.AccountMethodView{
		"claude": {{ID: "native-login", Label: "Native sign-in", Available: false, Reason: "Native login is unavailable"}, {ID: "token-manual", Label: "Paste subscription token", Available: true}},
		"codex":  {{ID: "device-code", Label: "Sign in in browser", Available: true}, {ID: "import-native", Label: "Import native login file", Available: false, Reason: "Import requires characterized refresh behavior"}},
	}}
	return c
}

func (c *accountUIClient) Accounts(req protocol.AccountsReq) (protocol.AccountsReply, error) {
	c.accountMu.Lock()
	defer c.accountMu.Unlock()
	c.requests = append(c.requests, req)
	if c.accountErr != nil {
		return protocol.AccountsReply{}, c.accountErr
	}
	return c.reply, nil
}

func accountUIOpen(t *testing.T, c *accountUIClient) tea.Model {
	t.Helper()
	m := loadedContextGuardOptions(t, c.contextGuardOptionsClient).(rootModel)
	m.client = c
	m.options.focus = optionsFocusAccounts
	next, cmd := m.Update(keyEnter)
	if cmd == nil {
		t.Fatal("Accounts entry did not dispatch list")
	}
	next, _ = next.Update(cmd())
	return next
}

func TestAccounts_OptionsLoadingRoundtripPreservesFocusAndPendingSettings(t *testing.T) {
	c := newAccountUIClient()
	m := newModel(t, c, detectMixed())
	opening, load := m.Update(keyRune('o'))
	rm := opening.(rootModel)
	rm.options.grouping = groupByRepo
	rm.options.ordering = orderByName
	opening = send(rm, keyUp)
	if opening.(rootModel).options.focus != optionsFocusAccounts {
		t.Fatal("Accounts not reachable during settings load")
	}
	accounts, accountsLoad := opening.Update(keyEnter)
	if accounts.(rootModel).screen != screenAccounts || accountsLoad == nil {
		t.Fatal("Enter while loading did not open Accounts")
	}
	accounts = send(accounts, load())
	if !accounts.(rootModel).options.contextGuard.loaded {
		t.Fatal("settings reply was discarded while Accounts open")
	}
	back := send(accounts, keyEsc).(rootModel)
	if back.screen != screenOptions || back.options.focus != optionsFocusAccounts || back.options.grouping != groupByRepo || back.options.ordering != orderByName {
		t.Fatal("Accounts roundtrip changed pending Options choices or focus")
	}
	back.options.focus = optionsFocusAutoCompact
	back.options.contextGuard.autoCompact.Enabled = true
	back.options.contextGuard.threshold.set("85")
	back.options.focus = optionsFocusAccounts
	accounts, _ = back.Update(keyEnter)
	returned := send(accounts, keyEsc).(rootModel)
	if !returned.options.contextGuard.dirty() || returned.options.contextGuard.savedCompact.Enabled || returned.options.contextGuard.threshold.text != "85" {
		t.Fatal("roundtrip discarded settings or changed cancellation snapshot")
	}
	cancelled := send(returned, keyEsc).(rootModel)
	if cancelled.general.grouping != groupByStatus || cancelled.general.ordering != orderByArrival {
		t.Fatal("Options Cancel applied pending board changes")
	}
}

func TestAccounts_OptionsSaveCompletesDuringRoundtrip(t *testing.T) {
	c := newAccountUIClient()
	m := loadedContextGuardOptions(t, c.contextGuardOptionsClient).(rootModel)
	m.client = c
	m.options.grouping = groupByRepo
	m.options.contextGuard.autoCompact.Enabled = true
	next, save := m.Update(keyEnter)
	rm := next.(rootModel)
	rm.options.focus = optionsFocusAccounts
	accounts, _ := rm.Update(keyEnter)
	settled := send(accounts, save()).(rootModel)
	if settled.screen != screenAccounts || settled.options.contextGuard.saving || settled.general.grouping != groupByRepo || settled.options.contextGuard.dirty() {
		t.Fatal("save completed on Accounts without committing or preserving form")
	}
	back := send(settled, keyEsc).(rootModel)
	if back.screen != screenOptions || !back.options.contextGuard.autoCompact.Enabled {
		t.Fatal("save result lost on return")
	}
}

func TestAccounts_EntryAlwaysVisibleAndUnsupportedIsUsable(t *testing.T) {
	for _, failure := range []bool{false, true} {
		c := newAccountUIClient()
		if failure {
			c.getErr = errors.New("private error")
		}
		m := newModel(t, c, detectMixed())
		opening, cmd := m.Update(keyRune('o'))
		if !strings.Contains(view(opening), "Accounts →") {
			t.Fatal("loading hides Accounts action")
		}
		loaded := send(opening, cmd())
		if !strings.Contains(view(loaded), "Accounts →") {
			t.Fatal("settings failure hides Accounts action")
		}
	}
	m := send(newModel(t, newFakeClient(), detectMixed()), keyRune('o'))
	m = send(m, keyUp)
	m = send(m, keyEnter)
	if m.(rootModel).screen != screenAccounts || !strings.Contains(view(m), "unavailable on this daemon") {
		t.Fatal("old daemon Accounts page missing")
	}
	m = send(m, keyRune('a'))
	m = send(m, keyEsc)
	if m.(rootModel).screen != screenOptions {
		t.Fatal("unsupported Accounts page cannot return")
	}
}

func TestAccounts_TokenPasteMaskedBoundedAndOwnsInput(t *testing.T) {
	c := newAccountUIClient()
	m := accountUIOpen(t, c).(rootModel)
	m.accounts.wizardOpen = true
	m.accounts.wizard = accountWizard{step: accountStepAuthenticate, provider: "claude", method: "token-manual"}
	m.launch.cwd.set("launch-unmodified")
	m.general.editing = true
	m.general.edit.set("rename-unmodified")
	const token = "synthetic-subscription-token"
	next := send(m, tea.PasteMsg{Content: " \t" + token + "\r\n"}).(rootModel)
	if next.accounts.wizard.input.text != token || next.launch.cwd.text != "launch-unmodified" || next.general.edit.text != "rename-unmodified" {
		t.Fatal("token paste did not own input")
	}
	if strings.Contains(view(next), token) || !strings.Contains(view(next), "•••") {
		t.Fatal("token input rendered in clear")
	}
	rejected := send(next, tea.PasteMsg{Content: "bad\x00input"}).(rootModel)
	if rejected.accounts.wizard.input.text != token || !strings.Contains(rejected.accounts.wizard.err, "control") {
		t.Fatal("control byte accepted")
	}
	rejected = send(next, tea.PasteMsg{Content: strings.Repeat("x", accountTokenMaxBytes)}).(rootModel)
	if rejected.accounts.wizard.input.text != token || !strings.Contains(rejected.accounts.wizard.err, "32 KiB") {
		t.Fatal("oversized token accepted")
	}
	next.pairing = &pairingModal{}
	blocked := send(next, tea.PasteMsg{Content: "hidden"}).(rootModel)
	if blocked.accounts.wizard.input.text != token {
		t.Fatal("pairing modal leaked paste")
	}
	next = send(next, tea.KeyPressMsg{Code: 'n'}).(rootModel)
	left := send(next, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}).(rootModel)
	if left.accounts.wizard.input.text != "" || left.accounts.wizard.step != accountStepMethod {
		t.Fatal("leaving token screen retained secret")
	}
	left.accounts.wizard = accountWizard{step: accountStepAuthenticate, method: "token-manual", input: newLineEditor(token)}
	left = send(left, keyEsc).(rootModel)
	if left.accounts.wizard.input.text != "" || left.accounts.wizardOpen {
		t.Fatal("leaving wizard retained secret")
	}
}

func TestAccounts_UnavailableTokenMethodExplainsReasonWithoutAcceptingInput(t *testing.T) {
	c := newAccountUIClient()
	const reason = "Token identity and effective settings cannot be verified; unavailable for discussions."
	c.reply.Methods["claude"][1].Available = false
	c.reply.Methods["claude"][1].Reason = reason
	m := accountUIOpen(t, c)
	m = send(m, keyRune('a'))
	m = send(m, keyEnter) // Claude
	m = send(m, keyDown)  // The unavailable method remains reachable for its reason.
	if !strings.Contains(view(m), "Paste subscription token (unavailable)") || !strings.Contains(view(m), reason) {
		t.Fatal("unavailable token method does not show its explanation")
	}
	requests := len(c.requests)
	next, cmd := m.Update(keyEnter)
	if cmd != nil || next.(rootModel).accounts.wizard.step != accountStepMethod {
		t.Fatal("unavailable token method opened authentication")
	}
	next = send(next, tea.PasteMsg{Content: "synthetic-token-must-not-be-accepted"})
	next, cmd = next.Update(keyEnter)
	if cmd != nil || len(c.requests) != requests || next.(rootModel).accounts.wizard.input.text != "" {
		t.Fatal("unavailable token method accepted or submitted a token")
	}
	for _, step := range []int{accountStepAuthenticate, accountStepVerify, accountStepReview} {
		w := accountWizard{step: step, provider: "claude", method: "token-manual"}
		if rendered := w.view(100, false, false, false, nil); !strings.Contains(rendered, "unavailable for discussions") || strings.Contains(rendered, "manual selection only") {
			t.Fatalf("token step %d implies discussion eligibility", step)
		}
	}
}

func TestAccounts_KeyboardWizardDisabledMethodAndAdmission(t *testing.T) {
	c := newAccountUIClient()
	m := accountUIOpen(t, c)
	m = send(m, keyRune('a'))
	m = send(m, keyEnter) // Claude
	disabled, cmd := m.Update(keyEnter)
	if cmd != nil || disabled.(rootModel).accounts.wizard.step != accountStepMethod || !strings.Contains(view(disabled), "Native login is unavailable") {
		t.Fatal("unsupported native method started")
	}
	m = send(disabled, keyDown)
	m = send(m, keyEnter)
	m = send(m, tea.PasteMsg{Content: "synthetic-manual-token"})
	starting, start := m.Update(keyEnter)
	if start == nil || starting.(rootModel).accounts.wizard.input.text != "" {
		t.Fatal("submitted token retained in model")
	}
	ready := protocol.AccountEnrollmentView{ID: "job-1", Provider: "claude", Method: "token-manual", State: "ready"}
	c.reply = protocol.AccountsReply{Revision: 8, Job: &ready}
	m, _ = starting.Update(start())
	if m.(rootModel).accounts.wizard.step != accountStepVerify || !strings.Contains(view(m), "Identity: unverified") {
		t.Fatal("candidate bypassed verification or falsely claimed identity")
	}
	if got := c.requests[len(c.requests)-1]; got.Action != "start" || got.Token != "synthetic-manual-token" {
		t.Fatal("wrong enrollment request")
	}
	// Ignore follow-up list command in this deterministic test; it is busy until
	// its corresponding reply lands in the real program.
	rm := m.(rootModel)
	rm.accounts.busy = false
	m = rm
	m = send(m, keyEnter)
	m = send(m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m = sendType(m, "Local account")
	m = send(m, keyEnter)
	if m.(rootModel).accounts.wizard.step != accountStepReview {
		t.Fatal("label skipped review")
	}
	admitting, admit := m.Update(keyEnter)
	if admit == nil {
		t.Fatal("review did not admit")
	}
	c.reply = protocol.AccountsReply{Revision: 9}
	m, _ = admitting.Update(admit())
	request := c.requests[len(c.requests)-1]
	if request.Action != "admit" || request.JobID != "job-1" || request.Label != "Local account" || request.ExpectedRevision != 8 {
		t.Fatal("admission lost job, label or revision")
	}
	if m.(rootModel).screen != screenAccounts || m.(rootModel).accounts.wizardOpen {
		t.Fatal("admission did not finish on Accounts")
	}
}

func TestAccounts_LeaveCancelAndStartCancellationRace(t *testing.T) {
	c := newAccountUIClient()
	m := accountUIOpen(t, c).(rootModel)
	m.accounts.wizardOpen = true
	m.accounts.wizard = accountWizard{step: accountStepAuthenticate, provider: "codex", method: "device-code", job: protocol.AccountEnrollmentView{ID: "job-1", State: "authenticating", VerificationURL: "https://example.test/login", UserCode: "TEST"}}
	left, cmd := m.Update(keyEsc)
	if cmd != nil || left.(rootModel).accounts.wizardOpen || !strings.Contains(view(left), "Sign-in continues in Accounts") {
		t.Fatal("Esc cancelled sign-in instead of leaving")
	}
	if left.(rootModel).accounts.wizard.job.UserCode != "" {
		t.Fatal("detached wizard retained live code")
	}
	m.accounts.busy = true
	cancelling, cancel := m.Update(keyCtrlX)
	if cancel == nil {
		t.Fatal("Cancel was blocked by in-flight status read")
	}
	c.reply = protocol.AccountsReply{Job: &protocol.AccountEnrollmentView{ID: "job-1", State: "admitted"}}
	settled, _ := cancelling.Update(cancel())
	if !strings.Contains(view(settled), "already added") {
		t.Fatal("racing committed admission claimed cancellation/deletion")
	}
	// Ctrl+X before start returns waits for the job ID, then sends cancellation.
	m.accounts.wizard = accountWizard{step: accountStepAuthenticate, provider: "codex", method: "device-code"}
	m.accounts.busy = false
	starting, start := m.startAccountEnrollment()
	deferred := send(starting, keyCtrlX).(rootModel)
	if !deferred.accounts.cancelAfterStart {
		t.Fatal("early cancellation discarded intent")
	}
	c.reply = protocol.AccountsReply{Job: &protocol.AccountEnrollmentView{ID: "job-race", State: "authenticating"}}
	identified, terminalCancel := deferred.Update(start())
	if terminalCancel == nil || identified.(rootModel).accounts.cancelAfterStart {
		t.Fatal("job ID did not release deferred cancellation")
	}
	terminalCancel()
	if req := c.requests[len(c.requests)-1]; req.Action != "cancel" || req.JobID != "job-race" {
		t.Fatal("deferred cancellation targeted wrong attempt")
	}
}

func TestAccounts_ReauthenticationTargetSurvivesDetachAndNewClient(t *testing.T) {
	for _, freshClient := range []bool{false, true} {
		t.Run(fmt.Sprintf("new-client-%t", freshClient), func(t *testing.T) {
			c := newAccountUIClient()
			job := protocol.AccountEnrollmentView{ID: "reauth-job", Provider: "claude", Method: "token-manual", State: "ready", TargetAccountID: "existing-account"}
			c.reply.Jobs = []protocol.AccountEnrollmentView{job}
			m := accountUIOpen(t, c).(rootModel)
			m.accounts.wizardOpen = true
			m.accounts.wizard = accountWizard{step: accountStepVerify, provider: job.Provider, method: job.Method, job: job, targetID: job.TargetAccountID}
			m = send(m, keyEsc).(rootModel)
			if freshClient {
				m = accountUIOpen(t, c).(rootModel)
			} else {
				// Preserve the target even when a same-page old response omits it.
				m.accounts.reply.Jobs[0].TargetAccountID = ""
			}
			m.accounts.focus = 1 // Claude's detached enrollment.
			reopened, status := m.Update(keyEnter)
			if status == nil || reopened.(rootModel).accounts.wizard.targetID != job.TargetAccountID {
				t.Fatal("detached reauthentication lost logical account identity")
			}
			c.reply = protocol.AccountsReply{Revision: 8, Job: &job}
			settled, _ := reopened.Update(status())
			m = settled.(rootModel)
			m.accounts.busy = false
			m = send(m, keyEnter).(rootModel) // Verify -> label.
			m = send(m, keyEnter).(rootModel) // Label -> review.
			_, admit := m.Update(keyEnter)
			if admit == nil {
				t.Fatal("reauthentication review did not submit")
			}
			admit()
			if req := c.requests[len(c.requests)-1]; req.Action != "admit" || req.AccountID != job.TargetAccountID || req.JobID != job.ID {
				t.Fatal("reauthentication admitted a new account instead of its logical target")
			}
		})
	}
}

func TestAccounts_MoveDiscussionRequiresOwnerReviewAndShowsUnmanagedRefusal(t *testing.T) {
	c := newAccountUIClient()
	c.endpointID = "owner"
	c.reply.Accounts = []protocol.AccountView{{ID: "destination", Provider: "claude", Label: "Personal B", State: "available"}}
	m := accountUIOpen(t, c).(rootModel)
	m.general.sessions = []protocol.SessionView{
		{ID: "owner/same-provider", EndpointID: "owner", Agent: "claude", Name: "Current discussion"},
		{ID: "owner/different-provider", EndpointID: "owner", Agent: "codex", Name: "Other provider"},
		{ID: "remote/same-provider", EndpointID: "remote", Agent: "claude", Name: "Remote discussion"},
		{ID: "owner/hidden", EndpointID: "owner", Agent: "claude", Name: "Hidden predecessor", RosterHidden: true},
	}
	m.accounts.focus = 1
	m = send(m, keyEnter).(rootModel)
	for range 4 {
		m = send(m, keyDown).(rootModel)
	}
	m = send(m, keyEnter).(rootModel)
	if !m.accounts.moveOpen || len(m.accounts.moveSessions) != 1 || !strings.Contains(view(m), "Unmanaged discussions are unavailable") {
		t.Fatal("move chooser did not filter provider/owner/visible discussions or explain its source gate")
	}
	before := len(c.requests)
	review, cmd := m.Update(keyEnter)
	if cmd != nil || !review.(rootModel).accounts.moveConfirm || len(c.requests) != before {
		t.Fatal("choosing a discussion skipped owner review")
	}
	back := send(review, keyEsc).(rootModel)
	if back.accounts.moveConfirm || !back.accounts.moveOpen || len(c.requests) != before {
		t.Fatal("cancel review performed a move")
	}
	review = send(back, keyEnter)
	c.accountErr = protocol.ErrAccountMoveUnmanaged
	requesting, request := review.Update(keyEnter)
	if request == nil {
		t.Fatal("confirmed move did not dispatch")
	}
	refused, _ := requesting.Update(request())
	if req := c.requests[len(c.requests)-1]; req.Action != "move" || req.AccountID != "destination" || req.SessionID != "same-provider" || req.ExpectedRevision != 7 {
		t.Fatal("move lost destination, owner-local discussion identity or revision")
	}
	if !strings.Contains(view(refused), "Move refused: unmanaged discussion") || refused.(rootModel).accounts.moveConfirm {
		t.Fatal("unmanaged migration refusal was hidden or left confirmed")
	}
	failed := refused.(rootModel)
	failed.accounts.busy = false
	c.accountErr = errors.New("synthetic-secret-token-and-provider-output")
	retrying, retry := failed.beginAccountsRequest(protocol.AccountsReq{Action: "move", AccountID: "destination", SessionID: "same-provider"})
	refused, _ = retrying.Update(retry())
	if !strings.Contains(view(refused), "could not verify a safe handoff") || strings.Contains(view(refused), "synthetic-secret-token") {
		t.Fatal("move error exposed unsafe provider output")
	}
	c.accountErr = nil
	review = send(refused, keyEnter)
	requesting, request = review.Update(keyEnter)
	moved, _ := requesting.Update(request())
	if moved.(rootModel).accounts.moveOpen || !strings.Contains(view(moved), "Move requested") || len(c.launched) != 0 {
		t.Fatal("accepted move did not return to account details or claimed completion/performed new model work")
	}
}

func TestAccounts_QuotaAgeAndDistinctAvailabilityActions(t *testing.T) {
	c := newAccountUIClient()
	used := 42
	reset := time.Now().Add(time.Hour)
	c.reply.Accounts = []protocol.AccountView{{ID: "a", Provider: "claude", Label: "Personal", CredentialKind: "native", State: "cooling_down", Assigned: 3, RefreshSupported: true, Quota: []protocol.AccountQuotaView{{Label: "Sonnet week", UsedPercent: &used, ResetAt: &reset, ObservedAt: time.Now().Add(-2 * time.Minute)}, {Label: "Opus week"}}}}
	m := accountUIOpen(t, c).(rootModel)
	m.accounts.focus = 1
	m.accounts.detail = true
	text := view(m)
	for _, want := range []string{"42% used", "Opus week: unknown", "last observed: 2m ago", "3 assigned discussions", "cooling down", "Retry availability (unavailable)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("account detail missing %q", want)
		}
	}
	if strings.Contains(text, "Opus week: 0%") {
		t.Fatal("unknown quota shown as zero")
	}
	refreshing, refresh := m.Update(keyRune('r'))
	if refresh == nil {
		t.Fatal("supported refresh missing")
	}
	refresh()
	if req := c.requests[len(c.requests)-1]; req.Action != "refresh" {
		t.Fatal("refresh used retry action")
	}
	_ = refreshing
	m.accounts.detailFocus = 2
	_, retry := m.Update(keyEnter)
	if retry != nil {
		t.Fatal("unsupported retry cleared availability")
	}
	m.accounts.reply.Accounts[0].RetrySupported = true
	_, retry = m.Update(keyEnter)
	if retry == nil {
		t.Fatal("supported explicit availability retry missing")
	}
	retry()
	if req := c.requests[len(c.requests)-1]; req.Action != "retry" {
		t.Fatal("explicit retry used quota refresh")
	}
}

func TestAccounts_StalePageAndClientRepliesCannotMutate(t *testing.T) {
	c := newAccountUIClient()
	m := accountUIOpen(t, c).(rootModel)
	stale := accountsReplyMsg{generation: m.accounts.generation, clientGeneration: m.accountsClientGeneration, action: "list", reply: protocol.AccountsReply{Revision: 99}}
	closed := send(m, keyEsc)
	reopened, load := closed.(rootModel).enterAccounts()
	if load == nil {
		t.Fatal("reopen missing load")
	}
	reopened = send(reopened, stale)
	if reopened.(rootModel).accounts.reply.Revision == 99 {
		t.Fatal("stale prior page reply applied")
	}
	rm := reopened.(rootModel)
	stale.generation = rm.accounts.generation
	rm.accountsClientGeneration++
	changed := send(rm, stale).(rootModel)
	if changed.accounts.reply.Revision == 99 {
		t.Fatal("stale prior client reply applied")
	}
}

func TestAccounts_DisconnectInvalidatesOptionsRPCWithoutDiscardingChoices(t *testing.T) {
	c := newAccountUIClient()
	m := accountUIOpen(t, c).(rootModel)
	old := m.options.contextGuard.generation
	m.options.grouping = groupByRepo
	m.options.contextGuard.autoCompact.Enabled = true
	oldRevision := m.options.contextGuard.revision
	lost := send(m, connectionLostMsg{from: m.events})
	late := send(lost, contextGuardSettingsLoadedMsg{generation: old, settings: protocol.ContextGuardSettings{Revision: 99}}).(rootModel)
	if late.options.contextGuard.revision != oldRevision || late.options.grouping != groupByRepo || !late.options.contextGuard.autoCompact.Enabled {
		t.Fatal("old socket settings reply discarded pending choices or changed revision")
	}
}

func TestAccounts_NativeLoginUsesPrivateRunner(t *testing.T) {
	c := newAccountUIClient()
	m := accountUIOpen(t, c).(rootModel)
	called := false
	m.accountLoginRunner = func(job protocol.AccountEnrollmentView) error { called = job.ID == "private-job"; return nil }
	m.accounts.wizardOpen = true
	m.accounts.wizard = accountWizard{step: accountStepAuthenticate, provider: "claude", method: "native-login", job: protocol.AccountEnrollmentView{ID: "private-job", LoginSocket: "private-socket", State: "authenticating"}}
	if strings.Contains(view(m), "private-socket") {
		t.Fatal("native login socket path exposed")
	}
	next, attach := m.Update(keyEnter)
	if attach == nil || next.(rootModel).screen != screenAccounts {
		t.Fatal("native sign-in entered discussion attach")
	}
	attach()
	if !called || len(c.launched) != 0 {
		t.Fatal("native sign-in created ordinary agent session")
	}
}

func TestAccounts_LongListAndResizePreserveFocus(t *testing.T) {
	c := newAccountUIClient()
	for i := 0; i < 60; i++ {
		c.reply.Accounts = append(c.reply.Accounts, protocol.AccountView{ID: fmt.Sprint(i), Provider: "claude", Label: fmt.Sprintf("Account %02d", i), State: "unknown"})
	}
	m := accountUIOpen(t, c).(rootModel)
	m.width = 48
	m.height = 14
	m.accounts.focus = 60
	text := view(m)
	if !strings.Contains(text, "▌ Account 59") {
		t.Fatal("scrolling list hid focused account")
	}
	for _, line := range strings.Split(text, "\n") {
		if lipgloss.Width(line) > 48 {
			t.Fatal("Accounts overflowed narrow terminal")
		}
	}
	narrow := send(m, tea.WindowSizeMsg{Width: 25, Height: 7}).(rootModel)
	if narrow.accounts.focus != 60 || !strings.Contains(view(narrow), "Resize") || !strings.Contains(view(narrow), "esc back") {
		t.Fatal("small terminal lost focus or escape")
	}
}

func TestAccounts_ReconnectReloadsCapabilitiesAndRejectsOldClientReplies(t *testing.T) {
	c := newAccountUIClient()
	fresh := newAccountUIClient()
	fresh.reply.Revision = 12
	m := accountUIOpen(t, c).(rootModel)
	m.reconnector = func() (Client, error) { return fresh, nil }
	oldGeneration := m.accounts.generation
	lost, _ := m.Update(connectionLostMsg{from: m.events})
	rm := lost.(rootModel)
	if !rm.connectionLost || !rm.reconnecting {
		t.Fatal("connection loss did not begin bounded reconnect")
	}
	dialing, dial := rm.Update(daemonReconnectTickMsg{generation: rm.reconnectGeneration})
	if dial == nil {
		t.Fatal("reconnect timer did not dial")
	}
	reconnected, _ := dialing.Update(dial())
	rm = reconnected.(rootModel)
	if rm.connectionLost || rm.client != fresh || rm.events != fresh.events || !rm.accounts.busy {
		t.Fatal("reconnect did not resubscribe and reload Accounts")
	}
	old := accountsReplyMsg{generation: oldGeneration, clientGeneration: 0, action: "list", reply: protocol.AccountsReply{Revision: 99}}
	if after := send(rm, old).(rootModel); after.accounts.reply.Revision == 99 {
		t.Fatal("old socket reply applied after reconnect")
	}
	staleLoss := send(rm, connectionLostMsg{from: c.events}).(rootModel)
	if staleLoss.connectionLost {
		t.Fatal("old stream loss poisoned replacement client")
	}
	if len(c.launched) != 0 || len(fresh.launched) != 0 {
		t.Fatal("reconnection performed model work")
	}
}

func TestAccounts_ReconnectReconcilesAuthoritativeRosterAndPreservesEditors(t *testing.T) {
	old, fresh := newAccountUIClient(), newAccountUIClient()
	m := accountUIOpen(t, old).(rootModel)
	m.general.apply(protocol.SessionView{ID: "deleted", Agent: "codex", Name: "Old"})
	m.general.apply(protocol.SessionView{ID: "retained", Agent: "claude", Name: "Kept"})
	m.general.grouping = groupByRepo
	m.general.ordering = orderByName
	m.general.restoreSel("retained")
	m.general.editing = true
	m.general.editID = "retained"
	m.general.edit.set("pending rename")
	m.connectionLost, m.reconnecting, m.reconnectGeneration = true, true, 1
	settled, _ := m.applyDaemonReconnected(daemonReconnectedMsg{generation: 1, client: fresh, events: fresh.events, sessions: []protocol.SessionView{{ID: "retained", Agent: "claude", Name: "Kept"}}})
	next := settled.(rootModel)
	if _, present := next.general.sessionByID("deleted"); present {
		t.Fatal("authoritative reconnect retained deleted discussion")
	}
	if next.general.selectedID() != "retained" || next.general.grouping != groupByRepo || next.general.ordering != orderByName || next.general.edit.text != "pending rename" || next.general.editID != "retained" {
		t.Fatal("roster reconciliation lost selection/layout/pending editor")
	}
	late := send(next, eventMsg{from: old.events, ev: protocol.Event{Session: protocol.SessionView{ID: "deleted", Agent: "codex"}}}).(rootModel)
	if _, present := late.general.sessionByID("deleted"); present {
		t.Fatal("old socket event resurrected removed discussion")
	}
	m.connectionLost, m.reconnecting, m.reconnectGeneration = true, true, 2
	empty, _ := m.applyDaemonReconnected(daemonReconnectedMsg{generation: 2, client: fresh, events: fresh.events, sessions: []protocol.SessionView{}})
	if len(empty.(rootModel).general.sessions) != 0 {
		t.Fatal("authoritative empty roster did not remove all stale rows")
	}
	m.connectionLost, m.reconnecting, m.reconnectGeneration = true, true, 3
	unavailable, _ := m.applyDaemonReconnected(daemonReconnectedMsg{generation: 3, client: fresh, events: fresh.events, sessions: nil})
	if len(unavailable.(rootModel).general.sessions) != 2 {
		t.Fatal("failed roster read falsely removed cached rows")
	}
	fresh.listErr = errors.New("synthetic list failure")
	if reconnectRoster(fresh) != nil {
		t.Fatal("failed List was reported as authoritative empty")
	}
	fresh.listErr = nil
	if snapshot := reconnectRoster(fresh); snapshot == nil || len(snapshot) != 0 {
		t.Fatal("successful empty List was not authoritative")
	}
	blocked := &blockingClient{release: make(chan struct{})}
	defer close(blocked.release)
	if reconnectRoster(blocked) != nil {
		t.Fatal("timed-out List was reported as authoritative empty")
	}
}
