//go:build linux

package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAccountProjectBoundaryGuardRetainsOldErasedSourceContract(t *testing.T) {
	state, ref, raw := boundaryProjectionFixture(t, accounts.ProviderClaude, true)
	// A newer unmarked projection/current generation must not hide the older
	// marked source. The source row is ended, hidden, and names an erased binding;
	// the compatibility guard reads no credential file or current binding.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "ProjectBoundary")
	legacyRaw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	newRef := replaceBoundaryProjection(t, state, "", legacyRaw)
	accountStore, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = accountStore.Close() }()
	admitCandidate := func() accounts.Candidate {
		candidate, err := accountStore.CreateCandidate(accounts.ProviderClaude, accounts.KindNative)
		if err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","scopes":["user:inference"],"subscriptionType":"pro"}}`,
			".claude.json":      `{"oauthAccount":{"accountUuid":"synthetic-boundary-owner","organizationUuid":"synthetic-boundary-org"}}`,
		} {
			if err := os.WriteFile(filepath.Join(candidate.ProfilePath, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		candidate, err = accountStore.VerifyCandidate(candidate)
		if err != nil {
			t.Fatal(err)
		}
		return candidate
	}
	first := admitCandidate()
	registry, err := accountStore.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	registry, account, err := accountStore.Admit(registry.Revision, first, "synthetic-boundary")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := accountStore.CurrentBinding(account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	second := admitCandidate()
	registry, account, err = accountStore.Reauthenticate(registry.Revision, account.ID, second)
	if err != nil {
		t.Fatal(err)
	}
	currentBinding, err := accountStore.CurrentBinding(account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	registry, err = accountStore.SetLifecycle(registry.Revision, account.ID, accounts.LifecycleRetiring)
	if err != nil {
		t.Fatal(err)
	}
	registry, err = accountStore.BeginCredentialErasure(registry.Revision, account.ID, 1, "2.1.288")
	if err != nil {
		t.Fatal(err)
	}
	registry, err = accountStore.EraseCredentials(registry.Revision, account.ID, 1, accounts.ErasureProof{WritersStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if !registry.Accounts[account.ID].Generations[1].CredentialErased || registry.Accounts[account.ID].CurrentGeneration != 2 {
		t.Fatal("fixture did not retain an actually erased old generation")
	}
	store, err := persist.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range []persist.Meta{{ID: "erased-old-source", AgentType: accounts.ProviderClaude, AccountBinding: &binding, AccountProjectionRef: ref, RosterHidden: true, Status: status.Status{Process: status.ProcessExited}}, {ID: "new-current-source", AgentType: accounts.ProviderClaude, AccountBinding: &currentBinding, AccountProjectionRef: newRef, Status: status.Status{Process: status.ProcessExited}}} {
		if err := store.Save(meta); err != nil {
			t.Fatal(err)
		}
	}
	if err := accountStateGuard(state, boundaryCard(1)); err == nil {
		t.Fatal("current unmarked source hid retained old boundary")
	}
	if err := accountStateGuard(state, boundaryCard(2)); err != nil {
		t.Fatal(err)
	}
}
