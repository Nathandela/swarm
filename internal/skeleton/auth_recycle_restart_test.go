package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/status"
)

func TestPersistedRunningKillClaimRestoresEmbargoBeforeWatcherReturns(t *testing.T) {
	dir := t.TempDir()
	state := []byte(`{"identities":{"codex":"` + identityB + `"},"pending":{"codex":["s1"]},"killed":{"s1":true}}`)
	if err := os.WriteFile(filepath.Join(dir, authWatchStateFile), state, 0o600); err != nil {
		t.Fatal(err)
	}
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000020"))
	restored := make(chan string, 1)
	w := newAuthWatcher(dir, "ep-test01", []string{"codex"},
		func(string) string { return "" }, // hold the asynchronous first tick
		f.list, f.get, f.kill, f.launch, f.remove,
		nil,
		authRecycleCoordination{
			restore: func(local string) { restored <- local },
			clear:   func(string) {},
		})
	defer w.close()
	select {
	case got := <-restored:
		if got != "s1" {
			t.Fatalf("restored embargo for %q, want s1", got)
		}
	default:
		t.Fatal("newAuthWatcher returned before restoring its persisted live kill claim")
	}
}

func TestPersistedTerminalKillClaimRestoresResumeAuthorityBeforeWatcherReturns(t *testing.T) {
	dir := t.TempDir()
	state := []byte(`{"identities":{"codex":"` + identityB + `"},"pending":{"codex":["s1"]},"killed":{"s1":true}}`)
	if err := os.WriteFile(filepath.Join(dir, authWatchStateFile), state, 0o600); err != nil {
		t.Fatal(err)
	}
	f := newAuthFake(identityB)
	m := runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000028")
	m.Status.Process = status.ProcessExited
	f.add(m)
	restored := make(chan string, 1)
	w := newAuthWatcher(dir, "ep-test01", []string{"codex"},
		func(string) string { return "" },
		f.list, f.get, f.kill, f.launch, f.remove, nil,
		authRecycleCoordination{restore: func(local string) { restored <- local }})
	defer w.close()
	select {
	case got := <-restored:
		if got != "s1" {
			t.Fatalf("restored resume authority for %q, want s1", got)
		}
	default:
		t.Fatal("newAuthWatcher returned before restoring its terminal replacement obligation")
	}
}

func TestPersistedRunningKillClaimRedrivesAndRetainsOnFailure(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000021"))
	f.killErr = errors.New("signal unavailable")
	w := testWatcher(t, f)
	w.settled = false
	w.state.Identities["codex"] = identityB
	w.state.Pending["codex"] = []string{"s1"}
	w.state.Killed["s1"] = true
	claimedCalls := 0
	w.withClaimedRecycleFence = func(local string, attempt func() error) error {
		if local != "s1" {
			t.Fatalf("redrive fence = local %q, want s1", local)
		}
		claimedCalls++
		return attempt()
	}

	w.tick() // first reconciled tick remains hold-only
	if claimedCalls != 0 || len(f.killed) != 0 {
		t.Fatalf("first tick redrove claim: fences=%d kills=%v", claimedCalls, f.killed)
	}
	w.tick() // first redrive fails
	if claimedCalls != 1 {
		t.Fatalf("failed redrive fence calls=%d, want 1", claimedCalls)
	}
	if !w.state.Killed["s1"] {
		t.Fatal("failed redrive discarded the durable kill claim")
	}
	if got := w.state.Pending["codex"]; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("failed redrive pending=%v, want s1 retained", got)
	}

	f.killErr = nil
	w.tick()
	if claimedCalls != 2 || len(f.killed) != 1 || len(f.launched) != 1 {
		t.Fatalf("recovered redrive = fences %d kills %v launches %d, want 2/[s1]/1",
			claimedCalls, f.killed, len(f.launched))
	}
}

func TestRestoredRecycleFenceRetriesButCommittedFenceDoesNotDuplicate(t *testing.T) {
	d := &Daemon{}
	d.restoreAuthRecycle("s1")
	if !d.composerRecycleInFlight("s1") {
		t.Fatal("restored durable claim did not publish a fail-closed embargo")
	}
	wantErr := errors.New("temporary signal failure")
	calls := 0
	if err := d.withClaimedAuthRecycleFence("s1", func() error {
		calls++
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("restored redrive = %v, want %v", err, wantErr)
	}
	if !d.composerRecycleInFlight("s1") {
		t.Fatal("failed restored redrive opened the input embargo")
	}
	if err := d.withClaimedAuthRecycleFence("s1", func() error {
		calls++
		return nil
	}); err != nil {
		t.Fatalf("second restored redrive: %v", err)
	}
	if calls != 2 {
		t.Fatalf("redrive callbacks=%d, want 2", calls)
	}
	if err := d.withClaimedAuthRecycleFence("s1", func() error {
		calls++
		return nil
	}); !errors.Is(err, errAuthRecycleUnsafe) {
		t.Fatalf("duplicate committed recycle = %v, want unsafe refusal", err)
	}
	if calls != 2 {
		t.Fatalf("committed recycle invoked duplicate callback; calls=%d", calls)
	}
}

func TestRecycleDoesNotResumeARowDeletedAfterItsKill(t *testing.T) {
	f := newAuthFake(identityB)
	f.add(runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000022"))
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityA
	w.kill = func(local string) error {
		f.killed = append(f.killed, local)
		delete(f.sessions, local) // an owner Delete wins after auth's signal
		return nil
	}
	w.pause = func(time.Duration) {}
	w.tick()
	if len(f.launched) != 0 {
		t.Fatalf("owner-deleted row was resurrected %d time(s)", len(f.launched))
	}
	if len(w.state.Killed) != 0 || len(w.state.Pending["codex"]) != 0 {
		t.Fatalf("deleted row left auth state killed=%v pending=%v", w.state.Killed, w.state.Pending)
	}
}

func TestPersistedTerminalClaimUsesTheLifecycleResumeFence(t *testing.T) {
	f := newAuthFake(identityB)
	m := runningCodex("s1", identityA, status.TurnIdle, "01a05600-0000-7000-8000-000000000027")
	m.Status.Process = status.ProcessExited
	f.add(m)
	w := testWatcher(t, f)
	w.state.Identities["codex"] = identityB
	w.state.Pending["codex"] = []string{"s1"}
	w.state.Killed["s1"] = true
	fenced := false
	w.withResumeFence = func(local string, attempt func() bool) (bool, bool) {
		fenced = true
		return true, attempt()
	}
	w.tick()
	if !fenced || len(f.launched) != 1 {
		t.Fatalf("terminal claim fenced=%v launches=%d, want true/1", fenced, len(f.launched))
	}
}

// Keep the constructor's launch type visible in this focused file when its
// signature is compiled before the fake method expression above.
var _ func(daemon.LaunchSpec) = func(daemon.LaunchSpec) {}
