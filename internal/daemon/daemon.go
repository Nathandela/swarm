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
)

// deleteWait bounds Delete's wait for a shim to exit after termination, and
// killSpawnedShim's wait for an aborting shim. It is a var (not a const) so a test
// can shorten it to exercise the termination-timeout path quickly.
var deleteWait = 10 * time.Second

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

	// PreLaunch and PreDelete are optional launch-time isolation hooks (Epic 12):
	// the daemon core calls them generically and carries no worktree-specific
	// logic itself. Both are nil by default (opt-in; every pre-Epic-12 Config
	// leaves current behavior unchanged).
	//
	// PreLaunch runs in launch(), after the session id is reserved and before the
	// shim is spawned. A non-nil cwdOverride replaces the AGENT's working
	// directory (e.g. an isolated git worktree) without altering the persisted
	// meta.Cwd. An error aborts the launch cleanly before any shim exists.
	PreLaunch func(id string, spec LaunchSpec) (cwdOverride string, err error)

	// PreDelete runs in Delete, before the session's directory is torn down. Its
	// error is logged and returned, but never blocks the mandatory teardown.
	PreDelete func(m persist.Meta) error

	// OnSessionStart and OnSessionEnd are optional status-engine wiring hooks (Epic
	// 11). Both are nil by default (opt-in; pre-Epic-11 configs are unchanged).
	//
	// OnSessionStart fires for a launched session — once at fresh launch (after the
	// shim identity is persisted) and again on reconcile when a live shim is
	// reconnected after a daemon restart (L2) — with the session's meta and its
	// per-session hook token. The assembly registers the session with the engine
	// here so an authenticated hook callback can drive its status. The token is
	// never written to meta.json; at fresh launch it arrives from the launch path,
	// and on reconnect reconcile re-reads it from the 0600 shim-launch.json (ADR-004).
	//
	// OnSessionEnd fires when a session ends (its shim exits, or it is deleted), so
	// the assembly retires the engine session and its token (S6).
	OnSessionStart func(m persist.Meta, token string)
	OnSessionEnd   func(id string)

	// ConnHandler, when non-nil, REPLACES the daemon's minimal 4-byte version
	// handshake (serveClient) as the handler for every connection accepted on the
	// singleton socket (Epic 8 assembly). The daemon still owns the socket —
	// flock-before-bind, stale-socket reclaim under the lock, and the accept loop
	// all stay here (S12) — but the connection SERVING moves to the caller, so the
	// skeleton can run protocol.Server's per-connection loop (and demux hook posts)
	// on the daemon's own socket instead of binding a second one. Each accepted
	// connection is handed to ConnHandler in its own goroutine; the handler owns
	// closing it.
	ConnHandler func(net.Conn)
}

// LaunchSpec is a request to launch a new session.
type LaunchSpec struct {
	AgentType  string
	Argv       []string
	Cwd        string
	ClientEnv  []string
	Cols, Rows int
	Options    map[string]string
	// InitialPrompt is the optional first prompt text. The Epic 9 adapter composes
	// it into the agent argv; the daemon only carries it (F8).
	InitialPrompt string
	// ResumedFrom, when non-empty, is the LOCAL id of a prior session this launch
	// resumes (Epic 11 / R-2). The daemon stamps it into the new session's
	// meta.ResumedFrom, linking the two; resolving the reference and composing the
	// adapter's resume argv is the assembly's job (the daemon only carries the link).
	ResumedFrom string
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

	// tombMu guards deleted, a set of ids removed by Delete. A concurrent exit-merge
	// must not recreate a session's dir or registry entry after Delete removed it
	// (F3): saveMeta/putMem consult this set and skip a tombstoned id. It is a leaf
	// lock, taken only for the brief map op. The set grows by one per Delete over the
	// daemon's lifetime; that bound is acceptable (ids are never reused).
	tombMu  sync.Mutex
	deleted map[string]struct{}
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
		deleted:  make(map[string]struct{}),
	}
	writePIDFile(cfg.StateDir) // best-effort; for `swarm daemon restart`

	// Rebuild + reconnect BEFORE serving so List/Get are correct. A scan failure is
	// fatal to Open: serving a blind, possibly-empty registry would silently drop
	// live sessions (F4). Release everything acquired above before returning.
	if err := d.reconcile(); err != nil {
		d.listener.Close()
		removePIDFile(cfg.StateDir)
		_ = releaseLock(lockFile)
		return nil, err
	}

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

