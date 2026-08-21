package shim

// FAILING-FIRST (TDD RED, GG-5) for the R7 containment EDGE paths the final audit committee
// flagged (codex HIGH-7). The two shim-death paths are already fenced (r7_backend_test.go);
// these are the UNTESTED edges around the backend RECORD:
//
//  1. A FAILURE TO WRITE backend.json used to be only logged (shim.go), and execution carried
//     on with a live, account-authenticated app-server the daemon could never identify:
//     backend.json is the daemon's ONLY means of finding an orphan backend (backend.go), so
//     that backend + a SIGKILL of the shim = an orphan FOREVER. The edge rule: a backend the
//     daemon cannot find must not survive the shim's failure to record it.
//
//  2. A backend that never becomes servable within ReadyTimeout used to stay ALIVE (in its own
//     group, unrecorded) until Run finalized -- for a modeIdle-shaped agent, indefinitely.
//     Same hole: unrecorded + shim SIGKILL = unreapable orphan; and `codex app-server` is TWO
//     pids, so the kill must be BY GROUP, and any early/stale record must be cleaned.
//
// Both tests keep the session ALIVE (modeIdle agent) while asserting the backend group is
// dead, because that is what distinguishes the fix from finishEscalation's finalization KILL,
// which already reaps the group once Run returns.

import (
	"io"
	"log"
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

	"github.com/Nathandela/swarm/internal/shimwire"
)

// ---------------------------------------------------------------------------
// The EDGE backend helper process: this test binary re-exec'd through its own
// env var and its own init(), exactly as r7_backend_test.go's helper is, so the
// two helper families cannot collide. It differs in ONE way: it reports its
// pid(s) through a file, because these tests assert on processes that are
// deliberately NEVER recorded in backend.json.
// ---------------------------------------------------------------------------

const r7EdgeEnvVar = "SWARM_SHIM_TEST_BACKEND_EDGE"

const (
	r7EdgeBind           = "bind"             // bind the socket, accept forever, silent
	r7EdgeNeverBindChild = "never-bind-child" // never bind; spawn a same-group child; block
)

func init() {
	mode := os.Getenv(r7EdgeEnvVar)
	if mode == "" {
		return
	}
	sock := os.Getenv(r7EdgeEnvVar + "_SOCK")
	pidFile := os.Getenv(r7EdgeEnvVar + "_PIDS")
	switch mode {
	case r7EdgeBind:
		ln, err := net.Listen("unix", sock)
		if err != nil {
			os.Exit(90)
		}
		r7EdgeWritePids(pidFile, os.Getpid())
		for {
			c, err := ln.Accept()
			if err != nil {
				os.Exit(91)
			}
			_ = c.Close()
		}
	case r7EdgeNeverBindChild:
		// The two-pid app-server shape (R1 leg 1: a node launcher plus the vendored rust
		// binary): a child in the LEADER's group. It must be reaped BY GROUP or not at all.
		child := exec.Command("/bin/sleep", "300")
		if err := child.Start(); err != nil {
			os.Exit(92)
		}
		r7EdgeWritePids(pidFile, os.Getpid(), child.Process.Pid)
		// Waiting on the child keeps a live M (a goroutine parked in wait4 does not trip
		// the runtime's deadlock detector the way a bare select{} does; see park()).
		_ = child.Wait()
		os.Exit(93)
	default:
		os.Exit(94)
	}
}

// r7EdgeWritePids records the helper's pid(s), one per line, atomically enough for a poller
// (write-then-rename, so a reader never sees a partial file).
func r7EdgeWritePids(path string, pids ...int) {
	var b strings.Builder
	for _, p := range pids {
		b.WriteString(strconv.Itoa(p))
		b.WriteString("\n")
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, []byte(b.String()), 0o600) != nil || os.Rename(tmp, path) != nil {
		os.Exit(95)
	}
}

