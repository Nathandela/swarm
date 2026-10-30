package daemon

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/Nathandela/swarm/internal/loginmigration"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// v0.25's migration bound discussions to the managed home through their
// environment only. Every daemon re-save must keep that binding, and no other
// home may be persisted.
func TestResaveKeepsMigratedManagedHomeBinding(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	home := filepath.Join(cfg.StateDir, "logins", "provider", "codex", "native")
	migrated := []string{"HOME=/ordinary", "CODEX_HOME=" + home}
	seed := persist.Meta{ID: "s1", AgentType: "codex", Env: migrated, Status: status.Status{Process: status.ProcessExited}}
	if err := d.saveMeta(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	check := func(step string) {
		t.Helper()
		mem, _ := d.Get("s1")
		disk, err := d.store.Load("s1")
		if err != nil {
			t.Fatal(err)
		}
		for name, m := range map[string]persist.Meta{"memory": mem, "disk": disk} {
			if !slices.Equal(m.Env, migrated) || !loginmigration.ManagedBound(cfg.StateDir, m) {
				t.Fatalf("%s: %s env = %v", step, name, m.Env)
			}
		}
	}
	check("save")
	if err := d.Rename("s1", "renamed"); err != nil {
		t.Fatal(err)
	}
	check("rename")
	if err := d.SetTag("s1", "backend"); err != nil {
		t.Fatal(err)
	}
	check("tag")
	if err := d.SetRosterHidden("s1", true); err != nil {
		t.Fatal(err)
	}
	check("archive")
}

func TestResaveDropsOtherProviderHomes(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	managed := filepath.Join(cfg.StateDir, "logins", "provider", "codex", "native")
	for id, env := range map[string][]string{
		"elsewhere":   {"HOME=/ordinary", "CODEX_HOME=/home/owner/.codex-other", "CODEX_HOME_EXTRA=" + managed, "CLAUDE_CONFIG_DIR=" + managed},
		"wrong-agent": {"HOME=/ordinary", "CLAUDE_CONFIG_DIR=" + filepath.Join(cfg.StateDir, "logins", "provider", "claude", "native"), "CODEX_HOME=" + managed},
	} {
		agent := "codex"
		if id == "wrong-agent" {
			agent = "hermes"
		}
		if err := d.saveMeta(persist.Meta{ID: id, AgentType: agent, Env: env}); err != nil {
			t.Fatal(err)
		}
		disk, err := d.store.Load(id)
		if err != nil || !slices.Equal(disk.Env, []string{"HOME=/ordinary"}) {
			t.Fatalf("%s persisted env = %v %v", id, disk.Env, err)
		}
	}
}
