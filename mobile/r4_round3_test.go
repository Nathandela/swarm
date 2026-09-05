package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 round 3's BLOCKING finding on D2/MM8: one
// broken namespace degraded EVERY other pairing -- the exact property this wave claims
// to prove. registryRuntimeLocked aborted the whole runtime on the FIRST
// resumeNamespace error, and ensureMachinesLocked propagated that to App.Machines,
// App.GlobalInbox, App.SelectMachine and App.ForgetMachine alike: with two registered
// machines and only machine B's blob corrupt, App.Machines failed WHOLESALE with the
// state-corrupt remedy "clear this app's data, then pair again" -- healthy machine A's
// row unreachable, and the broken machine impossible even to forget, so the only
// offered remedy destroyed every pairing. That directly contradicts
// screen_coverage.tsv's machines.recovery row ("the aggregate surface says WHICH row is
// broken ... rather than to a global error") and deliverable 2's "one machine's
// failure leaves the other untouched". r4_isolation_test.go proves isolation one layer
// BELOW the seam that had the defect; this test drives the mobile seam itself.

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// TestRegistryOnly_NewAppBootstrapsUnderRegistry proves first launch keeps its Core
// out of the retired root layout. The empty registry and staging namespace reopen, so
// pairing can begin before an authenticated machine id exists without importing v1.
func TestRegistryOnly_NewAppBootstrapsUnderRegistry(t *testing.T) {
	dir := t.TempDir()
	custody := r4r3Custody{}
	app, err := NewApp(&Config{StateDir: dir}, custody)
	if err != nil {
		t.Fatalf("NewApp fresh v2 root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, phonecore.StateFileName)); !os.IsNotExist(err) {
		t.Fatalf("fresh NewApp wrote retired root state: stat=%v", err)
	}
	reg, err := phonecore.OpenMachineRegistry(dir)
	if err != nil {
		t.Fatalf("OpenMachineRegistry: %v", err)
	}
	if got := reg.Entries(); len(got) != 0 {
		t.Fatalf("fresh registry entries = %v, want empty", got)
	}
	if app.coreDir != reg.BootstrapDir() {
		t.Fatalf("fresh Core directory = %q, want bootstrap namespace %q", app.coreDir, reg.BootstrapDir())
	}
	if err := app.Start(); err == nil {
		t.Fatal("fresh unregistered bootstrap connected before pairing committed")
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := NewApp(&Config{StateDir: dir}, custody); err != nil {
		t.Fatalf("NewApp reopens fresh bootstrap: %v", err)
	}
}

// TestRegistryOnly_NewAppRefusesLegacyRoot makes the replacement boundary visible to
// the mobile entry point; it must not silently resume or migrate a root phone-state.
func TestRegistryOnly_NewAppRefusesLegacyRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, phonecore.StateFileName), []byte("old"), 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	_, err := NewApp(&Config{StateDir: dir}, r4r3Custody{})
	if !errors.Is(err, phonecore.ErrLegacyStateResetRequired) {
		t.Fatalf("NewApp error = %v, want ErrLegacyStateResetRequired", err)
	}
}

