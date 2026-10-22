package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

func TestFinalSolLoginStoreFailureReleasesCore(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "slf-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(state) }()
	if err = os.Mkdir(filepath.Join(state, "logins"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, "logins", "registry.json"), []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StateDir: state, SocketPath: filepath.Join(state, "d.sock"), LockPath: filepath.Join(state, "d.lock"), LogPath: filepath.Join(state, "d.log"), ShimBinary: "/unused"}
	if d, e := Serve(cfg); e == nil {
		_ = d.Close()
		t.Fatal("corrupt registry accepted")
	}
	if err := os.Remove(filepath.Join(state, "logins", "registry.json")); err != nil {
		t.Fatal(err)
	}
	d, e := Serve(cfg)
	if errors.Is(e, daemon.ErrAlreadyRunning) {
		t.Fatalf("failed startup leaked core singleton lock: %v", e)
	}
	if e != nil {
		t.Fatal(e)
	}
	_ = d.Close()
}
