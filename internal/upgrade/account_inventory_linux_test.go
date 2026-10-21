//go:build linux

package upgrade

import (
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
)

func TestAccountInventoryGuardFencesActualErasureMarkers(t *testing.T) {
	state, store, account, reg := inventoryAccountFixture(t)
	legacy := legacyInventoryCard(t)
	current := CurrentManifest("v0.15.0")
	reg, err := store.SetLifecycle(reg.Revision, account.ID, accounts.LifecycleRetiring)
	if err != nil {
		t.Fatal(err)
	}
	reg, err = store.BeginCredentialErasure(reg.Revision, account.ID, account.CurrentGeneration, "0.160.0")
	if err != nil {
		t.Fatal(err)
	}
	if reg.Accounts[account.ID].Generations[account.CurrentGeneration].ErasureInventory == "" {
		t.Fatal("actual writer did not publish permanent inventory marker")
	}
	if err := accountStateGuard(state, legacy); err == nil {
		t.Fatal("same old card could install a strict reader that rejects erasure_inventory")
	}
	if err := accountStateGuard(state, current); err != nil {
		t.Fatal("current inventory reader refused fenced native erasure", err)
	}
	reg, err = store.EraseCredentials(reg.Revision, account.ID, account.CurrentGeneration, accounts.ErasureProof{WritersStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	generation := reg.Accounts[account.ID].Generations[account.CurrentGeneration]
	if generation.CredentialErasing || !generation.CredentialErased || generation.ErasureInventory == "" {
		t.Fatal("erasure completion lost permanent compatibility marker")
	}
	if err := accountStateGuard(state, legacy); err == nil {
		t.Fatal("completed erasure allowed reader lacking permanent marker support")
	}
	if err := accountStateGuard(state, current); err != nil {
		t.Fatal(err)
	}
}
