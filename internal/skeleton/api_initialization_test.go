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

func TestCoreAPIAssemblyStartsWatchAfterConfiguration(t *testing.T) {
	dir, err := os.MkdirTemp("", "skinit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(persist.Meta{ID: "seed", AgentType: "codex", Status: status.Status{Process: status.ProcessExited}}); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	api := newCoreAPIUnstarted(core, "", "ep", nil)
	t.Cleanup(api.close)
	api.pokeWatch()
	select {
	case <-api.Events():
		t.Fatal("roster watch ran before assembly configuration")
	case <-time.After(20 * time.Millisecond):
	}
	settings := openContextGuardSettingsStore(dir)
	api.contextGuardSettings = settings
	api.contextGuards = newContextGuardManager(nil, dir, settings)
	api.statusObserver = func(string, status.Status) {}
	api.startWatch()
	select {
	case event := <-api.Events():
		if event.ID != "seed" {
			t.Fatalf("initial roster event=%q", event.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("configured watch did not start")
	}
}

func TestUnstartedCoreAPICloseDoesNotWaitForAWorker(t *testing.T) {
	api := newCoreAPIUnstarted(nil, "", "ep", nil)
	done := make(chan struct{})
	go func() { api.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closing unstarted API waited for a worker")
	}
}
