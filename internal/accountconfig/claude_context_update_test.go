package accountconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeContextRefreshRecoversOnlyRecordedPartialWrites(t *testing.T) {
	for _, stage := range []string{"settings", "preferences", "history", "committed", "private-edit"} {
		t.Run(stage, func(t *testing.T) {
			f := fixture(t, "claude")
			lease := func(_ string, install func() error) error { return install() }
			put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"high"}`)
			put(t, filepath.Join(f.home, ".claude.json"), `{"mcpServers":{"synthetic":{"command":"old"}}}`)
			put(t, filepath.Join(f.candidate, ".claude.json"), `{"oauthAccount":{"organizationUuid":"selected-private"}}`)
			old, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
			if err != nil {
				t.Fatal(err)
			}
			history, err := PrepareClaudeHistoryAlias(old, f.candidate, "-existing", true, lease)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"low"}`)
			put(t, filepath.Join(f.home, ".claude.json"), `{"mcpServers":{"synthetic":{"command":"new"}}}`)
			other := filepath.Join(f.root, "new-context")
			if err := os.Mkdir(other, 0700); err != nil {
				t.Fatal(err)
			}
			next, err := PrepareClaudeContext(other, f.cwd, f.env, lease)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeClaudeContextUpdate(f.candidate, claudeContextUpdate{1, old, next}); err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.candidate, "settings.json"), `{"effortLevel":"low"}`)
			if stage != "settings" {
				put(t, filepath.Join(f.candidate, ".claude.json"), `{"oauthAccount":{"organizationUuid":"selected-private"},"mcpServers":{"synthetic":{"command":"new"}}}`)
			}
			if stage == "history" || stage == "committed" {
				inventory, err := refreshClaudeHistoryInventory(old, next, f.candidate)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(inventory)
				put(t, filepath.Join(f.candidate, ClaudeHistoryMarker), string(raw))
			}
			if stage == "committed" {
				raw, _ := json.Marshal(next)
				put(t, filepath.Join(f.candidate, ClaudeContextMarker), string(raw))
			}
			if stage == "private-edit" {
				put(t, filepath.Join(f.candidate, "settings.json"), `{"language":"Spanish"}`)
			}
			refreshed, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease, true)
			if stage == "private-edit" {
				if err == nil {
					t.Fatal("unrelated native private edit overwritten")
				}
				return
			}
			if err != nil || refreshed.GlobalGeneration != next.GlobalGeneration {
				t.Fatal("recorded partial update did not recover", err)
			}
			if _, err := os.Stat(filepath.Join(f.candidate, ClaudeContextUpdateMarker)); !os.IsNotExist(err) {
				t.Fatal("committed update intent remains")
			}
			resumed, err := PrepareClaudeHistoryAlias(next, f.candidate, history.ProjectKey, false, lease)
			if err != nil || resumed.SourcePath != history.SourcePath || resumed.Inode != history.Inode {
				t.Fatal("partial update changed history authority")
			}
			state, err := readClaudeContextState(filepath.Join(f.candidate, ".claude.json"))
			if err != nil || state["oauthAccount"].(map[string]any)["organizationUuid"] != "selected-private" {
				t.Fatal("partial update changed selected account metadata")
			}
		})
	}
}

func TestClaudeContextExplicitRefreshUnlinksOnlyRecordedDeletedAsset(t *testing.T) {
	f := fixture(t, "claude")
	lease := func(_ string, install func() error) error { return install() }
	put(t, filepath.Join(f.source, "CLAUDE.md"), "original customization")
	old, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.source, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease); err == nil {
		t.Fatal("ordinary refresh removed alias without explicit action")
	}
	next, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease, true)
	if err != nil {
		t.Fatal(err)
	}
	if next.GlobalGeneration == old.GlobalGeneration {
		t.Fatal("deleted asset did not change owner cohort")
	}
	if _, err := os.Lstat(filepath.Join(f.candidate, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("dangling recorded alias remains")
	}
	if err := RevalidateClaudeContext(next, f.candidate); err != nil {
		t.Fatal(err)
	}
}
