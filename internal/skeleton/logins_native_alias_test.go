package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
)

func TestPrepareLoginLaunchUsesInheritedOwnerHomeForProjectAlias(t *testing.T) {
	f := newLoginFixture(t)
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	home, target, cwd := filepath.Join(root, "home"), filepath.Join(root, "native"), filepath.Join(root, "home", "project")
	for _, dir := range []string{cwd, target} {
		if e = os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.WriteFile(filepath.Join(target, "config.toml"), []byte("model = \"gpt-6-luna\"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(target, filepath.Join(home, ".codex")); e != nil {
		t.Fatal(e)
	}
	spec := daemon.LaunchSpec{AgentType: logins.ProviderCodex, Cwd: cwd, Argv: []string{"codex"}, ClientEnv: []string{"HOME=" + home}, LoginRuntime: &f.runtime}
	if _, e = f.m.d.prepareLoginLaunch("", spec); e != nil {
		t.Fatalf("actual inherited owner alias refused: %v", e)
	}
	spec.ClientEnv = nil
	if _, e = f.m.d.prepareLoginLaunch("", spec); !errors.Is(e, logins.ErrUnsafePath) {
		t.Fatalf("missing inherited HOME authorized alias: %v", e)
	}
}
