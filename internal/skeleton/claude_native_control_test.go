package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func nativeControlUnsafeFixture(t *testing.T, runtime string) (*Daemon, persist.Meta, *persist.Store) {
	t.Helper()
	root, err := os.MkdirTemp("", "s32-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := a2QuarantineMeta(t, root, "retained", false)
	m.RequiredRuntime = runtime
	if err := store.Save(m); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "d"), LockPath: filepath.Join(root, "l"), LogPath: filepath.Join(root, "log"), MaxSessions: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	idle := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	if err := core.SetStatus(m.ID, idle); err != nil {
		t.Fatal(err)
	}
	m, _ = core.Get(m.ID)
	return &Daemon{core: core, stateDir: root}, m, store
}

func TestClaudeNativeControlRestoredResumeNeverLosesRuntimeVeto(t *testing.T) {
	for _, kind := range []string{"account", "cli"} {
		t.Run(kind, func(t *testing.T) {
			d, m, _ := nativeControlUnsafeFixture(t, "claude-native-control")
			m.Status.Process = status.ProcessExited
			if err := d.core.SetStatus(m.ID, m.Status); err != nil {
				t.Fatal(err)
			}
			d.restoreAuthRecycle(m.ID)
			directEffects := 0
			attempted, retry := d.withAuthResumeFence(m.ID, func() bool { directEffects++; return false })
			if attempted || !retry || directEffects != 0 {
				t.Errorf("restored fence reached successor constructor: attempted=%t retry=%t effects=%d", attempted, retry, directEffects)
			}
			fake := newAuthFake(identityB)
			fake.add(m)
			watcher := testWatcher(t, fake)
			watcher.withResumeFence = d.withAuthResumeFence
			watcher.state.Killed[m.ID] = true
			watcher.state.Candidates = map[string]string{}
			watcher.state.Retries = map[string]recycleRetry{}
			launches := 0
			watcher.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
				launches++
				if spec.RequiredRuntime != "" {
					t.Error("regression fixture must reach the existing runtime-stripped successor path")
				}
				return persist.Meta{}, errors.New("unexpected successor constructor")
			}
			if kind == "cli" {
				target := *m.CLIIdentity
				target.Version = "2.1.311"
				watcher.state.CLI = map[string]cliRefreshRecord{m.ID: {AgentType: "claude", Target: target, State: cliRefreshPending}}
				watcher.cliProbe = func(string, string, []string, string) (*persist.CLIIdentity, error) { return &target, nil }
			}
			if !watcher.resumeClaimed("claude", m) {
				t.Error("automatic hold discarded the owed claim")
			}
			if launches != 0 || len(fake.killed)+len(fake.deleted) != 0 {
				t.Fatalf("restored %s obligation performed session effects: launches=%d", kind, launches)
			}
		})
	}
}

func TestClaudeNativeControlRestoredAutomaticClaimLeavesManualActions(t *testing.T) {
	d, m, _ := nativeControlUnsafeFixture(t, "claude-native-control")
	d.restoreAuthRecycle(m.ID)
	manual := 0
	err := d.withOwnerSessionEnd(m.ID, func() error { manual++; return nil })
	if err != nil || manual != 1 {
		t.Fatalf("unqualified automatic claim blocked explicit owner action: effects=%d err=%v", manual, err)
	}
}

func TestClaudeNativeControlRestoredClaimPreservesOwnerStopDelete(t *testing.T) {
	for _, action := range []string{"stop", "delete"} {
		t.Run(action, func(t *testing.T) {
			d, m, store := nativeControlUnsafeFixture(t, "claude-native-control")
			m.Status.Process = status.ProcessExited
			if err := d.core.SetStatus(m.ID, m.Status); err != nil {
				t.Fatal(err)
			}
			if action == "delete" {
				// This fixture only created metadata; no native writer was ever born.
				if _, err := store.RetireClaudeReservation(m.ID, func() error { return nil }); err != nil {
					t.Fatal("no-writer fixture retirement prerequisite", err)
				}
			}
			d.restoreAuthRecycle(m.ID)
			var err error
			if action == "stop" {
				err = d.ownerKill(m.ID)
			} else {
				err = d.ownerDelete(m.ID)
			}
			if err != nil {
				t.Fatalf("explicit owner %s was stranded by restored automatic claim: %v", action, err)
			}
			if action == "delete" {
				if _, exists := d.core.Get(m.ID); exists {
					t.Fatal("owner Delete did not remove ended no-writer fixture")
				}
			}
			launched := 0
			attempted, retry := d.withAuthResumeFence(m.ID, func() bool { launched++; return true })
			if attempted || retry || launched != 0 {
				t.Fatalf("owner action did not cancel restored successor: attempted=%t retry=%t launches=%d", attempted, retry, launched)
			}
		})
	}
}

func TestClaudeNativeControlAutomaticConsumersAlwaysUnsafe(t *testing.T) {
	for _, kind := range []string{"missing-store-engine", "missing-record", "malformed-record", "stale-idle"} {
		t.Run(kind, func(t *testing.T) {
			d, m, store := nativeControlUnsafeFixture(t, "claude-native-control")
			if kind != "missing-store-engine" {
				d.eng = engine.New(engine.Config{})
				d.executionStore = store
			}
			if kind == "malformed-record" {
				if err := os.WriteFile(filepath.Join(d.stateDir, m.ID, "execution.json"), []byte(`{"version":999,"incomplete":""}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "stale-idle" {
				d.eng.RegisterSession(m.ID, "token", 0, nil, m.Status)
			}
			if !d.executionCustodyUnsafe(m.ID) {
				t.Fatal("new runtime granted automatic effects with missing or idle evidence")
			}
			if d.contextGuardQuiet(m.ID) {
				t.Fatal("ContextGuard accepted the runtime's forged Idle status")
			}
			fake := newAuthFake(identityB)
			m.AuthIdentity = identityA
			fake.add(m)
			watcher := testWatcher(t, fake)
			watcher.agents = []string{"claude"}
			watcher.unsafe = d.executionCustodyUnsafe
			probes := 0
			watcher.identity = func(string) string { probes++; return fake.identity }
			if !watcher.sessionUnsafe(m.ID) {
				t.Fatal("authwatch bypassed the existing unsafe consumer")
			}
			watcher.tick()
			if len(fake.killed)+len(fake.launched)+len(fake.deleted) != 0 {
				t.Fatal("authwatch performed an automatic session effect")
			}
			if probes == 0 {
				t.Fatal("session hold disabled ordinary credential observation")
			}
			if err := d.core.SetTag(m.ID, "manual-label"); err != nil {
				t.Fatalf("automatic-only veto blocked explicit metadata action: %v", err)
			}
		})
	}
	ordinary, m, _ := nativeControlUnsafeFixture(t, "")
	if ordinary.executionCustodyUnsafe(m.ID) {
		t.Fatal("new runtime veto changed ordinary Claude policy")
	}
}
