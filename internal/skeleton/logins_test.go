package skeleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

type loginFixture struct {
	m             *loginManager
	rows          map[string]persist.Meta
	first, second logins.Login
	runtime       logins.Runtime
	launched      []daemon.LaunchSpec
	released      int
}

func testLoginCredential(id string) logins.Credential {
	return logins.Credential{Auth: json.RawMessage(`{"auth_mode":"chatgpt","tokens":{"account_id":"` + id + `","access_token":"synthetic-access-` + id + `","refresh_token":"synthetic-refresh-` + id + `"}}`)}
}
func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := logins.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	add := func(id string) logins.Login {
		r, _ := store.Snapshot()
		_, l, e := store.Add(r.Revision, logins.ProviderCodex, id, testLoginCredential(id))
		if e != nil {
			t.Fatal(e)
		}
		return l
	}
	f := &loginFixture{rows: map[string]persist.Meta{}, first: add("first"), second: add("second")}
	r, _ := store.Snapshot()
	op, err := store.BeginSwitch(r.Revision, logins.ProviderCodex, f.first.ID, "initial", nil)
	if err != nil {
		t.Fatal(err)
	}
	op.Phase = "stopping"
	if err = store.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	op.Phase = "stopped"
	if err = store.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	if err = store.CaptureOutgoing(op.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.InstallIncoming(op.ID); err != nil {
		t.Fatal(err)
	}
	f.runtime, err = store.CommitSwitch(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteSwitch(op.ID); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{stateDir: state, api: &coreAPI{endpointID: "machine"}}
	w := &authWatcher{stateDir: state, settled: true, state: emptyAuthWatchState(), resolve: func(string, []string) (string, error) { return "/synthetic/codex", nil }}
	w.get = func(id string) (persist.Meta, bool) { v, ok := f.rows[id]; return v, ok }
	w.list = func() []persist.Meta {
		var rows []persist.Meta
		for _, v := range f.rows {
			rows = append(rows, v)
		}
		return rows
	}
	w.kill = func(id string) error {
		v := f.rows[id]
		v.Status.Process = status.ProcessExited
		f.rows[id] = v
		return nil
	}
	w.ready = func(persist.Meta) bool { return true }
	w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		for _, v := range f.rows {
			if v.ResumedFrom == "" && v.Status.Process == status.ProcessRunning {
				t.Fatal("resumed before every outgoing writer stopped")
			}
		}
		if spec.InitialPrompt != "" {
			t.Fatal("replayed a prompt")
		}
		f.launched = append(f.launched, spec)
		id := fmt.Sprintf("child-%d", len(f.launched))
		sourceID := strings.TrimPrefix(spec.Options[protocol.OptionResumeFrom], "machine/")
		source := f.rows[sourceID]
		child := persist.Meta{ID: id, AgentType: spec.AgentType, ConversationID: source.ConversationID, ResumedFrom: sourceID, LoginRuntime: spec.LoginRuntime, CreatedAt: time.Now(), Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}}
		f.rows[id] = child
		return child, nil
	}
	d.authw = w
	f.m = &loginManager{d: d, store: store, qualifyCodexOwnership: func([]persist.Meta) (bool, error) { return false, nil }, stopProof: func(meta persist.Meta) error {
		if meta.Status.Process == status.ProcessRunning {
			return logins.ErrInUse
		}
		return nil
	}, release: func(string, string) error { f.released++; return nil }, recordResumeError: func(id, reason string) error {
		row := f.rows[id]
		row.ResumeError = reason
		f.rows[id] = row
		return nil
	}}
	w.logins = f.m
	d.logins = f.m
	d.api.logins = f.m
	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("source-%d", i)
		now := time.Now().UTC()
		stamp := fmt.Sprintf("%012x", now.UnixMilli())
		conv := stamp[:8] + "-" + stamp[8:] + "-7000-8000-" + fmt.Sprintf("%012d", i)
		cwd := t.TempDir()
		f.rows[id] = persist.Meta{ID: id, Name: id, AgentType: logins.ProviderCodex, ConversationID: conv, LoginRuntime: &f.runtime, Cwd: cwd, CreatedAt: now, Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}, LaunchOptions: map[string]string{"model": "old-launch-model", "sandbox": "workspace-write"}}
		path := filepath.Join(f.runtime.Home, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"), "rollout-"+now.Format("2006-01-02T15-04-05")+"-"+conv+".jsonl")
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": conv, "cwd": cwd, "timestamp": now.UTC().Format(time.RFC3339Nano), "source": "cli"}})
		if err = os.WriteFile(path, append(append(meta, '\n'), []byte("{\"type\":\"turn_context\",\"payload\":{\"model\":\"current-native-model\"}}\n")...), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func driveLoginSwitch(t *testing.T, f *loginFixture) {
	t.Helper()
	for i := 0; i < 16; i++ {
		f.m.step()
		r, err := f.m.store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Switches) == 0 {
			return
		}
	}
	r, _ := f.m.store.Snapshot()
	t.Fatalf("switch did not settle: %+v", r.Switches)
}
func TestProviderLoginSwitchStopsWholeFleetThenResumesExactConversations(t *testing.T) {
	f := newLoginFixture(t)
	op, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.ValidateRuntime(f.runtime); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("ordinary runtime admitted during pending: %v", err)
	}
	if err = f.m.inputGate("source-1"); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("new turn admitted during pending: %v", err)
	}
	driveLoginSwitch(t, f)
	if len(f.launched) != 2 || f.released != 2 {
		t.Fatalf("launches=%d releases=%d", len(f.launched), f.released)
	}
	for _, spec := range f.launched {
		if spec.LoginRuntime.Epoch != op.ToEpoch || spec.Options["model"] != "" || spec.InitialPrompt != "" {
			t.Fatalf("wrong exact continuation: %+v", spec)
		}
	}
	if len(f.m.d.authw.state.Killed) != 0 {
		t.Fatal("provider stops escaped into generic recovery claims")
	}
}
func TestProviderSwitchWaitsForBusyAndPermissionHeldRowsWithoutBlockingControls(t *testing.T) {
	for _, interaction := range []status.Interaction{status.InteractionNone, status.InteractionPermission} {
		t.Run(string(interaction), func(t *testing.T) {
			f := newLoginFixture(t)
			row := f.rows["source-2"]
			if interaction == status.InteractionNone {
				row.Status.Turn = status.TurnActive
			} else {
				row.Status.Interaction = interaction
			}
			f.rows[row.ID] = row
			if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); err != nil {
				t.Fatal(err)
			}
			f.m.step()
			if f.rows[row.ID].Status.Process != status.ProcessRunning || len(f.launched) != 0 {
				t.Fatal("busy/approval-held row was interrupted or early resume occurred")
			}
			if got, err := f.m.store.Snapshot(); err != nil || got.Providers[logins.ProviderCodex].ActiveID != f.first.ID {
				t.Fatal("credentials changed before the busy writer stopped")
			}
			row.Status.Turn = status.TurnIdle
			row.Status.Interaction = status.InteractionNone
			f.rows[row.ID] = row
			driveLoginSwitch(t, f)
		})
	}
}
func TestProviderJournalCrashDoesNotCreateGenericAuthResumes(t *testing.T) {
	f := newLoginFixture(t)
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); err != nil {
		t.Fatal(err)
	}
	f.m.step()
	r, _ := f.m.store.Snapshot()
	op := r.Switches[logins.ProviderCodex]
	if op.Phase != "stopped" {
		t.Fatalf("phase=%s", op.Phase)
	}
	// A new watcher sees only its ordinary claims; the provider's stopped rows
	// stay exclusively in the durable switch journal until incoming auth commits.
	fresh := &authWatcher{stateDir: t.TempDir(), state: emptyAuthWatchState(), get: f.m.d.authw.get, list: f.m.d.authw.list, logins: f.m, launch: func(daemon.LaunchSpec) (persist.Meta, error) {
		t.Fatal("generic watcher resurrected provider claim")
		return persist.Meta{}, nil
	}}
	fresh.state.Pending[logins.ProviderCodex] = []string{"source-1", "source-2"}
	fresh.workPending(logins.ProviderCodex, f.runtime.Identity)
	if len(f.launched) != 0 || len(fresh.state.Killed) != 0 {
		t.Fatal("old account resumed during crash reconciliation")
	}
	driveLoginSwitch(t, f)
}
func TestNoSelectedLoginPreservesOrdinaryLaunchAndSelectedFailureRefuses(t *testing.T) {
	f := newLoginFixture(t)
	_ = f.m.store.Close()
	state, _ := filepath.EvalSymlinks(t.TempDir())
	store, err := logins.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	f.m.store = store
	spec := daemon.LaunchSpec{AgentType: logins.ProviderCodex, ClientEnv: []string{"HOME=/ordinary", "PATH=/bin"}}
	if err = f.m.d.api.bindLoginLaunch(&spec); err != nil || spec.LoginRuntime != nil {
		t.Fatalf("ordinary launch changed: %+v %v", spec, err)
	}
	// A pending initial selection is not an excuse to return to owner credentials.
	_, login, err := store.Add(0, logins.ProviderCodex, "new", testLoginCredential("new"))
	if err != nil {
		t.Fatal(err)
	}
	stateView, _ := store.Snapshot()
	_, err = store.BeginSwitch(stateView.Revision, logins.ProviderCodex, login.ID, "explicit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.d.api.bindLoginLaunch(&spec); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("selected manager failure silently fell back: %v", err)
	}
}

