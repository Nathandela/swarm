package shim

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.1's SHIM half: the Codex app-server is a
// shim-owned CHILD with its OWN process group, its OWN TERM->grace->KILL, its OWN Wait, and a
// go-ahead handshake that gates the agent spawn on the daemon being connected.
// Bead: agents-tracker-hggx.8. ADR-013 §R7.2a/b/c/e and §R7.6.
//
// THE CLAIM THIS FILE EXISTS TO FENCE, and it is a retraction. The first draft of the R7
// design said the backend "joins the agent's existing TERM->grace->KILL containment". IT DOES
// NOT. Every kill on that path is `syscall.Kill(-s.pgid, ...)` (server.go:291,293,309,331) and
// s.pgid is the AGENT's group -- documented at server.go:59 and pinned by
// spawn_test.go:175-191, which asserts the agent's pgid equals its own pid AND differs from
// the shim's. A sibling exec.Cmd started by the shim inherits the SHIM's group, so all four of
// those kills MISS IT, and cmd.Wait() (shim.go:228) waits the agent only, so it is never
// reaped. Run would return leaving a live app-server -- a process authenticated to a real
// ChatGPT account -- behind, forever, with no PTY to HUP it and no stream to notice.
//
// THE CONTRACT these tests freeze:
//
//	type BackendConfig struct {
//	    Program        string        // ALREADY RESOLVED by the core (adapter names, core LookPaths)
//	    Args           []string
//	    Env            []string
//	    SocketPath     string        // <session-dir>/codex.sock
//	    ReadyTimeout   time.Duration // bound on "the socket became servable"
//	    GoAheadTimeout time.Duration // bound on waitBackendGoAhead
//	}
//	Config.Backend *BackendConfig    // nil == pre-R7 session, byte-for-byte unchanged
//	const BackendFile = "backend.json"
//	type BackendInfo struct{ PID, PGID int; StartedAtMS int64; SocketPath string }
//	func ReadBackendInfo(sessionDir string) (BackendInfo, bool)
//	ExitInfo.BackendExit *int        // so a backend death is never an unexplained agent exit

import (
	"encoding/json"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// ---------------------------------------------------------------------------
// The BACKEND helper process
//
// It is re-exec'd through this test binary, exactly as the agent helper is, but through its
// OWN env var and its OWN init() -- so main_test.go's TestMain interception is untouched and
// the two helper families cannot collide. init() runs before TestMain, so a re-exec that sets
// this var never reaches the test suite at all.
//
// It models the ONE property of the real app-server that a supervisor must design around:
// R1 leg 1 recorded that `codex app-server` writes NOTHING to stdout or stderr for an entire
// session (r1-codex-gate.md:75-79), so readiness and liveness can only come from the socket
// and from the pid -- never from the streams.
// ---------------------------------------------------------------------------

const r7BackendEnvVar = "SWARM_SHIM_TEST_BACKEND"

const (
	r7BackendBind        = "bind"         // bind the socket, accept forever, silent
	r7BackendIgnoreTerm  = "ignore-term"  // bind, then ignore SIGTERM forever
	r7BackendNeverBinds  = "never-bind"   // never bind; the readiness bound must expire
	r7BackendDiesEarly   = "dies-early"   // bind, then exit 7 after a beat
	r7BackendSlowToStart = "slow-to-bind" // bind only after 600ms
)

func init() {
	mode := os.Getenv(r7BackendEnvVar)
	if mode == "" {
		return
	}
	sock := os.Getenv(r7BackendEnvVar + "_SOCK")
	switch mode {
	case r7BackendNeverBinds:
		select {}
	case r7BackendSlowToStart:
		time.Sleep(600 * time.Millisecond)
	case r7BackendIgnoreTerm:
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		go func() {
			for range ch {
			}
		}()
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(90)
	}
	if mode == r7BackendDiesEarly {
		time.Sleep(200 * time.Millisecond)
		_ = ln.Close()
		os.Exit(7)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			os.Exit(91)
		}
		_ = c.Close()
	}
}