// SetStatus routes an engine-derived status change through the daemon's single
// meta writer (G6), making it durable and observable in List — the write seam the
// status engine's Emit uses (Epic 11 seam c). Only the ACTIVITY dimensions
// (turn/interaction) are applied: the daemon stays the sole authority on the
// lifecycle dimension (process), so a late engine emit can never resurrect an
// exited or lost session. It is a no-op for an unchanged status and an error for
// an unknown session; a tombstoned (deleted) session is dropped by saveMeta.
func (d *Daemon) SetStatus(id string, s status.Status) error {
	// Hold writeMu across the whole read-modify-write so the status write is an
	// ATOMIC RMW against the CURRENT meta under one serialization boundary. Every
	// process-dimension writer (handleShimExit/markLost/reconcile via saveMeta)
	// also commits under writeMu, so re-reading the live meta HERE, inside the
	// write section, observes any exit that already committed — closing the TOCTOU
	// window in which a stale read let a late emit resurrect an exited session (F1).
	d.writeMu.Lock()
	d.mu.Lock()
	sess, ok := d.sessions[id]
	var m persist.Meta
	if ok {
		m = sess.meta
	}
	d.mu.Unlock()
	if !ok {
		d.writeMu.Unlock()
		return fmt.Errorf("daemon: unknown session %q", id)
	}
	// Refuse to persist activity for a session that is no longer running: the
	// daemon stays the sole authority on the process dimension, so a late engine
	// emit can never overwrite an exited or lost session back to running (F1).
	if m.Status.Process != status.ProcessRunning {
		d.writeMu.Unlock()
		return nil
	}
	next := m.Status
	next.Turn = s.Turn
	next.Interaction = s.Interaction
	if next == m.Status {
		d.writeMu.Unlock()
		return nil // no activity change to persist
	}
	m.Status = next
	m.SchemaVersion = persist.SchemaVersion
	written, err := d.saveMetaLocked(m)
	d.writeMu.Unlock()
	if err != nil || !written {
		return err
	}
	if d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(m)
	}
	return nil
}

// SetConversationID records a session's native conversation id captured from its
// output (Epic 11 / R-2) — the id a later resume replays. It is WRITE-ONCE: the
// first non-empty capture wins and later captures are ignored, so the id never
// flaps. The write goes through the sole meta writer under writeMu (G6, no second
// writer); an unknown or tombstoned session and an empty id are no-ops.
func (d *Daemon) SetConversationID(id, convID string) error {
	if convID == "" {
		return nil
	}
	d.writeMu.Lock()
	d.mu.Lock()
	sess, ok := d.sessions[id]
	var m persist.Meta
	if ok {
		m = sess.meta
	}
	d.mu.Unlock()
	if !ok {
		d.writeMu.Unlock()
		return fmt.Errorf("daemon: unknown session %q", id)
	}
	if m.ConversationID != "" {
		d.writeMu.Unlock()
		return nil // write-once: already captured
	}
	m.ConversationID = convID
	m.SchemaVersion = persist.SchemaVersion
	written, err := d.saveMetaLocked(m)
	d.writeMu.Unlock()
	if err != nil || !written {
		return err
	}
	if d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(m)
	}
	return nil
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

	d.writeMu.Lock()
	written, err := d.saveMetaLocked(m)
	d.writeMu.Unlock()
	if err != nil || !written {
		return err
	}
	if d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(m)
	}
	return nil
}

// saveMetaLocked persists m to disk AND publishes it to the in-memory registry as
// ONE step; the caller holds writeMu, so the on-disk write and the in-memory
// update commit together and no reader can observe disk and memory disagreeing
// (this is what lets SetStatus re-read a trustworthy live process dimension under
// writeMu, F1). The tombstone check + store.Save are under writeMu, mutually
// exclusive with Delete's tombstone-set + store.Delete, so a merge racing a Delete
// can never recreate the session dir after it is removed (F3). It returns
// written=false for a tombstoned id, leaving disk and memory untouched. Firing the
// onMetaSave observer is the caller's job, done after writeMu is released.
//
// m.Env is NOT re-filtered here (R3.1.2 dedup): store.Save is the sole allowlist
// enforcement point (persist.go, ADR-004), and every caller of saveMeta/
// saveMetaLocked already carries pre-filtered Env — freshly filtered at Meta
// construction (launch.go) or inherited unchanged from a previously-persisted,
// already-filtered Meta (reconcile scan, sess.meta). A future caller that
// constructs a Meta from a raw, unfiltered env source must filter before
// reaching here, or the unfiltered value will reach the in-memory registry
// (store.Save only protects disk, since it filters its own copy of m).
func (d *Daemon) saveMetaLocked(m persist.Meta) (written bool, err error) {
	if d.isDeleted(m.ID) {
		return false, nil // session was deleted; do not resurrect its on-disk state
	}
	if err := d.store.Save(m); err != nil {
		return false, err
	}
	d.putMem(m)
	return true, nil
}

