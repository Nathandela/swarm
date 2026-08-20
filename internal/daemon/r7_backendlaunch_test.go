package daemon

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.1's DAEMON half: the backend's launch config,
// its recovery across a daemon restart, and the orphan reaper that closes the SIGKILL
// residual. Bead: agents-tracker-hggx.8. ADR-013 §R7.2b/c and §R7.6.
//
// THE CONTRACT these tests freeze:
//
//	LaunchSpec.Backend *BackendSpec   // carried, never derived -- the daemon imports no
//	                                  // adapter package (LaunchSpec.CaptureEvents' own rule)
//	type BackendSpec struct{ Program string; Args, AgentArgs, Env []string }
//	shimSpawnConfig gains backend_program / backend_args / backend_agent_args /
//	  backend_socket_path, on HookSocketPath's exact UNSET-MEANS-DISABLED convention
//	func backendSocketPath(stateDir, id string) string          // <session-dir>/codex.sock
//	func (d *Daemon) SessionBackend(id string) (BackendChannel, bool)
//	func (d *Daemon) backendAliveAt(sessionDir string) bool   // pid alive AND start-time matches
//	func (d *Daemon) reapOrphanBackend(id string)             // called from reconcile's orphan path
//
// WHY LIVENESS IS A FACT AND NOT A PROBE. The agent's documented last-resort residual (an
// uncatchable SIGKILL of the shim) is bounded in practice by the PTY: the master closes and
// the agent takes SIGHUP/EIO. The app-server has NO PTY, no controlling terminal, and writes
// to neither stream ever, so a SIGKILL of its shim leaves a process authenticated to a real
// ChatGPT account alive indefinitely, still serving <session-dir>/codex.sock -- and still
// looking HEALTHY to any rule whose readiness test is "the dial succeeded". A restarted daemon
// would redial, succeed, and report a session live whose agent died hours ago.

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// r7BackendSpec is a plan the daemon can carry. Program is ALREADY RESOLVED: the assembly
// runs adapter.ResolveBackend (obligations 9a/9c) before it ever reaches this layer, which is
// why nothing here imports internal/adapter.
// THE PROGRAM IS DELIBERATELY A PATH THAT DOES NOT EXIST, and both reasons matter (round-3
// review, and hard rules 7 and 10):
//
//  1. These tests launch REAL shims. Naming the installed CLI made every one of them exec
//     `/usr/local/bin/codex app-server` on the owner's machine against the owner's real
//     account -- a CI-facing test running the real CLI, which hard rule 7 forbids in as many
//     words ("CI-facing tests are fixture-driven").
//  2. It was also FLAKY, and the flake was self-inflicted. r7Squatter BINDS the very socket
//     the shim told its backend to serve, so the shim's readiness poll -- which can only ask
//     the socket, since `codex app-server` writes nothing to either stream -- saw the
//     SQUATTER's listener, declared the backend servable, and wrote backend.json naming the
//     process it had spawned. That write RACED the test's own, and when it won, backendAliveAt
//     read a pid that had already died (PROBED: file pid 38518, squatter pid 38519).
//
// A program that cannot start means startBackend fails at cmd.Start and NOTHING is written, so
// the test's own backend.json is the only one there is. Nothing under test is weakened: every
// assertion here is about the plan the DAEMON carries and persists, never about a process.
func r7BackendSpec(sock string) *BackendSpec {
	return &BackendSpec{
		Program:   "/nonexistent/swarm-test/codex",
		Args:      []string{"app-server", "--listen", "unix://" + sock},
		AgentArgs: []string{"--remote", "unix://" + sock},
	}
}

// r7ReadLaunchConfig decodes a session's 0600 shim-launch.json as a raw object, so a test can
// assert on WIRE NAMES rather than on a struct field the test and the code share.
func r7ReadLaunchConfig(t *testing.T, d *Daemon, id string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(d.sessionDir(id), shimLaunchConfigFile))
	if err != nil {
		t.Fatalf("read %s for %s: %v", shimLaunchConfigFile, id, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode %s: %v", shimLaunchConfigFile, err)
	}
	return raw
}