// r7BackendCfg builds a Config whose agent is the info helper and whose backend is this test
// binary in the named backend mode.
func r7BackendCfg(t *testing.T, backendMode string, agentArgs []string) Config {
	t.Helper()
	cfg := helperConfig(t, modeInfo, agentArgs, nil)
	cfg.SessionDir = r7ShortSessionDir(t)
	sock := filepath.Join(cfg.SessionDir, "codex.sock")
	cfg.Backend = &BackendConfig{
		Program:    selfExe(t),
		Args:       []string{"app-server", "--listen", "unix://" + sock},
		SocketPath: sock,
		Env: []string{
			r7BackendEnvVar + "=" + backendMode,
			r7BackendEnvVar + "_SOCK=" + sock,
			"PATH=" + os.Getenv("PATH"),
		},
		ReadyTimeout:   5 * time.Second,
		GoAheadTimeout: 5 * time.Second,
	}
	return cfg
}

// r7WaitBackendInfo polls for backend.json, which the shim writes the moment the backend's
// socket is SERVABLE (§R7.2c).
func r7WaitBackendInfo(t *testing.T, sessionDir string, within time.Duration) BackendInfo {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if info, ok := ReadBackendInfo(sessionDir); ok {
			return info
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s appeared in %s within %s; the daemon cannot know a pid it did not spawn, "+
		"so this file is the ONLY thing that lets a restarted daemon tell a live backend from an "+
		"unrelated process on a reused socket path (§R7.2c)", BackendFile, sessionDir, within)
	return BackendInfo{}
}

func r7Alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// ---------------------------------------------------------------------------
// §R7.2e -- the spawn-ordering handshake
// ---------------------------------------------------------------------------

// TestR7ShimBackend_TheAgentDoesNotSpawnUntilTheDaemonSaysGoAhead is the whole point of the
// handshake, stated as the only observable that matters: the agent process does not exist
// until the daemon has had its chance to connect. That is what makes it IMPOSSIBLE to miss a
// `thread/started` notification and what removes the gate's 15-17 s rollout race at cold start
// (r1-codex-gate.md:113-119) rather than retrying around it.
func TestR7ShimBackend_TheAgentDoesNotSpawnUntilTheDaemonSaysGoAhead(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.Backend.GoAheadTimeout = 10 * time.Second
	done := runShimAsync(cfg)

	// The backend is up and its identity is recorded before anything else happens.
	info := r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)

	// The control socket is bound (shim.go:117, unchanged) but the agent is NOT running: the
	// shim is blocked in waitBackendGoAhead.
	c := dialShim(t, cfg.SocketPath)
	c.startReader() // dialShim does not; every other test in this package starts it explicitly
	c.hello(shimwire.Version)
	c.attach()
	time.Sleep(750 * time.Millisecond)
	if txt := c.observedText(); strings.Contains(txt, "INFO_DONE") {
		_ = c.conn.Close()
		t.Fatalf("the agent ran BEFORE the go-ahead (grid already shows %q). The whole handshake "+
			"exists so the daemon is a connected client before the agent process exists; a shim "+
			"that spawns first has re-opened the race the gate hit", txt)
	}
	c.writeControl(shimwire.Control{Type: shimwire.TypeBackendAttach})
	if got := c.waitObserved("INFO_DONE", 15*time.Second); got == "" {
		_ = c.conn.Close()
		t.Fatal("the agent never ran after the go-ahead arrived")
	}
	_ = c.conn.Close()
	waitRun(t, done, 20*time.Second)

	if !strings.HasPrefix(info.SocketPath, cfg.SessionDir) {
		t.Errorf("backend.json names socket %q, which is not under the session dir %q", info.SocketPath, cfg.SessionDir)
	}
}

