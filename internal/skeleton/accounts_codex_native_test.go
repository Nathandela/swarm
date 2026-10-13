//go:build linux

package skeleton

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
)

func TestManagedCodexFollowsInstalledRuntimeAcrossNPMUpgrade(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	home := filepath.Join(m.stateRoot, "home")
	versions := providerVersionsDir("codex", []string{"HOME=" + home})
	bin := filepath.Join(home, "bin")
	for _, dir := range []string{versions, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(versions, accountconfig.CharacterizedCodexVersion)
	copyRetentionELF(t, source)
	alias := filepath.Join(bin, "codex")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	selected, err := m.managedNative("codex", env, m.stateRoot)
	if err != nil || selected.Version != accountconfig.MinimumCodexVersion || selected.Path != alias {
		t.Fatalf("not selected: %+v %v", selected, err)
	}
	// Existing retained copies must not override the installed CLI.
	if _, err := m.retainNative("codex", selected, env, m.stateRoot); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(versions, "0.162.1")
	copyRetentionELF(t, newer)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newer, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	again, err := m.managedNative("codex", env, m.stateRoot)
	if err != nil || again.Version != "0.162.1" || again.Path != alias {
		t.Fatalf("npm update was pinned to an old runtime: %+v %v", again, err)
	}
	ambient, err := probeCLIIdentity("codex", "", env, m.stateRoot)
	if err != nil || ambient.Version != "0.162.1" {
		t.Fatalf("ambient installation changed: %+v %v", ambient, err)
	}
	// The launch resolver uses qualified Codex only for managed discussions.
	api := &coreAPI{accounts: m}
	path, err := api.accountLaunchResolver(daemon.LaunchSpec{AgentType: "codex", AccountBinding: &accounts.Binding{Provider: "codex"}})("codex", env)
	if err != nil || path != again.Path {
		t.Fatalf("managed resolver: %q %v", path, err)
	}
	path, err = api.accountLaunchResolver(daemon.LaunchSpec{AgentType: "codex"})("codex", env)
	if err != nil || path != alias {
		t.Fatalf("unmanaged resolver: %q %v", path, err)
	}
	// A fresh daemon advertises the installed version after vendor GC.
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	restarted := openAccountManager(m.stateRoot, "", nil)
	t.Cleanup(restarted.close)
	if restarted.native["codex"] == nil || *restarted.native["codex"] != *again || !restarted.methods()["codex"][0].Available {
		t.Fatal("restart lost current Codex")
	}
}

func TestManagedCodexDoesNotFallBackToOldRetainedRuntime(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	home := filepath.Join(m.stateRoot, "home")
	versions := providerVersionsDir("codex", []string{"HOME=" + home})
	if err := os.MkdirAll(versions, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(versions, accountconfig.CharacterizedCodexVersion)
	copyRetentionELF(t, source)
	env := []string{"HOME=" + home, "PATH=/no-installed-codex"}
	identity, err := probeCLIIdentity("codex", source, env, m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.retainNative("codex", identity, env, m.stateRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := m.managedNative("codex", env, m.stateRoot); err == nil {
		t.Fatal("missing installation silently downgraded to retained Codex")
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/no-installed-codex")
	m.startupManagedNative("codex")
	if m.native["codex"] != nil || m.methods()["codex"][0].Available {
		t.Fatal("missing installed CLI advertised an old retained executable")
	}
}
