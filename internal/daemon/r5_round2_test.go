package daemon

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-2 review fix-pack (bead
// agents-tracker-hggx.6), BLOCKER 1: retiring a phantom reservation must COMPENSATE
// its PreLaunch side effect.
//
// The defect this file freezes out: launch()'s replay path retires the prior
// attempt's phantom reservation (LOST, ShimPID==0) with a bare dropReserved. The
// phantom's meta was persisted AFTER d.cfg.PreLaunch ran (launch.go orders PreLaunch
// before saveMeta), so a crash between reserve and spawn -- exactly the window the
// R5 fault fence models at phaseReserved -- leaves a phantom whose PreLaunch side
// effect (Epic 12: a git worktree) exists on disk. launch.go's own rollbackReserved
// doc states the rule: "Once dropReserved erases the meta, no future Delete() can
// ever look this id up again, so any hook side effect (e.g. a git worktree) must be
// undone HERE or it leaks permanently." Before R5 the phantom row survived and
// `swarm delete` ran PreDelete (lifecycle.go); the R5 retire erased the row with no
// compensation, so the worktree became unreachable forever.
//
// The contract: retiring the phantom runs the SAME PreDelete compensation
// rollbackReserved runs, over the phantom's PERSISTED meta -- same id, same Cwd,
// same LaunchOptions -- so the production worktree hook (skeleton preDeleteWorktree,
// which reads m.LaunchOptions["worktree"] and m.Cwd/m.ID) can remove what its
// PreLaunch created.

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

// r5HookRecorder models the Epic 12 worktree hooks: PreLaunch records the id it
// created a side effect for; PreDelete records the meta it was asked to compensate.
type r5HookRecorder struct {
	mu      sync.Mutex
	created []string
	deleted []persist.Meta
}

func (r *r5HookRecorder) preLaunch(id string, spec LaunchSpec) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, id)
	return "", nil // no cwd override; the side effect is the recorded creation
}

func (r *r5HookRecorder) preDelete(m persist.Meta) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, m)
	return nil
}

func (r *r5HookRecorder) snapshot() (created []string, deleted []persist.Meta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.created...), append([]persist.Meta(nil), r.deleted...)
}

// TestR5Round2_PhantomRetireCompensatesPreLaunchSideEffect: crash at phaseReserved
// (PreLaunch ran, meta persisted, no shim), restart, replay the same operation id.
// The re-drive retires the phantom row -- and retiring it MUST run PreDelete over the
// phantom's persisted meta, or the PreLaunch side effect (the worktree) leaks with no
// session row left to ever reach it again.
func TestR5Round2_PhantomRetireCompensatesPreLaunchSideEffect(t *testing.T) {
	hooks := &r5HookRecorder{}
	cfg := daemonConfig(t)
	cfg.PreLaunch = hooks.preLaunch
	cfg.PreDelete = hooks.preDelete

	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const op = "devA:01JR2PHANTOM"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := launchOpSpec(t, pidFile, op)

	var phantomID string
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			phantomID = m.ID
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d1.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	if phantomID == "" {
		t.Fatal("probe never saw phaseReserved")
	}
	d1.abandon()

	d2 := openDaemon(t, cfg)
	m, err := d2.Launch(spec) // the phone's replay of the SAME signed operation id
	if err != nil {
		t.Fatalf("replay Launch: %v", err)
	}
	t.Cleanup(func() { killTree(m.ShimPID) })
	if m.ID == phantomID {
		t.Fatalf("replay adopted the phantom %s; the phantom has no process and must be re-driven", phantomID)
	}
	if n := len(d2.List()); n != 1 {
		t.Errorf("after replay, %d sessions exist; want exactly 1 (the phantom row retired)", n)
	}

	created, deleted := hooks.snapshot()
	if len(created) != 2 {
		t.Fatalf("PreLaunch ran for %v; want 2 runs (the phantom's and the re-drive's)", created)
	}
	if len(deleted) != 1 {
		t.Fatalf("PreDelete compensated %d side effects, want exactly 1 (the phantom's): "+
			"dropReserved erases the meta, so an uncompensated PreLaunch side effect -- the "+
			"worktree -- leaks permanently (rollbackReserved's own stated rule)", len(deleted))
	}
	if deleted[0].ID != phantomID {
		t.Errorf("PreDelete compensated session %q, want the phantom %q", deleted[0].ID, phantomID)
	}
	if deleted[0].Cwd != spec.Cwd {
		t.Errorf("PreDelete meta.Cwd = %q, want the phantom's persisted Cwd %q (preDeleteWorktree "+
			"resolves the worktree from it)", deleted[0].Cwd, spec.Cwd)
	}
}

// TestR5Round2_PhantomRetireWithoutHooksStillRetires: the compensation degrades to
// the plain retire when no hooks are configured -- the fix must not make hook-less
// daemons (every unit-test daemon in this package) crash or keep the corpse row.
func TestR5Round2_PhantomRetireWithoutHooksStillRetires(t *testing.T) {
	cfg := daemonConfig(t)
	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const op = "devA:01JR2NOHOOK"
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := launchOpSpec(t, pidFile, op)
	probe := func(phase launchPhase, m persist.Meta) error {
		if phase == phaseReserved {
			return errInjectedCrash
		}
		return nil
	}
	if _, err := d1.launch(spec, probe); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("launch error = %v; want injected crash", err)
	}
	d1.abandon()

	d2 := openDaemon(t, cfg)
	m, err := d2.Launch(spec)
	if err != nil {
		t.Fatalf("replay Launch: %v", err)
	}
	t.Cleanup(func() { killTree(m.ShimPID) })
	if n := len(d2.List()); n != 1 {
		t.Errorf("after replay, %d sessions exist; want exactly 1", n)
	}
}
