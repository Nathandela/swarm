package shim

// THE SESSION BACKEND (Wave R7, Mirror M4.1; ADR-013 §R7.2a/b/c/e and §R7.6): the
// `codex app-server` a Codex session needs, owned by THE SHIM as a sibling of the agent.
//
// WHY THE SHIM AND NOT THE DAEMON. Shims outlive daemons (ADR-001). A daemon-owned backend
// would be orphaned by every `swarm daemon restart` while the agent it serves kept running,
// and the agent under `codex --remote unix://SOCK` is a CLIENT of that server -- R1 leg 2
// recorded its whole boot handshake, model resolution and MCP boot going through it -- so an
// orphaned backend is a wedged terminal, not merely an unmirrored one.
//
// THE CONTAINMENT CLAIM THIS FILE EXISTS TO MAKE TRUE, and it is a RETRACTION. An earlier
// draft said the backend "joins the agent's existing TERM->grace->KILL". IT DOES NOT. Every
// kill on that path is syscall.Kill(-s.pgid, ...) and s.pgid is the AGENT's group (server.go,
// pinned by spawn_test.go); a sibling exec.Cmd started by the shim inherits the SHIM's group,
// so all of those kills MISS IT, and cmd.Wait() reaps the agent only. Run would return leaving
// a live app-server -- a process authenticated to a real ChatGPT account -- behind forever,
// with no PTY to HUP it and no stream for anybody to notice.
//
// So the backend gets its OWN process group (Setpgid), is signalled BESIDE the agent's group
// on the same TERM->grace->KILL (server.killGroups), and is joined by its OWN Wait goroutine
// AFTER finishEscalation has issued the final group KILL -- which is what guarantees the join
// returns rather than parking Run behind a process that ignores TERM.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
)

// BackendFile is the per-session side-file naming the live backend process. It is 0600 for
// the same reason shim-launch.json is: it names a pid and a socket path for a process holding
// real account credentials, and it sits in a state dir.
const BackendFile = "backend.json"

// Default bounds for the two waits a backend introduces. Both are overridable per session and
// both exist so a backend failure is a failure for the BACKEND only.
const (
	defaultBackendReadyTimeout   = 20 * time.Second
	defaultBackendGoAheadTimeout = 20 * time.Second
)

// backendDialTimeout bounds ONE readiness probe of the backend socket.
const backendDialTimeout = 500 * time.Millisecond

// backendPollInterval is how often readiness is re-probed.
const backendPollInterval = 25 * time.Millisecond

// BackendConfig describes the side process for one session. Config.Backend == nil is the
// PRE-R7 SESSION, byte-for-byte: no file written, no timeout waited, no handshake expected.
// It is HookSocketPath's exact unset-means-disabled convention.
type BackendConfig struct {
	// Program is the backend executable, ALREADY RESOLVED to an absolute path by the core
	// (the adapter NAMES a program, adapter.ResolveBackend LookPaths it). It is exec'd
	// DIRECTLY, never through a shell -- obligation 9b, and the rule this package already
	// states for the agent's own argv.
	Program string
	Args    []string
	// Env is the backend process's environment, used verbatim.
	Env []string
	// SocketPath is the endpoint the backend serves and the agent attaches to. It is inside
	// the session dir, which is what makes the core's containment check measurable.
	SocketPath string
	// ReadyTimeout bounds "the socket became servable" (0 => defaultBackendReadyTimeout).
	ReadyTimeout time.Duration
	// GoAheadTimeout bounds waitBackendGoAhead (0 => defaultBackendGoAheadTimeout).
	GoAheadTimeout time.Duration
}

// BackendInfo is the decoded backend.json.
//
// WHY IT EXISTS AT ALL. The daemon cannot know a pid it did not spawn, and the app-server has
// NO PTY, no controlling terminal and writes to neither stream ever -- so a SIGKILL of the
// shim leaves an orphan that stays alive indefinitely, keeps serving the session's socket, and
// looks HEALTHY to any rule whose readiness test is "the dial succeeded". This file is what
// lets a restarted daemon tell a live backend from an unrelated process on a reused path:
// liveness is (pid alive AND start-time matches AND the owning shim is live), never a dial.
type BackendInfo struct {
	PID         int    `json:"pid"`
	PGID        int    `json:"pgid"`
	StartedAtMS int64  `json:"started_at_ms"`
	SocketPath  string `json:"socket_path"`
}

// ReadBackendInfo decodes a session's backend.json. ok==false for a session that declared no
// backend, or whose backend never became servable -- absence is absence.
func ReadBackendInfo(sessionDir string) (BackendInfo, bool) {
	data, err := os.ReadFile(filepath.Join(sessionDir, BackendFile))
	if err != nil {
		return BackendInfo{}, false
	}
	var info BackendInfo
	if json.Unmarshal(data, &info) != nil || info.PID <= 0 {
		return BackendInfo{}, false
	}
	return info, true
}

// backendProc is one running backend, from the shim's side.
type backendProc struct {
	cmd  *exec.Cmd
	pgid int
	// dead is CLOSED by the dedicated Wait goroutine once the backend is reaped, and
	// exitCode is written before the close. A closed channel rather than a value channel so
	// every observer -- the readiness poll, Run's own die-first edge, and the final join --
	// sees the fact without consuming it from the others.
	dead     chan struct{}
	exitCode int
}

