package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 2 (bead agents-tracker-hggx.5):
// independent MachineClient state/key/connection loops and the aggregate event stream --
// ADR-018 MM2 ("each of these is per-pairing and shared with nothing"), MM3 (every
// aggregate event machine-qualified), MM4 ((machine_id, session_id) is the identity),
// MM7/MM8 (one machine's revocation, removal or fault never reaches another pairing).
//
// THE CONTRACT UNDER TEST (undefined today, the R4 implementation supplies it):
//
//   - NewMachineRegistry(root): a fresh, LIVE, initially-empty N-entry registry (the
//     first-run multi-machine state, no singleton to migrate).
//   - (*MachineRegistry).AddMachine(d) (dir, error): creates the per-machine namespace
//     and the durable descriptor row; refuses a duplicate id.
//   - (*MachineRegistry).RemoveMachine(id): deletes exactly that pairing's namespace.
//   - NewRegistryManager(reg, ManagerOptions): the N-entry MachineManager. Its wiring
//     is also what deletes the ~15 b94Allowed MM4 rows in
//     internal/verify/phaseb_reachability_test.go:114-133 -- the fence is bidirectional.
//
// ISOLATION IS PROVED AT THE BYTE LEVEL where it matters: machine B's namespace is
// hashed before and after machine A's revocation and MUST be identical -- "with no
// shared key, cursor, seq space, operation id or file, a cross-pairing bug has no
// medium to travel through" (ADR-018, Positive consequences).

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// r4TwoMachines provisions a live registry with two real pairings, each with its own
// custody, push binding, send-seq ceiling and relay cursor.
type r4TwoMachines struct {
	reg   *MachineRegistry
	a, b  *r4Phone
	addrA PushAddress
	addrB PushAddress
	keyA  crypto.WakeKey
	keyB  crypto.WakeKey
	dirA  string
	dirB  string
}

