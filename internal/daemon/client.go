package daemon

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProtocolVersion is the client<->daemon handshake version. It is a var (not a
// const) so a skew can be exercised by dialing at ProtocolVersion+1.
var ProtocolVersion = 1

// ErrVersionSkew is returned by Dial when the daemon speaks a different protocol
// version than the client; the wrapped message names the fix, `swarm daemon
// restart` (D-8).
var ErrVersionSkew = errors.New("daemon: protocol version skew")

// Environment variables through which the production spawner hands a detached
// `swarm daemon` its configuration. Shared with cmd/swarm's daemon role.
const (
	EnvStateDir = "SWARM_DAEMON_STATE"
	EnvSocket   = "SWARM_DAEMON_SOCK"
	EnvLock     = "SWARM_DAEMON_LOCK"
	EnvLog      = "SWARM_DAEMON_LOG"
)

const (
	ensureTimeout = 10 * time.Second // bound EnsureDaemon's backoff-dial after spawn
	ensureBackoff = 50 * time.Millisecond
)

// ClientConfig locates the daemon and describes how to spawn one on demand.
type ClientConfig struct {
	SocketPath string
	LockPath   string
	StateDir   string
	DaemonBin  string
	LogPath    string

	// spawnDaemon overrides the production detached-spawn (test seam). Production
	// uses defaultSpawnDaemon when this is nil.
	spawnDaemon func(ClientConfig) error
}

// Dial connects to the daemon socket and runs the version handshake: it sends the
// client's version and reads the daemon's. A mismatch returns ErrVersionSkew
// naming `swarm daemon restart`; a match returns the live connection.
func Dial(socketPath string, clientVersion int) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(helloIO))

	var out [4]byte
	binary.BigEndian.PutUint32(out[:], uint32(clientVersion))
	if _, err := conn.Write(out[:]); err != nil {
		conn.Close()
		return nil, err
	}
	var in [4]byte
	if _, err := io.ReadFull(conn, in[:]); err != nil {
		conn.Close()
		return nil, err
	}
	daemonVersion := int(binary.BigEndian.Uint32(in[:]))
	if daemonVersion != clientVersion {
		conn.Close()
		return nil, fmt.Errorf("%w: daemon speaks protocol v%d, client v%d; run `swarm daemon restart` "+
			"(safe: your running sessions keep running and are reconnected — no live sessions are lost)",
			ErrVersionSkew, daemonVersion, clientVersion)
	}
	_ = conn.SetDeadline(time.Time{}) // hand the caller a fresh, deadline-free conn
	return conn, nil
}

// EnsureDaemon is the D-1 auto-start choke point: it dials the daemon, and on a
// miss spawns one detached and backoff-dials until it answers. Idempotent — a
// second call with a daemon already up connects without spawning.
//
// DEFERRED: the production `swarm list` client command that calls EnsureDaemon is
// the Epic 6/7 client layer; Epic 5 provides and tests this choke point but does
// not wire a user-facing client command through it.
func EnsureDaemon(cfg ClientConfig) (net.Conn, error) {
	if conn, err := Dial(cfg.SocketPath, ProtocolVersion); err == nil {
		return conn, nil
	}

	spawn := cfg.spawnDaemon
	if spawn == nil {
		spawn = defaultSpawnDaemon
	}
	if err := spawn(cfg); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(ensureTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := Dial(cfg.SocketPath, ProtocolVersion)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(ensureBackoff)
	}
	return nil, fmt.Errorf("daemon did not become reachable after spawn: %w", lastErr)
}

// defaultSpawnDaemon starts a detached `swarm daemon` (setsid, own session) with
// its stdio redirected to the log file and its configuration passed via the
// SWARM_DAEMON_* environment (D-1). It does not block on the child.
func defaultSpawnDaemon(cfg ClientConfig) error {
	bin := cfg.DaemonBin
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		bin = exe
	}

	// Create the private state dir (0700) BEFORE opening the log: on a truly cold
	// start the dir does not exist yet, and the log lives inside it, so opening the
	// log first would ENOENT (F5, D-1/D-6).
	if cfg.StateDir != "" {
		if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(cfg.StateDir, 0o700); err != nil {
			return err
		}
	}

	logf, err := openDaemonLog(cfg.LogPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "daemon")
	cmd.Env = append(os.Environ(),
		EnvStateDir+"="+cfg.StateDir,
		EnvSocket+"="+cfg.SocketPath,
		EnvLock+"="+cfg.LockPath,
		EnvLog+"="+cfg.LogPath,
	)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	startErr := cmd.Start()
	logf.Close() // the child holds its own dup of the fd
	if startErr != nil {
		return startErr
	}
	go func() { _ = cmd.Wait() }() // reap if the client outlives the daemon
	return nil
}

