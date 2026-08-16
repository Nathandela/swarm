package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 5 (bead agents-tracker-hggx.5):
// bounded foreground connection concurrency and deterministic stale-state rendering --
// ADR-018 MM3's ruling that "the cap is documented and the overflow policy is
// deterministic least-recently-viewed", from playbook 4.2 (:200-202): "When
// foregrounded, the app connects to every paired machine within a documented
// concurrency cap ... Connections beyond the cap use a deterministic
// least-recently-viewed policy and visibly show their last-sync age."
//
// THE CONTRACT UNDER TEST (undefined today):
//
//   - ManagerOptions{Cap, Now}: the documented cap and the injected clock every
//     determinism assertion rides on.
//   - (*RegistryManager).MarkViewed(id): the user looked at this machine; the ONLY
//     input the arbitration policy takes.
//   - (*RegistryManager).ConnectedIDs(): which pairings hold a live connection right
//     now. Never more than Cap.
//   - (*RegistryManager).RecordSync(id, at): the connection loop's last-successful-sync
//     hook, the fact a parked row's age is computed from.
//   - (*RegistryManager).Rows(): the immutable snapshot the aggregate UI reads
//     (playbook :574 -- "immutable snapshots from MachineManager, not mutable singleton
//     globals"). A parked row is Stale with its LastSyncUnixMs; a connected row is not
//     Stale. MachineRow{ID, DisplayName, Connected, Stale, LastSyncUnixMs}.

import (
	"testing"
	"time"
)

