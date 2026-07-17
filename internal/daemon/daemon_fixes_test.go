package daemon

// Audit-004 review-fix tests (Epic 5). These live in a NEW file; the frozen
// designer test files are never modified. Each test targets one fix (F1..F10) and
// exercises the production seam it hardened.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

// ---------------------------------------------------------------------------
// Shared helpers for the fix tests
// ---------------------------------------------------------------------------

// fakeShim is an in-test UDS listener that speaks just enough of the G2 hello
// handshake to stand in for a shim: it answers a hello with a configurable
// WireVersion and records whether it ever received a connection or a signal op.
// It lets a test drive the daemon's reconnect/kill paths against a shim whose
// wire version or mere presence is controlled (F6/F9).
type fakeShim struct {
	ln          net.Listener
	wireVersion int

	mu        sync.Mutex
	conns     int
	gotSignal bool
}

// startFakeShim binds a fake shim at sock (0600, like a real shim) that answers
// hello with wireVersion, and registers listener cleanup.
func startFakeShim(t *testing.T, sock string, wireVersion int) *fakeShim {
	t.Helper()
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", sock)
	syscall.Umask(old)
	if err != nil {
		t.Fatalf("fake shim listen %s: %v", sock, err)
	}
	fs := &fakeShim{ln: ln, wireVersion: wireVersion}
	go fs.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return fs
}

func (fs *fakeShim) serve() {
	for {
		conn, err := fs.ln.Accept()
		if err != nil {
			return
		}
		go fs.handle(conn)
	}
}

func (fs *fakeShim) handle(conn net.Conn) {
	defer conn.Close()
	fs.mu.Lock()
	fs.conns++
	fs.mu.Unlock()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	for {
		typ, payload, err := wire.ReadFrame(conn)
		if err != nil {
			return
		}
		if typ != wire.TControl {
			continue
		}
		ctrl, err := shimwire.Decode(payload)
		if err != nil {
			continue
		}
		switch ctrl.Type {
		case shimwire.TypeHello:
			reply, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: fs.wireVersion})
			_ = wire.WriteFrame(conn, wire.TControl, reply)
		case shimwire.TypeSignal:
			fs.mu.Lock()
			fs.gotSignal = true
			fs.mu.Unlock()
		}
	}
}

func (fs *fakeShim) connCount() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.conns
}

func (fs *fakeShim) signalled() bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.gotSignal
}

// writeDaemonPIDFile writes a "PID STARTTIME" pidfile into stateDir, the on-disk
// format stopRunningDaemon parses (F1).
func writeDaemonPIDFile(t *testing.T, stateDir string, pid int, start int64) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, pidFileName), []byte(fmt.Sprintf("%d %d\n", pid, start)), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
}

// waitLogContains waits until the daemon log at path contains substr.
func waitLogContains(t *testing.T, path, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), substr) {
			return
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("daemon log %s never contained %q within %s", path, substr, timeout)
}

