package protocol

// FAILING-FIRST for slice S4b -- ADR-007 B15, second consequence: the remote-tier socket
// must be 0600.
//
// ServeRemoteWithID net.Listen'd and inherited the process umask, leaning entirely on the
// 0700 state dir to keep the socket private. D4 specifies 0600 on the socket itself. That
// gap was previously reachable only on machines whose operator had opted into a remote
// socket; since B15 every PROVISIONED machine has one, so the mode is now a property of
// the default install rather than of one operator's shell.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestServeRemote_SocketIsPrivate pins the mode under the most permissive umask there is.
// umask 0 is not a contrivance to make the test fail -- it is what isolates the assertion
// from the developer's shell: the socket's mode must come from the code, not from whatever
// the daemon happened to be launched with. Any implementation that only relies on umask
// inheritance produces 0777 here.
func TestServeRemote_SocketIsPrivate(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir, err := os.MkdirTemp("/tmp", "swrm") // short path keeps sun_path under the 104-byte limit
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")

	srv, err := ServeRemoteWithID(newStubDaemon(), sock, "ep")
	if err != nil {
		t.Fatalf("ServeRemoteWithID: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatalf("lstat %s: %v", sock, err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("remote socket mode = %#o, want 0600 (ADR-007 D4). The socket every phone's "+
			"gateway dials is reachable by anyone who can reach its directory.", got)
	}
}
