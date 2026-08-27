package skeleton

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestCoreAPISetTagReachesTheRealDaemon(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sktag")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	const id = "tag-session"
	if err := store.Save(persist.Meta{
		ID:        id,
		AgentType: "codex",
		Status:    status.Status{Process: status.ProcessExited},
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	core, err := daemon.Open(daemon.Config{
		StateDir:   dir,
		SocketPath: filepath.Join(dir, "d.sock"),
		LockPath:   filepath.Join(dir, "d.lock"),
		LogPath:    filepath.Join(dir, "d.log"),
	})
	if err != nil {
		t.Fatalf("open daemon: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	api := &coreAPI{core: core}
	if err := api.SetTag(id, "frontend"); err != nil {
		t.Fatalf("SetTag: %v", err)
	}
	got, ok := core.Get(id)
	if !ok || got.Tag != "frontend" {
		t.Fatalf("daemon tag = %q (ok=%v), want frontend", got.Tag, ok)
	}
}
