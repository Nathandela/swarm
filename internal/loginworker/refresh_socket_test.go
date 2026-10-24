package loginworker

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshSocketCleanupKeepsReplacement(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "replaced"}[replaced], func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "refresh-socket-")
			if err != nil {
				t.Fatal(err)
			}
			dir, err = filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			anchor, _ := os.Stat(dir)
			files := &jobFiles{root: root, path: dir, anchor: anchor}
			path := filepath.Join(dir, "n")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			listener.SetUnlinkOnClose(false)
			defer listener.Close()
			if err = os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			owned, err := captureRefreshSocket(files)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.close()
			_ = listener.Close()
			if replaced {
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, []byte("unrelated replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = owned.removeAfterStop(files)
			if replaced {
				if !errors.Is(err, ErrUnsafe) {
					t.Fatalf("replacement cleanup = %v", err)
				}
				if raw, e := os.ReadFile(path); e != nil || string(raw) != "unrelated replacement" {
					t.Fatal("replacement removed")
				}
			} else if _, e := os.Lstat(path); err != nil || !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("owned socket retained: %v %v", err, e)
			}
		})
	}
}

func TestRefreshSocketCaptureRejectsChangedAliasOrParent(t *testing.T) {
	for _, change := range []string{"alias", "parent"} {
		t.Run(change, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "job")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			anchor, _ := os.Stat(dir)
			files := &jobFiles{root: root, path: dir, anchor: anchor}
			path := filepath.Join(dir, "n")
			listen := func() *net.UnixListener {
				l, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if e != nil {
					t.Fatal(e)
				}
				l.SetUnlinkOnClose(false)
				t.Cleanup(func() { _ = l.Close() })
				if e = os.Chmod(path, 0600); e != nil {
					t.Fatal(e)
				}
				return l
			}
			first := listen()
			alias, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			_ = first.Close()
			if change == "parent" {
				if err = os.Rename(dir, filepath.Join(base, "original")); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
			listen()
			captured, err := pinRefreshSocket(files, path, alias, anchor)
			if captured != nil {
				captured.close()
			}
			if !errors.Is(err, ErrUnsafe) {
				t.Fatalf("changed %s captured as owned: %v", change, err)
			}
			if current, e := os.Lstat(path); e != nil || current.Mode()&os.ModeSocket == 0 {
				t.Fatal("replacement was removed")
			}
		})
	}
}
