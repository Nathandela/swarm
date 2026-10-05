package accountconfig

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestClaudeNativeTrustDoesNotHoldAccountOrOverwritePrivateAcknowledgement(t *testing.T) {
	f := fixture(t, "claude")
	lease := func(_ string, install func() error) error { return install() }
	put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"high"}`)
	put(t, filepath.Join(f.home, ".claude.json"), `{"projects":{"/existing":{"hasTrustDialogAccepted":false}}}`)
	c, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.candidate, ".claude.json"), `{"oauthAccount":{"organizationUuid":"selected"},"projects":{"/existing":{"hasTrustDialogAccepted":true,"lastCost":7},"/new":{"hasTrustDialogAccepted":true,"allowedTools":[],"mcpServers":{},"enabledMcpjsonServers":[],"disabledMcpjsonServers":[],"ignorePatterns":[]}}}`)
	if err := RevalidateClaudeContext(c, f.candidate); err != nil {
		t.Fatal("native trust write held whole account", err)
	}
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease); err != nil {
		t.Fatal("native trust write held next launch", err)
	}
	put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"low"}`)
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease, true); err != nil {
		t.Fatal(err)
	}
	state, err := readClaudeContextState(filepath.Join(f.candidate, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	project := state["projects"].(map[string]any)["/existing"].(map[string]any)
	if project["hasTrustDialogAccepted"] != true || project["lastCost"] != float64(7) {
		t.Fatal("refresh lost private trust or native volatile state")
	}
	if state["oauthAccount"].(map[string]any)["organizationUuid"] != "selected" {
		t.Fatal("refresh changed selected metadata")
	}
}

func TestClaudeNativePermissionOrMCPAmendmentStillHolds(t *testing.T) {
	for _, amendment := range []map[string]any{{"allowedTools": []string{"Bash(*)"}}, {"mcpServers": map[string]any{"synthetic": map[string]string{"command": "changed"}}}} {
		f := fixture(t, "claude")
		lease := func(_ string, install func() error) error { return install() }
		c, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
		if err != nil {
			t.Fatal(err)
		}
		amendment["hasTrustDialogAccepted"] = true
		raw, _ := json.Marshal(map[string]any{"projects": map[string]any{f.cwd: amendment}})
		put(t, filepath.Join(f.candidate, ".claude.json"), string(raw))
		if err := RevalidateClaudeContext(c, f.candidate); err == nil {
			t.Fatal("permission/MCP amendment silently ignored")
		}
		if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease, true); err == nil {
			t.Fatal("permission/MCP amendment overwritten by refresh")
		}
	}
}