// TestR7ShimBackend_AGoAheadThatNeverArrivesSpawnsTheAgentANYWAY is what keeps the handshake
// from being a new way to hang. A daemon that crashed between spawning the shim and dialing
// the backend must not leave the owner with a terminal that never starts.
func TestR7ShimBackend_AGoAheadThatNeverArrivesSpawnsTheAgentANYWAY(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.Backend.GoAheadTimeout = 500 * time.Millisecond
	done := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader() // dialShim does not; every other test in this package starts it explicitly
	c.hello(shimwire.Version)
	c.attach()
	if got := c.waitObserved("INFO_DONE", 15*time.Second); got == "" {
		_ = c.conn.Close()
		t.Fatal("no go-ahead ever arrived and the agent NEVER SPAWNED; the timeout exists precisely " +
			"so the degraded path is a session that runs, not a session that hangs (§R7.2e)")
	}
	_ = c.conn.Close()
	waitRun(t, done, 20*time.Second)
}

// TestR7ShimBackend_BackendAttachAppendsAgentArgsVerbatim proves the go-ahead's payload
// reaches the agent argv unchanged. Whether it carries a thread id is r7-open-questions.md Q2;
// the handshake must be identical either way, which is why this test asserts on the MECHANISM
// and not on a particular flag.
func TestR7ShimBackend_BackendAttachAppendsAgentArgsVerbatim(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.Backend.GoAheadTimeout = 10 * time.Second
	done := runShimAsync(cfg)
	r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)

	extra := []string{"--remote", "unix://" + cfg.Backend.SocketPath}
	c := dialShim(t, cfg.SocketPath)
	c.startReader() // dialShim does not; every other test in this package starts it explicitly
	c.hello(shimwire.Version)
	c.attach()
	c.writeControl(shimwire.Control{Type: shimwire.TypeBackendAttach, AgentArgs: extra})
	if c.waitObserved("INFO_DONE", 15*time.Second) == "" {
		_ = c.conn.Close()
		t.Fatal("the agent never ran after the go-ahead")
	}
	grid := c.observedText()
	_ = c.conn.Close()
	waitRun(t, done, 20*time.Second)

	for _, want := range extra {
		if !strings.Contains(grid, want) {
			t.Errorf("the agent's argv does not contain %q; the shim appends AgentArgs VERBATIM, "+
				"and an agent launched without --remote is a terminal talking to nothing while the "+
				"daemon mirrors an empty thread. grid:\n%s", want, grid)
		}
	}
}

// ---------------------------------------------------------------------------
// §R7.2a -- the backend's OWN containment
// ---------------------------------------------------------------------------

// TestR7ShimBackend_TheBackendLeadsItsOwnProcessGroup is mutation fence 2 of §R7.2a: delete
// Setpgid and this fails. A backend in the SHIM's group is contained only by the daemon's
// containment of the shim -- exactly the accident this rule refuses to rely on -- and R1 leg 1
// recorded that one `codex app-server` is TWO pids (a node launcher plus the vendored rust
// binary), so the group is what reaps both.
func TestR7ShimBackend_TheBackendLeadsItsOwnProcessGroup(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.Backend.GoAheadTimeout = 300 * time.Millisecond
	done := runShimAsync(cfg)
	info := r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)

	if info.PID <= 0 {
		t.Fatalf("backend.json records pid %d", info.PID)
	}
	if info.PGID != info.PID {
		t.Errorf("backend pgid %d != pid %d -- the backend does not LEAD its own group, so a "+
			"kill(-pgid) aimed at it either misses it or hits the shim's whole group", info.PGID, info.PID)
	}
	if info.PGID == syscall.Getpgrp() {
		t.Errorf("the backend's pgid %d is the TEST process group; it inherited the shim's group, "+
			"which is the exact defect that made the retracted design's containment claim false", info.PGID)
	}
	if info.StartedAtMS <= 0 {
		t.Errorf("backend.json records started_at_ms %d; liveness is (pid alive AND START-TIME "+
			"MATCHES AND the owning shim is live), and without the start-time a recycled pid reads live",
			info.StartedAtMS)
	}
	waitRun(t, done, 25*time.Second)
}

