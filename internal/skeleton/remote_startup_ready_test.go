package skeleton

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// A configured remote.sock is bound before the rest of assembly finishes. It
// must not process even Hello in that interval: input gates and persisted auth
// recycle embargoes are restored only before ready closes.
func TestRemoteSocketDoesNotServeBeforeAssemblyReady(t *testing.T) {
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swsk-ready")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	bound := make(chan struct{})
	release := make(chan struct{})
	serveDone := make(chan struct {
		d   *Daemon
		err error
	}, 1)
	go func() {
		d, serveErr := Serve(Config{
			StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"),
			LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log"),
			ShimBinary: swarmBin, FakeAgentBin: fakeAgentBin, MaxSessions: 4,
			RemoteSocketPath: filepath.Join(dir, "r.sock"),
			remoteBound: func() {
				close(bound)
				<-release
			},
		})
		serveDone <- struct {
			d   *Daemon
			err error
		}{d, serveErr}
	}()
	select {
	case <-bound:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("remote listener was not bound")
	}

	conn, err := net.DialTimeout("unix", filepath.Join(dir, "r.sock"), time.Second)
	if err != nil {
		close(release)
		t.Fatalf("dial bound remote socket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	r := &rawRemote{t: t, conn: conn}
	r.write(protocol.Control{Op: protocol.OpHello, ProtocolVersion: protocol.Version})
	if reply, readErr := r.readTry(150 * time.Millisecond); readErr == nil {
		close(release)
		result := <-serveDone
		if result.d != nil {
			_ = result.d.Close()
		}
		t.Fatalf("remote socket served before assembly ready: %+v", reply)
	}

	close(release)
	result := <-serveDone
	if result.err != nil {
		t.Fatalf("Serve after startup release: %v", result.err)
	}
	t.Cleanup(func() { _ = result.d.Close() })
	if reply := r.read(5 * time.Second); reply.Op != protocol.OpHello {
		t.Fatalf("remote hello after ready = %q, want %q", reply.Op, protocol.OpHello)
	}
}

func TestFailedAssemblyClosesBoundRemoteListener(t *testing.T) {
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swsk-ready-fail")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// newSupervisor expects a directory. A regular file makes assembly fail only
	// after both protocol servers and remote.sock have been created.
	if err := os.WriteFile(filepath.Join(dir, "supervision"), []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"),
		LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log"),
		ShimBinary: swarmBin, FakeAgentBin: fakeAgentBin, MaxSessions: 4,
		RemoteSocketPath: filepath.Join(dir, "r.sock"),
	}
	if d, serveErr := Serve(cfg); serveErr == nil {
		_ = d.Close()
		t.Fatal("Serve unexpectedly succeeded with an invalid supervision store")
	}
	if conn, dialErr := net.DialTimeout("unix", cfg.RemoteSocketPath, 200*time.Millisecond); dialErr == nil {
		_ = conn.Close()
		t.Fatal("failed assembly left its remote listener accepting connections")
	}

	if err := os.Remove(filepath.Join(dir, "supervision")); err != nil {
		t.Fatal(err)
	}
	d, err := Serve(cfg)
	if err != nil {
		t.Fatalf("same paths could not be rebound after fixing assembly input: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
}
