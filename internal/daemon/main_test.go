// Package daemon is the Epic 5 daemon core: the lifecycle authority that can die
// (kill -9) and come back without losing any agent (invariant S1). It is a
// SINGLETON (flock-before-bind, S12), rebuilds its registry from persist.Scan on
// start and reconnects to live shims by (PID, process-start-time) match (D-4,
// S3), launches sessions two-phase with crash-safe reconciliation (S11), merges
// shim side-files as the SOLE meta writer (G6), routes kill/delete, enforces a
// max-session cap (S-7), and auto-starts detached on demand (D-1).
//
// These are FAILING-FIRST white-box tests (package daemon). They exercise the
// frozen production API a separate implementer will build. The RED state is
// "undefined-only": `go test ./internal/daemon/` fails because these production
// symbols do not yet exist (types, funcs, methods, and a handful of unexported
// test seams the implementer must wire in — see the FROZEN API block below).
//
// FROZEN API (orchestrator decisions; refinements are documented in the final
// test-designer report):
//
//	type Registry interface { List() []persist.Meta; Get(id string) (persist.Meta, bool) }
//	type Config struct {
//	    StateDir, SocketPath, LockPath string
//	    MaxSessions int
//	    ShimBinary, LogPath string
//	    onMetaSave func(persist.Meta) // test seam (E5.3): fires after every daemon meta write
//	}
//	type LaunchSpec struct { AgentType string; Argv []string; Cwd string; ClientEnv []string; Cols, Rows int; Options map[string]string }
//	func Open(cfg Config) (*Daemon, error)     // flock+bind singleton; rebuild+reconnect; ErrAlreadyRunning if the lock is held
//	func (d *Daemon) Launch(spec LaunchSpec) (persist.Meta, error)
//	func (d *Daemon) Kill(id string) error
//	func (d *Daemon) Delete(id string) error
//	func (d *Daemon) List() []persist.Meta
//	func (d *Daemon) Get(id string) (persist.Meta, bool)
//	func (d *Daemon) Close() error
//	var ErrAlreadyRunning, ErrMaxSessions, ErrVersionSkew error
//	var ProtocolVersion int                    // client<->daemon handshake version (var so skew is testable)
//	func Dial(socketPath string, clientVersion int) (net.Conn, error) // handshake; ErrVersionSkew names `swarm daemon restart`
//	type ClientConfig struct { SocketPath, LockPath, StateDir, DaemonBin, LogPath string; spawnDaemon func(ClientConfig) error }
//	func EnsureDaemon(cfg ClientConfig) (net.Conn, error) // D-1 auto-start choke point
//	func processStartTime(pid int) (int64, error)         // /proc/<pid>/stat f22 (linux) | sysctl kinfo_proc (darwin)
//	func generateID() string                              // lowercase-only, collision-resistant, path-safe
//	func shimSocketPath(stateDir, id string) string       // DETERMINISTIC per-session socket path (spawn + reconcile agree)
//
//	// Two-phase-launch crash-injection seam (E5.4/S11):
//	type launchPhase int
//	const ( phaseReserved launchPhase = iota; phaseSpawned; phaseConfirmed )
//	type launchProbe func(phase launchPhase, m persist.Meta) error
//	func (d *Daemon) launch(spec LaunchSpec, probe launchProbe) (persist.Meta, error) // Launch == launch(spec, nil)
//
//	// kill -9 model seam (E5.8/S1): drop lock+socket+shim-conn fds with NO cleanup
//	// and NO shim signalling, exactly as the OS does when the daemon is SIGKILLed.
//	func (d *Daemon) abandon()
//
// DRIVER STRATEGY:
//   - The daemon spawns REAL `swarm shim --config` subprocesses (ShimBinary =
//     the built swarm binary), which exec REAL agents. Shims are detached
//     (setsid) and survive the daemon, so S1/S11 are exercised against real
//     process independence, never a mock.
//   - Agents are this test binary re-exec'd into a mode (announce-pid, env-dump,
//     catch-term). The trigger is an ARGV marker, not an env var, because the
//     launch path FILTERS env (persist.FilterEnv) — an env trigger would be
//     dropped, which is the very thing the differential test asserts.
//   - swarm-fake-agent covers plain scripted lifecycles where a PID is not needed.
//
// Every test carries a deadline; nothing may hang. UNIX socket paths are capped
// near ~104 bytes, so daemon state dirs live under /tmp (short) and session
// sockets are shimSocketPath(stateDir, id) = <stateDir>/<id>/shim.sock.
package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// ---------------------------------------------------------------------------
// Re-exec agent modes (triggered by argv[1], since the launch path filters env)
// ---------------------------------------------------------------------------

