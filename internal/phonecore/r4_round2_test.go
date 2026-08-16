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

// TestR4R2_NewMachineRegistry_RefusesARootHoldingAnUnmigratedSingleton: constructing an
// empty registry over a root that still holds phone-state.json makes Resume refuse with
// ErrStateMigrated (keyed off registry-file existence alone) while the registry names
// ZERO machines -- the pairing is bricked with the old blob intact but unopenable. The
// only correct doorway for such a root is the migration.
func TestR4R2_NewMachineRegistry_RefusesARootHoldingAnUnmigratedSingleton(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, StateFileName), []byte(`{"sealed":"blob"}`), 0o600); err != nil {
		t.Fatalf("planting the singleton blob: %v", err)
	}

	if _, err := NewMachineRegistry(root); err == nil {
		t.Fatalf("NewMachineRegistry constructed an EMPTY live registry over a root that still "+
			"holds an unmigrated %s; Resume now answers ErrStateMigrated while the registry "+
			"names no machine -- migrate, never construct", StateFileName)
	}
	if _, err := os.Stat(registryPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused construction left a registry file behind (stat: %v); the old "+
			"singleton must remain the sole authority", err)
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
