package skeleton

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
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

func TestProvedLostManagerStartupRequiresExactUnstartedCustody(t *testing.T) {
	const instance = "0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "exact contained startup"},
		{name: "no joined stop proof", change: func(files map[string]any) { delete(files, shim.NativeStoppedFile) }},
		{name: "mismatched writer generation", change: func(files map[string]any) {
			stopped := files[shim.NativeStoppedFile].(shim.NativeStoppedInfo)
			stopped.Native.Generation = "ffffffffffffffffffffffffffffffff"
			files[shim.NativeStoppedFile] = stopped
		}},
		{name: "no scope containment", change: func(files map[string]any) {
			stopped := files[shim.NativeStoppedFile].(shim.NativeStoppedInfo)
			stopped.DescendantsContained = false
			files[shim.NativeStoppedFile] = stopped
		}},
		{name: "backend identity was recorded", change: func(files map[string]any) {
			stopped := files[shim.NativeStoppedFile].(shim.NativeStoppedInfo)
			stopped.Native.BackendPID = 1 << 29
			files[shim.NativeProcessFile], files[shim.NativeStoppedFile] = stopped.Native, stopped
		}},
		{name: "backend was not required", change: func(files map[string]any) {
			files["shim-launch.json"].(map[string]any)["backend_required"] = false
		}},
		{name: "different backend failure", change: func(files map[string]any) {
			files[sessionDegradedFile] = historyTornMarker{Version: 1, SessionInstance: instance, Generation: backendGapGeneration(gapBackendLost)}
		}},
		{name: "different instance marker", change: func(files map[string]any) {
			files[sessionDegradedFile] = historyTornMarker{Version: 1, SessionInstance: "another", Generation: backendGapGeneration(gapBackendNeverServed)}
		}},
		{name: "conflicting backend record", change: func(files map[string]any) {
			files[shim.BackendFile] = shim.BackendInfo{}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := shortSocketTestDir(t)
			meta := persist.Meta{ID: "testmanageraaaaa", AgentType: "codex", Status: status.Status{Process: status.ProcessLost}, ShimPID: 1 << 30, ShimStartTime: 1}
			dir := filepath.Join(root, meta.ID)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			native := shim.NativeProcessInfo{SchemaVersion: shim.NativeWriterSchemaVersion, ShimPID: meta.ShimPID, ShimStartTime: meta.ShimStartTime, Generation: instance, Provider: meta.AgentType, BootID: "old-boot"}
			files := map[string]any{
				"shim-launch.json": map[string]any{"session_id": meta.ID, "session_dir": dir, "native_provider": meta.AgentType,
					"backend_required": true, "backend_program": "/test/codex", "backend_socket_path": filepath.Join(dir, "codex.sock")},
				sessionDegradedFile:    historyTornMarker{Version: 1, SessionInstance: instance, Generation: backendGapGeneration(gapBackendNeverServed)},
				shim.NativeProcessFile: native,
				shim.NativeStoppedFile: shim.NativeStoppedInfo{SchemaVersion: shim.NativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true},
			}
			if test.change != nil {
				test.change(files)
			}
			for name, value := range files {
				if err := writeJSONStateFile(filepath.Join(dir, name), value); err != nil {
					t.Fatal(err)
				}
			}
			if proved := provedLostManagerStartup(root, meta, instance); proved != (test.change == nil) {
				t.Fatalf("proved = %t", proved)
			}
		})
	}
}