// processRank orders the process dimension for the terminal-write precedence
// (S1): a terminal transition may only ADVANCE the rank, so exited (from the
// authoritative exit side-file) always wins over lost, lost wins over running, and
// neither can regress a more-terminal state. Two concurrent finalizers (markLost
// vs handleShimExit) thus converge on the highest-rank outcome regardless of the
// order in which they commit.
func processRank(p status.Process) int {
	switch p {
	case status.ProcessExited:
		return 2
	case status.ProcessLost:
		return 1
	default: // running (or unknown)
		return 0
	}
}

// finalizeTerminal atomically transitions a session to a terminal process state
// under writeMu, re-reading the LIVE meta so two concurrent finalizers cannot
// clobber each other (S1). compute derives the target meta from the CURRENT meta;
// the write is applied ONLY if it advances the process rank (exited > lost >
// running), so a late lost can never overwrite an authoritative exited+code and no
// finalizer regresses a more-terminal state. Returns whether it wrote, and fires
// the onMetaSave observer on a write (mirrors saveMeta).
func (d *Daemon) finalizeTerminal(id string, compute func(cur persist.Meta) persist.Meta) bool {
	d.writeMu.Lock()
	d.mu.Lock()
	sess, ok := d.sessions[id]
	var cur persist.Meta
	if ok {
		cur = sess.meta
	}
	d.mu.Unlock()
	if !ok {
		d.writeMu.Unlock()
		return false
	}
	next := compute(cur)
	if processRank(next.Status.Process) <= processRank(cur.Status.Process) {
		d.writeMu.Unlock()
		return false // would not advance the terminal state: refuse (S1)
	}
	next.SchemaVersion = persist.SchemaVersion
	written, err := d.saveMetaLocked(next)
	d.writeMu.Unlock()
	if err != nil {
		d.logf("finalize: persist terminal meta for %s: %v", id, err)
		return false
	}
	if written && d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(next)
	}
	return written
}

// isDeleted reports whether id has been tombstoned by Delete (F3).
func (d *Daemon) isDeleted(id string) bool {
	d.tombMu.Lock()
	_, ok := d.deleted[id]
	d.tombMu.Unlock()
	return ok
}

// tombstoneID marks id as deleted so no later write recreates its dir or registry
// entry (F3). Delete calls it under d.mu, together with the registry removal.
func (d *Daemon) tombstoneID(id string) {
	d.tombMu.Lock()
	d.deleted[id] = struct{}{}
	d.tombMu.Unlock()
}

// logf appends a line to the daemon log (best-effort). It surfaces reconcile and
// side-file-merge persistence errors that would otherwise be silently dropped,
// rather than letting the served registry diverge from disk unseen (F4).
func (d *Daemon) logf(format string, args ...any) {
	f, err := openDaemonLog(d.cfg.LogPath)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "daemon: "+format+"\n", args...)
	_ = f.Close()
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

// writePIDFile records the daemon's PID and its process-start-time ("PID START")
// for `swarm daemon restart`, so the stop step can verify the pidfile still names
// THIS daemon before signalling it — not a PID-reused stranger (S3, F1).
// Best-effort: if the start-time cannot be read the pidfile is skipped, which makes
// a later restart a safe no-op rather than an unverifiable signal.
func writePIDFile(stateDir string) {
	pid := os.Getpid()
	st, err := processStartTime(pid)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(stateDir, pidFileName), []byte(fmt.Sprintf("%d %d\n", pid, st)), 0o600)
}

func removePIDFile(stateDir string) {
	_ = os.Remove(filepath.Join(stateDir, pidFileName))
}