func provisionTwoMachines(t *testing.T, root string) *r4TwoMachines {
	t.Helper()
	reg, err := NewMachineRegistry(root)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	tw := &r4TwoMachines{reg: reg}

	tw.dirA, err = reg.AddMachine(MachineDescriptor{ID: "m-a", DisplayName: "laptop"})
	if err != nil {
		t.Fatalf("AddMachine m-a: %v", err)
	}
	tw.dirB, err = reg.AddMachine(MachineDescriptor{ID: "m-b", DisplayName: "laptop"})
	if err != nil {
		t.Fatalf("AddMachine m-b: %v", err)
	}

	tw.a = &r4Phone{dir: tw.dirA, machine: "m-a", wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	tw.b = &r4Phone{dir: tw.dirB, machine: "m-b", wake: s14aNewSealer(t), content: s14aNewSealer(t)}

	coreA := tw.a.resume(t)
	coreB := tw.b.resume(t)
	tw.addrA, tw.keyA = r3aBinding(t, coreA, 0xAA)
	tw.addrB, tw.keyB = r3aBinding(t, coreB, 0xBB)
	if err := coreA.AcceptWakeV1(r3aSeal(t, tw.keyA, tw.addrA, 1, time.Now())); err != nil {
		t.Fatalf("machine A's binding wake: %v", err)
	}
	if err := coreB.AcceptWakeV1(r3aSeal(t, tw.keyB, tw.addrB, 1, time.Now())); err != nil {
		t.Fatalf("machine B's binding wake: %v", err)
	}
	if err := coreA.Mutate(func(st *State) {
		st.RelayCursor = 11
		if st.SendSeq == nil {
			st.SendSeq = map[uint32]uint64{}
		}
		st.SendSeq[st.EpochID] = 50
	}); err != nil {
		t.Fatalf("provisioning A: %v", err)
	}
	if err := coreB.Mutate(func(st *State) {
		st.RelayCursor = 22
		if st.SendSeq == nil {
			st.SendSeq = map[uint32]uint64{}
		}
		st.SendSeq[st.EpochID] = 60
	}); err != nil {
		t.Fatalf("provisioning B: %v", err)
	}
	return tw
}

// TestR4_TwoMachines_NamespacesKeysSeqSpacesAndWakeAddressesAreDisjoint: MM2's
// enumeration made checkable. Two pairings share no state directory, no wake address,
// no send-seq space, no relay cursor and no operation queue.
func TestR4_TwoMachines_NamespacesKeysSeqSpacesAndWakeAddressesAreDisjoint(t *testing.T) {
	tw := provisionTwoMachines(t, t.TempDir())

	if tw.dirA == tw.dirB {
		t.Fatalf("both machines were namespaced into ONE directory %q", tw.dirA)
	}
	if strings.HasPrefix(tw.dirA, tw.dirB+string('/')) || strings.HasPrefix(tw.dirB, tw.dirA+string('/')) {
		t.Fatalf("one machine's namespace nests inside the other's: %q vs %q", tw.dirA, tw.dirB)
	}
	if tw.addrA == tw.addrB {
		t.Errorf("both pairings hold the SAME wake address; ADR-018 MM5 requires one independent opaque address per machine")
	}

	coreA := tw.a.resume(t)
	coreB := tw.b.resume(t)

	// Advancing A's durable coordinates must not move B's.
	if err := coreA.Mutate(func(st *State) { st.SendSeq[st.EpochID] = 99; st.RelayCursor = 77 }); err != nil {
		t.Fatalf("advancing A: %v", err)
	}
	stB := coreB.State()
	if got := stB.SendSeq[stB.EpochID]; got != 60 {
		t.Errorf("advancing machine A's send-seq moved machine B's ceiling to %d (want 60): the seq spaces are shared", got)
	}
	if stB.RelayCursor != 22 {
		t.Errorf("advancing machine A's relay cursor moved machine B's to %d (want 22)", stB.RelayCursor)
	}

	// Operation queues are per-pairing instances, not one shared journal.
	if coreA.Ops() == coreB.Ops() {
		t.Errorf("both machines share one OpQueue; the operation-id space must be per-pairing (MM2)")
	}
}

// TestR4_TwoMachines_RevocationIsolatesOnePairing: MM7's isolation rule, byte-level.
// Machine A's revocation severs A's binding forever and leaves machine B's namespace
// BYTE-IDENTICAL and its binding live.
func TestR4_TwoMachines_RevocationIsolatesOnePairing(t *testing.T) {
	tw := provisionTwoMachines(t, t.TempDir())

	beforeB := hashDir(t, tw.dirB)

	coreA := tw.a.resume(t)
	if err := coreA.HonorMachineRevoke(tw.addrA); err != nil {
		t.Fatalf("HonorMachineRevoke(A): %v", err)
	}
	if err := coreA.AcceptWakeV1(r3aSeal(t, tw.keyA, tw.addrA, 2, time.Now())); err == nil {
		t.Errorf("machine A accepted a wake after its revocation")
	}

	if afterB := hashDir(t, tw.dirB); afterB != beforeB {
		t.Errorf("machine A's revocation REWROTE machine B's namespace: \"one machine's removal or " +
			"revocation cannot alter another pairing\" (ADR-018 MM7); B's keys, cursors, seq spaces " +
			"and push address are not read, not rewritten, and not invalidated")
	}
	coreB := tw.b.resume(t)
	if err := coreB.AcceptWakeV1(r3aSeal(t, tw.keyB, tw.addrB, 2, time.Now())); err != nil {
		t.Errorf("machine B's binding died with machine A's revocation: %v", err)
	}
}

// TestR4_TwoMachines_ProcessDeathRestoresBothPairings: R4's exit clause "process death
// restores all three", at N=2: after an Android SIGKILL (nothing shut down cleanly),
// resuming both namespaces restores each pairing's own coordinates -- and A's
// revocation, taken before the death, is still in force while B stays live.
func TestR4_TwoMachines_ProcessDeathRestoresBothPairings(t *testing.T) {
	tw := provisionTwoMachines(t, t.TempDir())
	if err := tw.a.resume(t).HonorMachineRevoke(tw.addrA); err != nil {
		t.Fatalf("HonorMachineRevoke(A): %v", err)
	}

	// "Process death" is dropping every live Core and re-resuming from disk.
	coreA := tw.a.resume(t)
	coreB := tw.b.resume(t)

	if err := coreA.AcceptWakeV1(r3aSeal(t, tw.keyA, tw.addrA, 3, time.Now())); err == nil {
		t.Errorf("machine A's severance did not survive process death")
	}
	if err := coreB.AcceptWakeV1(r3aSeal(t, tw.keyB, tw.addrB, 3, time.Now())); err != nil {
		t.Errorf("machine B did not survive process death live: %v", err)
	}
	stA, stB := coreA.State(), coreB.State()
	if got := stA.SendSeq[stA.EpochID]; got != 50 {
		t.Errorf("machine A's send-seq ceiling after death: %d, want 50", got)
	}
	if got := stB.SendSeq[stB.EpochID]; got != 60 {
		t.Errorf("machine B's send-seq ceiling after death: %d, want 60", got)
	}

	// The registry itself also restores: both descriptors, unchanged.
	reg, err := OpenMachineRegistry(tw.regRoot(t))
	if err != nil {
		t.Fatalf("re-opening the registry after process death: %v", err)
	}
	if got := len(reg.Entries()); got != 2 {
		t.Errorf("registry restored %d entries after process death, want 2", got)
	}
}

// regRoot recovers the root the registry was created under from either namespace's
// parent -- a helper, not contract: the contract is only that OpenMachineRegistry(root)
// restores what NewMachineRegistry(root) built.
func (tw *r4TwoMachines) regRoot(t *testing.T) string {
	t.Helper()
	return tw.reg.Root()
}

// TestR4_TwoMachines_RemovalDeletesExactlyOneNamespace: the forget arm of MM7. Removing
// machine A deletes A's descriptor and namespace and leaves B's namespace byte-identical.
func TestR4_TwoMachines_RemovalDeletesExactlyOneNamespace(t *testing.T) {
	tw := provisionTwoMachines(t, t.TempDir())
	beforeB := hashDir(t, tw.dirB)

	if err := tw.reg.RemoveMachine("m-a"); err != nil {
		t.Fatalf("RemoveMachine(m-a): %v", err)
	}
	entries := tw.reg.Entries()
	if len(entries) != 1 || entries[0].ID != "m-b" {
		t.Fatalf("after removing m-a the registry holds %v, want exactly m-b", entries)
	}
	if afterB := hashDir(t, tw.dirB); afterB != beforeB {
		t.Errorf("removing machine A rewrote machine B's namespace")
	}
}

// TestR4_RegistryManager_AggregateStreamIsMachineQualifiedAndDuplicateSessionIDsDoNotCollide:
// MM3 ("every event on the aggregate stream is machine-qualified") and MM4 ("session
// identity is always the tuple (machine_id, session_id)"), through the REAL N-entry
// manager. Two machines legitimately serve the SAME session id; the aggregate stream
// must deliver two distinguishable events, never fold them.
func TestR4_RegistryManager_AggregateStreamIsMachineQualifiedAndDuplicateSessionIDsDoNotCollide(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	if _, err := reg.AddMachine(MachineDescriptor{ID: "m-a", DisplayName: "laptop"}); err != nil {
		t.Fatalf("AddMachine m-a: %v", err)
	}
	if _, err := reg.AddMachine(MachineDescriptor{ID: "m-b", DisplayName: "laptop"}); err != nil {
		t.Fatalf("AddMachine m-b: %v", err)
	}
	mgr, err := NewRegistryManager(reg, ManagerOptions{Cap: 2, Now: time.Now})
	if err != nil {
		t.Fatalf("NewRegistryManager: %v", err)
	}
	defer func() { _ = mgr.Close() }()

	chA := make(chan Event, 1)
	chB := make(chan Event, 1)
	if err := mgr.Add(MachineDescriptor{ID: "m-a", DisplayName: "laptop"}, &r4FakeClient{id: "m-a", events: chA}); err != nil {
		t.Fatalf("manager Add m-a: %v", err)
	}
	if err := mgr.Add(MachineDescriptor{ID: "m-b", DisplayName: "laptop"}, &r4FakeClient{id: "m-b", events: chB}); err != nil {
		t.Fatalf("manager Add m-b: %v", err)
	}

	// The SAME session id and the SAME display-ambiguous payload from both machines.
	chA <- Event{Kind: "session", Stream: "journal", SessionID: "s-1", State: "needs_input"}
	chB <- Event{Kind: "session", Stream: "journal", SessionID: "s-1", State: "needs_input"}

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case ev := <-mgr.Events():
			if ev.MachineID == "" {
				t.Fatalf("aggregate event %d carries no machine id; a consumer that cannot say "+
					"which pairing woke it cannot route it (MM3)", i)
			}
			if ev.SessionID != "s-1" {
				t.Fatalf("aggregate event %d rewrote the session id to %q", i, ev.SessionID)
			}
			key := ev.MachineID + "/" + ev.SessionID
			if got[key] {
				t.Fatalf("two machines' events folded into one identity %q; duplicate session ids "+
					"must not collide (MM4, R4 exit)", key)
			}
			got[key] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("aggregate stream delivered %d of 2 events", i)
		}
	}
	if !got["m-a/s-1"] || !got["m-b/s-1"] {
		t.Fatalf("aggregate identities %v, want both (m-a,s-1) and (m-b,s-1)", got)
	}
}

