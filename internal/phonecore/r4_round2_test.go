package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 round 2 (bead agents-tracker-hggx.5):
// the adversarial review of the round-1 GREEN found four phonecore-side defects, each
// pinned here BEFORE its fix so the fix cannot be narrower than the finding.
//
//   - D5 BLOCKING: RegistryManager.Remove stops the removed client only AFTER
//     arbitration has promoted its successor, so the documented connection cap is
//     exceeded between the promote and the stop. The cap is a HARD bound.
//   - D2/D5 MEDIUM: a PARKED client's events still reach the aggregate stream --
//     the exact fail-open class SingleMachineManager.relay closes with stopped()
//     and stopSignal() (machinemanager.go:130), left open on the N-entry seam.
//   - LOW: NewMachineRegistry over a root that still holds an unmigrated singleton
//     blob constructs an EMPTY live registry, after which Resume refuses with
//     ErrStateMigrated while the registry names zero machines: the pairing is
//     bricked with the old blob intact on disk but nothing willing to open it.
//   - LOW: validMachineID permits an id equal to the registry file's own name, so
//     the id's namespace directory is the committed registry's path and every
//     later commit rename fails permanently -- a wedged root no retry can clear.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// r4Peak tracks the high-water mark of concurrently running clients across one
// manager's whole lifecycle. All Start/Stop calls happen on the caller's goroutine
// under the manager's lock, so plain fields suffice.
type r4Peak struct{ cur, peak int }

// r4CountingClient is a MachineClient double that charges every real Start/Stop
// transition against a shared r4Peak, so a test can assert the cap as a bound over
// TIME, not just at the instants it happens to sample.
type r4CountingClient struct {
	id      string
	events  chan Event
	running bool
	peak    *r4Peak
}

func (c *r4CountingClient) ID() string { return c.id }
func (c *r4CountingClient) Start() error {
	if !c.running {
		c.running = true
		c.peak.cur++
		if c.peak.cur > c.peak.peak {
			c.peak.peak = c.peak.cur
		}
	}
	return nil
}
func (c *r4CountingClient) Stop() error {
	if c.running {
		c.running = false
		c.peak.cur--
	}
	return nil
}
func (c *r4CountingClient) Running() bool        { return c.running }
func (c *r4CountingClient) Core() *Core          { return nil }
func (c *r4CountingClient) Events() <-chan Event { return c.events }

