package accountconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRestrictedClaudeCheckDropsInteractiveConfiguration(t *testing.T) {
	f := fixture(t, "claude")
	config := filepath.Join(f.root, "accounts", "checks", "synthetic", "native-config")
	credential := filepath.Join(f.root, "accounts", "profiles", "synthetic")
	cwd := filepath.Join(f.root, "accounts", "checks", "synthetic", "native-cwd")
	for _, path := range []string{config, credential, cwd} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(credential, ".claude.json"), `{"oauthAccount":{"accountUuid":"selected","organizationUuid":"selected-org"},"userID":"selected-user","primaryApiKey":"synthetic-secret","mcpServers":{"unsafe":{"command":"synthetic"}},"projects":{"/synthetic":{"hasTrustDialogAccepted":true}}}`)
	put(t, filepath.Join(credential, "settings.json"), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"synthetic"}]}]}}`)
	if err := PrepareRestrictedClaudeConfig(credential, config); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(config, ".claude.json"))
	var settings map[string]any
	_ = json.Unmarshal(raw, &settings)
	if len(settings) != 2 || settings["oauthAccount"] == nil || settings["userID"] == nil {
		t.Fatal("interactive auth/configuration copied")
	}
	env := []string{"HOME=" + f.home, "CLAUDE_CONFIG_DIR=" + config, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + credential}
	wrongEnv := append([]string(nil), env...)
	wrongEnv[2] = "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + config
	if err := ValidateRestrictedClaudeCheck(f.root, credential, config, cwd, wrongEnv); err == nil {
		t.Fatal("wrong securestore selected")
	}
	if runtime.GOOS != "linux" {
		if err := ValidateRestrictedClaudeCheck(f.root, credential, config, cwd, env); err == nil || !strings.Contains(err.Error(), "availability-managed-platform-not-characterized") {
			t.Fatalf("uncharacterized platform accepted availability check: %v", err)
		}
		return
	}
	if err := ValidateRestrictedClaudeCheck(f.root, credential, config, cwd, env); err != nil {
		t.Fatal(err)
	}
}
