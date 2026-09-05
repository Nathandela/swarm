package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 round 3's MEDIUM-HIGH finding on D1/MM7:
// a forget that can fail to forget, and a re-add that inherits the forgotten keys.
//
//   - MachineRegistry.RemoveMachine commits the registry FIRST and RemoveAll's the
//     namespace SECOND; a crash between the two leaves an orphan namespace holding the
//     forgotten pairing's sealed device key and durable state, and nothing collected
//     it. AddMachine then did a bare MkdirAll and never cleared residue, so re-adding
//     the SAME machine id silently adopted the forgotten pairing's key material and
//     durable coordinates.
//   - RegistryManager.Remove returned on a Stop error BEFORE reg.RemoveMachine, so the
//     "forgotten" machine reappeared with its keys on the next launch -- a latent
//     fail-open on any MachineClient whose Stop can error.
//
// Round 3 closes all three: AddMachine clears the namespace before use,
// OpenMachineRegistry purges namespaces the committed registry does not name, and
// RegistryManager.Remove removes the durable row even when Stop fails.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// r4r3PlantOrphan simulates exactly RemoveMachine's crash window: a namespace
// directory holding residue that the COMMITTED registry does not name.
func r4r3PlantOrphan(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, "machines", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll orphan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "device.key"), []byte("forgotten-pairing-key"), 0o600); err != nil {
		t.Fatalf("plant device.key: %v", err)
	}
	return dir
}

// TestR4R3_AddMachine_AdoptsNoResidueFromAForgottenPairing: after RemoveMachine's
// crash window (registry committed without the row, namespace left behind), re-adding
// the SAME machine id must yield a CLEAN namespace -- never the forgotten pairing's
// sealed key material and durable coordinates.
func TestR4R3_AddMachine_AdoptsNoResidueFromAForgottenPairing(t *testing.T) {
	root := t.TempDir()
	reg, err := NewMachineRegistry(root)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	planted := r4r3PlantOrphan(t, root, "m-a")

	dir, err := reg.AddMachine(MachineDescriptor{ID: "m-a", DisplayName: "laptop"})
	if err != nil {
		t.Fatalf("AddMachine m-a: %v", err)
	}
	if dir != planted {
		t.Fatalf("AddMachine returned %s, want the namespace path %s", dir, planted)
	}
	if _, err := os.Stat(filepath.Join(dir, "device.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the re-added namespace still holds the forgotten pairing's device.key " +
			"(stat answered nil/other); a re-pair silently adopted forgotten key material")
	}
}

// TestR4R3_OpenMachineRegistry_PurgesNamespacesTheRegistryDoesNotName: the committed
// registry is authoritative; a namespace it does not name is RemoveMachine's crash
// residue and must be collected at Open -- while every named namespace is untouched.
func TestR4R3_OpenMachineRegistry_PurgesNamespacesTheRegistryDoesNotName(t *testing.T) {
	root := t.TempDir()
	reg, err := NewMachineRegistry(root)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	dirA, err := reg.AddMachine(MachineDescriptor{ID: "m-a", DisplayName: "laptop"})
	if err != nil {
		t.Fatalf("AddMachine m-a: %v", err)
	}
	keep := filepath.Join(dirA, "phone-state.json")
	if err := os.WriteFile(keep, []byte(`{"live":"state"}`), 0o600); err != nil {
		t.Fatalf("write live namespace state: %v", err)
	}
	orphan := r4r3PlantOrphan(t, root, "m-x")

	if _, err := OpenMachineRegistry(root); err != nil {
		t.Fatalf("OpenMachineRegistry: %v", err)
	}

	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the orphan namespace %s survived Open; the forgotten pairing's sealed "+
			"key material is never collected", orphan)
	}
	got, err := os.ReadFile(keep)
	if err != nil || string(got) != `{"live":"state"}` {
		t.Errorf("the NAMED namespace was touched by the purge (read: %q, %v); only "+
			"unnamed residue may be collected", got, err)
	}
}

// r4r3StopFailClient is a MachineClient whose Stop always errors: the generic client
// RegistryManager.Remove must stay fail-closed against.
type r4r3StopFailClient struct {
	id     string
	events chan Event
}

func (c *r4r3StopFailClient) ID() string           { return c.id }
func (c *r4r3StopFailClient) Start() error         { return nil }
func (c *r4r3StopFailClient) Stop() error          { return errors.New("stop failed") }
func (c *r4r3StopFailClient) Running() bool        { return false }
func (c *r4r3StopFailClient) Core() *Core          { return nil }
func (c *r4r3StopFailClient) Events() <-chan Event { return c.events }

