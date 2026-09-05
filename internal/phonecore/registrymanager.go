package phonecore

// RegistryManager is the N-entry MachineManager (ADR-018 MM3, wave R4). It owns the
// lifecycle of one MachineClient per registered pairing, relays every client's
// events onto ONE aggregate stream qualified with its machine id (MM3), and arbitrates
// the foreground connection cap with a deterministic least-recently-viewed policy
// (playbook 4.2: "Connections beyond the cap use a deterministic least-recently-viewed
// policy and visibly show their last-sync age").
//
// Determinism is by construction, not by clock: every Add and every MarkViewed advances
// one monotonic stamp, and the connected set is exactly the Cap highest-stamped
// clients. The injected clock orders nothing -- under a frozen clock the same view
// history still produces the same connected set -- it exists so the durable last-sync
// facts a row renders are read against an instant a test controls.

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ManagerOptions configures a RegistryManager: the documented foreground connection cap
// and the injected clock.
type ManagerOptions struct {
	Cap int
	Now func() time.Time
}

// MachineRow is one immutable row of the aggregate machines snapshot (playbook :574:
// "immutable snapshots from MachineManager, not mutable singleton globals"). A parked
// row is Stale with its durable LastSyncUnixMs; a connected row is never Stale.
type MachineRow struct {
	ID             string
	DisplayName    string
	Connected      bool
	Stale          bool
	LastSyncUnixMs int64
}

// managedClient is the manager's bookkeeping for one pairing.
type managedClient struct {
	d MachineDescriptor
	c MachineClient
	// stamp is the client's most-recent view (or registration) in the manager's
	// monotonic sequence -- the ONLY input the arbitration policy takes.
	stamp uint64
	// viewedAt is the clock reading of the last MarkViewed, informational beside stamp.
	viewedAt time.Time
	// lastSyncMs is the durable last-successful-sync instant RecordSync stored, the
	// fact a parked row's visible age is computed from.
	lastSyncMs int64
}

// RegistryManager implements MachineManager over a live MachineRegistry.
type RegistryManager struct {
	reg *MachineRegistry
	cap int
	now func() time.Time

	mu      sync.Mutex
	clients map[string]*managedClient
	order   []string // registration order, for List
	stamp   uint64
	closed  bool

	events chan MachineEvent
	done   chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
}

var _ MachineManager = (*RegistryManager)(nil)

// NewRegistryManager constructs the manager over reg. Cap must be at least 1 -- a
// manager that may hold no connection at all cannot serve a foregrounded app -- and Now
// defaults to time.Now. Callers must Close it, or the closer goroutine never runs.
func NewRegistryManager(reg *MachineRegistry, opts ManagerOptions) (*RegistryManager, error) {
	if reg == nil {
		return nil, errors.New("phonecore: NewRegistryManager requires a live MachineRegistry")
	}
	if opts.Cap < 1 {
		return nil, fmt.Errorf("phonecore: connection cap %d; the documented cap is at least 1", opts.Cap)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &RegistryManager{
		reg:     reg,
		cap:     opts.Cap,
		now:     now,
		clients: map[string]*managedClient{},
		events:  make(chan MachineEvent),
		done:    make(chan struct{}),
	}, nil
}

// Registry is the durable registry this manager arbitrates over.
func (m *RegistryManager) Registry() *MachineRegistry { return m.reg }

// Add registers client c for pairing d and starts it if the connection cap allows,
// parking the least-recently-viewed connection otherwise. Every event c emits is
// relayed onto the aggregate stream qualified with d.ID (MM3), so two machines serving
// the SAME session id stay two identities (MM4).
func (m *RegistryManager) Add(d MachineDescriptor, c MachineClient) error {
	if err := validMachineID(d.ID); err != nil {
		return err
	}
	if c == nil {
		return errors.New("phonecore: Add requires a MachineClient")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("phonecore: manager is closed")
	}
	if _, dup := m.clients[d.ID]; dup {
		return fmt.Errorf("phonecore: machine %q is already managed", d.ID)
	}
	m.stamp++
	m.clients[d.ID] = &managedClient{d: d, c: c, stamp: m.stamp}
	m.order = append(m.order, d.ID)

	m.wg.Add(1)
	go m.relay(d.ID, c)
	m.arbitrateLocked()
	return nil
}

// stopAware is the OPTIONAL client surface relay consults so a PARKED client's events
// never reach the aggregate stream. CoreMachineClient implements both: stopped covers
// events not yet dequeued, and stopSignal abandons one parked mid-send. A double that
// does not keeps the forward-everything shape.
type stopAware interface {
	stopped() bool
	stopSignal() <-chan struct{}
}

// relay forwards one client's events onto the aggregate stream, machine-qualified. It
// exits when the manager closes or the client's channel does. A Stop()-parked client's
// events are dropped, not forwarded: parking must bound the client's event flow, not
// merely its Running() boolean.
func (m *RegistryManager) relay(id string, c MachineClient) {
	defer m.wg.Done()
	sa, _ := c.(stopAware)
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				return
			}
			if sa != nil && sa.stopped() {
				continue
			}
			// A nil stop channel blocks forever, which is exactly the pre-stopAware
			// behavior for clients that cannot signal.
			var stop <-chan struct{}
			if sa != nil {
				stop = sa.stopSignal()
			}
			select {
			case m.events <- MachineEvent{MachineID: id, Event: e}:
			case <-m.done:
				return
			case <-stop:
				continue
			}
		case <-m.done:
			return
		}
	}
}