// openDaemonLog opens the daemon's stdio log (append, 0600), or /dev/null when no
// path is configured. An already-existing log that predates this daemon with a
// wider mode is hardened back to 0600 on every open — O_CREATE does not chmod an
// existing file (E5.7/D-6, F10).
func openDaemonLog(path string) (*os.File, error) {
	if path == "" {
		return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Restart performs the `swarm daemon restart` half of D-8: stop the running
// daemon (its shims survive independently — S1), then spawn a fresh one that
// reconnects them. A stale or absent pidfile makes the stop a no-op.
//
// It reports success ONLY once the replacement has actually taken over (N1): it
// waits for the old daemon to RELEASE the flock before spawning (Close unlinks the
// socket before releasing the lock, so the socket going quiet is not proof the
// lock is free), then confirms the replacement is reachable. Spawning too early
// would make the replacement lose the singleton and exit while restart falsely
// reported success.
func Restart(cfg ClientConfig) error {
	stopRunningDaemon(cfg)

	// The replacement cannot win the singleton until the old daemon drops the lock.
	if err := waitLockFree(cfg.LockPath, ensureTimeout); err != nil {
		return fmt.Errorf("daemon restart: previous daemon did not release the lock: %w", err)
	}

	spawn := cfg.spawnDaemon
	if spawn == nil {
		spawn = defaultSpawnDaemon
	}
	if err := spawn(cfg); err != nil {
		return err
	}

	// Confirm the replacement actually took over before reporting success.
	deadline := time.Now().Add(ensureTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := Dial(cfg.SocketPath, ProtocolVersion)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(ensureBackoff)
	}
	return fmt.Errorf("daemon restart: replacement did not become reachable: %w", lastErr)
}

// waitLockFree polls until the daemon lock at path can be acquired — proof the old
// daemon released it — releasing it immediately, or the timeout elapses (N1). A
// real error opening the lock file is returned at once; a still-held lock past the
// deadline yields ErrAlreadyRunning.
func waitLockFree(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		f, err := acquireLock(path)
		if err == nil {
			_ = releaseLock(f) // release at once so the replacement can take it
			return nil
		}
		if !errors.Is(err, ErrAlreadyRunning) {
			return err
		}
		if !time.Now().Before(deadline) {
			return err // still held (ErrAlreadyRunning)
		}
		time.Sleep(ensureBackoff)
	}
}

// stopRunningDaemon best-effort terminates the daemon named in the state dir's
// pidfile and waits (bounded) for its socket to go quiet, so the replacement can
// re-acquire the singleton. A clean daemon Close releases the lock; SIGTERM
// drives exactly that.
func stopRunningDaemon(cfg ClientConfig) {
	data, err := os.ReadFile(cfg.StateDir + "/" + pidFileName)
	if err != nil {
		return
	}
	pid, start, ok := parsePIDFile(data)
	if !ok || pid <= 0 {
		return
	}
	// Verify the pidfile still names THIS daemon before signalling: after a crash +
	// PID reuse the recorded PID may belong to an unrelated process — the exact S3
	// hazard, here in the daemon's own pidfile. A start-time mismatch (or an
	// un-readable PID) makes stop a genuine no-op rather than signal a stranger (F1).
	if st, serr := processStartTime(pid); serr != nil || st != start {
		return
	}
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		return // already gone
	}
	deadline := time.Now().Add(ensureTimeout)
	for time.Now().Before(deadline) {
		if _, derr := Dial(cfg.SocketPath, ProtocolVersion); derr != nil {
			return // socket no longer answers: the old daemon has released it
		}
		time.Sleep(ensureBackoff)
	}
}

// parsePIDFile parses a "PID STARTTIME" daemon pidfile (F1). It returns ok=false —
// an explicit, non-fatal rejection, never a misparse into a wrong PID — for every
// input this build cannot safely act on:
//   - a legacy PID-only pidfile (one field): no start-time, so identity cannot be
//     verified. v1.0 is the first release, so no such files exist in the field, but
//     the rejection is deliberate rather than an accidental partial parse.
//   - garbage: a non-integer PID or start-time, or not exactly two fields.
//
// stopRunningDaemon maps ok=false to a safe no-op (it signals nothing). That never
// blocks a fresh daemon from starting: when no old daemon is actually running the
// lock is free and Restart proceeds; only a live-but-unidentifiable daemon (lock
// held) prevents a hand-off, which is the correct, safe outcome.
func parsePIDFile(data []byte) (pid int, start int64, ok bool) {
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, 0, false
	}
	p, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, false
	}
	s, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return p, s, true
}
