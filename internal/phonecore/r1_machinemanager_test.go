package phonecore

// FAILING-FIRST (TDD RED, GG-5) tests for the MachineClient/MachineManager seam ADR-018
// MM2-MM4 and playbook 6.4 name, with ZERO behavior change to the shipping single-machine
// path: nothing here touches Core, Resume, mobile.App or any wire type. It is purely an
// ADDITIVE composition layer over the EXISTING Core surface (ADR-018 MM1's frozen table),
// scoped to R1's compatibility adapter -- real N-entry state migration is MM6 and lands at
// R4 (bd agents-tracker-hggx.5), not here.
//
// WHY THIS LIVES IN internal/phonecore RATHER THAN A NEW PACKAGE: SingleMachineAdapter's
// entire job is to wrap *Core unchanged (MachineClient.Core() returns the exact pointer a
// caller gave it, never a copy or a reimplementation of a read model Core already has), and
// putting the seam in the same package as the thing it wraps is the smallest diff that
// states that. It costs nothing on the bind-legality side: mobile.App -- the ONLY bound
// facade (mobile/bind_test.go PB-BIND-1) -- imports none of this in R1, so it exports
// NOTHING NEW and the bind guard stays green by construction, not by a new assertion.
//
// THE SEAMS these tests pin (undefined symbols -> compile-fail RED):
//
//	type Event struct {                          // one core-sourced notification, the
//	    Kind, Stream, SessionID, State, Message string   // same shape mobile.Event already
//	    Cursor   int64                                   // carries (mobile/types.go) -- MM3's
//	    Dropped  int                                      // "relays ... unchanged" is only
//	}                                                      // checkable if nothing is dropped.
//
//	type MachineClient interface {               // ADR-018 MM2: one pairing entire.
//	    ID() string
//	    Start() error
//	    Stop() error
//	    Running() bool
//	    Core() *Core                              // the EXISTING read-model/command-sequencer
//	    Events() <-chan Event                     // surface this client wraps, unreinvented.
//	}
//
//	type MachineDescriptor struct { ID, DisplayName string }   // one registry row (MM3).
//
//	type MachineEvent struct { MachineID string; Event }       // MM3: "every event on the
//	                                                            // aggregate stream is
//	                                                            // machine-qualified".
//
//	type MachineManager interface {              // ADR-018 MM3: the only plural object.
//	    List() []MachineDescriptor
//	    Select(id string) (MachineClient, error)
//	    Add(d MachineDescriptor, c MachineClient) error
//	    Remove(id string) error
//	    Events() <-chan MachineEvent
//	    ConnectionCap() int                       // MM3's connection-cap policy hook.
//	    Close() error                             // stops the aggregate-stream relay goroutine.
//	}
//
//	type SingleMachineAdapter struct{ ... }       // ADR-018 MM4's compatibility adapter:
//	func NewSingleMachineAdapter(id string, core *Core, events <-chan Event) *SingleMachineAdapter
//
//	type SingleMachineManager struct{ ... }       // holds exactly the one adapter as its
//	func NewSingleMachineManager(displayName string, adapter *SingleMachineAdapter) *SingleMachineManager  // sole entry (MM4).
//
//	var ErrMachineNotFound error
//	var ErrMultiMachineNotImplemented error        // stable, checkable via errors.Is; the
//	                                                // R4 boundary Add and Remove both refuse
//	                                                // behind, since removing the sole entry
//	                                                // is equally undefined before MM6 lands.

import (
	"errors"
	"testing"
	"time"
)

// newMachineTestCore resumes a throwaway *Core the same way every other phonecore test
// does (see processdeath_test.go's phoneTestSealer/noopAcker): a fresh temp dir, a fixed
// test KEK pair, no relay. What SingleMachineAdapter wraps is deliberately a REAL Core, not
// a stub -- "adapter conformance" means conforming against the actual surface it wraps.
func newMachineTestCore(t *testing.T, machine string) *Core {
	t.Helper()
	core, err := Resume(Config{
		Dir:           t.TempDir(),
		Machine:       machine,
		Ack:           noopAcker{},
		WakeSealer:    phoneTestSealer(0x11),
		ContentSealer: phoneTestSealer(0x22),
	})
	if err != nil {
		t.Fatalf("Resume(%q): %v", machine, err)
	}
	return core
}

// Compile-time conformance pins (also part of what "adapter conformance" verifies): the
// concrete types below must satisfy the interfaces above with no adapter method missing.
var (
	_ MachineClient  = (*SingleMachineAdapter)(nil)
	_ MachineManager = (*SingleMachineManager)(nil)
)

// TestSingleMachineAdapter_ConformsAndWrapsCoreUnchanged is the adapter-conformance test:
// the adapter reports the identity it was constructed with, hands back the EXACT *Core
// pointer it was given (never a clone, never a reimplemented read model), and its
// Start/Stop/Running lifecycle is a bookkeeping flag only -- idempotent in both directions,
// touching nothing on Core (Core itself has no Close; see core.go's doc for why).
func TestSingleMachineAdapter_ConformsAndWrapsCoreUnchanged(t *testing.T) {
	core := newMachineTestCore(t, "m1")
	events := make(chan Event)
	adapter := NewSingleMachineAdapter("m1", core, events)

	if got := adapter.ID(); got != "m1" {
		t.Fatalf("adapter.ID() = %q; want %q", got, "m1")
	}
	if adapter.Core() != core {
		t.Fatalf("adapter.Core() returned a different pointer than the one it was constructed with")
	}
	if adapter.Running() {
		t.Fatalf("a freshly constructed adapter must not be running before Start")
	}

	if err := adapter.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !adapter.Running() {
		t.Fatalf("Running() = false after Start")
	}
	if err := adapter.Start(); err != nil {
		t.Fatalf("a second Start must be idempotent, got: %v", err)
	}
	if !adapter.Running() {
		t.Fatalf("Running() = false after an idempotent second Start")
	}

	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if adapter.Running() {
		t.Fatalf("Running() = true after Stop")
	}
	if err := adapter.Stop(); err != nil {
		t.Fatalf("a second Stop must be idempotent, got: %v", err)
	}
}