// Remove forgets pairing id: the client is stopped, the relay retires, and the durable
// registry row and namespace go with it (MM7's forget arm -- exactly one namespace).
func (m *RegistryManager) Remove(id string) error {
	m.mu.Lock()
	mc, ok := m.clients[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("phonecore: remove %q: %w", id, ErrMachineNotFound)
	}
	// Stop the removed client BEFORE arbitration can promote a parked successor: the
	// cap is a HARD bound, and stop-after-promote holds Cap+1 live connections in
	// between the two.
	stopErr := mc.c.Stop()
	regErr := m.reg.RemoveMachine(id)
	if errors.Is(regErr, ErrMachineNotFound) {
		// A registry that never held the row (a client managed ahead of registration) is
		// not a failed forget.
		regErr = nil
	}
	// A pre-rename registry failure leaves the pairing durable, so retain its client,
	// order and cap arbitration. A post-rename failure has already made the durable
	// forget authoritative; only then may this manager hide the pairing.
	if regErr == nil || atomicWriteCommitted(regErr) {
		delete(m.clients, id)
		for i, o := range m.order {
			if o == id {
				m.order = append(m.order[:i:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.arbitrateLocked()
	m.mu.Unlock()
	return errors.Join(stopErr, regErr)
}

// List returns the managed descriptors in registration order.
func (m *RegistryManager) List() []MachineDescriptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MachineDescriptor, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.clients[id].d)
	}
	return out
}

// Select resolves id to its client, or ErrMachineNotFound.
func (m *RegistryManager) Select(id string) (MachineClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mc, ok := m.clients[id]
	if !ok {
		return nil, fmt.Errorf("phonecore: select %q: %w", id, ErrMachineNotFound)
	}
	return mc.c, nil
}

// Events is the aggregate event stream (MM3): every event machine-qualified, identity
// always the tuple (machine_id, session_id) (MM4).
func (m *RegistryManager) Events() <-chan MachineEvent { return m.events }

// ConnectionCap is the documented foreground connection cap.
func (m *RegistryManager) ConnectionCap() int { return m.cap }

// Close stops every client and retires the relays. Idempotent. The aggregate channel is
// closed once every relay has exited, so a ranging consumer terminates.
func (m *RegistryManager) Close() error {
	m.once.Do(func() {
		m.mu.Lock()
		m.closed = true
		clients := make([]*managedClient, 0, len(m.clients))
		for _, mc := range m.clients {
			clients = append(clients, mc)
		}
		m.mu.Unlock()
		close(m.done)
		for _, mc := range clients {
			_ = mc.c.Stop()
		}
		go func() {
			m.wg.Wait()
			close(m.events)
		}()
	})
	return nil
}

// MarkViewed records that the user looked at machine id -- the ONLY input the
// arbitration policy takes -- and re-arbitrates the connected set.
func (m *RegistryManager) MarkViewed(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mc, ok := m.clients[id]
	if !ok || m.closed {
		return
	}
	m.stamp++
	mc.stamp = m.stamp
	mc.viewedAt = m.now()
	m.arbitrateLocked()
}

// ConnectedIDs are the pairings holding a live connection right now, sorted by id.
// Never more than Cap.
func (m *RegistryManager) ConnectedIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for id, mc := range m.clients {
		if mc.c.Running() {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// RecordSync is the connection loop's last-successful-sync hook: the durable fact a
// parked row's visible age is computed from.
func (m *RegistryManager) RecordSync(id string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mc, ok := m.clients[id]; ok {
		mc.lastSyncMs = at.UnixMilli()
	}
}

// Rows is the immutable snapshot the aggregate UI reads, sorted by machine id. A parked
// row is Stale with its durable last-sync instant; a connected row is never Stale.
// Staleness is a pure function of durable facts, never of call timing: two reads under
// a frozen clock are identical.
func (m *RegistryManager) Rows() []MachineRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]MachineRow, 0, len(ids))
	for _, id := range ids {
		mc := m.clients[id]
		connected := mc.c.Running()
		out = append(out, MachineRow{
			ID:             id,
			DisplayName:    mc.d.DisplayName,
			Connected:      connected,
			Stale:          !connected,
			LastSyncUnixMs: mc.lastSyncMs,
		})
	}
	return out
}

// arbitrateLocked enforces the cap: the connected set is exactly the Cap
// highest-stamped clients. Demotions run BEFORE promotions, so the number of live
// connections never exceeds Cap even between arbitrations. Caller holds m.mu.
func (m *RegistryManager) arbitrateLocked() {
	ids := make([]string, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	// Highest stamp first; stamps are unique, so the order -- and therefore the
	// connected set -- is fully determined by the view history.
	sort.Slice(ids, func(i, j int) bool { return m.clients[ids[i]].stamp > m.clients[ids[j]].stamp })

	keep := map[string]bool{}
	for i, id := range ids {
		if i < m.cap {
			keep[id] = true
		}
	}
	for _, id := range ids {
		if mc := m.clients[id]; !keep[id] && mc.c.Running() {
			_ = mc.c.Stop()
		}
	}
	if m.closed {
		return
	}
	for _, id := range ids {
		if mc := m.clients[id]; keep[id] && !mc.c.Running() {
			_ = mc.c.Start()
		}
	}
}
