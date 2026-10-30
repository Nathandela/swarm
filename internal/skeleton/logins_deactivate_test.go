package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func deactivateFixture(t *testing.T, f *loginFixture) {
	t.Helper()
	op, err := f.m.BeginDeactivate(logins.ProviderCodex)
	if err != nil || !op.Deactivate {
		t.Fatalf("deactivation not started: %+v %v", op, err)
	}
	driveLoginSwitch(t, f)
}

func assertDeactivated(t *testing.T, f *loginFixture, ids ...string) {
	t.Helper()
	r, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Switches) != 0 || r.Providers[logins.ProviderCodex].ActiveID != "" {
		t.Fatalf("selection not cleared: %+v %+v", r.Providers, r.Switches)
	}
	if len(f.launched) != 0 || f.released != 0 {
		t.Fatalf("deactivation resumed discussions: launches=%d releases=%d", len(f.launched), f.released)
	}
	for _, id := range ids {
		if f.rows[id].Status.Process == status.ProcessRunning {
			t.Fatalf("%s still running", id)
		}
		if f.m.d.composerRecycleInFlight(id) {
			t.Fatalf("%s kept its stop claim", id)
		}
	}
}

func TestDeactivateStopsRunningDiscussionsWithoutResume(t *testing.T) {
	f := newLoginFixture(t)
	op, err := f.m.BeginDeactivate(logins.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if !op.Deactivate || op.ToID != "" || op.Reason != "owner returned to native sign-in" || len(op.Sessions) != 2 || op.Sessions[0].SourceID != "source-1" || op.Sessions[1].SourceID != "source-2" {
		t.Fatalf("bad deactivation journal: %+v", op)
	}
	f.m.d.authw.launch = func(daemon.LaunchSpec) (persist.Meta, error) {
		t.Fatal("deactivation launched a discussion")
		return persist.Meta{}, nil
	}
	// Watch the committed journal mark every row cancelled before it disappears.
	for i := 0; i < 16; i++ {
		before, _ := f.m.store.Snapshot()
		f.m.step()
		after, _ := f.m.store.Snapshot()
		if _, ok := after.Switches[logins.ProviderCodex]; !ok {
			last := before.Switches[logins.ProviderCodex]
			if last.Phase != "committed" {
				t.Fatalf("completed from phase %q", last.Phase)
			}
			for _, row := range last.Sessions {
				if row.Phase != "cancelled" || row.LastError != "ended by return to native sign-in" {
					t.Fatalf("row not durably ended before completion: %+v", row)
				}
			}
			break
		}
		if cur := after.Switches[logins.ProviderCodex]; cur.Phase == "resuming" {
			t.Fatal("deactivation entered resume")
		}
	}
	assertDeactivated(t, f, "source-1", "source-2")
}

func TestDeactivateCompletionReplayIsSafe(t *testing.T) {
	f := newLoginFixture(t)
	if _, err := f.m.BeginDeactivate(logins.ProviderCodex); err != nil {
		t.Fatal(err)
	}
	var committed logins.Switch
	for i := 0; i < 16 && committed.ID == ""; i++ {
		f.m.step()
		r, _ := f.m.store.Snapshot()
		if op := r.Switches[logins.ProviderCodex]; op.Phase == "committed" {
			committed = op
		}
	}
	if committed.ID == "" {
		t.Fatal("deactivation did not commit")
	}
	// Crash after one row was durably cancelled, before completion.
	committed.Sessions[0].Phase = "cancelled"
	if err := f.m.store.SaveSwitch(committed); err != nil {
		t.Fatal(err)
	}
	driveLoginSwitch(t, f)
	assertDeactivated(t, f, "source-1", "source-2")
}

func TestDeactivateSkipsContextPreflightAndImport(t *testing.T) {
	f := newLoginFixture(t)
	f.m.preflight = func(string, []persist.Meta) error { t.Fatal("context preflight ran"); return nil }
	f.m.prepareContext = func(string, []persist.Meta) error { t.Fatal("context import ran"); return nil }
	// Nothing is resumed, so a discussion whose CLI no longer resolves still stops.
	f.m.d.authw.resolve = func(string, []string) (string, error) { return "", errors.New("synthetic unresolved CLI") }
	deactivateFixture(t, f)
	assertDeactivated(t, f, "source-1", "source-2")
}

func TestDeactivateRefusedWhileEarlierRestartReconciles(t *testing.T) {
	f := newLoginFixture(t)
	f.m.d.authw.state.Killed["source-1"] = true
	if _, err := f.m.BeginDeactivate(logins.ProviderCodex); err == nil || !strings.Contains(err.Error(), "still being reconciled") {
		t.Fatalf("deactivation ignored an unreconciled restart: %v", err)
	}
	if r, _ := f.m.store.Snapshot(); len(r.Switches) != 0 {
		t.Fatal("refused deactivation left a journal")
	}
}

func TestDeactivateWaitsForBusyOrAttachedDiscussion(t *testing.T) {
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
			if _, err := f.m.BeginDeactivate(logins.ProviderCodex); err != nil {
				t.Fatal(err)
			}
			f.m.step()
			f.m.step()
			r, _ := f.m.store.Snapshot()
			op := r.Switches[logins.ProviderCodex]
			if op.Phase != "stopping" || !strings.Contains(op.LastError, `waiting for discussion "source-2" to finish or detach`) {
				t.Fatalf("busy discussion did not hold the journal: %+v", op)
			}
			if f.rows[row.ID].Status.Process != status.ProcessRunning || r.Providers[logins.ProviderCodex].ActiveID != f.first.ID {
				t.Fatal("busy discussion interrupted or selection cleared early")
			}
			row.Status.Turn = status.TurnIdle
			row.Status.Interaction = status.InteractionNone
			f.rows[row.ID] = row
			driveLoginSwitch(t, f)
			assertDeactivated(t, f, "source-1", "source-2")
		})
	}
}