func TestNativeCodexFailureOnlyRotatesTerminalSubscriptionOrKnownAuthFailures(t *testing.T) {
	cases := []struct {
		name, code           string
		retry, auth, limited bool
	}{
		{"terminal subscription", "usageLimitExceeded", false, false, true},
		{"native retry", "usageLimitExceeded", true, false, false},
		{"generic 429", "httpConnectionFailed", false, false, false},
		{"known terminal auth", "unauthorized", false, true, false},
		{"network", "networkConnectionFailed", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginFixture(t)
			conv := f.rows["source-1"].ConversationID
			raw, _ := json.Marshal(map[string]any{"params": map[string]any{"threadId": conv, "willRetry": tc.retry, "error": map[string]string{"codexErrorInfo": tc.code}}})
			f.m.observeNativeFailure("source-1", "trusted-instance", &backendFeed{}, "error", raw)
			r, _ := f.m.store.Snapshot()
			var active logins.Login
			for _, l := range r.Logins {
				if l.ID == f.first.ID {
					active = l
				}
			}
			if active.LimitKnown != tc.limited || (active.AuthState == logins.AuthNeedsLogin) != tc.auth {
				t.Fatalf("wrong native classification: %+v", active)
			}
			f.m.step()
			r, _ = f.m.store.Snapshot()
			op, pending := r.Switches[logins.ProviderCodex]
			if pending != (tc.limited || tc.auth) {
				t.Fatalf("switch pending=%v for %s", pending, tc.name)
			}
			if pending && op.ToID != f.second.ID {
				t.Fatal("did not choose next locally eligible saved login")
			}
		})
	}
}
func TestFailedResumeKeepsItsReasonAfterProviderJournalCompletes(t *testing.T) {
	f := newLoginFixture(t)
	f.m.d.authw.launch = func(daemon.LaunchSpec) (persist.Meta, error) {
		return persist.Meta{}, errors.New("synthetic native resume refused")
	}
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); err != nil {
		t.Fatal(err)
	}
	driveLoginSwitch(t, f)
	for _, id := range []string{"source-1", "source-2"} {
		if !strings.Contains(f.rows[id].ResumeError, "synthetic native resume refused") {
			t.Fatal("terminal resume reason disappeared with the journal")
		}
	}
}
func TestOldProviderEpochFailureCannotExhaustTheNewActiveLogin(t *testing.T) {
	f := newLoginFixture(t)
	source := f.rows["source-1"]
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); err != nil {
		t.Fatal(err)
	}
	driveLoginSwitch(t, f)
	if err := f.m.recordExhaustion(source, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, _ := f.m.store.Snapshot()
	for _, l := range r.Logins {
		if l.ID == f.second.ID && l.LimitKnown {
			t.Fatal("old-epoch failure exhausted destination login")
		}
	}
}