// TestR4R3_RegistryManagerRemove_ForgetsDurablyEvenWhenStopFails: a Stop error must
// not leave the durable row and namespace behind while the in-memory manager has
// already dropped the client -- that resurrects the "forgotten" pairing with its keys
// on the next launch. The error is still reported; the forget still forgets.
func TestR4R3_RegistryManagerRemove_ForgetsDurablyEvenWhenStopFails(t *testing.T) {
	root := t.TempDir()
	reg, err := NewMachineRegistry(root)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	mgr, err := NewRegistryManager(reg, ManagerOptions{Cap: 1, Now: time.Now})
	if err != nil {
		t.Fatalf("NewRegistryManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	d := MachineDescriptor{ID: "m-a", DisplayName: "laptop"}
	dir, err := reg.AddMachine(d)
	if err != nil {
		t.Fatalf("AddMachine: %v", err)
	}
	if err := mgr.Add(d, &r4r3StopFailClient{id: d.ID, events: make(chan Event)}); err != nil {
		t.Fatalf("manager Add: %v", err)
	}

	if err := mgr.Remove("m-a"); err == nil {
		t.Error("Remove swallowed the Stop failure; the operator learns nothing")
	}
	if got := len(reg.Entries()); got != 0 {
		t.Fatalf("the registry still holds %d entrie(s) after Remove; the forgotten "+
			"machine reappears with its keys on the next launch", got)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the forgotten namespace %s survived Remove", dir)
	}
}

func TestRegistryManagerRemove_PrecommitFailureKeepsLiveManager(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"m-a", "m-b"} {
		if _, err := reg.AddMachine(MachineDescriptor{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	mgr, err := NewRegistryManager(reg, ManagerOptions{Cap: 1, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	a := &r4CountingClient{id: "m-a", events: make(chan Event), peak: &r4Peak{}}
	b := &r4CountingClient{id: "m-b", events: make(chan Event), peak: a.peak}
	if err := mgr.Add(MachineDescriptor{ID: "m-a"}, a); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Add(MachineDescriptor{ID: "m-b"}, b); err != nil {
		t.Fatal(err)
	}
	root := reg.root
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg.root = blocked
	if err := mgr.Remove("m-b"); err == nil {
		t.Fatal("Remove succeeded through precommit failure")
	}
	reg.root = root
	if _, err := mgr.Select("m-b"); err != nil {
		t.Fatalf("precommit failure hid live client: %v", err)
	}
	if got := mgr.List(); len(got) != 2 || got[1].ID != "m-b" {
		t.Fatalf("order after precommit failure = %v", got)
	}
	if got := mgr.ConnectedIDs(); len(got) != 1 || got[0] != "m-b" {
		t.Fatalf("arbitration after failure = %v", got)
	}
}

func TestRegistryManagerRemove_PostRenameFailureRemainsRemoved(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := MachineDescriptor{ID: "m-a"}
	if _, err := reg.AddMachine(d); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewRegistryManager(reg, ManagerOptions{Cap: 1, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	client := &r4CountingClient{id: d.ID, events: make(chan Event), peak: &r4Peak{}}
	if err := mgr.Add(d, client); err != nil {
		t.Fatal(err)
	}
	oldSync := syncPhonecoreDir
	syncPhonecoreDir = func(string) error { return errors.New("injected post-rename fsync failure") }
	t.Cleanup(func() { syncPhonecoreDir = oldSync })
	err = mgr.Remove(d.ID)
	if !atomicWriteCommitted(err) {
		t.Fatalf("Remove error = %v, want committed post-rename error", err)
	}
	if _, err := mgr.Select(d.ID); !errors.Is(err, ErrMachineNotFound) {
		t.Fatalf("post-rename failure resurrected manager client: %v", err)
	}
	if got := reg.Entries(); len(got) != 0 {
		t.Fatalf("post-rename failure resurrected registry entries: %v", got)
	}
	if err := mgr.Remove(d.ID); !errors.Is(err, ErrMachineNotFound) {
		t.Fatalf("second Remove = %v, want not found", err)
	}
}

func TestRegistryManagerRemove_CleanupFailureRemainsRemoved(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := MachineDescriptor{ID: "m-a"}
	if _, err := reg.AddMachine(d); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewRegistryManager(reg, ManagerOptions{Cap: 1, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.Add(d, &r4CountingClient{id: d.ID, events: make(chan Event), peak: &r4Peak{}}); err != nil {
		t.Fatal(err)
	}
	oldRemove := removeRegistryNamespace
	removeRegistryNamespace = func(string) error { return errors.New("injected namespace cleanup failure") }
	t.Cleanup(func() { removeRegistryNamespace = oldRemove })
	err = mgr.Remove(d.ID)
	if !atomicWriteCommitted(err) {
		t.Fatalf("Remove error = %v, want committed cleanup error", err)
	}
	if _, err := mgr.Select(d.ID); !errors.Is(err, ErrMachineNotFound) {
		t.Fatalf("cleanup failure retained removed manager client: %v", err)
	}
	if got := reg.Entries(); len(got) != 0 {
		t.Fatalf("cleanup failure resurrected registry entries: %v", got)
	}
}
