package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
)

func TestManagerLifecycleClaudeUsesSelectedNativeAccountIdentity(t *testing.T) {
	for _, kind := range []string{"default", "selected", "missing_selected", "changed_account", "token_refresh", "relative_selected"} {
		t.Run(kind, func(t *testing.T) {
			f := newLoginFixture(t)
			home := t.TempDir()
			selected := t.TempDir()
			a := []byte(`{"oauthAccount":{"accountUuid":"account-a","organizationUuid":"org"}}`)
			b := []byte(`{"oauthAccount":{"accountUuid":"account-b","organizationUuid":"org"}}`)
			if err := os.WriteFile(filepath.Join(home, ".claude.json"), a, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(selected, ".claude.json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			meta := managerLifecycleMeta(f.rows["source-1"])
			meta.AgentType = "claude"
			meta.LoginRuntime = nil
			meta.Env = []string{"HOME=" + home}
			meta.AuthIdentity, _ = logins.ClaudeIdentity(a)
			dir := ""
			switch kind {
			case "selected":
				dir = selected
				meta.AuthIdentity, _ = logins.ClaudeIdentity(b)
			case "missing_selected":
				dir = filepath.Join(selected, "missing")
			case "relative_selected":
				dir = "relative-profile"
			case "changed_account":
				if err := os.WriteFile(filepath.Join(home, ".claude.json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			case "token_refresh":
				if err := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(`{"accessToken":"rotated-synthetic-token"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Additive private origin metadata must not be confused with a global AuthProbe.
			raw, _ := json.Marshal(map[string]string{"manager_claude_config_dir": dir})
			if err := json.Unmarshal(raw, &meta); err != nil {
				t.Fatal(err)
			}
			f.rows[meta.ID] = meta
			err := f.m.inputGate(meta.ID)
			wantOK := kind == "default" || kind == "selected" || kind == "token_refresh"
			if wantOK && err != nil {
				t.Fatalf("healthy native Claude manager refused: %v", err)
			}
			if !wantOK && err == nil {
				t.Fatal("unknown/changed selected account accepted")
			}
		})
	}
}

func TestManagerNativeIdentityStampIsManagerOnlyAndUsesNoTokens(t *testing.T) {
	home := t.TempDir()
	raw := []byte(`{"oauthAccount":{"accountUuid":"account","organizationUuid":"organization"},"projects":{"keep":true}}`)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	want, err := logins.ClaudeIdentity(raw)
	if err != nil {
		t.Fatal(err)
	}
	spec := daemon.LaunchSpec{AgentType: "claude", ClientEnv: []string{"HOME=" + home}, Manager: &daemon.ManagerLaunchSpec{ConversationID: "conversation"}}
	if err := prepareManagerNativeIdentity(&spec); err != nil || spec.AuthIdentity != want {
		t.Fatalf("manager native stamp = %q, %v", spec.AuthIdentity, err)
	}
	if AuthIdentityForEnv("claude", spec.ClientEnv) != "" {
		t.Fatal("manager identity broadened ordinary worker auth probing")
	}
	missing := filepath.Join(home, "missing")
	spec.Manager.ClaudeConfigDir = &missing
	if err := prepareManagerNativeIdentity(&spec); err == nil {
		t.Fatal("explicit missing identity fell back to HOME")
	}
	spec.Manager = nil
	spec.AuthIdentity = "ordinary-native-default"
	if err := prepareManagerNativeIdentity(&spec); err != nil || spec.AuthIdentity != "ordinary-native-default" {
		t.Fatal("ordinary worker launch changed")
	}
}
