package skeleton

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// A manager can lose its frontend before there is a registered backend feed to tear down.
// Its terminal ledger must become readable while the supervisor remains online.
func TestUnregisteredManagerConnectionReleasesStartupLedger(t *testing.T) {
	for _, connected := range []bool{false, true} {
		name := "failed dial"
		if connected {
			name = "connection lost before registration"
		}
		t.Run(name, func(t *testing.T) {
			root := shortSocketTestDir(t)
			m := persist.Meta{ID: "testmanageraaaaa", AgentType: "codex", Role: "manager", ManagerConversationID: "discussion", Status: status.Status{Process: status.ProcessExited}, ShimPID: 1 << 30, ShimStartTime: 1}
			dir := filepath.Join(root, m.ID)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			replayID, err := managerproof.InitializeReplay(dir, m.ID, m.ManagerConversationID)
			if err != nil {
				t.Fatal(err)
			}
			m.ManagerReplayID = replayID
			store, err := persist.NewStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(m); err != nil {
				t.Fatal(err)
			}
			core, err := daemon.Open(daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "daemon.sock"), LockPath: filepath.Join(root, "daemon.lock"), LogPath: filepath.Join(root, "daemon.log")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = core.Close() })
			closed := make(chan struct{})
			defer close(closed)
			d := &Daemon{core: core, stateDir: root, backendReady: time.Second, closing: closed}
			if err := d.recordSessionInstance(m.ID, "instance", m.ShimPID, m.ShimStartTime); err != nil {
				t.Fatal(err)
			}
			sock := filepath.Join(dir, "absent.sock")
			if connected {
				sock = newR7R4Server(t).sock
			}
			conn, feed, err := d.dialSessionBackend(m.ID, "instance", daemon.BackendChannel{SocketPath: sock})
			if connected {
				if err != nil {
					t.Fatal(err)
				}
				if feed.manager.actor.Load() == nil {
					t.Fatal("test did not establish custody")
				}
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-feed.manager.done:
				case <-time.After(2 * time.Second):
					t.Fatal("backend released but its proof ledger is still owned")
				}
			} else if err == nil {
				t.Fatal("connected to absent backend")
			}
			replay, err := managerproof.OpenReplay(dir, m.ID, m.ManagerConversationID, m.ManagerReplayID)
			if err != nil {
				t.Fatal("startup proof is still locked", err)
			}
			if err := replay.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