// TestRegistryOnly_PendingStagingPairingRecoversThroughApp models process death after
// authenticated pairing facts were sealed but before the registry flip. The restarted
// App keeps the core unregistered (and cannot Start) until the same pairing commit path
// explicitly records its authority; it neither loses the fresh keys nor silently uses it.
func TestRegistryOnly_PendingStagingPairingRecoversThroughApp(t *testing.T) {
	dir := t.TempDir()
	custody := r4r3Custody{}
	first, err := NewApp(&Config{StateDir: dir}, custody)
	if err != nil {
		t.Fatalf("first NewApp: %v", err)
	}
	if err := first.core.Mutate(func(st *phonecore.State) {
		st.Machine = "m-a"
		st.MachineName = "laptop"
		st.RelayCursor = 17
	}); err != nil {
		t.Fatalf("pin staging pairing facts: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first App: %v", err)
	}

	restarted, err := NewApp(&Config{StateDir: dir}, custody)
	if err != nil {
		t.Fatalf("restart pending pairing: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Start(); err == nil {
		t.Fatal("pending staging pairing connected before explicit commit")
	}
	if err := restarted.commitBootstrapPairing(); err != nil {
		t.Fatalf("explicit pairing commit after restart: %v", err)
	}
	reg, err := phonecore.OpenMachineRegistry(dir)
	if err != nil {
		t.Fatalf("OpenMachineRegistry: %v", err)
	}
	entries := reg.Entries()
	if len(entries) != 1 || entries[0].ID != "m-a" {
		t.Fatalf("explicit commit entries = %v, want m-a", entries)
	}
	if st := restarted.core.State(); st.RelayCursor != 17 {
		t.Fatalf("restart lost staging cursor: %d", st.RelayCursor)
	}
}

// TestRegistryOnly_PinWithholdsAckUntilUncertainBootstrapRetry is the App-level
// pairing Commit callback contract: an uncertainty returns an error to RunDevice (so
// it cannot ACK), retains bootstrap, and only an exact retry clears it.
func TestRegistryOnly_PinWithholdsAckUntilUncertainBootstrapRetry(t *testing.T) {
	app, err := NewApp(&Config{StateDir: t.TempDir()}, r4r3Custody{})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	original := commitBootstrapAuthority
	calls := 0
	commitBootstrapAuthority = func(reg *phonecore.MachineRegistry, d phonecore.MachineDescriptor) error {
		calls++
		if calls == 1 {
			if err := reg.CommitBootstrap(d); err != nil {
				return err
			}
			return phonecore.ErrBootstrapCommitUncertain
		}
		return reg.CommitBootstrap(d)
	}
	t.Cleanup(func() { commitBootstrapAuthority = original })
	out := &pairing.DeviceOutcome{Machine: pairing.MachinePayload{MachineEndpointID: "m-a"}}
	if err := app.pin(out); !errors.Is(err, phonecore.ErrBootstrapCommitUncertain) {
		t.Fatalf("first pin error = %v, want uncertainty so RunDevice withholds ACK", err)
	}
	if app.bootstrap == nil {
		t.Fatal("uncertain pin cleared bootstrap before retry")
	}
	if err := app.pin(out); err != nil {
		t.Fatalf("exact retry pin: %v", err)
	}
	if app.bootstrap == nil {
		t.Fatal("local retry cleared bootstrap before remote completion")
	}
	p := &Pairing{app: app, state: pairConfirming}
	p.finish(&pairing.DeviceOutcome{}, nil, context.Background())
	if app.bootstrap != nil {
		t.Fatal("acknowledged completion retained bootstrap")
	}
}

// r4r3Custody is the two-tier KEK seam, deterministic per tier, so state sealed during
// provisioning opens under the App's own production sealers.
type r4r3Custody struct{}

func (r4r3Custody) WakeKEK() ([]byte, error)    { return r4r3KEK("wake"), nil }
func (r4r3Custody) ContentKEK() ([]byte, error) { return r4r3KEK("content"), nil }

func r4r3KEK(tier string) []byte {
	sum := sha256.Sum256([]byte("r4r3-kek-" + tier))
	return sum[:]
}

// r4r3TwoMachineApp provisions a two-machine v2 registry world -- machine m-a
// (the App's own pairing) plus machine m-b -- then corrupts ONLY m-b's durable blob and
// builds the App over it. This is production's key world exactly: ONE at-rest KEK pair
// shared across every namespace, the App's own custodySealers.
func r4r3TwoMachineApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	custody := r4r3Custody{}
	wake := custodySealer{tier: "wake", fetch: custody.WakeKEK}
	content := custodySealer{tier: "content", fetch: custody.ContentKEK}

	reg, err := phonecore.NewMachineRegistry(dir)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	dirA, err := reg.AddMachine(phonecore.MachineDescriptor{ID: "m-a", DisplayName: "laptop a"})
	if err != nil {
		t.Fatalf("AddMachine m-a: %v", err)
	}
	core, err := phonecore.Resume(phonecore.Config{
		Dir: dirA, Machine: "m-a", WakeSealer: wake, ContentSealer: content,
	})
	if err != nil {
		t.Fatalf("provisioning m-a's namespace: %v", err)
	}
	if err := core.Mutate(func(st *phonecore.State) { st.MachineName = "laptop a" }); err != nil {
		t.Fatalf("persisting m-a's namespace: %v", err)
	}

	// Machine m-b beside it, with a real durable blob in its namespace...
	dirB, err := reg.AddMachine(phonecore.MachineDescriptor{ID: "m-b", DisplayName: "laptop b"})
	if err != nil {
		t.Fatalf("AddMachine m-b: %v", err)
	}
	coreB, err := phonecore.Resume(phonecore.Config{
		Dir: dirB, Machine: "m-b", WakeSealer: wake, ContentSealer: content,
	})
	if err != nil {
		t.Fatalf("provisioning m-b's namespace: %v", err)
	}
	if err := coreB.Mutate(func(st *phonecore.State) { st.MachineName = "laptop b" }); err != nil {
		t.Fatalf("persisting m-b's namespace: %v", err)
	}
	// ...then corrupt ONLY that blob: Keystore invalidation, backup exclusion, a bad
	// flash -- MM8's per-machine failure, scoped to one pairing by construction.
	if err := os.WriteFile(filepath.Join(dirB, "phone-state.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("corrupting m-b's blob: %v", err)
	}

	app, err := NewApp(&Config{StateDir: dir, MachineID: "m-a"}, custody)
	if err != nil {
		t.Fatalf("NewApp over the v2 registry: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app, dir
}

// TestR4R3_OneBrokenNamespaceDoesNotDegradeTheOthers: with m-b's blob corrupt, the
// aggregate surface must keep serving -- m-a healthy, m-b rendered as the broken row
// (machines.recovery: the surface says WHICH row is broken), the global inbox live,
// and the broken pairing still forgettable. Never a wholesale state-corrupt failure
// whose only remedy destroys every pairing.
func TestR4R3_OneBrokenNamespaceDoesNotDegradeTheOthers(t *testing.T) {
	app, stateDir := r4r3TwoMachineApp(t)

	list, err := app.Machines()
	if err != nil {
		t.Fatalf("App.Machines failed WHOLESALE with one broken namespace: %v -- healthy "+
			"machine m-a is unreachable and the offered remedy destroys every pairing", err)
	}
	n, err := list.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Fatalf("Machines served %d row(s), want 2: the broken pairing must be a ROW, not a hole", n)
	}
	rows := map[string]*MachineInfo{}
	for i := 0; i < n; i++ {
		m, err := list.At(i)
		if err != nil {
			t.Fatalf("At(%d): %v", i, err)
		}
		rows[m.ID] = m
	}
	if a := rows["m-a"]; a == nil || a.Broken {
		t.Errorf("healthy machine m-a rendered %+v; one broken namespace must leave it untouched", a)
	}
	b := rows["m-b"]
	if b == nil {
		t.Fatal("broken machine m-b has no row; the aggregate surface cannot say WHICH row is broken")
	}
	if !b.Broken || b.BrokenReason == "" {
		t.Errorf("m-b rendered %+v; the row must say it is broken and why (machines.recovery)", b)
	}
	if b.Connected || !b.Stale {
		t.Errorf("m-b rendered connected=%v stale=%v; a broken pairing is neither connected nor fresh",
			b.Connected, b.Stale)
	}

	// The global inbox stays live over the healthy pairings.
	if _, err := app.GlobalInbox(); err != nil {
		t.Errorf("GlobalInbox failed wholesale with one broken namespace: %v", err)
	}

	// Selecting the broken pairing names ITS fault rather than pretending it is absent.
	if err := app.SelectMachine("m-b"); err == nil {
		t.Error("SelectMachine over the broken pairing reported success")
	}
	// The healthy pairing still selects.
	if err := app.SelectMachine("m-a"); err != nil {
		t.Errorf("SelectMachine m-a: %v", err)
	}

	// The broken pairing is still FORGETTABLE -- the row's recovery affordance.
	if err := app.ForgetMachine("m-b"); err != nil {
		t.Fatalf("ForgetMachine over the broken pairing: %v -- the only recovery left is "+
			"clearing the app's data, which destroys every pairing", err)
	}
	// The forget is DURABLE: the on-disk registry no longer names m-b and its
	// namespace is gone (asserted against disk, not this test's stale handle).
	reg, err := phonecore.OpenMachineRegistry(stateDir)
	if err != nil {
		t.Fatalf("reopening the registry after the forget: %v", err)
	}
	if got := len(reg.Entries()); got != 1 {
		t.Fatalf("after forgetting m-b the committed registry holds %d entrie(s), want 1", got)
	}
	if _, err := os.Stat(reg.MachineDir("m-b")); !os.IsNotExist(err) {
		t.Errorf("m-b's namespace survived the forget (stat: %v)", err)
	}
	list, err = app.Machines()
	if err != nil {
		t.Fatalf("Machines after the forget: %v", err)
	}
	if n, _ := list.Count(); n != 1 {
		t.Errorf("after the forget Machines serves %d row(s), want 1", n)
	}
}
