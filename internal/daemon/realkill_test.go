package daemon

// Real-subprocess fidelity tests (Epic 5, audit-004 F7/F8). These spawn genuine
// daemon PROCESSES — a test-binary daemon host that launches and OWNS real shims
// (F7), and two production `swarm daemon` processes racing the singleton (F8) — so
// S1/S12 are proven against real process death, reparenting and fd inheritance,
// not the in-process abandon() model. New file; frozen tests untouched.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// envHostActive activates the daemon-host subprocess entrypoint; envHostN is how
// many sessions it launches. Test-internal, kept beside the entrypoint.
const (
	envHostActive = "SWARM_TEST_DAEMON_HOST"
	envHostN      = "SWARM_TEST_HOST_N"
)

// waitDial polls Dial until the daemon at sock answers or the timeout elapses.
func waitDial(t *testing.T, sock string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if conn, err := Dial(sock, ProtocolVersion); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("no daemon reachable at %s within %s", sock, timeout)
}

// listIDs returns the ids currently in a daemon's registry.
func listIDs(d *Daemon) []string {
	metas := d.List()
	ids := make([]string, 0, len(metas))
	for _, m := range metas {
		ids = append(ids, m.ID)
	}
	return ids
}

// TestDaemonHostSubprocess is the daemon-host ENTRYPOINT, re-exec'd by the real
// kill-9 test via `-test.run`. When activated it opens a real daemon, launches N
// long-lived announce sessions (which become its own child shims), and serves
// until it is SIGKILLed. In a normal suite run it is inert.
func TestDaemonHostSubprocess(t *testing.T) {
	if os.Getenv(envHostActive) != "1" {
		t.Skip("daemon-host subprocess entrypoint; only runs when re-exec'd")
	}
	stateDir := os.Getenv(envDaemonState)
	n, _ := strconv.Atoi(os.Getenv(envHostN))
	pidDir := filepath.Join(stateDir, "agentpids")
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "daemon-host: mkdir piddir:", err)
		os.Exit(1)
	}
	cfg := Config{
		StateDir:    stateDir,
		SocketPath:  os.Getenv(envDaemonSock),
		LockPath:    os.Getenv(envDaemonLock),
		LogPath:     os.Getenv(envDaemonLog),
		MaxSessions: 64,
		ShimBinary:  swarmBin,
	}
	d, err := Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon-host: open:", err)
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon-host: executable:", err)
		os.Exit(1)
	}
	for i := 0; i < n; i++ {
		pidFile := filepath.Join(pidDir, fmt.Sprintf("agent-%d.pid", i))
		if _, err := d.Launch(LaunchSpec{
			AgentType: "fake",
			Argv:      []string{self, markerAnnounce, pidFile},
			Cwd:       stateDir,
			ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
			Cols:      80,
			Rows:      24,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "daemon-host: launch:", err)
			os.Exit(1)
		}
	}
	select {} // serve until the parent SIGKILLs this process
}

