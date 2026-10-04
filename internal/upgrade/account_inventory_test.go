package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
)

func legacyInventoryCard(t *testing.T) CompatManifest {
	t.Helper()
	raw, err := json.Marshal(CurrentManifest("v0.15.0"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "account_inventory")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "compat.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	card, err := readStagedManifest(stage)
	if err != nil {
		t.Fatal(err)
	}
	if card.AccountInventory != 0 {
		t.Fatal("legacy staged card acquired inventory capability")
	}
	return card
}

func inventoryAccountFixture(t *testing.T) (string, *accounts.Store, accounts.Account, accounts.Registry) {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	candidate, err := store.CreateCandidate(accounts.ProviderCodex, accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate.ProfilePath, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"synthetic-inventory-account","access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate, err = store.VerifyCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	reg, account, err := store.Admit(reg.Revision, candidate, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return state, store, account, reg
}

func TestAccountInventoryCardUsesNativeWriterSchema(t *testing.T) {
	card := CurrentManifest("v0.15.0")
	if card.AccountInventory != accounts.NativeInventorySchemaVersion {
		t.Fatal("card inventory axis differs from native writer schema")
	}
	if card.AccountSchema != 1 || card.AccountJobs != 1 {
		t.Fatal("optional retirement markers changed existing schemas")
	}
	if legacyInventoryCard(t).AccountInventory != 0 {
		t.Fatal("missing additive card field should be legacy0")
	}
}

func TestAccountInventoryGuardFencesActualErasureMarkers(t *testing.T) {
	state, store, account, reg := inventoryAccountFixture(t)
	legacy := legacyInventoryCard(t)
	current := CurrentManifest("v0.15.0")
	if err := accountStateGuard(state, legacy); err != nil {
		t.Fatal("unmarked account refused same legacy card", err)
	}
	reg, err := store.SetLifecycle(reg.Revision, account.ID, accounts.LifecycleRetiring)
	if err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, legacy); err != nil {
		t.Fatal("retirement alone required new inventory reader", err)
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

func TestAccountInventoryGuardFencesConsumedJobsWithoutAccounts(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	id := strings.Repeat("a", 32)
	job := map[string]any{"id": id, "generation": 1, "provider": "codex", "method": "device-code", "state": "cancelled", "candidate": accounts.Candidate{ID: id, Provider: accounts.ProviderCodex, Kind: accounts.KindNative, Source: accounts.SourceNativeLogin, ProfileGeneration: id, Verification: accounts.VerificationUnverified, CreatedAt: time.Now().UTC()}}
	jobs := map[string]any{"schema_version": 1, "revision": 2, "jobs": map[string]any{id: job}}
	path := filepath.Join(state, "accounts", "enrollment.json")
	write := func() {
		t.Helper()
		raw, err := json.Marshal(jobs)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	legacy := legacyInventoryCard(t)
	current := CurrentManifest("v0.15.0")
	write()
	if err := accountStateGuard(state, legacy); err != nil {
		t.Fatal("unmarked terminal enrollment required new inventory reader", err)
	}
	for _, consumed := range []bool{true, false} {
		// Presence, including an explicitly encoded false, is unknown to the
		// old strict reader. Native writers omit false with omitempty.
		job["custody_consumed"] = consumed
		write()
		if err := accountStateGuard(state, legacy); err == nil {
			t.Fatal("consumed job marker hidden by empty registry")
		}
		if err := accountStateGuard(state, current); err != nil {
			t.Fatal("current reader refused consumed terminal custody", err)
		}
	}
	for _, malformed := range []any{"true", nil, map[string]any{}} {
		job["custody_consumed"] = malformed
		write()
		if err := accountStateGuard(state, current); err == nil {
			t.Fatal("malformed custody marker accepted")
		}
	}
	job["custody_consumed"] = true
	job["state"] = "running"
	write()
	if err := accountStateGuard(state, current); err == nil {
		t.Fatal("nonterminal consumed custody accepted")
	}
}

func TestAccountInventoryGuardRefusesMalformedGenerationMarker(t *testing.T) {
	state, _, account, _ := inventoryAccountFixture(t)
	path := filepath.Join(state, "accounts", "registry.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var registry map[string]any
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	generation := registry["accounts"].(map[string]any)[account.ID].(map[string]any)["generations"].(map[string]any)["1"].(map[string]any)
	for _, malformed := range []any{false, nil, "", map[string]any{}} {
		generation["erasure_inventory"] = malformed
		raw, err = json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := accountStateGuard(state, CurrentManifest("v0.15.0")); err == nil {
			t.Fatal("malformed generation marker accepted")
		}
	}
}
