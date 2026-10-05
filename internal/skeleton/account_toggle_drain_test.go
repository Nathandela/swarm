package skeleton

import (
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
)

func TestPoolDisablePreventsUnclaimedKillAndDrainsOnlyClaimedDestination(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-claim", true: "after-claim"}[claimed], func(t *testing.T) {
			store, root, bindings := accountTestStore(t, 3)
			source := accountTestSource(t, root, bindings[0])
			accountTestRollout(t, store, bindings[0], source.Cwd, "opaque native history\n")
			m, fake := rotationTestManager(t, store, root, source)
			if err := m.reportFailure(m.w, source.ID, "quota", "exact-model", "disable-pool"); err != nil {
				t.Fatal(err)
			}
			m.step()
			reserved := m.w.state.AccountRotations[source.ID]
			if reserved.State != accountReserved || reserved.Destination == nil {
				t.Fatal("missing frozen reservation")
			}
			if claimed {
				m.step()
				if !m.w.state.Killed[source.ID] {
					t.Fatal("source claim was not durable")
				}
			}
			registry, _ := store.Snapshot()
			if _, err := store.SetEnabled(registry.Revision, accounts.ProviderCodex, false); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 8; i++ {
				m.step()
			}
			if !claimed {
				if len(fake.killed) != 0 || len(fake.launched) != 0 {
					t.Fatal("disabled pool initiated unclaimed recovery")
				}
				return
			}
			if len(fake.launched) != 1 || fake.launched[0].AccountBinding == nil || *fake.launched[0].AccountBinding != *reserved.Destination {
				t.Fatal("claimed recovery did not drain its exact destination")
			}
			// Pool disable never restores eligibility to a retired account.
			registry, _ = store.Snapshot()
			account := registry.Accounts[reserved.Destination.AccountID]
			account.Lifecycle = accounts.LifecycleRetiring
			registry.Accounts[account.ID] = account
			if m.reservationEligible(registry, accountRotationRecord{SourceID: source.ID, State: accountClaimed, Destination: reserved.Destination, Incident: reserved.Incident}) {
				t.Fatal("drain bypassed retirement")
			}
		})
	}
}