// TestSurvival_RealKillNineReconnectsAll asserts E5.8/S1 against a REAL daemon
// process: a genuine daemon subprocess launches N real shims (its children), then
// is SIGKILLed. Every agent must survive the daemon's real death (reparented to
// init, fds independent), and a fresh daemon must reconnect all N and be able to
// drive them (Kill). This is the true kill-9 proof — stronger than abandon(),
// which never reparents the shims off the test process.
func TestSurvival_RealKillNineReconnectsAll(t *testing.T) {
	const n = 3
	dir := shortStateDir(t)
	sock := filepath.Join(dir, "daemon.sock")
	lock := filepath.Join(dir, "daemon.lock")
	logp := filepath.Join(dir, "daemon.log")

	host := exec.Command(selfExe(t), "-test.run", "^TestDaemonHostSubprocess$")
	host.Env = append(os.Environ(),
		envHostActive+"=1",
		envHostN+"="+strconv.Itoa(n),
		envDaemonState+"="+dir,
		envDaemonSock+"="+sock,
		envDaemonLock+"="+lock,
		envDaemonLog+"="+logp,
	)
	host.Stdout, host.Stderr = os.Stderr, os.Stderr
	if err := host.Start(); err != nil {
		t.Fatalf("start daemon host: %v", err)
	}
	hostPID := host.Process.Pid
	hostDone := make(chan struct{})
	go func() { _, _ = host.Process.Wait(); close(hostDone) }()
	t.Cleanup(func() { killTree(hostPID) })

	// Wait until the host has launched all N (each announce agent writes its pidfile).
	pidDir := filepath.Join(dir, "agentpids")
	var agentPIDs []int
	deadline := time.Now().Add(launchTimeout)
	for time.Now().Before(deadline) {
		agentPIDs = agentPIDs[:0]
		for i := 0; i < n; i++ {
			b, err := os.ReadFile(filepath.Join(pidDir, fmt.Sprintf("agent-%d.pid", i)))
			if err != nil {
				continue
			}
			if pid, cerr := strconv.Atoi(strings.TrimSpace(string(b))); cerr == nil && pid > 0 {
				agentPIDs = append(agentPIDs, pid)
			}
		}
		if len(agentPIDs) == n {
			break
		}
		select {
		case <-hostDone:
			t.Fatalf("daemon host exited before launching all %d agents", n)
		default:
		}
		time.Sleep(pollStep)
	}
	if len(agentPIDs) != n {
		t.Fatalf("host launched %d/%d agents before kill", len(agentPIDs), n)
	}
	captured := append([]int(nil), agentPIDs...)
	t.Cleanup(func() {
		for _, p := range captured {
			killTree(p)
		}
	})
	waitDial(t, sock, pollTimeout) // the host is serving (reconcile+launch done)

	// REAL kill -9 of the REAL daemon process.
	if err := syscall.Kill(hostPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill -9 daemon host: %v", err)
	}
	<-hostDone // the daemon process is truly gone (lock released, socket stale)

	// S1: every agent survives the daemon's real death.
	for _, pid := range agentPIDs {
		if !processAlive(pid) {
			t.Fatalf("agent %d died when the real daemon was kill -9'd; violates S1 survival", pid)
		}
	}

	// A fresh daemon over the same state dir reconnects all N — in-process so we can
	// observe its registry and drive it (the killed daemon was the real subprocess).
	cfg := Config{StateDir: dir, SocketPath: sock, LockPath: lock, LogPath: logp, MaxSessions: 64, ShimBinary: swarmBin}
	d2 := openDaemon(t, cfg)
	ids := listIDs(d2)
	if len(ids) != n {
		t.Fatalf("restarted registry size = %d; want %d", len(ids), n)
	}
	for _, id := range ids {
		got := waitStatus(t, d2, id, status.ProcessRunning, pollTimeout)
		if got.Status.Process != status.ProcessRunning {
			t.Fatalf("session %s not reconnected after real kill -9: process = %q", id, got.Status.Process)
		}
	}
	for _, pid := range agentPIDs {
		if !processAlive(pid) {
			t.Fatalf("agent %d not alive after reconnect", pid)
		}
	}
	// Drive proof: the reconnected daemon kills each session; the agents die.
	for _, id := range ids {
		if err := d2.Kill(id); err != nil {
			t.Fatalf("Kill %s via the reconnected daemon: %v", id, err)
		}
	}
	for _, pid := range agentPIDs {
		waitProcessGone(t, pid, pollTimeout)
	}
}