func TestDeactivateRowWithoutConversationIDDoesNotBlock(t *testing.T) {
	f := newLoginFixture(t)
	fresh := f.rows["source-1"]
	fresh.ConversationID = ""
	f.rows[fresh.ID] = fresh
	late := f.rows["source-2"]
	late.ConversationID = ""
	late.Status.Turn = status.TurnActive
	f.rows[late.ID] = late
	op, err := f.m.BeginDeactivate(logins.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range op.Sessions {
		if row.ConversationID != "" {
			t.Fatalf("row invented a conversation: %+v", row)
		}
	}
	f.m.step()
	// The busy discussion's native conversation appears after the journal began.
	late.ConversationID = "019a0000-0000-7000-8000-000000000009"
	late.Status.Turn = status.TurnIdle
	f.rows[late.ID] = late
	driveLoginSwitch(t, f)
	assertDeactivated(t, f, "source-1", "source-2")
}

func TestDeactivateNoopWithoutActiveLogin(t *testing.T) {
	f := newLoginFixture(t)
	deactivateFixture(t, f)
	before, _ := f.m.store.Snapshot()
	op, err := f.m.BeginDeactivate(logins.ProviderCodex)
	if err != nil || op.ID != "" {
		t.Fatalf("deactivation without a selection: %+v %v", op, err)
	}
	if op, err = f.m.BeginDeactivate(logins.ProviderClaude); err != nil || op.ID != "" {
		t.Fatalf("deactivation of an unselected provider: %+v %v", op, err)
	}
	if after, _ := f.m.store.Snapshot(); after.Revision != before.Revision || len(after.Switches) != 0 {
		t.Fatal("no-op deactivation changed the registry")
	}
	if _, err = f.m.BeginDeactivate("opencode"); !errors.Is(err, logins.ErrIneligible) {
		t.Fatalf("unmanaged provider admitted: %v", err)
	}
}

func TestInputGateRefusesDuringDeactivation(t *testing.T) {
	f := newLoginFixture(t)
	if _, err := f.m.BeginDeactivate(logins.ProviderCodex); err != nil {
		t.Fatal(err)
	}
	err := f.m.inputGate("source-1")
	if !errors.Is(err, logins.ErrPending) || !strings.Contains(err.Error(), "codex is returning to native sign-in; this message was not sent") {
		t.Fatalf("deactivation input refusal: %v", err)
	}
}

func TestInputGateKeepsSwitchMessage(t *testing.T) {
	f := newLoginFixture(t)
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.inputGate("source-1"); !errors.Is(err, logins.ErrPending) || !strings.Contains(err.Error(), "codex is waiting to change its login; this message was not sent") {
		t.Fatalf("switch input refusal: %v", err)
	}
}

func TestLaunchAfterDeactivateUsesNativeHome(t *testing.T) {
	f := newLoginFixture(t)
	pending := daemon.LaunchSpec{AgentType: logins.ProviderCodex, ClientEnv: []string{"HOME=/ordinary", "PATH=/bin"}}
	if _, err := f.m.BeginDeactivate(logins.ProviderCodex); err != nil {
		t.Fatal(err)
	}
	if err := f.m.d.api.bindLoginLaunch(&pending); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("launch admitted while deactivating: %v", err)
	}
	driveLoginSwitch(t, f)
	spec := daemon.LaunchSpec{AgentType: logins.ProviderCodex, ClientEnv: []string{"HOME=/ordinary", "PATH=/bin"}}
	if err := f.m.d.api.bindLoginLaunch(&spec); err != nil || spec.LoginRuntime != nil || spec.LoginStateRoot != "" || spec.AuthIdentity != "" {
		t.Fatalf("launch still bound to the managed home: %+v %v", spec, err)
	}
	prepared, err := f.m.d.prepareLoginLaunch("", spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range prepared.ClientEnv {
		for _, key := range []string{"CLAUDE_CONFIG_DIR=", "CODEX_HOME=", "CLAUDE_SECURESTORAGE_CONFIG_DIR="} {
			if strings.HasPrefix(entry, key) {
				t.Fatalf("Swarm injected %s", entry)
			}
		}
	}
	if !slices.Equal(prepared.ClientEnv, []string{"HOME=/ordinary", "PATH=/bin"}) {
		t.Fatalf("native launch environment changed: %v", prepared.ClientEnv)
	}
}

func TestActivateAndReloginWhileDeactivatingArePending(t *testing.T) {
	f := newLoginFixture(t)
	row := f.rows["source-2"]
	row.Status.Turn = status.TurnActive
	f.rows[row.ID] = row
	op, err := f.m.BeginDeactivate(logins.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	f.m.step()
	r, _ := f.m.store.Snapshot()
	for name, call := range map[string]func() error{
		"activate": func() error { _, e := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "owner"); return e },
		"activate-at-revision": func() error {
			_, e := f.m.BeginSwitchAtRevision(r.Revision, logins.ProviderCodex, f.second.ID, "owner")
			return e
		},
		"relogin": func() error { _, e := f.m.BeginRelogin(f.first.ID, testLoginCredential("first"), "owner"); return e },
		"relogin-at-revision": func() error {
			_, e := f.m.BeginReloginAtRevision(r.Revision, f.first.ID, testLoginCredential("first"), "owner")
			return e
		},
		"relogin-job": func() error {
			_, e := f.m.BeginReloginJob(r.Revision, strings.Repeat("e", 32), f.first.ID, testLoginCredential("first"), "owner")
			return e
		},
		"deactivate": func() error { _, e := f.m.BeginDeactivate(logins.ProviderCodex); return e },
	} {
		if err := call(); !errors.Is(err, logins.ErrPending) {
			t.Errorf("%s admitted during deactivation: %v", name, err)
		}
	}
	after, _ := f.m.store.Snapshot()
	if got := after.Switches[logins.ProviderCodex]; got.ID != op.ID || !got.Deactivate || after.Revision != r.Revision {
		t.Fatalf("refused call changed the journal: %+v", got)
	}
}

func TestDeactivateRefusedWhileSwitchPending(t *testing.T) {
	f := newLoginFixture(t)
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.BeginDeactivate(logins.ProviderCodex); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("deactivation admitted beside a switch: %v", err)
	}
}