// TestR4R2_ConnectionCap_RemainsAHardBoundAcrossRemove: removing the CONNECTED machine
// must not let its promoted successor run while the removed client is still live. The
// round-1 evidence claimed "demote-before-promote keeps the cap a hard bound", but the
// Remove path stopped the removed client only after arbitrateLocked had already
// promoted a parked one -- a peak of Cap+1 live connections.
func TestR4R2_ConnectionCap_RemainsAHardBoundAcrossRemove(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	mgr, err := NewRegistryManager(reg, ManagerOptions{Cap: 1, Now: time.Now})
	if err != nil {
		t.Fatalf("NewRegistryManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	peak := &r4Peak{}
	a := &r4CountingClient{id: "m-a", events: make(chan Event), peak: peak}
	b := &r4CountingClient{id: "m-b", events: make(chan Event), peak: peak}
	for _, d := range []MachineDescriptor{{ID: "m-a"}, {ID: "m-b"}} {
		if _, err := reg.AddMachine(d); err != nil {
			t.Fatalf("AddMachine %s: %v", d.ID, err)
		}
	}
	if err := mgr.Add(MachineDescriptor{ID: "m-a"}, a); err != nil {
		t.Fatalf("manager Add m-a: %v", err)
	}
	if err := mgr.Add(MachineDescriptor{ID: "m-b"}, b); err != nil {
		t.Fatalf("manager Add m-b: %v", err)
	}
	// Cap 1: m-b (highest stamp) holds the one connection, m-a is parked.
	if got := mgr.ConnectedIDs(); len(got) != 1 || got[0] != "m-b" {
		t.Fatalf("connected set before Remove is %v, want exactly [m-b]", got)
	}

	if err := mgr.Remove("m-b"); err != nil {
		t.Fatalf("Remove m-b: %v", err)
	}

	if peak.peak > 1 {
		t.Errorf("%d clients ran concurrently during Remove; the documented cap is 1 and it is "+
			"a HARD bound -- the removed client must be stopped BEFORE arbitration promotes "+
			"its successor, not after", peak.peak)
	}
	if b.running {
		t.Errorf("the removed client is still running after Remove returned")
	}
	if got := mgr.ConnectedIDs(); len(got) != 1 || got[0] != "m-a" {
		t.Errorf("connected set after Remove is %v, want the promoted survivor [m-a]", got)
	}
}

// TestR4R2_ParkedClientEventsNeverReachTheAggregateStream: Stop()-parking a client must
// bound its event flow, not just a boolean. The clients here are REAL
// SingleMachineAdapters -- the exact type mobile/machines.go hands the manager in
// production -- so the stopped()/stopSignal() halves the single-machine relay already
// consults exist and are simply ignored by the N-entry relay under test.
func TestR4R2_ParkedClientEventsNeverReachTheAggregateStream(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	mgr, err := NewRegistryManager(reg, ManagerOptions{Cap: 1, Now: time.Now})
	if err != nil {
		t.Fatalf("NewRegistryManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	chA := make(chan Event, 1)
	chB := make(chan Event, 1)
	a := NewSingleMachineAdapter("m-a", nil, chA)
	b := NewSingleMachineAdapter("m-b", nil, chB)
	if err := mgr.Add(MachineDescriptor{ID: "m-a"}, a); err != nil {
		t.Fatalf("manager Add m-a: %v", err)
	}
	if err := mgr.Add(MachineDescriptor{ID: "m-b"}, b); err != nil {
		t.Fatalf("manager Add m-b: %v", err)
	}
	// Cap 1 parked m-a (started at its own Add, then demoted by m-b's higher stamp).
	if a.Running() || !b.Running() {
		t.Fatalf("precondition: want m-a parked and m-b live, got Running a=%v b=%v",
			a.Running(), b.Running())
	}

	chA <- Event{Kind: "session", SessionID: "s-parked", State: "needs_input"}
	chB <- Event{Kind: "session", SessionID: "s-live", State: "needs_input"}

	select {
	case ev := <-mgr.Events():
		if ev.MachineID != "m-b" {
			t.Fatalf("the aggregate stream delivered an event from PARKED machine %q; "+
				"Stop()-parking must bound the client's event flow, not merely its Running() "+
				"boolean (the fail-open class machinemanager.go's stop signal exists to close)",
				ev.MachineID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the live machine's event never arrived on the aggregate stream")
	}
	select {
	case ev := <-mgr.Events():
		t.Fatalf("a second aggregate event arrived (machine %q, session %q); the parked "+
			"client's event must be dropped, not queued behind the live one", ev.MachineID, ev.SessionID)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRegistryOnly_LegacySingletonRequiresReset: v2 is a fresh registry, never a
// migration. A root holding the old singleton layout must be refused explicitly so a
// caller can direct the owner to reset and pair again; it must never be imported or
// resumed by a registry helper.
func TestRegistryOnly_LegacySingletonRequiresReset(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, StateFileName), []byte(`{"sealed":"blob"}`), 0o600); err != nil {
		t.Fatalf("planting the singleton blob: %v", err)
	}

	if _, err := NewMachineRegistry(root); !errors.Is(err, ErrLegacyStateResetRequired) {
		t.Fatalf("NewMachineRegistry error = %v, want ErrLegacyStateResetRequired", err)
	}
	if _, err := OpenMachineRegistry(root); !errors.Is(err, ErrLegacyStateResetRequired) {
		t.Fatalf("OpenMachineRegistry error = %v, want ErrLegacyStateResetRequired", err)
	}
	if _, err := os.Stat(registryPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused construction left a registry file behind (stat: %v)", err)
	}
}

// TestRegistryOnly_FreshEmptyRegistrySurvivesReopen: a first-run v2 root is a live,
// empty registry rather than a singleton state file or a special one-machine adapter.
func TestRegistryOnly_FreshEmptyRegistrySurvivesReopen(t *testing.T) {
	root := t.TempDir()
	reg, err := NewMachineRegistry(root)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	if got := reg.Entries(); len(got) != 0 {
		t.Fatalf("fresh registry entries = %v, want empty", got)
	}
	reopened, err := OpenMachineRegistry(root)
	if err != nil {
		t.Fatalf("OpenMachineRegistry: %v", err)
	}
	if got := reopened.Entries(); len(got) != 0 {
		t.Fatalf("reopened fresh registry entries = %v, want empty", got)
	}
}

// TestRegistryOnly_BootstrapCommitSurvivesReopen proves the first authenticated
// pairing turns the staging directory into exactly one registry authority without a
// directory move. A restart resolves the same namespace, so keys and cursors remain
// with that pairing rather than returning to a root singleton.
func TestRegistryOnly_BootstrapCommitSurvivesReopen(t *testing.T) {
	root := t.TempDir()
	reg, err := NewMachineRegistry(root)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	staging, err := reg.EnsureBootstrap()
	if err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "sentinel"), []byte("fresh-key-state"), 0o600); err != nil {
		t.Fatalf("write staging sentinel: %v", err)
	}
	if err := reg.CommitBootstrap(MachineDescriptor{ID: "m-a", DisplayName: "laptop"}); err != nil {
		t.Fatalf("CommitBootstrap: %v", err)
	}
	reopened, err := OpenMachineRegistry(root)
	if err != nil {
		t.Fatalf("OpenMachineRegistry: %v", err)
	}
	entries := reopened.Entries()
	if len(entries) != 1 || entries[0].ID != "m-a" {
		t.Fatalf("reopened entries = %v, want exactly m-a", entries)
	}
	if got := reopened.MachineDir("m-a"); got != staging {
		t.Fatalf("reopened machine namespace = %q, want committed staging namespace %q", got, staging)
	}
	if data, err := os.ReadFile(filepath.Join(reopened.MachineDir("m-a"), "sentinel")); err != nil || string(data) != "fresh-key-state" {
		t.Fatalf("committed namespace did not retain fresh state: data=%q err=%v", data, err)
	}
}

// TestRegistryOnly_ForgettingLastBootstrapPairingDoesNotReuseItsState ensures a
// later fresh install gets a new empty staging directory, not the old pairing's keys
// or cursor files under a familiar path.
func TestRegistryOnly_ForgettingLastBootstrapPairingDoesNotReuseItsState(t *testing.T) {
	root := t.TempDir()
	reg, err := NewMachineRegistry(root)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	staging, err := reg.EnsureBootstrap()
	if err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "old-key"), []byte("must-not-return"), 0o600); err != nil {
		t.Fatalf("write old pairing state: %v", err)
	}
	if err := reg.CommitBootstrap(MachineDescriptor{ID: "m-a"}); err != nil {
		t.Fatalf("CommitBootstrap: %v", err)
	}
	if err := reg.RemoveMachine("m-a"); err != nil {
		t.Fatalf("RemoveMachine: %v", err)
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forgotten namespace stat = %v, want not exist", err)
	}
	reopened, err := OpenMachineRegistry(root)
	if err != nil {
		t.Fatalf("OpenMachineRegistry: %v", err)
	}
	fresh, err := reopened.EnsureBootstrap()
	if err != nil {
		t.Fatalf("EnsureBootstrap after forget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "old-key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh bootstrap inherited forgotten state: stat=%v", err)
	}
}

// TestRegistryOnly_FailedBootstrapCommitLeavesRecoverableUnregisteredState models the
// pre-ack boundary: the authenticated core may already have its machine identity, but
// a failed registry authority flip leaves no entry. On restart it remains staging for
// an explicit re-pair/commit, never a silently registered or connected machine.
func TestRegistryOnly_FailedBootstrapCommitLeavesRecoverableUnregisteredState(t *testing.T) {
	root := t.TempDir()
	reg, err := NewMachineRegistry(root)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	staging, err := reg.EnsureBootstrap()
	if err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	core, err := Resume(Config{Dir: staging, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume staging: %v", err)
	}
	if err := core.Mutate(func(st *State) { st.Machine = "m-a"; st.RelayCursor = 17 }); err != nil {
		t.Fatalf("durably pin staging core: %v", err)
	}

	// Force only the authority write path unavailable. CommitBootstrap returns this
	// error to pairing.RunDevice, which therefore sends no acknowledgement.
	reg.root = filepath.Join(root, "unavailable-root")
	if err := reg.CommitBootstrap(MachineDescriptor{ID: "m-a"}); err == nil {
		t.Fatal("CommitBootstrap succeeded through an unavailable authority path")
	}
	reg.root = root

	reopened, err := OpenMachineRegistry(root)
	if err != nil {
		t.Fatalf("OpenMachineRegistry after failed commit: %v", err)
	}
	if got := reopened.Entries(); len(got) != 0 {
		t.Fatalf("failed commit registered %v; no registry entry may exist before the acknowledgement", got)
	}
	recovered, err := Resume(Config{Dir: reopened.BootstrapDir(), WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume recoverable staging core: %v", err)
	}
	if st := recovered.State(); st.Machine != "m-a" || st.RelayCursor != 17 {
		t.Fatalf("recoverable staging state = machine %q cursor %d, want m-a/17", st.Machine, st.RelayCursor)
	}
}

// TestRegistryOnly_ReservedBootstrapNameCannotDeleteLivePairing keeps an authenticated
// id from colliding with the internal staging namespace. AddMachine must reject before
// it reaches RemoveAll, because after first pairing that path contains live keys.
func TestRegistryOnly_ReservedBootstrapNameCannotDeleteLivePairing(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	staging, err := reg.EnsureBootstrap()
	if err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "live-key"), []byte("do-not-delete"), 0o600); err != nil {
		t.Fatalf("write live staging state: %v", err)
	}
	if err := reg.CommitBootstrap(MachineDescriptor{ID: "m-a"}); err != nil {
		t.Fatalf("CommitBootstrap: %v", err)
	}
	if _, err := reg.AddMachine(MachineDescriptor{ID: ".staging"}); err == nil {
		t.Fatal("AddMachine accepted the reserved bootstrap namespace")
	}
	if data, err := os.ReadFile(filepath.Join(staging, "live-key")); err != nil || string(data) != "do-not-delete" {
		t.Fatalf("reserved ID altered the live pairing namespace: data=%q err=%v", data, err)
	}
}

// TestRegistryOnly_PostRenameCommitErrorKeepsTheInMemoryRegistry proves a directory
// fsync failure after rename is not treated as an uncommitted Add. Retrying after that
// rollback would create a second namespace/sequencer for the same pairing.
func TestRegistryOnly_PostRenameCommitErrorKeepsTheInMemoryRegistry(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	oldSync := syncPhonecoreDir
	syncPhonecoreDir = func(string) error { return errors.New("injected directory fsync failure") }
	t.Cleanup(func() { syncPhonecoreDir = oldSync })
	if _, err := reg.AddMachine(MachineDescriptor{ID: "m-a"}); !atomicWriteCommitted(err) {
		t.Fatalf("AddMachine error = %v, want committed post-rename error", err)
	}
	if got := reg.Entries(); len(got) != 1 || got[0].ID != "m-a" {
		t.Fatalf("in-memory entries after committed error = %v, want exactly m-a", got)
	}
	if _, err := reg.AddMachine(MachineDescriptor{ID: "m-a"}); err == nil {
		t.Fatal("retry after committed error created a second m-a pairing")
	}
}

func TestRegistryOnly_UncertainBootstrapCommitRetriesExactAuthority(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	if _, err := reg.EnsureBootstrap(); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	d := MachineDescriptor{ID: "m-a"}
	oldSync := syncPhonecoreDir
	syncPhonecoreDir = func(string) error { return errors.New("injected directory fsync failure") }
	if err := reg.CommitBootstrap(d); !errors.Is(err, ErrBootstrapCommitUncertain) {
		t.Fatalf("CommitBootstrap error = %v, want ErrBootstrapCommitUncertain", err)
	}
	syncPhonecoreDir = oldSync
	if err := reg.CommitBootstrap(d); err != nil {
		t.Fatalf("exact bootstrap retry did not confirm authority: %v", err)
	}
	if got := reg.Entries(); len(got) != 1 || got[0] != d {
		t.Fatalf("entries after retry = %v, want exactly %v", got, d)
	}
}

// TestRegistryOnly_LastForgetCrashCannotResurrectBootstrapState models the committed
// RemoveMachine record followed by process death before namespace deletion. Reopen must
// remove the retired staging directory before a fresh bootstrap can create new keys.
func TestRegistryOnly_LastForgetCrashCannotResurrectBootstrapState(t *testing.T) {
	root := t.TempDir()
	reg, err := NewMachineRegistry(root)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	staging, err := reg.EnsureBootstrap()
	if err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "revoked-key"), []byte("must-die"), 0o600); err != nil {
		t.Fatalf("write bootstrap state: %v", err)
	}
	if err := reg.CommitBootstrap(MachineDescriptor{ID: "m-a"}); err != nil {
		t.Fatalf("CommitBootstrap: %v", err)
	}

	// Persist exactly RemoveMachine's authority state, but omit its following remove to
	// simulate a crash in the commit-to-delete window.
	reg.mu.Lock()
	reg.entries = nil
	reg.bootstrap, err = newBootstrapNamespace()
	if err != nil {
		reg.mu.Unlock()
		t.Fatalf("new bootstrap namespace: %v", err)
	}
	if err := reg.commitLocked(); err != nil {
		reg.mu.Unlock()
		t.Fatalf("commit simulated forget: %v", err)
	}
	reg.mu.Unlock()

	reopened, err := OpenMachineRegistry(root)
	if err != nil {
		t.Fatalf("OpenMachineRegistry after crash: %v", err)
	}
	fresh, err := reopened.EnsureBootstrap()
	if err != nil {
		t.Fatalf("EnsureBootstrap after crash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "revoked-key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh bootstrap resurrected forgotten state: stat=%v", err)
	}
}

// TestR4R2_ValidMachineID_RefusesTheRegistryFileName: an id equal to the registry
// file's own name makes MachineDir(id) the committed registry's path -- AddMachine's
// MkdirAll (or the migration's) then creates a DIRECTORY where the registry file must
// be renamed into place, and every later commit fails permanently. Machine ids are
// authenticated endpoint ids, but this is a key/authz-adjacent naming function and the
// guard is one comparison.
func TestR4R2_ValidMachineID_RefusesTheRegistryFileName(t *testing.T) {
	if err := validMachineID(registryFileName); err == nil {
		t.Fatalf("validMachineID accepted %q, the registry file's own name; its namespace "+
			"directory would occupy the committed registry's path and wedge the root beyond "+
			"any retry", registryFileName)
	}
}