// TestR7ShimBackend_ATermIgnoringBackendIsDEADAfterRunReturns is mutation fence 1 of §R7.2a,
// and the single most important test in this file. Delete the `-backendPgid` kill from
// finishEscalation and it must fail.
//
// The residual it closes is qualitatively worse than the agent's: the app-server has no PTY,
// no controlling terminal and writes to neither stream ever, so an orphan stays alive
// indefinitely, keeps serving <session-dir>/codex.sock, and looks HEALTHY to any rule whose
// readiness test is "the dial succeeded".
func TestR7ShimBackend_ATermIgnoringBackendIsDEADAfterRunReturns(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendIgnoreTerm, nil)
	cfg.Backend.GoAheadTimeout = 300 * time.Millisecond
	cfg.GraceTimeout = 1 * time.Second
	done := runShimAsync(cfg)
	info := r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)

	// modeInfo exits on its own, which finalizes Run.
	waitRun(t, done, 30*time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !r7Alive(info.PID) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-info.PGID, syscall.SIGKILL) // do not leak the orphan out of the test
	t.Fatalf("the TERM-ignoring backend (pid %d, pgid %d) is STILL ALIVE after shim.Run returned. "+
		"It has no PTY to HUP it, no stream anybody watches, and a live socket that makes it look "+
		"healthy to a restarted daemon -- and it is authenticated to a real ChatGPT account",
		info.PID, info.PGID)
}

// TestR7ShimBackend_TheJoinDoesNotBlockRun is the third part of §R7.2a: the dedicated
// backendCmd.Wait() goroutine is joined AFTER finishEscalation has issued the final group
// KILL, which is what guarantees the join returns rather than blocking Run behind a backend
// that ignores TERM. Without that ordering this exact configuration deadlocks.
func TestR7ShimBackend_TheJoinDoesNotBlockRun(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendIgnoreTerm, nil)
	cfg.Backend.GoAheadTimeout = 300 * time.Millisecond
	cfg.GraceTimeout = 1 * time.Second
	done := runShimAsync(cfg)
	info := r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)
	defer func() { _ = syscall.Kill(-info.PGID, syscall.SIGKILL) }()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("shim.Run never returned with a TERM-ignoring backend attached; the backend's Wait " +
			"is being joined BEFORE the final group KILL, so Run is parked behind a process that " +
			"will never die on its own")
	}
}

// ---------------------------------------------------------------------------
// §R7.6 -- the app-server's OWN crash
// ---------------------------------------------------------------------------

// TestR7ShimBackend_ABackendThatDiesFirstEndsTheSessionAndIsRECORDED covers the one lifecycle
// event this topology introduces. Under `codex --remote unix://SOCK` the TUI is a CLIENT of
// the app-server -- R1 leg 2 recorded its whole boot handshake, model resolution and MCP boot
// going through the server -- so if the backend dies the owner's terminal is DEAD, not merely
// unmirrored. R7 does not restart it (a restarted server has no thread state and the TUI's
// connection is already broken); it fires the agent's existing TERM->grace->KILL from a new
// edge so the owner gets a clean end instead of a wedged TUI, and it records WHY.
func TestR7ShimBackend_ABackendThatDiesFirstEndsTheSessionAndIsRECORDED(t *testing.T) {
	// modeIdle parks forever: without the new edge this session never ends on its own.
	cfg := helperConfig(t, modeIdle, nil, nil)
	cfg.SessionDir = r7ShortSessionDir(t)
	sock := filepath.Join(cfg.SessionDir, "codex.sock")
	cfg.Backend = &BackendConfig{
		Program:        selfExe(t),
		Args:           []string{"app-server"},
		SocketPath:     sock,
		Env:            []string{r7BackendEnvVar + "=" + r7BackendDiesEarly, r7BackendEnvVar + "_SOCK=" + sock, "PATH=" + os.Getenv("PATH")},
		ReadyTimeout:   5 * time.Second,
		GoAheadTimeout: 300 * time.Millisecond,
	}
	cfg.GraceTimeout = 1 * time.Second

	done := runShimAsync(cfg)
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		t.Fatal("the backend exited and the shim left the session running; the owner's TUI is a " +
			"client of a dead server, so it is wedged rather than merely unmirrored (§R7.6)")
	}

	info := readExitInfo(t, cfg.SessionDir)
	if info.BackendExit == nil {
		t.Fatal("exit.json carries no backend_exit; \"the Codex backend died\" would be reported as " +
			"an unexplained agent exit, which is the one thing this field exists to prevent")
	}
	if *info.BackendExit != 7 {
		t.Errorf("backend_exit = %d, want the backend's real exit code 7", *info.BackendExit)
	}
}