func TestActivateFromNativeRefusesRunningNativeBoundDiscussionsAfterImport(t *testing.T) {
	f := newLoginFixture(t)
	marker := filepath.Join(f.m.d.stateDir, "logins", "provider", logins.ProviderCodex, "native", ".swarm-provider-context.json")
	native := func(id string, process status.Process) {
		f.rows[id] = persist.Meta{ID: id, Name: id, AgentType: logins.ProviderCodex, ConversationID: "019a0000-0000-7000-8000-0000000000" + id[len(id)-2:], Cwd: t.TempDir(), Status: status.Status{Process: process, Turn: status.TurnIdle, Interaction: status.InteractionNone}}
	}
	cancel := func() {
		t.Helper()
		if err := f.m.CancelSwitch(logins.ProviderCodex); err != nil {
			t.Fatal(err)
		}
		if r, _ := f.m.store.Snapshot(); len(r.Switches) != 0 {
			t.Fatal("untouched switch did not disappear")
		}
	}
	if err := os.WriteFile(marker, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	// A->B switches are unchanged by native-bound discussions.
	native("native-b1", status.ProcessRunning)
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "owner"); err != nil {
		t.Fatalf("A->B switch refused: %v", err)
	}
	cancel()
	delete(f.rows, "native-b1")
	deactivateFixture(t, f)
	native("native-b2", status.ProcessRunning)
	native("native-a1", status.ProcessRunning)
	native("native-c3", status.ProcessExited)
	_, err := f.m.BeginSwitch(logins.ProviderCodex, f.first.ID, "owner")
	want := `login switch: end or hand off these native sign-in discussions before activating a login: "native-a1", "native-b2"`
	if err == nil || err.Error() != want {
		t.Fatalf("activation from native sign-in: %v", err)
	}
	if r, _ := f.m.store.Snapshot(); len(r.Switches) != 0 || r.Providers[logins.ProviderCodex].ActiveID != "" {
		t.Fatal("refused activation changed the registry")
	}
	// Only ended native-bound discussions remain: activation proceeds.
	for _, id := range []string{"native-a1", "native-b2"} {
		row := f.rows[id]
		row.Status.Process = status.ProcessExited
		f.rows[id] = row
	}
	if _, err = f.m.BeginSwitch(logins.ProviderCodex, f.first.ID, "owner"); err != nil {
		t.Fatalf("activation refused with only ended native discussions: %v", err)
	}
	cancel()
	// Without the import marker the first-ever activation is unchanged.
	if err = os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	native("native-a1", status.ProcessRunning)
	if _, err = f.m.BeginSwitch(logins.ProviderCodex, f.first.ID, "owner"); err != nil {
		t.Fatalf("first activation refused: %v", err)
	}
}