// r7WriteBackendInfo forges a <session-dir>/backend.json, standing in for the shim.
func r7WriteBackendInfo(t *testing.T, dir string, pid, pgid int, startedAtMS int64, sock string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"pid": pid, "pgid": pgid, "started_at_ms": startedAtMS, "socket_path": sock,
	})
	if err != nil {
		t.Fatalf("marshal backend.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backend.json"), body, 0o600); err != nil {
		t.Fatalf("write backend.json: %v", err)
	}
}

// r7Squatter starts a long-lived process that BINDS sock and serves it, and returns its pid.
// It is the orphaned app-server: alive, serving, and attached to nothing.
func r7Squatter(t *testing.T, sock string) int {
	t.Helper()
	// `nc -lU` would do, but is not portable; a tiny sh loop holding an open listener is.
	cmd := exec.Command("/bin/sh", "-c", `exec 3<>/dev/null; while :; do sleep 3600; done`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start squatter: %v", err)
	}
	// REAP IT IN THE BACKGROUND. Without this the killed squatter lingers as a ZOMBIE of the
	// test process, and kill(pid, 0) on a zombie SUCCEEDS -- so a correctly reaped backend
	// would still read alive and the reaper fence could never pass.
	reaped := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(reaped) }()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("bind squatter socket: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-reaped
	})
	return cmd.Process.Pid
}

// ---------------------------------------------------------------------------
// §R7.2b -- the socket lives in the session dir, and absence means disabled
// ---------------------------------------------------------------------------

// TestR7BackendLaunch_TheSocketIsASiblingOfHookSock pins the path. It matters that it is
// deterministic and inside the session dir: the containment check of obligation 9c measures
// against exactly this directory, and the orphan reaper finds the socket by path.
func TestR7BackendLaunch_TheSocketIsASiblingOfHookSock(t *testing.T) {
	const state, id = "/state", "01JSESSION"
	got := backendSocketPath(state, id)
	want := filepath.Join(state, id, "codex.sock")
	if got != want {
		t.Errorf("backendSocketPath = %q, want %q (sibling of %q)", got, want, hookSocketPath(state, id))
	}
}

// TestR7BackendLaunch_ADeclaredBackendReachesTheShimLaunchConfigByWireName is the plumbing
// fence. The daemon PERSISTS the intent, because shims outlive daemons and a restarted daemon
// recovers everything about a session from this 0600 file (SessionHookChannel's own rule).
func TestR7BackendLaunch_ADeclaredBackendReachesTheShimLaunchConfigByWireName(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.AgentType = "codex"

	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	sock := backendSocketPath(d.cfg.StateDir, m.ID)
	spec.Backend = r7BackendSpec(sock)

	m2, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch with a backend: %v", err)
	}
	raw := r7ReadLaunchConfig(t, d, m2.ID)

	for _, key := range []string{"backend_program", "backend_args", "backend_agent_args", "backend_socket_path"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("%s carries no %q key for a session launched WITH a backend; a restarted daemon "+
				"recovers the whole session from this file and cannot invent what is not in it",
				shimLaunchConfigFile, key)
		}
	}
	if got, _ := raw["backend_socket_path"].(string); got != backendSocketPath(d.cfg.StateDir, m2.ID) {
		t.Errorf("backend_socket_path = %q, want the session's own %q", got, backendSocketPath(d.cfg.StateDir, m2.ID))
	}

	// The FIRST launch declared no backend: absence must be absence, on HookSocketPath's exact
	// unset-means-disabled convention (launch.go:123-125). This is what makes every pre-R7
	// session, and every non-Codex session forever, run exactly as it does today.
	raw0 := r7ReadLaunchConfig(t, d, m.ID)
	if v, ok := raw0["backend_socket_path"]; ok && v != "" {
		t.Errorf("a session launched with NO backend persisted backend_socket_path %v; an absent key "+
			"means \"this session was launched without a backend\" and is never a defect", v)
	}
}

// TestR7BackendLaunch_SessionBackendRecoversTheChannelAcrossADaemonRestart is the twin of
// SessionHookChannel and exists for the same reason: shims outlive daemons (ADR-001).
func TestR7BackendLaunch_SessionBackendRecoversTheChannelAcrossADaemonRestart(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.AgentType = "codex"
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	sock := backendSocketPath(cfg.StateDir, m.ID)
	spec.Backend = r7BackendSpec(sock)
	m2, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch with a backend: %v", err)
	}

	ch, ok := d.SessionBackend(m2.ID)
	if !ok {
		t.Fatal("SessionBackend reports no backend for a session launched with one")
	}
	if ch.SocketPath != backendSocketPath(cfg.StateDir, m2.ID) {
		t.Errorf("SessionBackend socket = %q, want %q", ch.SocketPath, backendSocketPath(cfg.StateDir, m2.ID))
	}
	if _, ok := d.SessionBackend(m.ID); ok {
		t.Error("SessionBackend fabricated a backend for a session launched without one; that is the " +
			"pre-R7 session and it must read as having none")
	}
}

