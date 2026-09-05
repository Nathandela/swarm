package swarmmobile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

func committedBootstrapAttempt(t *testing.T) (*App, string, r4r3Custody) {
	t.Helper()
	dir := t.TempDir()
	custody := r4r3Custody{}
	app, err := NewApp(&Config{StateDir: dir}, custody)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	if err := app.writePairingState(pairConfirming); err != nil {
		t.Fatalf("record in-flight pairing: %v", err)
	}
	if err := app.core.Mutate(func(st *phonecore.State) {
		st.Machine = "m-a"
		st.MachineName = "laptop"
	}); err != nil {
		t.Fatalf("persist authenticated machine: %v", err)
	}
	if err := app.commitBootstrapPairing(); err != nil {
		t.Fatalf("commit local registry authority: %v", err)
	}
	if got, err := app.readPairingState(); err != nil || got != pairBootstrapCommitted {
		t.Fatalf("local authority marker = %q, %v; want %q", got, err, pairBootstrapCommitted)
	}
	return app, dir, custody
}

func TestBootstrapRestart_UnresolvedRemoteCompletionStaysUnpaired(t *testing.T) {
	// Immediately before sendAck and immediately after it, before Pairing.finish records
	// success, the local durable image is deliberately identical. Neither boundary may
	// silently become a completed remote enrollment after process death.
	for _, boundary := range []string{"before_ack", "after_ack_before_finish"} {
		t.Run(boundary, func(t *testing.T) {
			app, dir, custody := committedBootstrapAttempt(t)
			if err := app.Close(); err != nil {
				t.Fatalf("Close before restart: %v", err)
			}

			restarted, err := NewApp(&Config{StateDir: dir}, custody)
			if err != nil {
				t.Fatalf("NewApp after unresolved completion: %v", err)
			}
			t.Cleanup(func() { _ = restarted.Close() })
			if err := restarted.Start(); err == nil {
				t.Fatal("unresolved pairing connected as though the machine had acknowledged it")
			}
			summary, err := restarted.StateSummary()
			if err != nil {
				t.Fatalf("StateSummary: %v", err)
			}
			if summary.Paired {
				t.Fatal("unresolved pairing was reported as paired")
			}
			if got, err := restarted.PairingState(); err != nil || got != pairFailed {
				t.Fatalf("PairingState = %q, %v; want recovery-facing %q", got, err, pairFailed)
			}
		})
	}
}