// TestR4_RegistryManager_AddRefusesDuplicateMachineID: the registry is keyed by machine
// id; a second Add of the same id is a refusal, not an upsert of another pairing's state.
func TestR4_RegistryManager_AddRefusesDuplicateMachineID(t *testing.T) {
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	if _, err := reg.AddMachine(MachineDescriptor{ID: "m-a"}); err != nil {
		t.Fatalf("AddMachine: %v", err)
	}
	// The refusal sentinel's exact identity is the implementation's to choose; what is
	// pinned is that the second add refuses and the FIRST add stands.
	if _, err := reg.AddMachine(MachineDescriptor{ID: "m-a", DisplayName: "other"}); err == nil {
		t.Fatalf("a second AddMachine(m-a) succeeded; two namespaces for one pairing is two live " +
			"send sequencers (MM6 step 5)")
	}
	entries := reg.Entries()
	if len(entries) != 1 || entries[0].DisplayName != "" {
		t.Errorf("the duplicate add altered the registry: %v", entries)
	}
}

// r4FakeClient is a MachineClient double for manager-level tests: identity, lifecycle
// flags and an event channel, nothing else. Core() answers nil deliberately -- the
// manager must never need to reach into a client's Core to route its events.
type r4FakeClient struct {
	id      string
	events  chan Event
	started bool
	stopped bool
}

func (c *r4FakeClient) ID() string           { return c.id }
func (c *r4FakeClient) Start() error         { c.started = true; c.stopped = false; return nil }
func (c *r4FakeClient) Stop() error          { c.stopped = true; c.started = false; return nil }
func (c *r4FakeClient) Running() bool        { return c.started && !c.stopped }
func (c *r4FakeClient) Core() *Core          { return nil }
func (c *r4FakeClient) Events() <-chan Event { return c.events }