func addFixtureLogin(t *testing.T, f *loginFixture, id string) logins.Login {
	t.Helper()
	r, _ := f.m.store.Snapshot()
	_, login, err := f.m.store.Add(r.Revision, logins.ProviderCodex, id, testLoginCredential(id))
	if err != nil {
		t.Fatal(err)
	}
	return login
}
func startUnreadyDestination(t *testing.T, f *loginFixture) logins.Switch {
	t.Helper()
	if err := f.m.recordExhaustion(f.rows["source-1"], time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.m.d.authw.ready = func(persist.Meta) bool { return false }
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "subscription exhausted"); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 12; attempt++ {
		f.m.step()
		r, _ := f.m.store.Snapshot()
		op := r.Switches[logins.ProviderCodex]
		if op.Phase == "resuming" && len(f.launched) == 2 {
			return op
		}
	}
	t.Fatal("destination did not reach native startup")
	return logins.Switch{}
}
func TestRejectedDestinationRetargetsOneJournalWithoutLosingOriginalConversations(t *testing.T) {
	f := newLoginFixture(t)
	third := addFixtureLogin(t, f, "third")
	original := startUnreadyDestination(t, f)
	if err := f.m.recordAuthFailure(f.rows["child-1"]); err != nil {
		t.Fatal(err)
	}
	f.m.step()
	r, _ := f.m.store.Snapshot()
	retry := r.Switches[logins.ProviderCodex]
	if retry.ID != original.ID || retry.Phase != "retry-stopping" || retry.NextID != third.ID {
		t.Fatalf("wrong provider retry: %+v", retry)
	}
	f.m.d.authw.ready = func(persist.Meta) bool { return true }
	driveLoginSwitch(t, f)
	r, _ = f.m.store.Snapshot()
	if r.Providers[logins.ProviderCodex].ActiveID != third.ID || len(f.launched) != 4 {
		t.Fatalf("original fleet was stranded: provider=%+v launches=%d", r.Providers[logins.ProviderCodex], len(f.launched))
	}
	for _, spec := range f.launched[2:] {
		if spec.LoginRuntime.Epoch != original.ToEpoch+1 || spec.InitialPrompt != "" || !strings.Contains(spec.Options[protocol.OptionResumeFrom], "source-") {
			t.Fatalf("retry did not resume original exact discussion: %+v", spec)
		}
	}
}
func TestRetryWaitsForPermissionWithoutEmbargoingUnclaimedChildControls(t *testing.T) {
	f := newLoginFixture(t)
	addFixtureLogin(t, f, "third")
	startUnreadyDestination(t, f)
	if err := f.m.recordAuthFailure(f.rows["child-1"]); err != nil {
		t.Fatal(err)
	}
	f.m.step()
	child := f.rows["child-1"]
	child.Status.Interaction = status.InteractionPermission
	f.rows[child.ID] = child
	f.m.step()
	r, _ := f.m.store.Snapshot()
	op := r.Switches[logins.ProviderCodex]
	for _, row := range op.Sessions {
		if row.ChildID == child.ID && row.ChildStopClaimed {
			t.Fatal("permission-held child gained a destructive stop claim")
		}
	}
	if f.m.d.composerRecycleInFlight(child.ID) {
		t.Fatal("retry blocked the approval controls needed to reach an idle boundary")
	}
	child.Status.Interaction = status.InteractionNone
	f.rows[child.ID] = child
	f.m.d.authw.ready = func(persist.Meta) bool { return true }
	driveLoginSwitch(t, f)
}
func TestOnlyInvalidDestinationCanBeReauthenticatedUnderTheSameJournal(t *testing.T) {
	f := newLoginFixture(t)
	original := startUnreadyDestination(t, f)
	if err := f.m.recordAuthFailure(f.rows["child-1"]); err != nil {
		t.Fatal(err)
	}
	f.m.step()
	r, _ := f.m.store.Snapshot()
	blocked := r.Switches[logins.ProviderCodex]
	if blocked.ID != original.ID || blocked.LastError == "" {
		t.Fatal("exhausted provider lost its original restart obligations")
	}
	credential := testLoginCredential("second")
	credential.Auth = []byte(strings.ReplaceAll(string(credential.Auth), "synthetic-access-second", "synthetic-fresh-access"))
	job := strings.Repeat("e", 32)
	if _, err := f.m.BeginReloginJob(r.Revision, job, f.second.ID, credential, "native reauthentication"); err != nil {
		t.Fatal(err)
	}
	f.m.d.authw.ready = func(persist.Meta) bool { return true }
	driveLoginSwitch(t, f)
	r, _ = f.m.store.Snapshot()
	if r.Providers[logins.ProviderCodex].ActiveID != f.second.ID || r.Providers[logins.ProviderCodex].Epoch != original.ToEpoch+1 || len(f.launched) != 4 {
		t.Fatalf("same-login reauthentication lost the fleet: %+v launches=%d", r.Providers[logins.ProviderCodex], len(f.launched))
	}
}
func TestOwnerEndedReadyChildDoesNotReappearAfterProviderRetry(t *testing.T) {
	f := newLoginFixture(t)
	addFixtureLogin(t, f, "third")
	startUnreadyDestination(t, f)
	// One child has become ready while another is still starting.
	f.m.d.authw.ready = func(meta persist.Meta) bool { return meta.ID == "child-1" }
	f.m.step()
	if err := f.m.OwnerEnd("child-1", "kill"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.recordAuthFailure(f.rows["child-2"]); err != nil {
		t.Fatal(err)
	}
	f.m.d.authw.ready = func(persist.Meta) bool { return true }
	driveLoginSwitch(t, f)
	if len(f.launched) != 3 {
		t.Fatalf("owner-ended discussion was resurrected: launches=%d", len(f.launched))
	}
}

func TestOwnerLoginActionsRejectAStaleRevisionWithoutChangingTheJournal(t *testing.T) {
	f := newLoginFixture(t)
	before, _ := f.m.store.Snapshot()
	addFixtureLogin(t, f, "third")
	if _, err := f.m.BeginSwitchAtRevision(before.Revision, logins.ProviderCodex, f.second.ID, "stale owner action"); !errors.Is(err, logins.ErrConflict) {
		t.Fatalf("stale activation accepted: %v", err)
	}
	current, _ := f.m.store.Snapshot()
	if len(current.Switches) != 0 {
		t.Fatal("stale activation created a provider barrier")
	}
	if _, err := f.m.BeginSwitchAtRevision(current.Revision, logins.ProviderCodex, f.second.ID, "current owner action"); err != nil {
		t.Fatal(err)
	}
	pending, _ := f.m.store.Snapshot()
	if err := f.m.CancelSwitchAtRevision(current.Revision, logins.ProviderCodex); !errors.Is(err, logins.ErrConflict) {
		t.Fatalf("stale cancellation accepted: %v", err)
	}
	if _, err := f.m.BeginReloginAtRevision(current.Revision, f.second.ID, testLoginCredential("second"), "stale owner sign-in"); !errors.Is(err, logins.ErrConflict) {
		t.Fatalf("stale reauthentication accepted: %v", err)
	}
	after, _ := f.m.store.Snapshot()
	if after.Revision != pending.Revision || after.Switches[logins.ProviderCodex].ID != pending.Switches[logins.ProviderCodex].ID {
		t.Fatal("stale owner action mutated the provider journal")
	}
}

func TestRetryCrashRestoresOnlyDurablyClaimedChildFences(t *testing.T) {
	f := newLoginFixture(t)
	addFixtureLogin(t, f, "third")
	startUnreadyDestination(t, f)
	if err := f.m.recordAuthFailure(f.rows["child-1"]); err != nil {
		t.Fatal(err)
	}
	f.m.step()
	kill := f.m.d.authw.kill
	f.m.d.authw.kill = func(string) error { return errors.New("synthetic crash after durable stop claim") }
	f.m.step()
	r, _ := f.m.store.Snapshot()
	op := r.Switches[logins.ProviderCodex]
	claimed := ""
	for _, row := range op.Sessions {
		if row.ChildStopClaimed {
			claimed = row.ChildID
		}
	}
	if claimed == "" {
		t.Fatal("retry did not persist its stop claim before signaling")
	}
	// A restarted daemon starts with no in-memory lifecycle lanes.
	f.m.d.composerLanes.Clear()
	if err := f.m.restoreClaims(); err != nil {
		t.Fatal(err)
	}
	for _, row := range op.Sessions {
		if f.m.d.composerRecycleInFlight(row.ChildID) != row.ChildStopClaimed {
			t.Fatalf("restored child fence without a matching durable claim: %+v", row)
		}
	}
	f.m.d.authw.kill = kill
	f.m.d.authw.ready = func(persist.Meta) bool { return true }
	driveLoginSwitch(t, f)
	if len(f.launched) != 4 {
		t.Fatalf("crash lost or duplicated original resume obligations: launches=%d", len(f.launched))
	}
}