// r4CapManager builds a live registry with n machines and a manager over it, each
// machine backed by a fake client whose Start/Stop the manager drives.
func r4CapManager(t *testing.T, n, cap int, now func() time.Time) (*RegistryManager, []*r4FakeClient) {
	t.Helper()
	reg, err := NewMachineRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	mgr, err := NewRegistryManager(reg, ManagerOptions{Cap: cap, Now: now})
	if err != nil {
		t.Fatalf("NewRegistryManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	clients := make([]*r4FakeClient, n)
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		d := MachineDescriptor{ID: "m-" + id, DisplayName: "machine " + id}
		if _, err := reg.AddMachine(d); err != nil {
			t.Fatalf("AddMachine %s: %v", d.ID, err)
		}
		clients[i] = &r4FakeClient{id: d.ID, events: make(chan Event)}
		if err := mgr.Add(d, clients[i]); err != nil {
			t.Fatalf("manager Add %s: %v", d.ID, err)
		}
	}
	return mgr, clients
}

// idSet is a tiny order-insensitive comparison helper.
func idSet(ids []string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// TestR4_ConnectionCap_NeverExceedsTheDocumentedBound: three paired machines, cap 2.
// However the views interleave, at no point do more than Cap clients hold a live
// connection -- the cap is a bound, not a target the manager may overshoot in between
// arbitrations.
func TestR4_ConnectionCap_NeverExceedsTheDocumentedBound(t *testing.T) {
	frozen := time.Unix(1_755_300_000, 0)
	mgr, clients := r4CapManager(t, 3, 2, func() time.Time { return frozen })

	views := []string{"m-a", "m-b", "m-c", "m-a", "m-c", "m-b", "m-a"}
	for _, id := range views {
		mgr.MarkViewed(id)
		if got := len(mgr.ConnectedIDs()); got > 2 {
			t.Fatalf("after viewing %s, %d connections are live; the documented cap is 2", id, got)
		}
		running := 0
		for _, c := range clients {
			if c.Running() {
				running++
			}
		}
		if running > 2 {
			t.Fatalf("after viewing %s, %d CLIENTS are running; the manager must Stop the "+
				"clients it parks, not merely stop reporting them", id, running)
		}
	}
}

// TestR4_ConnectionCap_OverflowPolicyIsDeterministicLeastRecentlyViewed: the SAME view
// history always produces the SAME connected set, and the machine that loses its
// connection is exactly the least recently viewed one.
func TestR4_ConnectionCap_OverflowPolicyIsDeterministicLeastRecentlyViewed(t *testing.T) {
	run := func() []string {
		frozen := time.Unix(1_755_300_000, 0)
		mgr, _ := r4CapManager(t, 3, 2, func() time.Time { return frozen })
		mgr.MarkViewed("m-a")
		mgr.MarkViewed("m-b")
		mgr.MarkViewed("m-c") // a is now least recently viewed: park a, keep b and c.
		return mgr.ConnectedIDs()
	}

	first := run()
	if got := idSet(first); !got["m-b"] || !got["m-c"] || got["m-a"] {
		t.Errorf("after viewing a,b,c with cap 2 the connected set is %v; least-recently-viewed "+
			"means m-a is the one parked", first)
	}
	second := run()
	if len(first) != len(second) || !idSet(second)["m-b"] || !idSet(second)["m-c"] {
		t.Errorf("the same view history produced %v then %v; the policy must be deterministic", first, second)
	}

	// Re-viewing the parked machine promotes it and demotes exactly the new LRV.
	frozen := time.Unix(1_755_300_000, 0)
	mgr, _ := r4CapManager(t, 3, 2, func() time.Time { return frozen })
	mgr.MarkViewed("m-a")
	mgr.MarkViewed("m-b")
	mgr.MarkViewed("m-c")
	mgr.MarkViewed("m-a")
	got := idSet(mgr.ConnectedIDs())
	if !got["m-a"] || !got["m-c"] || got["m-b"] {
		t.Errorf("after re-viewing m-a the connected set is %v; m-b (now least recently viewed) "+
			"is the one to park", mgr.ConnectedIDs())
	}
}

// TestR4_StaleRows_RenderTheirLastSyncAgeDeterministically: a parked row must "visibly
// show its last-sync age" -- Stale=true and the durable last-successful-sync instant --
// and a connected row must not be marked stale. Two reads under a frozen clock are
// identical: rendering staleness is a pure function of durable facts plus Now, never
// of call timing.
func TestR4_StaleRows_RenderTheirLastSyncAgeDeterministically(t *testing.T) {
	base := time.Unix(1_755_300_000, 0)
	now := base
	mgr, _ := r4CapManager(t, 3, 2, func() time.Time { return now })

	mgr.RecordSync("m-a", base.Add(-90*time.Second))
	mgr.RecordSync("m-b", base.Add(-10*time.Second))
	mgr.RecordSync("m-c", base.Add(-5*time.Second))
	mgr.MarkViewed("m-a")
	mgr.MarkViewed("m-b")
	mgr.MarkViewed("m-c") // m-a parked.

	rows := mgr.Rows()
	if len(rows) != 3 {
		t.Fatalf("Rows() returned %d rows for 3 paired machines", len(rows))
	}
	byID := map[string]MachineRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}

	parked, ok := byID["m-a"]
	if !ok {
		t.Fatalf("the parked machine vanished from the snapshot; the cap is a product-visible "+
			"limitation and must be rendered honestly, not hidden (ADR-018): %v", rows)
	}
	if parked.Connected {
		t.Errorf("the parked row reports Connected")
	}
	if !parked.Stale {
		t.Errorf("the parked row is not marked Stale; a deliberately-unconnected row rendered " +
			"as live is the dishonest rendering MM3 forbids")
	}
	if want := base.Add(-90 * time.Second).UnixMilli(); parked.LastSyncUnixMs != want {
		t.Errorf("the parked row's last-sync instant is %d, want %d -- the age the user sees is "+
			"computed from the durable last successful sync", parked.LastSyncUnixMs, want)
	}
	for _, id := range []string{"m-b", "m-c"} {
		if row := byID[id]; !row.Connected || row.Stale {
			t.Errorf("connected row %s renders Connected=%v Stale=%v; a live row must not be "+
				"marked stale", id, row.Connected, row.Stale)
		}
	}

	// Determinism: the same durable facts under the same clock render identically.
	again := mgr.Rows()
	for i := range rows {
		if rows[i] != again[i] {
			t.Errorf("two Rows() reads under a frozen clock differ at %d: %+v vs %+v", i, rows[i], again[i])
		}
	}
}