// ---------------------------------------------------------------------------
// Degrade and compat
// ---------------------------------------------------------------------------

// TestR7ShimBackend_ABackendThatNeverBecomesServableStillLaunchesTheAgent is §R7.2e's other
// degrade: a backend failure is a failure for the BACKEND only. The agent still spawns, with
// no --remote appended, and the session runs exactly as a pre-R7 Codex session does. (The
// honest structured_gap that must accompany it is the daemon's, fenced in
// internal/skeleton/r7_lifecycle_test.go.)
func TestR7ShimBackend_ABackendThatNeverBecomesServableStillLaunchesTheAgent(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendNeverBinds, nil)
	cfg.Backend.ReadyTimeout = 700 * time.Millisecond
	cfg.Backend.GoAheadTimeout = 300 * time.Millisecond
	done := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader() // dialShim does not; every other test in this package starts it explicitly
	c.hello(shimwire.Version)
	c.attach()
	if c.waitObserved("INFO_DONE", 20*time.Second) == "" {
		_ = c.conn.Close()
		t.Fatal("a backend that never became servable took the AGENT down with it; playbook §6.1's " +
			"posture (\"disk-full lets the agent continue locally\") applies here too -- the PTY " +
			"plane is never at this channel's mercy")
	}
	_ = c.conn.Close()
	waitRun(t, done, 25*time.Second)

	if _, ok := ReadBackendInfo(cfg.SessionDir); ok {
		t.Error("backend.json was written for a backend that never served; the file's whole meaning " +
			"is \"a socket at this path is being served by THIS pid\", and writing it early makes " +
			"reconcile adopt a backend that was never there")
	}
}

// TestR7ShimBackend_ANilBackendIsThePreR7SessionByteForByte is the compat fence. Every session
// launched before R7, and every session of every other CLI forever, has Config.Backend == nil.
// Nothing about it may change: no file written, no timeout waited, no handshake expected.
func TestR7ShimBackend_ANilBackendIsThePreR7SessionByteForByte(t *testing.T) {
	cfg := helperConfig(t, modeInfo, nil, nil)
	if cfg.Backend != nil {
		t.Fatal("helperConfig set a Backend")
	}
	start := time.Now()
	done := runShimAsync(cfg)
	c := dialShim(t, cfg.SocketPath)
	c.startReader() // dialShim does not; every other test in this package starts it explicitly
	c.hello(shimwire.Version)
	c.attach()
	if c.waitObserved("INFO_DONE", 15*time.Second) == "" {
		_ = c.conn.Close()
		t.Fatal("a session with no backend did not run its agent")
	}
	_ = c.conn.Close()
	waitRun(t, done, 20*time.Second)

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("a no-backend session took %s; it must not wait on a handshake it never declared "+
			"(the HookSocketPath unset-means-disabled convention, launch.go:123-125)", elapsed)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionDir, BackendFile)); err == nil {
		t.Error("a session with no backend wrote backend.json")
	}
}

// TestR7ShimBackend_BackendInfoIsZeroSixHundred is the same 0600 discipline shim-launch.json
// already keeps: the file names a pid and a socket path for a process holding real account
// credentials, and it sits in a state dir.
func TestR7ShimBackend_BackendInfoIsZeroSixHundred(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.Backend.GoAheadTimeout = 300 * time.Millisecond
	done := runShimAsync(cfg)
	r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)

	st, err := os.Stat(filepath.Join(cfg.SessionDir, BackendFile))
	if err != nil {
		t.Fatalf("stat %s: %v", BackendFile, err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s is mode %04o, want 0600", BackendFile, perm)
	}
	waitRun(t, done, 25*time.Second)
}