func TestBootstrapRestart_CompletedPairingRestartsWithoutRegistryRewrite(t *testing.T) {
	app, dir, custody := committedBootstrapAttempt(t)
	p := &Pairing{app: app, state: pairConfirming}
	p.finish(&pairing.DeviceOutcome{}, nil, context.Background())
	if got, err := p.State(); err != nil || got != pairPaired {
		t.Fatalf("Pairing.State = %q, %v; want %q", got, err, pairPaired)
	}
	if got, err := app.PairingState(); err != nil || got != "" {
		t.Fatalf("PairingState after acknowledged success = %q, %v; want empty", got, err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}

	registryPath := filepath.Join(dir, "machines", "machine-registry.json")
	oldTime := time.Unix(123, 0)
	if err := os.Chtimes(registryPath, oldTime, oldTime); err != nil {
		t.Fatalf("set registry timestamp: %v", err)
	}
	restarted, err := NewApp(&Config{StateDir: dir}, custody)
	if err != nil {
		t.Fatalf("NewApp after completed pairing: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Start(); err != nil {
		t.Fatalf("completed pairing did not restart normally: %v", err)
	}
	summary, err := restarted.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary: %v", err)
	}
	if !summary.Paired {
		t.Fatal("completed pairing was reported as unpaired")
	}
	info, err := os.Stat(registryPath)
	if err != nil {
		t.Fatalf("stat registry after restart: %v", err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("ordinary restart rewrote the registry: mtime = %v, want %v", info.ModTime(), oldTime)
	}
}

func TestBootstrapRestart_RetryCannotErasePendingCompletion(t *testing.T) {
	for _, boundary := range []string{"retry_then_crash", "retry_then_cancel"} {
		t.Run(boundary, func(t *testing.T) {
			app, dir, custody := committedBootstrapAttempt(t)
			retry, err := app.beginWith(app.core, pairing.QRPayload{})
			if err != nil {
				t.Fatalf("begin retry: %v", err)
			}
			if boundary == "retry_then_cancel" {
				if err := retry.Cancel(); err != nil {
					t.Fatalf("cancel retry: %v", err)
				}
			}
			if err := app.Close(); err != nil {
				t.Fatalf("Close before restart: %v", err)
			}

			restarted, err := NewApp(&Config{StateDir: dir}, custody)
			if err != nil {
				t.Fatalf("NewApp after abandoned retry: %v", err)
			}
			t.Cleanup(func() { _ = restarted.Close() })
			if err := restarted.Start(); err == nil {
				t.Fatal("abandoned retry erased the pending-completion connection gate")
			}
			summary, err := restarted.StateSummary()
			if err != nil {
				t.Fatalf("StateSummary: %v", err)
			}
			if summary.Paired {
				t.Fatal("abandoned retry erased the pending-completion paired-status gate")
			}
		})
	}
}

func TestPairingFinish_DoesNotReportSuccessWhenRecoveryRecordCannotClear(t *testing.T) {
	app, dir, custody := committedBootstrapAttempt(t)
	t.Cleanup(func() { _ = app.Close() })
	oldSync := syncPairingStateDir
	syncPairingStateDir = func(string) error { return errors.New("injected directory sync failure") }
	t.Cleanup(func() { syncPairingStateDir = oldSync })

	p := &Pairing{app: app, state: pairConfirming}
	p.finish(&pairing.DeviceOutcome{}, nil, context.Background())
	if got, err := p.State(); err != nil || got == pairPaired {
		t.Fatalf("Pairing.State = %q, %v; acknowledged enrollment with unresolved local recovery must not report paired", got, err)
	}
	if summary, err := app.StateSummary(); err != nil || summary.Paired {
		t.Fatalf("StateSummary after unresolved cleanup = %+v, %v; want unpaired", summary, err)
	}
	if err := app.Start(); err == nil {
		t.Fatal("pairing connected after its recovery record failed to clear")
	}
	if got, err := app.readPairingState(); err != nil || got != pairBootstrapCommitted {
		t.Fatalf("recovery state after failed clear = %q, %v; want %q", got, err, pairBootstrapCommitted)
	}

	// A clean restart after the failed durability acknowledgement must remain gated too.
	syncPairingStateDir = oldSync
	if err := app.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}
	restarted, err := NewApp(&Config{StateDir: dir}, custody)
	if err != nil {
		t.Fatalf("NewApp after failed cleanup: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Start(); err == nil {
		t.Fatal("restart connected after pairing recovery cleanup was not durable")
	}
	if summary, err := restarted.StateSummary(); err != nil || summary.Paired {
		t.Fatalf("restarted StateSummary = %+v, %v; want unpaired", summary, err)
	}
}

func TestBootstrapCommit_RequiresDurablePairingRecord(t *testing.T) {
	app, err := NewApp(&Config{StateDir: t.TempDir()}, r4r3Custody{})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.core.Mutate(func(st *phonecore.State) { st.Machine = "m-a" }); err != nil {
		t.Fatalf("persist authenticated machine: %v", err)
	}
	oldSync := syncPairingStateDir
	syncPairingStateDir = func(string) error { return errors.New("injected directory sync failure") }
	t.Cleanup(func() { syncPairingStateDir = oldSync })

	if err := app.commitBootstrapPairing(); err == nil {
		t.Fatal("registry authority committed after the pending-ACK record was not durably synced")
	}
	reg, err := phonecore.OpenMachineRegistry(app.stateDir)
	if err != nil {
		t.Fatalf("OpenMachineRegistry: %v", err)
	}
	if got := reg.Entries(); len(got) != 0 {
		t.Fatalf("registry entries = %v; want no authority after recovery-record durability failure", got)
	}
}

func TestBootstrapCommit_ConcurrentProgressCannotOverwritePending(t *testing.T) {
	app, err := NewApp(&Config{StateDir: t.TempDir()}, r4r3Custody{})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.writePairingState(pairConfirming); err != nil {
		t.Fatalf("record confirming: %v", err)
	}
	if err := app.core.Mutate(func(st *phonecore.State) { st.Machine = "m-a" }); err != nil {
		t.Fatalf("persist authenticated machine: %v", err)
	}

	oldRename := renamePairingState
	staleAtRename := make(chan struct{})
	releaseStale := make(chan struct{})
	var first atomic.Bool
	renamePairingState = func(oldPath, newPath string) error {
		if first.CompareAndSwap(false, true) {
			close(staleAtRename)
			<-releaseStale
		}
		return oldRename(oldPath, newPath)
	}
	t.Cleanup(func() { renamePairingState = oldRename })

	staleDone := make(chan struct{})
	go func() {
		defer close(staleDone)
		(&Pairing{app: app}).persist(pairPairing)
	}()
	<-staleAtRename

	oldCommit := commitBootstrapAuthority
	commitReached := make(chan struct{})
	commitBootstrapAuthority = func(reg *phonecore.MachineRegistry, d phonecore.MachineDescriptor) error {
		close(commitReached)
		return reg.CommitBootstrap(d)
	}
	t.Cleanup(func() { commitBootstrapAuthority = oldCommit })
	commitDone := make(chan error, 1)
	go func() { commitDone <- app.commitBootstrapPairing() }()

	select {
	case <-commitReached:
		// The unfixed implementation reaches the registry while the stale writer is
		// paused; releasing it now deterministically overwrites the pending marker.
	case <-time.After(100 * time.Millisecond):
		// The fixed implementation holds the shared attempt-state lock, so commit
		// cannot reach the registry until the stale writer completes.
	}
	close(releaseStale)
	<-staleDone
	if err := <-commitDone; err != nil {
		t.Fatalf("commit bootstrap: %v", err)
	}
	if got, err := app.readPairingState(); err != nil || got != pairBootstrapCommitted {
		t.Fatalf("pairing state after concurrent stale writer = %q, %v; want %q", got, err, pairBootstrapCommitted)
	}
}

func TestBeginPairing_RequiresDurableRecoveryRecord(t *testing.T) {
	app, err := NewApp(&Config{StateDir: t.TempDir()}, r4r3Custody{})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	path := filepath.Join(app.stateDir, pairingStateFile)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("block pairing record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "block-write"), []byte("x"), 0o600); err != nil {
		t.Fatalf("make pairing record non-replaceable: %v", err)
	}
	if _, err := app.beginWith(app.core, pairing.QRPayload{}); err == nil {
		t.Fatal("BeginPairing succeeded without recording the crash-recovery gate")
	}
}
