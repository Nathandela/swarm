package appserver_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/appserver"
)

func TestUnixEndpointPinsPrivateSocket(t *testing.T) {
	f := newFakeServer(t)
	canonical, err := filepath.EvalSymlinks(f.sock)
	if err != nil {
		t.Fatal(err)
	}
	f.sock = canonical
	if err := os.Chmod(f.sock, 0600); err != nil {
		t.Fatal(err)
	}
	pin, err := appserver.CaptureUnixEndpoint(f.sock)
	if err != nil {
		t.Fatal(err)
	}
	c, err := appserver.Dial(context.Background(), f.sock, appserver.Options{Endpoint: pin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	pid, uid, ok := c.PeerProcess()
	if !ok || pid != os.Getpid() || uid != uint32(os.Getuid()) {
		t.Fatalf("wrong kernel peer %d/%d/%v", pid, uid, ok)
	}
	if err = os.Chmod(f.sock, 0666); err != nil {
		t.Fatal(err)
	}
	if pin.Unchanged() {
		t.Fatal("permissions changed without invalidating pin")
	}
	if _, err = appserver.CaptureUnixEndpoint(f.sock); err == nil {
		t.Fatal("public socket admitted")
	}
	if _, err = appserver.Dial(context.Background(), f.sock, appserver.Options{Endpoint: pin}); err == nil {
		t.Fatal("changed pin dialed")
	}
}

func TestUnixEndpointRejectsAncestorAndArbitrarySymlinks(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "swep-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	private := filepath.Join(root, "private")
	if err = os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(private, "n")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err = os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err = os.Symlink(socket, alias); err != nil {
		t.Fatal(err)
	}
	if _, err = appserver.CaptureUnixEndpoint(alias); err == nil {
		t.Fatal("arbitrary alias admitted")
	}
	if err = os.Symlink(private, filepath.Join(root, "indirect")); err != nil {
		t.Fatal(err)
	}
	if _, err = appserver.CaptureUnixEndpoint(filepath.Join(root, "indirect", "n")); err == nil {
		t.Fatal("ancestor symlink admitted")
	}
	if err = os.Chmod(private, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = appserver.CaptureUnixEndpoint(socket); err == nil {
		t.Fatal("public parent admitted")
	}
}

func TestUnixEndpointAcceptsOnlyPinnedCodexAlias(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "swep-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	alias := filepath.Join(root, "n")
	tmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, fmt.Sprintf("codex-daemon-%d", os.Getuid()))
	if err = os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	target := filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(alias))))
	ln, err := net.Listen("unix", target)
	if err != nil {
		t.Fatal(err)
	}
	if unixListener, ok := ln.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(false)
	}
	defer func() { _ = ln.Close() }()
	owned, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		current, e := os.Lstat(target)
		if e == nil && os.SameFile(owned, current) {
			_ = os.Remove(target)
		}
	}()
	if err = os.Chmod(target, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	pin, err := appserver.CaptureUnixEndpoint(alias)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if pin.Unchanged() {
		t.Fatal("replaced alias retained authority")
	}
}