// ---------------------------------------------------------------------------
// §R7.2c -- liveness is a FACT, and the orphan reaper
// ---------------------------------------------------------------------------

// TestR7BackendLiveness_ADialThatSUCCEEDSIsNotLiveness is the mutation fence §R7.2c names in
// as many words: "make the liveness check dial(socketPath) == nil and a permanent test that
// leaves a live process serving a stale socket with no matching backend.json must fail."
func TestR7BackendLiveness_ADialThatSUCCEEDSIsNotLiveness(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	// shortStateDir, not t.TempDir(): macOS $TMPDIR plus a test-name subdirectory blows past
	// the 104-byte sun_path limit and the squatter's bind fails with EINVAL.
	dir := shortStateDir(t)
	sock := filepath.Join(dir, "codex.sock")

	// A live process is serving the socket -- a dial WILL succeed -- but backend.json names a
	// different pid. It is an unrelated process on a reused path.
	squatter := r7Squatter(t, sock)
	r7WriteBackendInfo(t, dir, squatter+100000, squatter+100000, time.Now().UnixMilli(), sock)

	if d.backendAliveAt(dir) {
		t.Fatal("the daemon called a backend live because the DIAL succeeded. A socket outlives the " +
			"process that bound it, and adopting an unrelated process on a reused path means the " +
			"daemon reports a session live whose agent died hours ago")
	}
}

// TestR7BackendLiveness_APidWhoseStartTimeDisagreesIsNotLive closes the recycled-pid hole. It
// uses the SAME (pid, start-time) pair reconcile already matches shims by (S1/L2,
// launch.go:415-425), which is why the fix costs no new mechanism.
func TestR7BackendLiveness_APidWhoseStartTimeDisagreesIsNotLive(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	// shortStateDir, not t.TempDir(): macOS $TMPDIR plus a test-name subdirectory blows past
	// the 104-byte sun_path limit and the squatter's bind fails with EINVAL.
	dir := shortStateDir(t)
	sock := filepath.Join(dir, "codex.sock")
	pid := r7Squatter(t, sock)

	// The right pid, an impossible start time.
	r7WriteBackendInfo(t, dir, pid, pid, 1, sock)
	if d.backendAliveAt(dir) {
		t.Error("a pid whose recorded start-time does not match the running process read LIVE; on a " +
			"busy machine a recycled pid is not exotic and adopting one attaches the daemon to a " +
			"stranger's process")
	}

	// The right pid AND the right start time is the only combination that is live.
	st, err := procStartTimeFn(pid)
	if err != nil {
		t.Fatalf("procStartTimeFn(%d): %v", pid, err)
	}
	r7WriteBackendInfo(t, dir, pid, pid, st, sock)
	if !d.backendAliveAt(dir) {
		t.Error("a pid that is alive with a matching start-time read DEAD; the check must not be so " +
			"strict that it reaps every healthy backend")
	}
}

// TestR7BackendReaper_AnOrphanedBackendIsKILLEDAndItsFileRemoved is the residual-closing path.
// An app-server whose shim is gone is BY CONSTRUCTION an orphan: nothing will ever reap it and
// no agent is attached to it.
func TestR7BackendReaper_AnOrphanedBackendIsKILLEDAndItsFileRemoved(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	dir := d.sessionDir(m.ID)
	sock := filepath.Join(dir, "codex.sock")
	pid := r7Squatter(t, sock)
	st, err := procStartTimeFn(pid)
	if err != nil {
		t.Fatalf("procStartTimeFn: %v", err)
	}
	r7WriteBackendInfo(t, dir, pid, pid, st, sock)

	d.reapOrphanBackend(m.ID)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Errorf("the orphaned backend (pid %d) survived reapOrphanBackend; it holds real account "+
			"credentials and nothing else in the system will ever reap it", pid)
	}
	if _, err := os.Stat(filepath.Join(dir, "backend.json")); err == nil {
		t.Error("backend.json survived the reap; a stale file makes the NEXT reconcile try to reap a " +
			"pid that is now somebody else's")
	}
}

