// Package daemon is the Epic 5 daemon core: the lifecycle authority that can die
// (kill -9) and come back without losing any agent (S1). It is a flock-before-
// bind singleton (S12), rebuilds its registry from persist.Scan on Open and
// reconnects to live shims by (PID, process-start-time) match (S3), launches
// sessions two-phase with crash-safe reconciliation (S11), merges shim side-
// files as the SOLE meta writer (G6), routes kill/delete, enforces a max-session
// cap (S-7), and auto-starts detached on demand (D-1).
package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// pidFileName is the daemon's own pidfile within the state dir, used by
// `swarm daemon restart` to stop the running daemon before starting its
// replacement. It is not a session and is skipped by persist.Scan (a file, not a
// session directory).
const pidFileName = "daemon.pid"

// Tunables for the daemon's async machinery. All bound waits so nothing hangs.
const (
	monitorPoll = 100 * time.Millisecond // liveness poll for reconnected shims
	dialTimeout = 3 * time.Second        // dial a shim's per-session socket
	helloIO     = 3 * time.Second        // per-op deadline on a shim handshake
	deleteWait  = 10 * time.Second       // bound Delete's wait for a shim to exit
)

// Registry is the read view of the session roster (frozen API).
type Registry interface {
	List() []persist.Meta
	Get(id string) (persist.Meta, bool)
}

// Config configures a daemon instance.
type Config struct {
	StateDir    string
	SocketPath  string
	LockPath    string
	MaxSessions int
	ShimBinary  string
	LogPath     string

	// onMetaSave, when non-nil, fires after every daemon meta write with the
	// just-saved meta (test seam E5.3). It observes the reconnect-before-lost
	// ordering: a live shim is never persisted as lost.
	onMetaSave func(persist.Meta)
}

// LaunchSpec is a request to launch a new session.
type LaunchSpec struct {
	AgentType  string
	Argv       []string
	Cwd        string
	ClientEnv  []string
	Cols, Rows int
	Options    map[string]string
}

// session is the daemon's live handle on one session: its last-known meta plus a
// per-session stop channel that halts its monitor without finalizing it (Delete,
// and the clean-shutdown / abandon paths via the shared stop below).
type session struct {
	meta persist.Meta
	stop chan struct{}
}

// Daemon is the running lifecycle authority. Exactly one holds the flock + bound
// socket (S12).
type Daemon struct {
	cfg      Config
	store    *persist.Store
	lockFile *os.File
	listener net.Listener

	mu       sync.Mutex          // guards sessions + closed
	sessions map[string]*session // the registry
	closed   bool

	writeMu sync.Mutex     // serializes meta writes (single-writer, G6)
	stopCh  chan struct{}  // closed by Close/abandon: stop monitors + accept loop
	wg      sync.WaitGroup // accept loop + poll/launched supervisors (not the reapers)
}

// Open acquires the singleton (flock-before-bind), rebuilds the registry from the
// meta scan, reconnects live shims, and starts serving. It returns
// ErrAlreadyRunning if another daemon holds the lock.
func Open(cfg Config) (*Daemon, error) {
	store, err := persist.NewStore(cfg.StateDir) // creates + hardens the state dir 0700
	if err != nil {
		return nil, err
	}

	lockFile, err := acquireLock(cfg.LockPath)
	if err != nil {
		return nil, err // ErrAlreadyRunning if held, else a real error
	}

	// Bind the socket only AFTER the lock is held; unlink a stale socket under the
	// lock so a crashed prior daemon's leftover path is reclaimed safely (S12).
	listener, err := bindSocket(cfg.SocketPath)
	if err != nil {
		_ = releaseLock(lockFile)
		return nil, err
	}

	d := &Daemon{
		cfg:      cfg,
		store:    store,
		lockFile: lockFile,
		listener: listener,
		sessions: make(map[string]*session),
		stopCh:   make(chan struct{}),
	}
	writePIDFile(cfg.StateDir) // best-effort; for `swarm daemon restart`

	d.reconcile() // rebuild + reconnect BEFORE serving so List/Get are correct

	d.wg.Add(1)
	go d.acceptLoop()

	return d, nil
}

// List returns a snapshot of every session's meta.
func (d *Daemon) List() []persist.Meta {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]persist.Meta, 0, len(d.sessions))
	for _, s := range d.sessions {
		out = append(out, s.meta)
	}
	return out
}

// Get returns a session's meta by id.
func (d *Daemon) Get(id string) (persist.Meta, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[id]
	if !ok {
		return persist.Meta{}, false
	}
	return s.meta, true
}

// Close is a clean shutdown: stop serving and release the singleton (flock +
// socket). Running shims are independent and survive; their monitors are stopped
// without finalizing them. The lock is released so a fresh daemon can take over.
func (d *Daemon) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	close(d.stopCh)
	d.mu.Unlock()

	d.listener.Close() // unlinks the socket (clean shutdown)
	d.wg.Wait()        // accept loop + supervisors drain on stopCh
	removePIDFile(d.cfg.StateDir)
	return releaseLock(d.lockFile)
}

// abandon models a kill -9 of the daemon (E5.8/S1): drop the lock + socket fds
// with NO cleanup and NO shim signalling, exactly as the OS does when the daemon
// is SIGKILLed. The socket file is deliberately left on disk (stale) for the next
// Open to reclaim under the lock; no meta is finalized and no shim is touched.
func (d *Daemon) abandon() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.stopCh) // stop monitors + accept loop WITHOUT finalizing anything
	d.mu.Unlock()

	// Leave the socket file on disk (as a crash would): drop the fd only.
	if ul, ok := d.listener.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	d.listener.Close()
	d.lockFile.Close() // release the flock as the OS would on process death
}

// saveMeta is the SINGLE meta-write choke point (G6): it stamps the meta to the
// on-disk form, persists it atomically, updates the in-memory registry, and fires
// the onMetaSave observer. Writes are serialized by writeMu.
func (d *Daemon) saveMeta(m persist.Meta) error {
	m.SchemaVersion = persist.SchemaVersion
	m.Env = persist.FilterEnv(m.Env)

	d.writeMu.Lock()
	err := d.store.Save(m)
	d.writeMu.Unlock()
	if err != nil {
		return err
	}

	d.putMem(m)
	if d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(m)
	}
	return nil
}

// sessionDir returns the on-disk directory for a session id.
func (d *Daemon) sessionDir(id string) string {
	return filepath.Join(d.cfg.StateDir, id)
}

// liveCountLocked counts running sessions; caller holds d.mu.
func (d *Daemon) liveCountLocked() int {
	n := 0
	for _, s := range d.sessions {
		if s.meta.Status.Process == status.ProcessRunning {
			n++
		}
	}
	return n
}

// writePIDFile records the daemon's PID for `swarm daemon restart`. Best-effort.
func writePIDFile(stateDir string) {
	_ = os.WriteFile(filepath.Join(stateDir, pidFileName), []byte(fmt.Sprintf("%d", os.Getpid())), 0o600)
}

func removePIDFile(stateDir string) {
	_ = os.Remove(filepath.Join(stateDir, pidFileName))
}
