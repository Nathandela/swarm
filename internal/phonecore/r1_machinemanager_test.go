package phonecore

import "testing"

// newMachineTestCore resumes a throwaway *Core with independent key state. The client
// lifecycle tests use a real Core rather than a substitute so Core()'s exact-pointer
// contract remains covered.
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

var (
	_ MachineClient  = (*CoreMachineClient)(nil)
	_ MachineManager = (*RegistryManager)(nil)
)

// TestCoreMachineClient_ConformsAndWrapsCoreUnchanged pins the shared client seam:
// identity and the exact Core pointer are retained, and lifecycle changes are
// idempotent bookkeeping rather than Core destruction.
func TestCoreMachineClient_ConformsAndWrapsCoreUnchanged(t *testing.T) {
	core := newMachineTestCore(t, "m1")
	client := NewCoreMachineClient("m1", core, make(chan Event))

	if got := client.ID(); got != "m1" {
		t.Fatalf("ID() = %q; want m1", got)
	}
	if client.Core() != core {
		t.Fatal("Core() returned a different pointer")
	}
	if client.Running() {
		t.Fatal("fresh client is running before Start")
	}
	if err := client.Start(); err != nil || !client.Running() {
		t.Fatalf("Start/Running = %v/%v; want nil/true", err, client.Running())
	}
	if err := client.Start(); err != nil || !client.Running() {
		t.Fatalf("idempotent Start/Running = %v/%v; want nil/true", err, client.Running())
	}
	if err := client.Stop(); err != nil || client.Running() {
		t.Fatalf("Stop/Running = %v/%v; want nil/false", err, client.Running())
	}
	if err := client.Stop(); err != nil {
		t.Fatalf("idempotent Stop: %v", err)
	}
}