// TestR7BackendReaper_NeverDials is the negative that keeps the reaper from re-introducing the
// very confusion it exists to resolve: it acts on the recorded pid and the recorded start-time
// and NOTHING else. A reaper that dialled would treat a squatter as its own backend and would
// spare it.
func TestR7BackendReaper_NeverDials(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	dir := d.sessionDir(m.ID)
	sock := filepath.Join(dir, "codex.sock")
	pid := r7Squatter(t, sock)

	// backend.json names a DIFFERENT pid. The socket is live and dialable; the recorded pid is
	// not this process.
	r7WriteBackendInfo(t, dir, pid+100000, pid+100000, time.Now().UnixMilli(), sock)
	d.reapOrphanBackend(m.ID)

	if syscall.Kill(pid, 0) != nil {
		t.Error("reapOrphanBackend killed a process backend.json does not name; the reaper acts on a " +
			"RECORDED pid, never on whatever happens to hold the socket")
	}
}

// ---------------------------------------------------------------------------
// §R7.6 -- the lifecycle trio
// ---------------------------------------------------------------------------

// TestR7BackendLifecycle_DaemonRestartUnderALiveShimAdoptsTheBackendRatherThanOrphaningIt is
// the ordinary case ADR-001 exists to make ordinary. A restarted daemon must find the same
// backend it minted at launch, and must NOT reap it.
func TestR7BackendLifecycle_DaemonRestartUnderALiveShimAdoptsTheBackendRatherThanOrphaningIt(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.AgentType = "codex"
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	sock := backendSocketPath(cfg.StateDir, m.ID)
	spec.Backend = r7BackendSpec(sock)
	m2, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch with a backend: %v", err)
	}
	dir := d.sessionDir(m2.ID)
	pid := r7Squatter(t, filepath.Join(dir, "codex.sock"))
	st, err := procStartTimeFn(pid)
	if err != nil {
		t.Fatalf("procStartTimeFn: %v", err)
	}
	r7WriteBackendInfo(t, dir, pid, pid, st, filepath.Join(dir, "codex.sock"))

	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	d2 := openDaemon(t, cfg)

	if syscall.Kill(pid, 0) != nil {
		t.Fatal("a daemon RESTART killed the backend of a session whose shim is still alive. " +
			"`swarm daemon restart` is the operation ADR-001 exists to make ordinary, and a rule " +
			"that reaps on restart makes it destructive")
	}
	ch, ok := d2.SessionBackend(m2.ID)
	if !ok {
		t.Fatal("the restarted daemon does not recover the session's backend channel")
	}
	if !d2.backendAliveAt(dir) {
		t.Errorf("the restarted daemon reads the live backend at %q as dead", ch.SocketPath)
	}
}

// TestR7BackendLifecycle_ACodexUpgradeUnderALiveSessionChangesNothing is the third of the
// trio. The running app-server keeps running; only a NEW session gets a new binary. The
// session's own launch config is the authority and it is on disk.
func TestR7BackendLifecycle_ACodexUpgradeUnderALiveSessionChangesNothing(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.AgentType = "codex"
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	sock := backendSocketPath(cfg.StateDir, m.ID)
	spec.Backend = r7BackendSpec(sock)
	spec.Backend.Program = "/opt/codex-0.147.0/bin/codex"
	m2, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	raw := r7ReadLaunchConfig(t, d, m2.ID)
	if got, _ := raw["backend_program"].(string); got != "/opt/codex-0.147.0/bin/codex" {
		t.Errorf("backend_program = %q, want the RESOLVED absolute path the assembly supplied. "+
			"Persisting a bare name would make a restarted daemon re-resolve it through a PATH that "+
			"may now point at a different version", got)
	}
	if strings.Contains(strings.Join(argStrings(raw["backend_args"]), " "), "sh -c") {
		t.Error("backend_args smells like a shell invocation; the plan is exec'd DIRECTLY (obligation 9b)")
	}
}

// argStrings coerces a decoded JSON array to []string.
func argStrings(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, _ := e.(string)
		out = append(out, s)
	}
	return out
}
