package voice

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func privateServerDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "vs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestPrivateServerOwnsSingletonSocketAndClosesOnCancel(t *testing.T) {
	dir := privateServerDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ready") })
	s, err := startPrivateHTTP(ctx, dir, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	info, err := os.Stat(s.socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket is not private", err)
	}
	if other, err := startPrivateHTTP(ctx, dir, h); err == nil {
		_ = other.Close()
		t.Fatal("duplicate controller admitted")
	}
	conn, err := net.DialTimeout("unix", s.socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	cancel()
	select {
	case <-s.closed:
	case <-time.After(time.Second):
		t.Fatal("private service did not stop")
	}
	if _, err := os.Lstat(s.socket); !os.IsNotExist(err) {
		t.Fatal("owned socket left behind", err)
	}
	next, err := startPrivateHTTP(context.Background(), dir, h)
	if err != nil {
		t.Fatal("singleton lock not released", err)
	}
	_ = next.Close()
}

func TestPrivateServerRefusesUnsafeExistingSocket(t *testing.T) {
	for _, kind := range []string{"regular", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateServerDir(t)
			path := filepath.Join(dir, "bridge.sock")
			if kind == "regular" {
				if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Symlink("missing", path); err != nil {
					t.Fatal(err)
				}
			}
			if s, err := startPrivateHTTP(context.Background(), dir, http.NotFoundHandler()); err == nil {
				_ = s.Close()
				t.Fatal("unsafe endpoint replaced")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("unsafe existing path removed", err)
			}
		})
	}
}

func TestPrivateServerCleanupDoesNotRemoveReplacement(t *testing.T) {
	dir := privateServerDir(t)
	s, err := startPrivateHTTP(context.Background(), dir, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.socket); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.socket, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.socket)
	if err != nil || string(data) != "replacement" {
		t.Fatal("cleanup removed another file", err)
	}
}
