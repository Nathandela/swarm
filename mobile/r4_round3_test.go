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
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
)

// r4r3Custody is the two-tier KEK seam, deterministic per tier, so state sealed during
// provisioning opens under the App's own production sealers.
type r4r3Custody struct{}

func (r4r3Custody) WakeKEK() ([]byte, error)    { return r4r3KEK("wake"), nil }
func (r4r3Custody) ContentKEK() ([]byte, error) { return r4r3KEK("content"), nil }

func r4r3KEK(tier string) []byte {
	sum := sha256.Sum256([]byte("r4r3-kek-" + tier))
	return sum[:]
}

// r4r3TwoMachineApp provisions a MIGRATED two-machine registry world -- machine m-a
// (the App's own pairing) plus machine m-b -- then corrupts ONLY m-b's durable blob and
// builds the App over it. This is production's key world exactly: ONE at-rest KEK pair
// shared across every namespace, the App's own custodySealers.
func r4r3TwoMachineApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	custody := r4r3Custody{}
	wake := custodySealer{tier: "wake", fetch: custody.WakeKEK}
	content := custodySealer{tier: "content", fetch: custody.ContentKEK}

	// The pre-migration singleton pairing, then MM6's migration into the registry.
	core, err := phonecore.Resume(phonecore.Config{
		Dir: dir, Machine: "m-a", WakeSealer: wake, ContentSealer: content,
	})
	if err != nil {
		t.Fatalf("provisioning the singleton pairing: %v", err)
	}
	if err := core.Mutate(func(st *phonecore.State) { st.MachineName = "laptop a" }); err != nil {
		t.Fatalf("persisting the singleton pairing: %v", err)
	}
	reg, err := phonecore.MigrateSingletonToRegistry(phonecore.MigrationConfig{
		Root: dir, WakeSealer: wake, ContentSealer: content,
	})
	if err != nil {
		t.Fatalf("MigrateSingletonToRegistry: %v", err)
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
		t.Fatalf("NewApp over the migrated registry: %v", err)
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