const (
	// markerAnnounce: argv = [self, markerAnnounce, pidFile]. Writes its own PID
	// to pidFile, then blocks on stdin — a long-lived, externally identifiable
	// agent for lifecycle/survival tests.
	markerAnnounce = "-swarm-agent-announce"
	// markerEnvDump: argv = [self, markerEnvDump, envFile]. Writes every env var
	// it was actually given (one KEY=VALUE per line) to envFile, then blocks —
	// the ground truth for the FilterEnv differential (what env the AGENT saw).
	markerEnvDump = "-swarm-agent-envdump"
	// markerCatchTerm: argv = [self, markerCatchTerm, sigFile]. Installs a
	// SIGTERM/SIGINT handler that appends the signal name to sigFile, then blocks.
	// Run as a PLAIN child (not under a shim) to prove reconcile sends zero
	// signals on an identity mismatch (S3).
	markerCatchTerm = "-swarm-agent-catchterm"
	// markerDaemonRun: argv = [self, markerDaemonRun]. Runs a real detached daemon
	// (Open + serve until killed), configured from the SWD_* env vars the spawner
	// sets — the faithful subprocess used by the D-1 auto-start test (E5.9).
	markerDaemonRun = "-swarm-test-daemon-run"
)

// Env var names the D-1 test's injected spawner uses to hand config to a
// markerDaemonRun subprocess. Test-internal (NOT a frozen production contract):
// the auto-start seam is ClientConfig.spawnDaemon, so how a spawned daemon reads
// its config is the spawner's business, kept here so both sides agree.
const (
	envDaemonState = "SWD_STATE"
	envDaemonSock  = "SWD_SOCK"
	envDaemonLock  = "SWD_LOCK"
	envDaemonLog   = "SWD_LOG"
)

// runAnnounceAgent writes its PID to argv[2] and blocks until stdin closes.
func runAnnounceAgent() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "announce agent: missing pidfile arg")
		os.Exit(2)
	}
	if err := os.WriteFile(os.Args[2], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "announce agent: write pidfile:", err)
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, os.Stdin) // block until the shim closes the PTY
	os.Exit(0)
}

// runEnvDumpAgent writes its environment (one KEY=VALUE per line) to argv[2],
// then blocks so the shim stays servable for the daemon's launch confirmation.
func runEnvDumpAgent() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "envdump agent: missing envfile arg")
		os.Exit(2)
	}
	var buf []byte
	for _, kv := range os.Environ() {
		buf = append(buf, kv...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(os.Args[2], buf, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "envdump agent: write envfile:", err)
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// runCatchTermAgent records any TERM/INT it receives (proving whether a signal
// was sent) and blocks. It never dies on TERM, so the process staying alive is
// itself evidence, and sigFile stays empty when reconcile correctly sends nothing.
func runCatchTermAgent() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "catchterm agent: missing sigfile arg")
		os.Exit(2)
	}
	sigFile := os.Args[2]
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for s := range ch {
			f, err := os.OpenFile(sigFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				continue
			}
			fmt.Fprintf(f, "%s\n", s)
			f.Close()
		}
	}()
	select {} // only a KILL ends this
}

// runTestDaemon is the faithful detached-daemon subprocess for E5.9: it opens a
// real daemon from the SWD_* env vars and serves until SIGKILLed.
func runTestDaemon() {
	cfg := Config{
		StateDir:    os.Getenv(envDaemonState),
		SocketPath:  os.Getenv(envDaemonSock),
		LockPath:    os.Getenv(envDaemonLock),
		LogPath:     os.Getenv(envDaemonLog),
		MaxSessions: 64,
	}
	d, err := Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-daemon: open:", err)
		os.Exit(1)
	}
	_ = d
	select {} // serve until killed
}

// ---------------------------------------------------------------------------
// TestMain: intercept an agent/daemon re-exec, else build the binaries once
// ---------------------------------------------------------------------------