// r7EdgeWaitPids polls for the helper's pid file and parses exactly n pids from it.
func r7EdgeWaitPids(t *testing.T, path string, n int, within time.Duration) []int {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == n {
				pids := make([]int, 0, n)
				for _, f := range fields {
					p, perr := strconv.Atoi(f)
					if perr != nil || p <= 0 {
						pids = nil
						break
					}
					pids = append(pids, p)
				}
				if len(pids) == n {
					return pids
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the edge backend helper never reported %d pid(s) in %s within %s", n, path, within)
	return nil
}

// r7EdgeAwaitDead polls until pid is gone, and fails saying why an orphan matters.
func r7EdgeAwaitDead(t *testing.T, pid int, within time.Duration, why string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(why)
}

// r7EdgeLockedBuf is a race-safe log sink; the shim's goroutines log concurrently.
type r7EdgeLockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *r7EdgeLockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *r7EdgeLockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// r7EdgeCaptureLog tees the stdlib logger (which Run uses for every degrade message) into a
// buffer the test can assert on. No test in this package runs Parallel, so the global logger
// is safe to redirect for the duration of one test.
func r7EdgeCaptureLog(t *testing.T) *r7EdgeLockedBuf {
	t.Helper()
	buf := &r7EdgeLockedBuf{}
	prev := log.Writer()
	log.SetOutput(io.MultiWriter(prev, buf))
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

// r7EdgeCfg builds a modeIdle-agent Config whose backend is this test binary in the named
// EDGE mode. modeIdle matters: the session stays alive, so a backend that is only reaped at
// finalization reads ALIVE to these tests -- which is exactly the bug.
func r7EdgeCfg(t *testing.T, backendMode string) (Config, string) {
	t.Helper()
	cfg := helperConfig(t, modeIdle, nil, nil)
	cfg.SessionDir = r7ShortSessionDir(t)
	cfg.GraceTimeout = 1 * time.Second
	sock := filepath.Join(cfg.SessionDir, "codex.sock")
	pids := filepath.Join(cfg.SessionDir, "backend.pids")
	cfg.Backend = &BackendConfig{
		Program:    selfExe(t),
		Args:       []string{"app-server"},
		SocketPath: sock,
		Env: []string{
			r7EdgeEnvVar + "=" + backendMode,
			r7EdgeEnvVar + "_SOCK=" + sock,
			r7EdgeEnvVar + "_PIDS=" + pids,
			"PATH=" + os.Getenv("PATH"),
		},
		ReadyTimeout:   5 * time.Second,
		GoAheadTimeout: 300 * time.Millisecond,
	}
	return cfg, pids
}

// r7EdgeEndSession ends the still-running modeIdle session cleanly and joins Run.
func r7EdgeEndSession(t *testing.T, cfg Config, done <-chan runResult) {
	t.Helper()
	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	c.waitObserved("IDLING", 10*time.Second)
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	waitRun(t, done, 20*time.Second)
	_ = c.conn.Close()
}

// ---------------------------------------------------------------------------
// Edge 1 -- a record-write failure must not leave an UNRECORDED live backend
// ---------------------------------------------------------------------------

// TestR7Edge_ARecordWriteFailureKillsTheUnrecordedBackend forces writeBackendInfo to fail
// (backend.json's path is occupied by a directory, so writeFileAtomic's rename fails exactly
// as a disk-full or permission failure does) AFTER the backend became servable, and asserts:
//
//   - the backend group is killed PROMPTLY, while the session is still running -- not left to
//     finishEscalation, which a SIGKILL of the shim never reaches;
//   - the session itself SURVIVES, degraded (the killed backend is not WATCHED, so its death
//     is not the sec7.6 "backend died -> end the session" edge);
//   - the failure is SURFACED (the log names it, and exit.json's backend_exit explains the
//     backend's death rather than leaving it an unexplained disappearance);
//   - no record survives on disk.
//
// MUTATION: restore the old write-failure branch (log only, backendWatch = backend). The
// prompt-death assert fails -- and the session-survives assert catches the half-mutation that
// kills the backend but keeps watching it.
func TestR7Edge_ARecordWriteFailureKillsTheUnrecordedBackend(t *testing.T) {
	logBuf := r7EdgeCaptureLog(t)
	cfg, pids := r7EdgeCfg(t, r7EdgeBind)

	// FORCE THE WRITE FAILURE: the record's path cannot be renamed onto.
	if err := os.Mkdir(filepath.Join(cfg.SessionDir, BackendFile), 0o755); err != nil {
		t.Fatalf("occupy %s: %v", BackendFile, err)
	}

	done := runShimAsync(cfg)
	backendPid := r7EdgeWaitPids(t, pids, 1, 10*time.Second)[0]
	t.Cleanup(func() { _ = syscall.Kill(-backendPid, syscall.SIGKILL) })

	r7EdgeAwaitDead(t, backendPid, 10*time.Second,
		"the backend survived a FAILURE TO WRITE backend.json. That file is the daemon's ONLY "+
			"means of identifying an orphan backend, so this live app-server -- authenticated to a "+
			"real account, no PTY, no streams -- plus a SIGKILL of the shim is an orphan FOREVER")

	// The session must still be running: the write failure is a failure for the BACKEND
	// CHANNEL only, and the killed backend must not be WATCHED (a watched death would fire
	// the sec7.6 edge and take the agent down for a channel it is not using).
	select {
	case <-done:
		t.Fatal("the session ended with the backend; a record-write failure must degrade to a " +
			"backend-less session, not kill the agent")
	default:
	}
	time.Sleep(2500 * time.Millisecond) // long past spawn+TERM+grace: a watched death would have ended Run by now
	select {
	case <-done:
		t.Fatal("the session ended shortly after the backend was killed: the killed backend is " +
			"still WATCHED, so its (deliberate) death fired the backend-died-first edge")
	default:
	}

	r7EdgeEndSession(t, cfg, done)

	if _, ok := ReadBackendInfo(cfg.SessionDir); ok {
		t.Error("a backend record exists after the write was reported failed; whatever it names, " +
			"the daemon would chase it")
	}
	if info := readExitInfo(t, cfg.SessionDir); info.BackendExit == nil {
		t.Error("exit.json carries no backend_exit; the backend this session killed on purpose " +
			"would be reported as an unexplained disappearance")
	}
	if !strings.Contains(logBuf.String(), "record the session backend") {
		t.Error("the record-write failure was never surfaced in the shim's log")
	}
}

// ---------------------------------------------------------------------------
// Edge 2 -- readiness timeout, with a live same-group child and an early record
// ---------------------------------------------------------------------------

// TestR7Edge_AReadinessTimeoutKillsTheBackendGroupAndCleansTheRecord runs a backend that
// never binds its socket AND holds a live child in its group (the real app-server's two-pid
// shape), over a stale early-written record, and asserts at the ReadyTimeout:
//
//   - leader AND child are killed -- by GROUP, promptly, while the session is still running,
//     not at finalization (a modeIdle-shaped agent means finalization may be hours away, all
//     of it spent as an unrecorded, possibly-late-binding app-server);
//   - the session itself survives degraded (the existing never-servable fence's rule);
//   - the record on disk -- written early, or left by a prior incarnation -- is CLEANED, so
//     no reconcile ever chases a pid this session already killed.
//
// MUTATION: delete containBackendFailure from the berr branch in shim.go. The prompt-death
// asserts fail (the group would only die at finalization, which modeIdle never reaches).
func TestR7Edge_AReadinessTimeoutKillsTheBackendGroupAndCleansTheRecord(t *testing.T) {
	cfg, pids := r7EdgeCfg(t, r7EdgeNeverBindChild)
	cfg.Backend.ReadyTimeout = 700 * time.Millisecond

	// The early/stale record: a plausible file naming a pid that is long gone. It must not
	// survive the readiness timeout.
	r7EdgeWriteStaleRecord(t, cfg.SessionDir, cfg.Backend.SocketPath)

	done := runShimAsync(cfg)
	ps := r7EdgeWaitPids(t, pids, 2, 10*time.Second)
	leader, child := ps[0], ps[1]
	t.Cleanup(func() {
		_ = syscall.Kill(-leader, syscall.SIGKILL)
		_ = syscall.Kill(child, syscall.SIGKILL)
	})

	r7EdgeAwaitDead(t, leader, 10*time.Second,
		"the never-servable backend's LEADER survived the readiness timeout while the session ran "+
			"on; unrecorded and alive, it becomes an unreapable orphan the moment the shim takes "+
			"an uncatchable SIGKILL")
	r7EdgeAwaitDead(t, child, 10*time.Second,
		"the never-servable backend's same-group CHILD survived; `codex app-server` is TWO pids, "+
			"so anything short of kill(-pgid) leaves the vendored binary running")

	select {
	case <-done:
		t.Fatal("the session ended at the readiness timeout; a backend failure is a failure for " +
			"the BACKEND only (the existing never-servable fence's rule)")
	default:
	}

	if _, ok := ReadBackendInfo(cfg.SessionDir); ok {
		t.Error("an early/stale backend.json survived the readiness timeout; the next daemon " +
			"reconcile would chase a pid that is not this session's backend")
	}

	r7EdgeEndSession(t, cfg, done)
}

// r7EdgeWriteStaleRecord plants a well-formed backend.json naming a pid that cannot be a live
// process (above every real pid on macOS and default Linux), standing in for an early write
// or a prior incarnation's leftover.
func r7EdgeWriteStaleRecord(t *testing.T, dir, sock string) {
	t.Helper()
	body := []byte(`{"pid":99999999,"pgid":99999999,"started_at_ms":1,"socket_path":"` + sock + `"}`)
	if err := os.WriteFile(filepath.Join(dir, BackendFile), body, 0o600); err != nil {
		t.Fatalf("plant stale %s: %v", BackendFile, err)
	}
}