// v0.25 migrated discussions are bound to the managed home by their environment,
// so they are not named as native sign-in discussions.
func TestActivateFromNativeDoesNotNameMigratedDiscussions(t *testing.T) {
	f := newLoginFixture(t)
	home := filepath.Join(f.m.d.stateDir, "logins", "provider", logins.ProviderCodex, "native")
	if err := os.WriteFile(filepath.Join(home, ".swarm-provider-context.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	deactivateFixture(t, f)
	for id, env := range map[string][]string{"native-a1": {"HOME=/ordinary"}, "migrated-b2": {"HOME=/ordinary", "CODEX_HOME=" + home}} {
		f.rows[id] = persist.Meta{ID: id, Name: id, AgentType: logins.ProviderCodex, ConversationID: "019a0000-0000-7000-8000-0000000000" + id[len(id)-2:], Cwd: t.TempDir(), Env: env, Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}}
	}
	_, err := f.m.BeginSwitch(logins.ProviderCodex, f.first.ID, "owner")
	if want := `login switch: end or hand off these native sign-in discussions before activating a login: "native-a1"`; err == nil || err.Error() != want {
		t.Fatalf("activation from native sign-in: %v", err)
	}
}

func TestDeactivateResumesAfterDaemonRestart(t *testing.T) {
	f := newLoginFixture(t)
	if _, err := f.m.BeginDeactivate(logins.ProviderCodex); err != nil {
		t.Fatal(err)
	}
	kill := f.m.d.authw.kill
	f.m.d.authw.kill = func(string) error { return errors.New("synthetic crash after durable stop claim") }
	f.m.step()
	r, _ := f.m.store.Snapshot()
	var claimed []string
	for _, row := range r.Switches[logins.ProviderCodex].Sessions {
		if row.Phase == "claimed" {
			claimed = append(claimed, row.SourceID)
		}
	}
	if len(claimed) == 0 {
		t.Fatalf("no durable stop claim: %+v", r.Switches)
	}
	// A restarted daemon has no in-memory lanes and a fresh manager.
	f.m.d.composerLanes.Clear()
	old := f.m
	f.m = &loginManager{d: old.d, store: old.store, stopProof: old.stopProof, release: old.release, recordResumeError: old.recordResumeError}
	f.m.d.logins, f.m.d.authw.logins, f.m.d.api.logins = f.m, f.m, f.m
	if err := f.m.restoreClaims(); err != nil {
		t.Fatal(err)
	}
	for _, id := range claimed {
		if !f.m.d.composerRecycleInFlight(id) {
			t.Fatalf("claim on %s not restored", id)
		}
	}
	// Cancellation after a claim still completes the return to native sign-in.
	if err := f.m.CancelSwitch(logins.ProviderCodex); err != nil {
		t.Fatal(err)
	}
	f.m.d.authw.kill = kill
	driveLoginSwitch(t, f)
	assertDeactivated(t, f, "source-1", "source-2")
}

func TestLoginsDeactivateProtocol(t *testing.T) {
	f := newLoginFixture(t)
	a := f.m.d.api
	r, _ := f.m.store.Snapshot()
	if _, err := a.Logins(protocol.LoginsReq{Action: "deactivate", Provider: logins.ProviderCodex, ExpectedRevision: r.Revision - 1}); !errors.Is(err, protocol.ErrLoginsStaleRevision) {
		t.Fatalf("stale deactivation: %v", err)
	}
	for _, provider := range []string{"", "opencode"} {
		if _, err := a.Logins(protocol.LoginsReq{Action: "deactivate", Provider: provider, ExpectedRevision: r.Revision}); !errors.Is(err, logins.ErrIneligible) {
			t.Fatalf("provider %q admitted: %v", provider, err)
		}
	}
	if after, _ := f.m.store.Snapshot(); len(after.Switches) != 0 {
		t.Fatal("refused deactivation left a journal")
	}
	row := f.rows["source-2"]
	row.Status.Turn = status.TurnActive
	f.rows[row.ID] = row
	reply, err := a.Logins(protocol.LoginsReq{Action: "deactivate", Provider: logins.ProviderCodex, ExpectedRevision: r.Revision})
	if err != nil {
		t.Fatal(err)
	}
	var view protocol.ProviderLoginView
	for _, v := range reply.Providers {
		if v.Provider == logins.ProviderCodex {
			view = v
		}
	}
	if view.State != "switching" || view.Message != "Returning to native sign-in: waiting for discussions to reach a safe point and detach; they will then end." {
		t.Fatalf("pending deactivation view: %+v", view)
	}
	f.m.step()
	f.m.step()
	reply, err = a.Logins(protocol.LoginsReq{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range reply.Providers {
		if v.Provider == logins.ProviderCodex && (v.State != "switching" || !strings.Contains(v.Message, "waiting for discussion")) {
			t.Fatalf("journal error did not take precedence: %+v", v)
		}
	}
	row.Status.Turn = status.TurnIdle
	f.rows[row.ID] = row
	driveLoginSwitch(t, f)
	reply, err = a.Logins(protocol.LoginsReq{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range reply.Providers {
		if v.Provider == logins.ProviderCodex && (v.State != "native" || v.ActiveID != "") {
			t.Fatalf("provider not back on native sign-in: %+v", v)
		}
	}
}

// stopOnlyFixture clears the selection the way an owner did by hand before
// deactivation existed: discussions keep running in the managed home and the
// Codex grant copy stays there.
func stopOnlyFixture(t *testing.T) (*loginFixture, string) {
	t.Helper()
	f := newLoginFixture(t)
	deactivateFixture(t, f)
	home := filepath.Join(f.m.d.stateDir, "logins", "provider", logins.ProviderCodex, "native")
	running := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	source := f.rows["source-1"]
	source.Status = running
	f.rows[source.ID] = source
	for id, env := range map[string][]string{"migrated-1": {"HOME=/ordinary", "CODEX_HOME=" + home}, "native-1": {"HOME=/ordinary"}} {
		f.rows[id] = persist.Meta{ID: id, Name: id, AgentType: logins.ProviderCodex, Cwd: t.TempDir(), Env: env, Status: running}
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"leftover-grant"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return f, home
}

func TestDeactivateStopOnlyStopsManagedBoundDiscussions(t *testing.T) {
	f, home := stopOnlyFixture(t)
	before, _ := f.m.store.Snapshot()
	op, err := f.m.BeginDeactivate(logins.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if !op.Deactivate || op.FromID != "" || len(op.Sessions) != 2 || op.Sessions[0].SourceID != "migrated-1" || op.Sessions[1].SourceID != "source-1" {
		t.Fatalf("bad stop-only journal: %+v", op)
	}
	driveLoginSwitch(t, f)
	assertDeactivated(t, f, "migrated-1", "source-1")
	if f.rows["native-1"].Status.Process != status.ProcessRunning {
		t.Fatal("stop-only deactivation stopped a native-bound discussion")
	}
	after, _ := f.m.store.Snapshot()
	if after.Providers[logins.ProviderCodex].Epoch != before.Providers[logins.ProviderCodex].Epoch+1 {
		t.Fatalf("epoch not advanced: %+v -> %+v", before.Providers, after.Providers)
	}
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || strings.Contains(string(raw), "leftover-grant") || strings.Contains(string(raw), "tokens") {
		t.Fatalf("leftover Codex grant not scrubbed: %s %v", raw, err)
	}
}

func TestDeactivateStopOnlyNoopWithOnlyNativeBound(t *testing.T) {
	f, _ := stopOnlyFixture(t)
	delete(f.rows, "migrated-1")
	delete(f.rows, "source-1")
	before, _ := f.m.store.Snapshot()
	op, err := f.m.BeginDeactivate(logins.ProviderCodex)
	if err != nil || op.ID != "" {
		t.Fatalf("native-bound discussions started a deactivation: %+v %v", op, err)
	}
	if after, _ := f.m.store.Snapshot(); after.Revision != before.Revision || len(after.Switches) != 0 {
		t.Fatal("no-op deactivation changed the registry")
	}
}

// Native-bound discussions never write the managed home, so a deactivation does
// not wait for them; managed-bound ones and normal switches stay strict.
func TestVerifyProviderWritersIgnoresNativeBoundOnDeactivation(t *testing.T) {
	f, home := stopOnlyFixture(t)
	delete(f.rows, "migrated-1")
	delete(f.rows, "source-1")
	delete(f.rows, "source-2")
	f.m.stopProof = nil
	deactivation := logins.Switch{Provider: logins.ProviderCodex, Deactivate: true}
	if err := f.m.verifyProviderWriters(deactivation); err != nil {
		t.Fatalf("native-bound discussion blocked deactivation: %v", err)
	}
	if err := f.m.verifyProviderWriters(logins.Switch{Provider: logins.ProviderCodex, ToID: f.second.ID}); !errors.Is(err, logins.ErrInUse) {
		t.Fatalf("normal switch ignored a running discussion: %v", err)
	}
	f.rows["migrated-1"] = persist.Meta{ID: "migrated-1", Name: "migrated-1", AgentType: logins.ProviderCodex, Env: []string{"CODEX_HOME=" + home}, Status: status.Status{Process: status.ProcessRunning}}
	if err := f.m.verifyProviderWriters(deactivation); !errors.Is(err, logins.ErrInUse) {
		t.Fatalf("deactivation ignored a managed-bound writer: %v", err)
	}
}

func TestLoginListCountsManagedRunningWithoutActiveLogin(t *testing.T) {
	f := newLoginFixture(t)
	count := func() (claude, codex int) {
		t.Helper()
		reply, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "list"})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range reply.Providers {
			if v.Provider == logins.ProviderCodex {
				codex = v.ManagedRunning
			} else {
				claude = v.ManagedRunning
			}
		}
		return
	}
	if claude, codex := count(); claude != 0 || codex != 0 {
		t.Fatalf("active login reported managed running: %d %d", claude, codex)
	}
	f, _ = stopOnlyFixture(t)
	hidden := f.rows["migrated-1"]
	hidden.ID, hidden.RosterHidden = "hidden-1", true
	f.rows[hidden.ID] = hidden
	if claude, codex := count(); claude != 0 || codex != 2 {
		t.Fatalf("managed running = %d %d, want 0 2", claude, codex)
	}
}