// spawnCatchTermChild starts a live catch-term child (records any TERM/INT it
// receives to sigFile) and returns its PID; it never dies on TERM, so its being
// alive plus an empty sigFile is proof no signal was sent.
func spawnCatchTermChild(t *testing.T, sigFile string) int {
	t.Helper()
	child := exec.Command(selfExe(t), markerCatchTerm, sigFile)
	child.Stdout, child.Stderr = os.Stderr, os.Stderr
	if err := child.Start(); err != nil {
		t.Fatalf("start catch-term child: %v", err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() { killTree(pid); _, _ = child.Process.Wait() })
	return pid
}

// ---------------------------------------------------------------------------
// F1 — Restart PID-reuse safety (stopRunningDaemon verifies start-time)
// ---------------------------------------------------------------------------

// TestStopRunningDaemon_PIDReuseSafety asserts F1/S3: stopRunningDaemon signals
// nothing when the pidfile's recorded start-time does not match the live PID (the
// PID-reuse case), and signals when it does. The daemon's own pidfile must not
// SIGTERM an unrelated process after a crash + PID reuse.
func TestStopRunningDaemon_PIDReuseSafety(t *testing.T) {
	dir := shortStateDir(t)
	cc := ClientConfig{StateDir: dir, SocketPath: filepath.Join(dir, "daemon.sock")}

	sigFile := filepath.Join(t.TempDir(), "sig")
	pid := spawnCatchTermChild(t, sigFile)
	realStart, err := processStartTime(pid)
	if err != nil {
		t.Fatalf("processStartTime(%d): %v", pid, err)
	}

	// Wrong start-time: the PID is alive but is not "our" daemon → no signal.
	writeDaemonPIDFile(t, dir, pid, realStart+1)
	stopRunningDaemon(cc)
	time.Sleep(400 * time.Millisecond)
	if !processAlive(pid) {
		t.Fatalf("stopRunningDaemon signalled a start-time-mismatched PID; want no-op (S3)")
	}
	if b, err := os.ReadFile(sigFile); err == nil && len(b) > 0 {
		t.Fatalf("start-time-mismatched child recorded a signal: %q; want none", b)
	}

	// Correct start-time: stop signals it (the child catches TERM and records it,
	// but does not die — so the record is the proof the signal was sent).
	writeDaemonPIDFile(t, dir, pid, realStart)
	stopRunningDaemon(cc) // no socket at cc.SocketPath, so the wait returns promptly
	deadline := time.Now().Add(pollTimeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(sigFile); err == nil && strings.Contains(string(b), "terminated") {
			return
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("stopRunningDaemon did not SIGTERM the matching PID; child recorded no signal")
}

// ---------------------------------------------------------------------------
// F2 — Launch identity-read error kills the shim, leaves no phantom
// ---------------------------------------------------------------------------

// TestLaunch_IdentityReadFailure_KillsShimNoPhantom asserts F2/N2: if the
// post-spawn process-start-time read fails, launch must NOT persist ShimStartTime=0
// (a later reconcile would mark the live shim lost). It must kill the just-spawned
// shim AND its agent — and the agent runs in its OWN process group, so a bare
// kill(-shimPID) would orphan it (N2). The agent here IGNORES HUP/TERM and never
// reads stdin, so it survives the shim's death (PTY-master-close SIGHUP): only an
// explicit group-kill delivered through the shim socket can terminate it. The
// injected failure is delayed until the agent PID exists, forcing cleanup to reach
// a live agent.
func TestLaunch_IdentityReadFailure_KillsShimNoPhantom(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	script := "trap '' HUP TERM INT; echo $$ > " + pidFile + "; while :; do sleep 1; done"
	spec := LaunchSpec{
		AgentType: "fake",
		Argv:      []string{"/bin/sh", "-c", script},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	}

	var gotShimPID, gotAgentPID int
	orig := procStartTimeFn
	procStartTimeFn = func(pid int) (int64, error) {
		gotShimPID = pid
		// Wait for the agent to actually exist before injecting the failure, so the
		// cleanup must terminate a live agent in its own process group (N2).
		gotAgentPID = waitPIDFileValue(pidFile, pollTimeout)
		return 0, errors.New("injected identity-read failure")
	}
	defer func() { procStartTimeFn = orig }()

	_, err := d.Launch(spec)
	if err == nil {
		t.Fatalf("Launch succeeded despite an identity-read failure; want an error")
	}
	if gotShimPID <= 0 {
		t.Fatalf("the seam never observed the spawned shim PID")
	}
	if gotAgentPID <= 0 {
		t.Fatalf("the agent never spawned; the test did not exercise the orphan path (N2)")
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-gotAgentPID, syscall.SIGKILL)
		_ = syscall.Kill(-gotShimPID, syscall.SIGKILL)
		killTree(gotAgentPID)
		killTree(gotShimPID)
	})

	// Neither the shim NOR the agent (its own process group) may be left running.
	waitProcessGone(t, gotAgentPID, pollTimeout)
	waitProcessGone(t, gotShimPID, pollTimeout)

	// No phantom: the registry has no running session, and nothing serves a socket.
	if n := liveCount(d); n != 0 {
		t.Fatalf("live session count = %d after a failed launch; want 0 (no phantom)", n)
	}
	if len(d.List()) != 0 {
		t.Fatalf("registry size = %d after a failed launch; want 0", len(d.List()))
	}
}

// waitPIDFileValue polls path for a positive PID, returning it or 0 on timeout. It
// is non-fatal (safe to call from inside an injected seam that runs within Launch).
func waitPIDFileValue(path string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			if pid, cerr := strconv.Atoi(strings.TrimSpace(string(b))); cerr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(pollStep)
	}
	return 0
}