// TestR7ShimBackend_BackendInfoDecodesToTheDocumentedWireShape pins the four keys the daemon
// reads, by NAME, because a restarted daemon written against a different spelling silently
// reads zero and treats every backend as dead.
func TestR7ShimBackend_BackendInfoDecodesToTheDocumentedWireShape(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.Backend.GoAheadTimeout = 300 * time.Millisecond
	done := runShimAsync(cfg)
	r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)

	data, err := os.ReadFile(filepath.Join(cfg.SessionDir, BackendFile))
	if err != nil {
		t.Fatalf("read %s: %v", BackendFile, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode %s: %v (%s)", BackendFile, err, data)
	}
	for _, key := range []string{"pid", "pgid", "started_at_ms", "socket_path"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("%s has no %q key; got %v", BackendFile, key, raw)
		}
	}
	if pid, _ := raw["pid"].(float64); int(pid) == 0 {
		t.Errorf("%s records pid 0", BackendFile)
	}
	waitRun(t, done, 25*time.Second)
}

// r7ShortSessionDir is a session dir whose path leaves room for a UDS. helperConfig uses
// t.TempDir(), and on macOS $TMPDIR plus a test-name-derived subdirectory blows past the
// 104-byte sun_path limit -- so <session-dir>/codex.sock, which every backend test binds,
// fails with EINVAL. newSocketPath (main_test.go) already keeps the CONTROL socket short the
// same way; this is that rule applied to the backend socket, which lives in the session dir
// by design (the containment check of obligation 9c measures against exactly that directory).
func r7ShortSessionDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "swbe")
	if err != nil {
		t.Fatalf("mktemp session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if p := filepath.Join(dir, "codex.sock"); len(p) > 100 {
		t.Fatalf("backend socket path too long (%d bytes): %s", len(p), p)
	}
	return dir
}

// TestR7ShimBackend_AnAgentThatCannotEXECStillTakesItsBackendDownWithIt is Wave R7 review
// BLOCKING 5, fenced.
//
// PROBED AGAINST ROUND 1: with the backend already running and serving, an exec failure on the
// AGENT (`cfg.Argv` naming a nonexistent program) returned from Run through the one exit that
// does not pass through finalization -- no finishEscalation, no touch of `backend` -- and the
// app-server was STILL ALIVE five seconds later, still holding the session socket. That is a
// process authenticated to a real ChatGPT account, with no PTY to HUP it and no stream anyone
// watches, which is the exact sentence backend.go opens with.
//
// The daemon-side reaper does not cover it either: reapOrphanBackend fires only from
// reconcile's LOST path, and this session's shim exited cleanly enough to leave the meta
// reconcilable only later.
func TestR7ShimBackend_AnAgentThatCannotEXECStillTakesItsBackendDownWithIt(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendIgnoreTerm, nil)
	cfg.Backend.GoAheadTimeout = 300 * time.Millisecond
	cfg.GraceTimeout = 1 * time.Second
	// The agent cannot be exec'd at all. Everything before it -- the pty pair, the control
	// socket, the backend spawn, backend.json -- has already happened.
	cfg.Argv = []string{"/nonexistent/definitely-not-a-program"}

	done := runShimAsync(cfg)
	info := r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)

	if res := waitRun(t, done, 30*time.Second); res.err == nil {
		t.Fatal("Run reported success for an agent that could not be exec'd")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !r7Alive(info.PID) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-info.PGID, syscall.SIGKILL) // do not leak the orphan out of the test
	t.Fatalf("the backend (pid %d, pgid %d) SURVIVED an agent exec failure. Run returned and left "+
		"a live app-server holding %s forever -- the exact leak §R7.2a was written to close",
		info.PID, info.PGID, cfg.Backend.SocketPath)
}