// swarmBin is the built swarm binary (used as ShimBinary: `swarm shim --config`).
// fakeAgentBin is the built swarm-fake-agent, for scripted-lifecycle cases.
var (
	swarmBin     string
	fakeAgentBin string
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case markerAnnounce:
			runAnnounceAgent() // never returns
		case markerEnvDump:
			runEnvDumpAgent()
		case markerCatchTerm:
			runCatchTermAgent()
		case markerDaemonRun:
			runTestDaemon()
		}
	}

	dir, err := os.MkdirTemp("", "swarm-daemon-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	swarmBin = filepath.Join(dir, "swarm")
	fakeAgentBin = filepath.Join(dir, "swarm-fake-agent")
	for _, b := range []struct{ out, pkg string }{
		{swarmBin, "github.com/Nathandela/swarm/cmd/swarm"},
		{fakeAgentBin, "github.com/Nathandela/swarm/cmd/swarm-fake-agent"},
	} {
		build := exec.Command("go", "build", "-o", b.out, b.pkg)
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build", b.pkg, ":", err)
			os.RemoveAll(dir)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

const (
	// launchTimeout bounds a two-phase Launch (spawn + confirm serving).
	launchTimeout = 20 * time.Second
	// pollTimeout bounds waits for an async side-effect (a file, a status change).
	pollTimeout = 15 * time.Second
	pollStep    = 20 * time.Millisecond
)

// selfExe is the absolute path to this test binary; agents re-exec it as argv[0].
func selfExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

// shortStateDir returns a short-pathed, auto-cleaned state directory. UNIX socket
// paths are capped near ~104 bytes and a session socket is
// <stateDir>/<id>/shim.sock, so the state root must be short — /tmp, not the
// long macOS $TMPDIR.
func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swd")
	if err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// daemonConfig builds a Config over a fresh short state dir with generous limits.
func daemonConfig(t *testing.T) Config {
	t.Helper()
	dir := shortStateDir(t)
	return Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		LockPath:    filepath.Join(dir, "daemon.lock"),
		MaxSessions: 64,
		ShimBinary:  swarmBin,
		LogPath:     filepath.Join(dir, "daemon.log"),
	}
}

// openDaemon opens a daemon and registers Close cleanup. Fatal on error.
func openDaemon(t *testing.T, cfg Config) *Daemon {
	t.Helper()
	d, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// announceSpec builds a LaunchSpec for a long-lived announce agent that writes
// its PID to pidFile. The marker rides in argv (env is filtered on launch).
func announceSpec(t *testing.T, pidFile string) LaunchSpec {
	t.Helper()
	return LaunchSpec{
		AgentType: "fake",
		Argv:      []string{selfExe(t), markerAnnounce, pidFile},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	}
}

// launchAnnounce launches a long-lived announce session and returns its meta and
// the agent's PID (read back from the pidfile the agent writes).
func launchAnnounce(t *testing.T, d *Daemon) (persist.Meta, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	m, err := d.Launch(announceSpec(t, pidFile))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	agentPID := readPIDFile(t, pidFile)
	t.Cleanup(func() { killTree(agentPID); killTree(m.ShimPID) })
	return m, agentPID
}

// readPIDFile waits for pidFile to hold a PID and returns it.
func readPIDFile(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(pollTimeout)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil && len(b) > 0 {
			pid, cerr := strconv.Atoi(string(b))
			if cerr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("agent PID file %s never populated", pidFile)
	return 0
}

// processAlive reports whether pid is a live process (signal 0 probe). EPERM
// means it exists but we may not signal it — still alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// killTree best-effort terminates pid and its process group (cleanup only).
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// waitProcessGone blocks until pid is no longer alive, failing on timeout.
func waitProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("process %d still alive after %s", pid, timeout)
}

// waitStatus polls d.Get(id) until the meta's process dimension equals want.
func waitStatus(t *testing.T, d *Daemon, id string, want status.Process, timeout time.Duration) persist.Meta {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last persist.Meta
	for time.Now().Before(deadline) {
		m, ok := d.Get(id)
		if ok {
			last = m
			if m.Status.Process == want {
				return m
			}
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("session %s never reached process=%q (last=%q)", id, want, last.Status.Process)
	return persist.Meta{}
}

// waitFile blocks until path exists, failing on timeout.
func waitFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("file %s never appeared within %s", path, timeout)
}

// getMeta returns d.Get(id) or fails.
func getMeta(t *testing.T, d *Daemon, id string) persist.Meta {
	t.Helper()
	m, ok := d.Get(id)
	if !ok {
		t.Fatalf("session %s not found in registry", id)
	}
	return m
}

// makeFinalSnapshot returns a valid vt final-snapshot.bin body containing marker
// text, for the side-file merge test (E5.5).
func makeFinalSnapshot(t *testing.T, marker string) []byte {
	t.Helper()
	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	emu.Feed([]byte(marker))
	b, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// Direct real-shim spawning (setup for reconcile/version tests that do not go
// through Launch). The socket lands at the daemon's DETERMINISTIC path so a
// later Open reconnects it.
// ---------------------------------------------------------------------------

// shimLaunchConfig mirrors cmd/swarm's `swarm shim --config` JSON schema.
type shimLaunchConfig struct {
	SessionID  string   `json:"session_id"`
	Argv       []string `json:"argv"`
	Cwd        string   `json:"cwd"`
	Env        []string `json:"env"`
	SocketPath string   `json:"socket_path"`
	SessionDir string   `json:"session_dir"`
	Cols       int      `json:"cols"`
	Rows       int      `json:"rows"`
	GraceMS    int      `json:"grace_ms"`
}

// spawnRealShim starts a real `swarm shim --config` subprocess running a
// long-lived announce agent for session id under stateDir. It returns the shim's
// PID (== the process we start; the shim setsids in place without changing PID)
// and the agent PID, and registers cleanup. The socket is placed at
// shimSocketPath(stateDir, id) so daemon reconcile finds it.
func spawnRealShim(t *testing.T, stateDir, id string) (shimPID, agentPID int) {
	t.Helper()
	sessionDir := filepath.Join(stateDir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), id+".pid")
	sock := shimSocketPath(stateDir, id)
	if len(sock) > 100 {
		t.Fatalf("shim socket path too long (%d): %s", len(sock), sock)
	}
	lc := shimLaunchConfig{
		SessionID:  id,
		Argv:       []string{selfExe(t), markerAnnounce, pidFile},
		Cwd:        t.TempDir(),
		Env:        []string{"PATH=" + os.Getenv("PATH")},
		SocketPath: sock,
		SessionDir: sessionDir,
		Cols:       80,
		Rows:       24,
		GraceMS:    2000,
	}
	cfgPath := filepath.Join(t.TempDir(), id+".json")
	writeJSON(t, cfgPath, lc)

	cmd := exec.Command(swarmBin, "shim", "--config", cfgPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shim: %v", err)
	}
	shimPID = cmd.Process.Pid
	agentPID = readPIDFile(t, pidFile)
	// Wait for the socket to be servable before returning (the shim binds async).
	waitFile(t, sock, pollTimeout)
	t.Cleanup(func() {
		killTree(agentPID)
		killTree(shimPID)
		_, _ = cmd.Process.Wait()
	})
	return shimPID, agentPID
}

// writeJSON marshals v to path as JSON.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeRunningMeta persists a running-session meta directly (the test acting as
// the prior daemon), for reconcile setup. Perms/env-filtering happen in Save.
func writeRunningMeta(t *testing.T, stateDir, id string, shimPID int, shimStart int64) {
	t.Helper()
	st, err := persist.NewStore(stateDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	m := persist.Meta{
		ID:            id,
		AgentType:     "fake",
		Cwd:           "/tmp",
		CreatedAt:     time.Now(),
		Status:        status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
		LastActivity:  time.Now(),
		ShimPID:       shimPID,
		ShimStartTime: shimStart,
	}
	if err := st.Save(m); err != nil {
		t.Fatalf("Save meta: %v", err)
	}
}

// readTranscriptFile reads a session's transcript.log (raw PTY capture).
func readTranscriptFile(t *testing.T, sessionDir string) string {
	t.Helper()
	// The transcript file name is owned by the shim package; mirror it here to
	// avoid a test-time import cycle on shim internals.
	b, err := os.ReadFile(filepath.Join(sessionDir, "transcript.log"))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return string(b)
}
