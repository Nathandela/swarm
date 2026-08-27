package skeleton

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

	// The production assembly, poller included: a bare &coreAPI{} would pass even
	// if the roster poller never noticed a tag-only change.
	api := newCoreAPI(core, "", "ep")
	t.Cleanup(api.close)
	// The poller's first sample announces the seeded session; drain it so the
	// event asserted below can only be the tag change.
	select {
	case <-api.Events():
	case <-time.After(3 * time.Second):
		t.Fatal("no initial roster event")
	}

	if err := api.SetTag(id, "frontend"); err != nil {
		t.Fatalf("SetTag: %v", err)
	}
	got, ok := core.Get(id)
	if !ok || got.Tag != "frontend" {
		t.Fatalf("daemon tag = %q (ok=%v), want frontend", got.Tag, ok)
	}
	// protocol.md set_tag: the daemon broadcasts a roster event so every client
	// converges, so a tag-only change must fan out.
	select {
	case m := <-api.Events():
		if m.Tag != "frontend" {
			t.Fatalf("roster event tag = %q, want frontend", m.Tag)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tag change never fanned out as a roster event")
	}
}
