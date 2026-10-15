package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestNewerClaudeStateRequiresTheClaudeMinimumWorker(t *testing.T) {
	for _, shape := range []string{"session", "enrollment", "onboarding", "recovery"} {
		t.Run(shape, func(t *testing.T) {
			state := t.TempDir()
			if err := os.Chmod(state, 0700); err != nil {
				t.Fatal(err)
			}
			store, err := accounts.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			_ = store.Close()
			identity := persist.CLIIdentity{Path: "/owner/claude", Version: "2.1.300", Fingerprint: "observed"}
			binding := accounts.Binding{Provider: accounts.ProviderClaude}
			var path string
			var value any
			switch shape {
			case "session":
				path = filepath.Join(state, "newer-session", "meta.json")
				value = map[string]any{"cli_identity": identity, "account_binding": binding}
			case "enrollment":
				path = filepath.Join(state, "accounts", "jobs", "newer-job", enrollment.ConfigFile)
				value = enrollment.Config{SchemaVersion: enrollment.SchemaVersion, Provider: accounts.ProviderClaude, NativeVersion: identity.Version, NativePath: identity.Path}
			case "onboarding":
				path = filepath.Join(state, "auth-watch-state.json")
				value = map[string]any{"cli_refresh": map[string]any{"source": map[string]any{"agent_type": accounts.ProviderClaude, "target": identity, "account_enrollment": map[string]any{"binding": binding}}}}
			case "recovery":
				path = filepath.Join(state, "auth-watch-state.json")
				value = map[string]any{"account_schema_version": accounts.RecoverySchemaVersion, "account_rotations": map[string]any{"incident": map[string]any{"source_binding": binding, "expected_cli_identity": identity}}}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			card := CurrentManifest("vNext")
			older := card
			older.AccountWorker = MinimumCodexWorkerCapability
			if err := accountStateGuard(state, older); err == nil {
				t.Fatal("older build accepted newer managed Claude state")
			}
			if err := accountStateGuard(state, card); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPendingBoundRefreshToNewerClaudeRequiresTheClaudeMinimumWorker(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	id := "boundclaude00001"
	bound := map[string]any{"cli_identity": persist.CLIIdentity{Path: "/owner/claude", Version: "2.1.289", Fingerprint: "observed"}, "account_binding": accounts.Binding{Provider: accounts.ProviderClaude}}
	refresh := map[string]any{"account_schema_version": accounts.RecoverySchemaVersion, "cli_refresh": map[string]any{id: map[string]any{"agent_type": accounts.ProviderClaude, "state": "pending", "target": persist.CLIIdentity{Path: "/owner/claude", Version: "2.1.300", Fingerprint: "newer"}}}}
	for path, value := range map[string]any{filepath.Join(state, id, "meta.json"): bound, filepath.Join(state, "auth-watch-state.json"): refresh} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	card := CurrentManifest("vNext")
	older := card
	older.AccountWorker = MinimumCodexWorkerCapability
	if err := accountRecoveryGuard(state, older); err == nil {
		t.Fatal("older build accepted a pending bound refresh to a newer Claude")
	}
	if err := accountRecoveryGuard(state, card); err != nil {
		t.Fatal(err)
	}
}