// ---------------------------------------------------------------------------
// F3 — Delete concurrent with a shim exit never resurrects the session dir
// ---------------------------------------------------------------------------

// TestDelete_ConcurrentMerge_NoResurrection asserts F3: the exit-handler side-file
// merge (handleShimExit) racing a Delete must never recreate the session dir or
// registry entry after Delete removed them. A merge that passed its membership
// check before Delete would otherwise saveMeta (store.Save) after store.Delete and
// resurrect the session. Run many times under -race; the tombstone + writeMu
// serialization closes both the on-disk and in-memory windows.
func TestDelete_ConcurrentMerge_NoResurrection(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const n = 300
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("delrace%04d", i)
		sessionDir := filepath.Join(cfg.StateDir, id)
		// Seed a running session both on disk and in the registry, with a dead shim
		// PID (so Delete sends no signal). handleShimExit will treat it as lost and
		// try to persist — the write we must not let resurrect a deleted session.
		writeRunningMeta(t, cfg.StateDir, id, -1, 0)
		d.putMem(persist.Meta{
			ID:           id,
			AgentType:    "fake",
			Cwd:          "/tmp",
			CreatedAt:    time.Now(),
			LastActivity: time.Now(),
			Status:       status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
			ShimPID:      -1,
		})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); d.handleShimExit(id) }() // the racing exit-merge
		go func() { defer wg.Done(); _ = d.Delete(id) }()     // the concurrent delete
		wg.Wait()

		if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
			t.Fatalf("iter %d: session dir %s reappeared after Delete (resurrection)", i, id)
		}
		if _, ok := d.Get(id); ok {
			t.Fatalf("iter %d: session %s still in registry after Delete (resurrection)", i, id)
		}
	}
}

// ---------------------------------------------------------------------------
// F4 — reconcile surfaces scan errors and logs merge/lost-write failures
// ---------------------------------------------------------------------------

// TestOpen_ScanErrorIsSurfaced asserts F4: a persist.Scan failure makes Open
// return an error rather than serving a blind, empty registry — and Open releases
// everything it acquired (a subsequent Open succeeds).
func TestOpen_ScanErrorIsSurfaced(t *testing.T) {
	cfg := daemonConfig(t)

	orig := scanStoreFn
	scanStoreFn = func(s *persist.Store) ([]persist.Meta, error) {
		return nil, errors.New("injected scan failure")
	}
	d, err := Open(cfg)
	if err == nil {
		_ = d.Close()
		scanStoreFn = orig
		t.Fatalf("Open succeeded despite a scan failure; want an error (no blind registry)")
	}
	if !strings.Contains(err.Error(), "scan") {
		t.Fatalf("Open error = %v; want it to name the scan failure", err)
	}
	scanStoreFn = orig

	// The failed Open must have released the lock: a fresh Open now succeeds.
	d2, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open after a scan-failed Open: %v; want success (lock released)", err)
	}
	_ = d2.Close()
}

// TestReconcile_SaveErrorIsLogged asserts F4: a side-file-merge / lost-write
// persistence failure is written to the daemon log, not silently dropped. We force
// store.Save to fail by making the session path a regular file (so its MkdirAll
// fails), then drive the exit-merge, and assert the log records it.
func TestReconcile_SaveErrorIsLogged(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	id := "savefail1"
	// A regular file where the session dir should be: store.Save's MkdirAll errors.
	if err := os.WriteFile(filepath.Join(cfg.StateDir, id), []byte("x"), 0o600); err != nil {
		t.Fatalf("plant blocking file: %v", err)
	}
	// A running session in the registry pointing at a dead shim, with no exit file →
	// handleShimExit marks it lost and tries to saveMeta, which fails and is logged.
	d.putMem(persist.Meta{
		ID:           id,
		AgentType:    "fake",
		Cwd:          "/tmp",
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Status:       status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
		ShimPID:      -1,
	})
	d.handleShimExit(id)

	waitLogContains(t, cfg.LogPath, id, pollTimeout)
	waitLogContains(t, cfg.LogPath, "persist", pollTimeout)
}

