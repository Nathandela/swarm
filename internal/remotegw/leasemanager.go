package remotegw

// A7 input Slice 4 — the LeaseManager: owns many persistent LeaseConns (Slice 3), keyed by
// namespaced session id, and ties a phone's take_control to keystroke routing. Where a
// LeaseConn is ONE connection holding ONE lease, the manager is the fan-out the gateway's
// input plane needs: many phones may control many sessions at once, each on its own lease
// conn, and every keystroke or resize for a session must ride THAT session's conn (the daemon
// binds the input gate to the take_control connection's lease). Begin opens+leases a conn;
// Input routes a frame to it; End tears one down; Close tears all down. A per-conn watcher
// removes a conn from the map the moment its lease dies (OpDetach, or the daemon/session
// closing the conn), so a stale conn never lingers to be written to.

import (
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// LeaseManager owns the set of live LeaseConns, one per session under control.
type LeaseManager struct {
	socketPath string        // daemon remote.sock every lease conn dials
	timeout    time.Duration // how long Begin waits for the OpLease grant

	mu    sync.Mutex
	conns map[string]*LeaseConn // namespaced session id -> its lease conn
}

// NewLeaseManager returns a manager whose lease conns dial socketPath and whose Begin waits
// up to awaitTimeout for each lease grant.
func NewLeaseManager(socketPath string, awaitTimeout time.Duration) *LeaseManager {
	return &LeaseManager{
		socketPath: socketPath,
		timeout:    awaitTimeout,
		conns:      make(map[string]*LeaseConn),
	}
}

// Begin dials ONE persistent lease conn for cmd.Session, forwards the phone-authored
// take_control on it, and blocks until the daemon grants the lease (or the wait times out).
// Only once the grant lands is the conn stored, keyed by session; a second Begin for a session
// already leased supersedes the old (the newly-granted conn replaces it and the prior one is
// closed). A per-conn watcher then removes the conn when its lease dies.
//
// The dial + await run WITHOUT the map lock so a slow grant never blocks Input/End/Close on
// other sessions; the lock is taken only for the brief store-and-swap once the lease is live.
func (m *LeaseManager) Begin(cmd protocol.RemoteCommand) error {
	lc, err := DialLease(m.socketPath, cmd)
	if err != nil {
		return err
	}
	if _, err := lc.AwaitLease(m.timeout); err != nil {
		_ = lc.Close()
		return err
	}
	m.mu.Lock()
	old := m.conns[cmd.Session]
	m.conns[cmd.Session] = lc
	m.mu.Unlock()
	if old != nil {
		_ = old.Close() // supersede: the new lease replaces the old; drop the stale conn
	}
	go m.watch(cmd.Session, lc)
	return nil
}

// Input routes an opened input frame to the session's lease conn: "data" writes the keystroke
// bytes, "resize" writes the new terminal size — both on the SAME connection the daemon bound
// the input gate to. Input for an unknown or ended session (no conn in the map) is DROPPED: it
// returns nil rather than writing to a closed conn, so a stray frame can never crash the
// gateway or leak onto another lease.
func (m *LeaseManager) Input(session string, f InputFrame) error {
	m.mu.Lock()
	lc := m.conns[session]
	m.mu.Unlock()
	if lc == nil {
		return nil // dropped: unknown/ended session
	}
	switch f.Kind {
	case "data":
		return lc.WriteDataIn(f.Data)
	case "resize":
		return lc.WriteResize(f.Cols, f.Rows)
	default:
		return nil // unknown kind: nothing to route
	}
}

// End closes and removes the session's lease conn: the client EOF releases the lease
// server-side (the phone's take_control_end). It is a no-op if the session holds no conn.
func (m *LeaseManager) End(session string) {
	m.mu.Lock()
	lc := m.conns[session]
	delete(m.conns, session)
	m.mu.Unlock()
	if lc != nil {
		_ = lc.Close()
	}
}

// Generation returns the captured OpLease generation of the session's lease conn (0 if the
// session holds no conn or its lease is not yet granted).
func (m *LeaseManager) Generation(session string) uint64 {
	m.mu.Lock()
	lc := m.conns[session]
	m.mu.Unlock()
	if lc == nil {
		return 0
	}
	return lc.Generation()
}

// Close tears down every lease conn the manager holds and empties the map. Each conn's close
// fires its Dead() channel, so the per-conn watchers wake and exit — no goroutine is left
// behind.
func (m *LeaseManager) Close() error {
	m.mu.Lock()
	conns := m.conns
	m.conns = make(map[string]*LeaseConn)
	m.mu.Unlock()
	for _, lc := range conns {
		_ = lc.Close()
	}
	return nil
}

// watch removes lc from the map when its lease dies. It blocks on the ONE lc.Dead() signal:
// End, a supersede, and Close all close the conn (which fires Dead), so the watcher is
// guaranteed to wake and return — it never leaks. The identity check (conns[session]==lc)
// makes it a no-op when a supersede has already swapped in a newer conn for the session, so a
// dying old conn never evicts its live replacement.
func (m *LeaseManager) watch(session string, lc *LeaseConn) {
	<-lc.Dead()
	m.mu.Lock()
	if m.conns[session] == lc {
		delete(m.conns, session)
	}
	m.mu.Unlock()
}