// startBackend spawns the backend in ITS OWN PROCESS GROUP and waits for its socket to become
// servable, then returns it. A backend that never becomes servable is a failure for the
// backend only: the caller still launches the agent (degraded), because playbook §6.1's
// posture -- "disk-full lets the agent continue locally" -- applies here too. The PTY plane is
// never at this channel's mercy.
func startBackend(cfg *BackendConfig, sessionDir string) (*backendProc, error) {
	if cfg.Program == "" {
		return nil, errors.New("shim: backend declared with no program")
	}
	_ = os.Remove(cfg.SocketPath) // clear a stale socket from a prior crash, listen()'s rule

	cmd := &exec.Cmd{
		Path: cfg.Program,
		Args: append([]string{cfg.Program}, cfg.Args...),
		Env:  cfg.Env,
		Dir:  sessionDir,
		// ITS OWN GROUP. A backend in the SHIM's group is contained only by the daemon's
		// containment of the shim -- the accident §R7.2a refuses to rely on -- and R1 leg 1
		// recorded that one `codex app-server` is TWO pids (a node launcher plus the vendored
		// rust binary), so the group is what reaps both.
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("shim: start backend: %w", err)
	}
	b := &backendProc{cmd: cmd, pgid: cmd.Process.Pid, dead: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		b.exitCode, _ = interpretExit(err)
		close(b.dead)
	}()

	ready := cfg.ReadyTimeout
	if ready <= 0 {
		ready = defaultBackendReadyTimeout
	}
	if !waitBackendServable(cfg.SocketPath, ready, b.dead) {
		return b, fmt.Errorf("shim: backend socket %s never became servable within %s", cfg.SocketPath, ready)
	}
	return b, nil
}

// containBackendFailure ends a backend the session can no longer use -- one whose identity
// record could not be written, or whose socket never became servable -- and scrubs any
// backend.json on disk. TERM to ITS OWN group, one grace, then always the final synchronous
// group KILL (finishEscalation's own discipline: it reaps a TERM-ignoring member, leader or
// child, and is a harmless ESRCH on a group that already emptied), joined on the backend's
// dedicated Wait goroutine -- the leader's KILL is what guarantees the join returns.
//
// WHY KILL NOW rather than leave it to finalization: backend.json is the daemon's ONLY means
// of identifying an orphan backend, and both callers are on paths where the record does not
// (or can not) exist. A live app-server with no record -- authenticated to a real account, no
// PTY, no streams -- survives an uncatchable SIGKILL of this shim FOREVER: the reaper cannot
// reap what was never recorded. So a backend the daemon cannot find must not outlive the
// shim's decision to stop using it.
//
// The record scrub covers both failure shapes: writeBackendInfo can fail AFTER its rename
// (leaving a record that names the pid this function just killed), and a prior incarnation
// can leave a stale one; either would send the next daemon reconcile chasing a pid that is
// not this session's backend.
func containBackendFailure(b *backendProc, sessionDir string, grace time.Duration) {
	if b != nil {
		_ = syscall.Kill(-b.pgid, syscall.SIGTERM)
		select {
		case <-b.dead:
		case <-time.After(grace):
		}
		_ = syscall.Kill(-b.pgid, syscall.SIGKILL)
		<-b.dead
	}
	_ = os.Remove(filepath.Join(sessionDir, BackendFile))
}

// waitBackendServable polls until the socket accepts a connection, the bound elapses, or the
// backend dies first. READINESS CAN ONLY COME FROM THE SOCKET: R1 leg 1 recorded that
// `codex app-server` writes NOTHING to stdout or stderr for an entire session, so there is no
// banner to wait for and no stream anybody can watch.
func waitBackendServable(path string, within time.Duration, dead <-chan struct{}) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		select {
		case <-dead:
			return false // it exited before it ever served; nothing will make it servable
		default:
		}
		conn, err := net.DialTimeout("unix", path, backendDialTimeout)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(backendPollInterval)
	}
	return false
}

// writeBackendInfo records the live backend, atomically at 0600, THE MOMENT ITS SOCKET IS
// SERVABLE and not a moment earlier. The file's whole meaning is "a socket at this path is
// being served by THIS pid", so writing it early would make a restarted daemon adopt a backend
// that was never there.
func writeBackendInfo(sessionDir string, b *backendProc, socketPath string) error {
	started, err := procstart.StartTime(b.cmd.Process.Pid)
	if err != nil {
		// Without the start time a recycled pid reads live, which is the exact hole the pair
		// closes. Report rather than write a file that would lie.
		return fmt.Errorf("shim: read backend start time: %w", err)
	}
	data, err := json.Marshal(BackendInfo{
		PID:         b.cmd.Process.Pid,
		PGID:        b.pgid,
		StartedAtMS: started,
		SocketPath:  socketPath,
	})
	if err != nil {
		return err
	}
	if err := writeFileAtomic(sessionDir, BackendFile, data); err != nil {
		return err
	}
	// writeFileAtomic's temp file is 0600 by os.CreateTemp, but the rename target may
	// pre-exist from a prior incarnation with a looser mode; re-tighten explicitly.
	return os.Chmod(filepath.Join(sessionDir, BackendFile), 0o600)
}