// ---------------------------------------------------------------------------
// F5 — spawner cold-start creates the state dir; skew text states restart is safe
// ---------------------------------------------------------------------------

// TestSpawnDaemon_ColdStateDirCreatedBeforeLog asserts F5/D-1: defaultSpawnDaemon
// creates the (nonexistent) state dir 0700 BEFORE opening the log, so a truly cold
// start does not ENOENT — and the spawned daemon then actually comes up.
func TestSpawnDaemon_ColdStateDirCreatedBeforeLog(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	base := shortStateDir(t)
	stateDir := filepath.Join(base, "cold") // does NOT exist yet
	cc := ClientConfig{
		StateDir:   stateDir,
		SocketPath: filepath.Join(stateDir, "daemon.sock"),
		LockPath:   filepath.Join(stateDir, "daemon.lock"),
		LogPath:    filepath.Join(stateDir, "daemon.log"),
		DaemonBin:  swarmBin,
	}
	t.Cleanup(func() { stopRunningDaemon(cc) })

	if err := defaultSpawnDaemon(cc); err != nil {
		t.Fatalf("defaultSpawnDaemon on a cold state dir: %v", err)
	}

	fi, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("cold state dir not created: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("cold state dir mode = %#o; want 0700 (no group/other bits)", perm)
	}
	if _, err := os.Stat(cc.LogPath); err != nil {
		t.Fatalf("daemon log not created on cold start: %v", err)
	}
	// The real `swarm daemon` the spawner launched must actually come up.
	waitDial(t, cc.SocketPath, pollTimeout)
}

// TestVersionSkew_ErrorStatesRestartIsSafe asserts F5/D-8: the ErrVersionSkew
// message names `swarm daemon restart` AND states the restart is safe / loses no
// live sessions.
func TestVersionSkew_ErrorStatesRestartIsSafe(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	_ = d

	_, err := Dial(cfg.SocketPath, ProtocolVersion+1)
	if !errors.Is(err, ErrVersionSkew) {
		t.Fatalf("Dial at an incompatible version error = %v; want ErrVersionSkew", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "swarm daemon restart") {
		t.Fatalf("skew error %q does not name `swarm daemon restart`", msg)
	}
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "safe") || !strings.Contains(lower, "no live sessions are lost") {
		t.Fatalf("skew error %q does not state the restart is safe / loses no live sessions (D-8)", msg)
	}
}

// ---------------------------------------------------------------------------
// F6 — Kill/Delete pre-signal identity recheck
// ---------------------------------------------------------------------------

