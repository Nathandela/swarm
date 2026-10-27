package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

func TestClaudeNativeControlPluginPreparation(t *testing.T) {
	for _, sender := range []bool{false, true} {
		d := &Daemon{cfg: Config{NativeInputEnabled: func() bool { return sender }}}
		dir := t.TempDir()
		spec := LaunchSpec{AgentType: "claude", RequiredRuntime: claudeinput.NativeControlRuntime, CLIIdentity: &persist.CLIIdentity{Version: "2.1.310"}}
		lc := shimSpawnConfig{Argv: []string{"/held/claude", "--resume", "exact"}}
		if err := d.prepareClaudePlugins(dir, spec, &lc); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(claudeinput.GuardPath(dir), "hooks/input.js")); err != nil {
			t.Error("sender-off lost observation guard", err)
		}
		if lc.ClaudeInput != sender {
			t.Error("sender option was changed")
		}
		args := strings.Join(lc.Argv, " ")
		if !strings.Contains(args, "--plugin-dir "+claudeinput.GuardPath(dir)) || !strings.HasSuffix(args, "--resume exact") {
			t.Errorf("native argv changed: %v", lc.Argv)
		}
		_, err := os.Stat(filepath.Join(dir, "claude-input-plugin"))
		if sender != (err == nil) {
			t.Error("sender artifacts disagree with existing option")
		}
	}
}

func TestClaudeNativeControlLaunchRemainsDormant(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	_, err := d.Launch(LaunchSpec{AgentType: "claude", RequiredRuntime: "claude-native-control", Argv: []string{"/must-not-start"}, Cwd: t.TempDir()})
	if !errors.Is(err, shim.ErrClaudeNativeControlDormant) {
		t.Fatalf("new runtime not explicitly dormant: %v", err)
	}
	if len(d.List()) != 0 {
		t.Fatal("dormant admission reserved a session")
	}
}

func TestClaudeNativeControlDaemonWritersPreserveRuntime(t *testing.T) {
	d, m := retainedClaudeDaemonFixture(t, nil)
	m.RequiredRuntime = "claude-native-control"
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(d.cfg.StateDir, m.ID, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	d.putMem(m)
	for _, write := range []func() error{
		func() error { return d.RenameAt(m.ID, "manual rename", time.Now()) },
		func() error { return d.SetTag(m.ID, "manual tag") },
		func() error { return d.SetStatus(m.ID, m.Status) },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
		memory, ok := d.Get(m.ID)
		disk, err := d.store.Load(m.ID)
		if !ok || err != nil || memory.SchemaVersion != 5 || disk.SchemaVersion != 5 || memory.RequiredRuntime != "claude-native-control" || disk.RequiredRuntime != memory.RequiredRuntime || persist.IsClaudeChatRuntime(disk) {
			t.Fatalf("writer changed runtime: memory=%+v disk=%+v err=%v", memory, disk, err)
		}
	}
}
