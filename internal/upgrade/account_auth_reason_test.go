package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
)

// TestAccountAuthReasonGuardFencesOlderStrictReaders: a registry carrying
// auth_reason must not be handed to a build whose strict decoder rejects it.
func TestAccountAuthReasonGuardFencesOlderStrictReaders(t *testing.T) {
	raw, err := json.Marshal(CurrentManifest("v0.17.8"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "account_auth")
	raw, _ = json.Marshal(fields)
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "compat.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := readStagedManifest(stage)
	if err != nil {
		t.Fatal(err)
	}
	current := CurrentManifest("v0.17.9")

	state, store, account, reg := inventoryAccountFixture(t)
	if err := accountStateGuard(state, legacy); err != nil {
		t.Fatal("an account without auth_reason refused an older card", err)
	}
	reg, err = store.SetAuthReason(reg.Revision, account.ID, accounts.AuthNeedsLogin, accounts.AuthReasonDrift)
	if err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, legacy); err == nil {
		t.Fatal("an older strict reader could be installed over a registry carrying auth_reason")
	}
	if err := accountStateGuard(state, current); err != nil {
		t.Fatal("the current reader refused auth_reason", err)
	}
	if _, err = store.SetAuth(reg.Revision, account.ID, accounts.AuthValid); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, legacy); err != nil {
		t.Fatal("a cleared auth_reason still fenced the older card", err)
	}
}