// TestKill_IdentityMismatch_NoSignalMarksLost asserts F6/S3: when a running
// session's recorded shim identity no longer matches (PID reuse / rebound socket),
// Kill sends NO signal to the socket and resolves the session as lost. A fake shim
// bound at the session socket records that it was never contacted.
func TestKill_IdentityMismatch_NoSignalMarksLost(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	id := "killmismatch1"
	sessionDir := filepath.Join(cfg.StateDir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	// A rebound listener sits at the session socket; if Kill wrongly dialed, it would
	// register a connection / signal.
	fs := startFakeShim(t, shimSocketPath(cfg.StateDir, id), shimwire.Version)

	// A live PID stands in for the recorded shim, but with a WRONG start-time so the
	// identity recheck fails.
	pid := spawnCatchTermChild(t, filepath.Join(t.TempDir(), "sig"))
	realStart, err := processStartTime(pid)
	if err != nil {
		t.Fatalf("processStartTime: %v", err)
	}
	d.putMem(persist.Meta{
		ID:            id,
		AgentType:     "fake",
		Cwd:           "/tmp",
		CreatedAt:     time.Now(),
		LastActivity:  time.Now(),
		Status:        status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
		ShimPID:       pid,
		ShimStartTime: realStart + 1, // deliberately wrong
	})

	if err := d.Kill(id); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	// The daemon must not have contacted the (rebound) socket at all.
	time.Sleep(400 * time.Millisecond)
	if fs.connCount() != 0 {
		t.Fatalf("Kill dialed the shim socket on an identity mismatch (%d conns); want zero (S3)", fs.connCount())
	}
	if fs.signalled() {
		t.Fatalf("Kill delivered a signal to a rebound socket on an identity mismatch; want none (S3)")
	}
	got := waitStatus(t, d, id, status.ProcessLost, pollTimeout)
	if got.Status.Process != status.ProcessLost {
		t.Fatalf("identity-mismatched Kill: process = %q; want lost", got.Status.Process)
	}
}

// ---------------------------------------------------------------------------
// F9 — reconnect hello rejects an incompatible WireVersion
// ---------------------------------------------------------------------------

// TestReconnectHello_RejectsWireVersionSkew asserts F9: confirmShimServing (the
// reconnect gate) compares WireVersion, not just the reply type. A shim answering
// hello with a compatible version is accepted; an incompatible one is rejected.
func TestReconnectHello_RejectsWireVersionSkew(t *testing.T) {
	dir := shortStateDir(t)

	sockOK := filepath.Join(dir, "ok.sock")
	startFakeShim(t, sockOK, shimwire.Version)
	if !confirmShimServing(sockOK) {
		t.Fatalf("compatible wire version rejected by the reconnect gate")
	}

	sockBad := filepath.Join(dir, "bad.sock")
	startFakeShim(t, sockBad, shimwire.Version+1)
	if confirmShimServing(sockBad) {
		t.Fatalf("incompatible wire version accepted; want rejected (F9)")
	}
}

// TestReconcile_WireVersionSkewMarksLost asserts F9 end to end: a running meta
// whose PID identity matches a live process but whose shim answers hello with an
// incompatible WireVersion must reconcile to lost (not adopted as running).
func TestReconcile_WireVersionSkewMarksLost(t *testing.T) {
	cfg := daemonConfig(t)
	id := "wireskew1"
	sessionDir := filepath.Join(cfg.StateDir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	// A live PID whose recorded (PID, start-time) matches — identity passes.
	pid := spawnCatchTermChild(t, filepath.Join(t.TempDir(), "sig"))
	start, err := processStartTime(pid)
	if err != nil {
		t.Fatalf("processStartTime: %v", err)
	}
	// But the shim at the socket advertises an incompatible wire version.
	startFakeShim(t, shimSocketPath(cfg.StateDir, id), shimwire.Version+1)
	writeRunningMeta(t, cfg.StateDir, id, pid, start)

	d := openDaemon(t, cfg)
	got := waitStatus(t, d, id, status.ProcessLost, pollTimeout)
	if got.Status.Process != status.ProcessLost {
		t.Fatalf("wire-version-skewed shim reconcile = %q; want lost (not adopted)", got.Status.Process)
	}
}

// ---------------------------------------------------------------------------
// F10 — daemon log hardened to 0600 even when it pre-exists wider
// ---------------------------------------------------------------------------

// TestDaemonLog_HardenedOnOpen asserts F10/E5.7: openDaemonLog re-hardens an
// already-existing log to 0600 (O_CREATE does not chmod an existing file), even
// under a permissive umask.
func TestDaemonLog_HardenedOnOpen(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := shortStateDir(t)
	logPath := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(logPath, []byte("pre-existing\n"), 0o644); err != nil {
		t.Fatalf("plant wide-perm log: %v", err)
	}

	f, err := openDaemonLog(logPath)
	if err != nil {
		t.Fatalf("openDaemonLog: %v", err)
	}
	_ = f.Close()

	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("daemon log mode = %#o; want 0600 (no group/other bits)", perm)
	}
}