// TestRestart_RealDaemonHandsOff asserts N1 against real processes: `swarm daemon
// restart` must stop the running daemon and bring up a replacement that actually
// took over — reporting success only once a client can reach the new daemon, never
// prematurely while the old daemon still holds the lock. Uses the real Restart()
// path (defaultSpawnDaemon), not the abandon()+Open model.
func TestRestart_RealDaemonHandsOff(t *testing.T) {
	dir := shortStateDir(t)
	cc := ClientConfig{
		StateDir:   dir,
		SocketPath: filepath.Join(dir, "daemon.sock"),
		LockPath:   filepath.Join(dir, "daemon.lock"),
		LogPath:    filepath.Join(dir, "daemon.log"),
		DaemonBin:  swarmBin,
	}

	// Start the first real daemon and wait for it to serve.
	old := exec.Command(swarmBin, "daemon")
	old.Env = append(os.Environ(),
		EnvStateDir+"="+dir,
		EnvSocket+"="+cc.SocketPath,
		EnvLock+"="+cc.LockPath,
		EnvLog+"="+cc.LogPath,
	)
	old.Stdout, old.Stderr = os.Stderr, os.Stderr
	if err := old.Start(); err != nil {
		t.Fatalf("start first daemon: %v", err)
	}
	oldPID := old.Process.Pid
	oldDone := make(chan struct{})
	go func() { _, _ = old.Process.Wait(); close(oldDone) }()
	t.Cleanup(func() { killTree(oldPID); stopRunningDaemon(cc) })
	waitDial(t, cc.SocketPath, pollTimeout)

	// The real restart: stop the old daemon, wait for the lock, spawn + confirm.
	if err := Restart(cc); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	// Restart returned success, so a client MUST reach a live daemon now.
	conn, err := Dial(cc.SocketPath, ProtocolVersion)
	if err != nil {
		t.Fatalf("Dial after Restart reported success: %v", err)
	}
	_ = conn.Close()

	// The old daemon is gone; the pidfile names a different, live daemon.
	select {
	case <-oldDone:
	case <-time.After(pollTimeout):
		t.Fatalf("old daemon still running after Restart")
	}
	if processAlive(oldPID) {
		t.Fatalf("old daemon PID %d still alive after Restart", oldPID)
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.pid"))
	if err != nil {
		t.Fatalf("read pidfile after Restart: %v", err)
	}
	newPID, _, ok := parsePIDFile(data)
	if !ok {
		t.Fatalf("pidfile after Restart is unparseable: %q", data)
	}
	if newPID == oldPID {
		t.Fatalf("pidfile still names the old daemon (%d); want a fresh one", oldPID)
	}
	if !processAlive(newPID) {
		t.Fatalf("replacement daemon PID %d not alive after Restart", newPID)
	}
}

// TestSingleton_TwoRealProcessesOneWins asserts E5.1/S12/D-7 against real
// processes: two production `swarm daemon` processes started simultaneously over
// the same lock/socket → exactly one acquires the singleton and serves, the other
// loses and exits. A client reaches the survivor.
func TestSingleton_TwoRealProcessesOneWins(t *testing.T) {
	dir := shortStateDir(t)
	sock := filepath.Join(dir, "daemon.sock")
	env := append(os.Environ(),
		EnvStateDir+"="+dir,
		EnvSocket+"="+sock,
		EnvLock+"="+filepath.Join(dir, "daemon.lock"),
		EnvLog+"="+filepath.Join(dir, "daemon.log"),
	)
	newDaemon := func() *exec.Cmd {
		cmd := exec.Command(swarmBin, "daemon")
		cmd.Env = env
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		return cmd
	}

	a, b := newDaemon(), newDaemon()
	if err := a.Start(); err != nil {
		t.Fatalf("start daemon a: %v", err)
	}
	if err := b.Start(); err != nil {
		t.Fatalf("start daemon b: %v", err)
	}
	t.Cleanup(func() { killTree(a.Process.Pid); killTree(b.Process.Pid) })

	aDone := make(chan struct{})
	bDone := make(chan struct{})
	go func() { _, _ = a.Process.Wait(); close(aDone) }()
	go func() { _, _ = b.Process.Wait(); close(bDone) }()

	// Exactly one loses the singleton and exits (ErrAlreadyRunning → exit 1).
	select {
	case <-aDone:
	case <-bDone:
	case <-time.After(pollTimeout):
		t.Fatalf("neither daemon exited; want exactly one to lose the singleton (S12)")
	}

	// The winner is up and reachable, and it is the only one still running.
	waitDial(t, sock, pollTimeout)
	aAlive := processAlive(a.Process.Pid)
	bAlive := processAlive(b.Process.Pid)
	if aAlive == bAlive {
		t.Fatalf("expected exactly one daemon alive (S12); a-alive=%v b-alive=%v", aAlive, bAlive)
	}
	conn, err := Dial(sock, ProtocolVersion)
	if err != nil {
		t.Fatalf("Dial the surviving daemon: %v", err)
	}
	_ = conn.Close()
}
