//go:build linux

package codexnative

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Opt-in, synthetic empty home only. Root owns the enclosing bounded native cgroup.
func TestResolveInstalledOfficialNPM(t *testing.T) {
	wrapper := os.Getenv("SWARM_TEST_CODEX_NPM_WRAPPER")
	if wrapper == "" {
		t.Skip("installed npm native resolver qualification is opt-in")
	}
	group := os.Getenv("SWARM_TEST_NATIVE_CGROUP")
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !strings.HasPrefix(group, "swarm-native-") || strings.ContainsAny(group, "/\n\r") || !strings.Contains(string(cgroup), "/"+group+".service") {
		t.Fatal("a unique owned swarm-native-* systemd service cgroup is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pin := func(path, expected string) {
		t.Helper()
		if !filepath.IsAbs(path) || len(expected) != 64 {
			t.Fatal("exact public installation path and SHA-256 required")
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		hash := sha256.New()
		count, err := io.Copy(hash, io.LimitReader(file, 1<<30+1))
		if err != nil || count > 1<<30 || hex.EncodeToString(hash.Sum(nil)) != expected {
			t.Fatal("installed public binary SHA-256 changed")
		}
	}
	if os.Getenv("SWARM_TEST_CODEX_NPM_WRAPPER_SHA256") != npmLauncherSHA256 {
		t.Fatal("exact qualified public wrapper source required")
	}
	pin(wrapper, npmLauncherSHA256)
	native := os.Getenv("SWARM_TEST_CODEX_BINARY")
	nativeSHA := os.Getenv("SWARM_TEST_CODEX_SHA256")
	pin(native, nativeSHA)
	installation, err := Resolve(wrapper)
	canonical, canonicalErr := filepath.EvalSymlinks(native)
	version := os.Getenv("SWARM_TEST_CODEX_VERSION")
	if err != nil || canonicalErr != nil || installation.Path != canonical || installation.Version != version || version == "" {
		t.Fatalf("actual npm native resolution: %+v %v %v", installation, err, canonicalErr)
	}
	root := t.TempDir()
	for _, name := range []string{"home", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	env := installation.ApplyEnvironment([]string{"PATH=/usr/bin:/bin", "HOME=" + root, "CODEX_HOME=" + filepath.Join(root, "home"), "TMPDIR=" + filepath.Join(root, "tmp"), "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "ALL_PROXY=http://127.0.0.1:1", "CODEX_MANAGED_PACKAGE_ROOT=/untrusted", "CODEX_MANAGED_BY_BUN=1"})
	probe := exec.CommandContext(ctx, installation.Path, "--version")
	probe.Env, probe.Dir = env, root
	output, err := probe.Output()
	if err != nil || strings.TrimSpace(string(output)) != "codex-cli "+version {
		t.Fatalf("actual native banner disagrees with manifest: %q %v", output, err)
	}
	socket := filepath.Join(root, "native.sock")
	cmd := exec.CommandContext(ctx, installation.Path, "app-server", "--listen", "unix://"+socket)
	cmd.Env, cmd.Dir = env, root
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var conn *net.UnixConn
	for ctx.Err() == nil {
		conn, err = net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || conn == nil {
		t.Fatalf("exact native backend did not serve: %v", err)
	}
	defer conn.Close()
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var peer *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) { peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || peerErr != nil || peer == nil || int(peer.Pid) != cmd.Process.Pid || peer.Uid != uint32(os.Getuid()) {
		t.Fatalf("native launcher PID is not exact socket peer: pid=%d peer=%+v %v %v", cmd.Process.Pid, peer, err, peerErr)
	}
	file, err := os.Open(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "environ"))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := io.ReadAll(io.LimitReader(file, 128<<10+1))
	_ = file.Close()
	if err != nil || len(observed) > 128<<10 {
		t.Fatal("native environment observation unavailable")
	}
	entries := strings.Split(string(observed), "\x00")
	for _, expected := range installation.Environment {
		found := false
		for _, entry := range entries {
			if entry == expected {
				found = true
			}
			if strings.HasPrefix(entry, "CODEX_MANAGED_BY_BUN=") || entry == "CODEX_MANAGED_PACKAGE_ROOT=/untrusted" {
				t.Fatal("native received inherited management selectors")
			}
		}
		if !found {
			t.Fatal("native did not receive verified management provenance")
		}
	}
	pin(native, nativeSHA)
	t.Logf("native_version=%s wrapper_sha256=%s native_sha256=%s launcher_pid=%d exact_peer_pid=%d peer_uid=%d verified_management=true empty_home=true", version, npmLauncherSHA256, nativeSHA, cmd.Process.Pid, peer.Pid, peer.Uid)
}