// TestSingleMachineManager_OneEntryListsAndSelects pins the one-entry registry: the
// manager constructed around the compatibility adapter lists exactly that one descriptor,
// Select resolves it to the exact same client, an unknown id is refused, and the
// connection cap -- nothing to arbitrate with only one possible entry -- reports 1.
func TestSingleMachineManager_OneEntryListsAndSelects(t *testing.T) {
	core := newMachineTestCore(t, "m1")
	adapter := NewSingleMachineAdapter("m1", core, make(chan Event))
	manager := NewSingleMachineManager("My Laptop", adapter)
	t.Cleanup(func() { _ = manager.Close() })

	list := manager.List()
	want := MachineDescriptor{ID: "m1", DisplayName: "My Laptop"}
	if len(list) != 1 || list[0] != want {
		t.Fatalf("List() = %+v; want exactly [%+v]", list, want)
	}

	got, err := manager.Select("m1")
	if err != nil {
		t.Fatalf("Select(%q): unexpected error %v", "m1", err)
	}
	if got != MachineClient(adapter) {
		t.Fatalf("Select(%q) returned a different client than the registered adapter", "m1")
	}

	if _, err := manager.Select("does-not-exist"); !errors.Is(err, ErrMachineNotFound) {
		t.Fatalf("Select of an unknown id: error = %v; want ErrMachineNotFound", err)
	}

	if cap := manager.ConnectionCap(); cap != 1 {
		t.Fatalf("ConnectionCap() = %d; want 1 (the compatibility adapter can never exceed one live connection)", cap)
	}
}

// TestSingleMachineManager_AggregateStreamRelaysCoreEventsUnchanged is MM3's "every event
// on the aggregate stream is machine-qualified": the single client's own events arrive on
// the manager's aggregate stream in the SAME order, with every field byte-for-byte
// unchanged, and gain only the MachineID qualifier -- nothing coalesced, dropped, or
// rewritten.
func TestSingleMachineManager_AggregateStreamRelaysCoreEventsUnchanged(t *testing.T) {
	core := newMachineTestCore(t, "m1")
	events := make(chan Event, 4)
	adapter := NewSingleMachineAdapter("m1", core, events)
	manager := NewSingleMachineManager("My Laptop", adapter)
	t.Cleanup(func() { _ = manager.Close() })

	want := []Event{
		{Kind: "journal", Stream: "journal", Cursor: 3},
		{Kind: "presence", State: "online"},
		{Kind: "terminal", Stream: "terminal", SessionID: "m1/s1"},
		{Kind: "overflow", Dropped: 2, Message: "queue overflow"},
	}
	for _, e := range want {
		events <- e
	}

	for i, w := range want {
		select {
		case got := <-manager.Events():
			if got.MachineID != "m1" {
				t.Fatalf("event %d: MachineID = %q; want %q", i, got.MachineID, "m1")
			}
			if got.Event != w {
				t.Fatalf("event %d relayed as %+v; want %+v unchanged", i, got.Event, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d never reached the aggregate stream", i)
		}
	}
}

// TestSingleMachineManager_MutatingOpsRefuseUntilR4 is the not-implemented-until-R4
// boundary (ADR-018 MM4/MM6): every registry MUTATION -- a second Add, and a Remove of the
// sole entry -- is refused with the SAME stable, errors.Is-checkable code rather than a
// generic failure, and refusal never partially mutates the registry: List() is unchanged
// after either attempt. Read-only operations (List/Select, pinned above) are unaffected.
func TestSingleMachineManager_MutatingOpsRefuseUntilR4(t *testing.T) {
	core := newMachineTestCore(t, "m1")
	adapter := NewSingleMachineAdapter("m1", core, make(chan Event))
	manager := NewSingleMachineManager("My Laptop", adapter)
	t.Cleanup(func() { _ = manager.Close() })

	second := NewSingleMachineAdapter("m2", newMachineTestCore(t, "m2"), make(chan Event))

	t.Run("second Add refuses", func(t *testing.T) {
		err := manager.Add(MachineDescriptor{ID: "m2", DisplayName: "Second Machine"}, second)
		if !errors.Is(err, ErrMultiMachineNotImplemented) {
			t.Fatalf("Add of a second pairing: error = %v; want ErrMultiMachineNotImplemented", err)
		}
		if list := manager.List(); len(list) != 1 || list[0].ID != "m1" {
			t.Fatalf("List() after a refused Add = %+v; want the sole m1 entry untouched", list)
		}
	})

	t.Run("Remove of the sole entry refuses", func(t *testing.T) {
		err := manager.Remove("m1")
		if !errors.Is(err, ErrMultiMachineNotImplemented) {
			t.Fatalf("Remove of the sole entry: error = %v; want ErrMultiMachineNotImplemented", err)
		}
		if list := manager.List(); len(list) != 1 || list[0].ID != "m1" {
			t.Fatalf("List() after a refused Remove = %+v; want the sole m1 entry untouched", list)
		}
	})
}
