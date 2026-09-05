package phonecore

// MachineClient/MachineManager is the per-pairing client seam and its plural registry.

import (
	"errors"
	"fmt"
	"sync"
)

// Event is one core-sourced notification. It deliberately mirrors mobile.Event's field set
// (mobile/types.go) field-for-field, so MachineManager's aggregate stream can relay it
// unchanged and add only the MachineID qualifier -- see MachineEvent.
type Event struct {
	Kind      string
	Stream    string
	SessionID string
	State     string
	Message   string
	Cursor    int64
	Dropped   int
}

// MachineClient is one pairing entire (ADR-018 MM2): its key store, relay destination,
// cursors, read models, connection loop, command sequencer and operation journal -- all of
// which already live on *Core, which is why Core returns the exact pointer rather than a
// reimplemented read model.
type MachineClient interface {
	ID() string
	Start() error
	Stop() error
	Running() bool
	Core() *Core
	Events() <-chan Event
}

// clientState is CoreMachineClient's lifecycle: distinct from the "running bool" the
// public Running() reports because relay's Stop contract (below) needs to tell "never
// started" (still relay: nothing has been stopped) apart from "explicitly stopped" (stop
// relaying), and both collapse to the same false if kept as one flag.
type clientState int

const (
	clientNeverStarted clientState = iota
	clientRunning
	clientStopped
)

// MachineDescriptor is one registry row (ADR-018 MM3).
type MachineDescriptor struct {
	ID          string
	DisplayName string
}

// MachineEvent is one Event qualified with the pairing it came from -- ADR-018 MM3's
// "every event on the aggregate stream is machine-qualified".
type MachineEvent struct {
	MachineID string
	Event
}

// MachineManager is the only plural object (ADR-018 MM3): the durable registry of machine
// descriptors, lifecycle orchestration of the clients, one aggregate event stream, the
// foreground connection cap, and add/remove/select.
type MachineManager interface {
	List() []MachineDescriptor
	Select(id string) (MachineClient, error)
	Add(d MachineDescriptor, c MachineClient) error
	Remove(id string) error
	Events() <-chan MachineEvent
	ConnectionCap() int
	Close() error
}

// ErrMachineNotFound is Select's refusal for an id the registry does not hold.
var ErrMachineNotFound = errors.New("phonecore: no machine registered with that id")

// ErrMultiMachineNotImplemented remains only for the blocked historical
// SingleMachineManager implementation. RegistryManager is the v2 implementation.
var ErrMultiMachineNotImplemented = errors.New("phonecore: multi-machine registry mutation is not implemented")

// CoreMachineClient wraps one independently namespaced Core.
// unchanged as a MachineClient. Start/Stop/Running never touch Core itself -- Core has no
// Close (see core.go), so nothing destructive happens on either transition -- but Stop does
// have one real effect: it tells the owning registry relay to stop forwarding this client's
// events (see stopped, below). Both Start and Stop are idempotent so a caller
// never has to track whether it already called one.
type CoreMachineClient struct {
	id     string
	core   *Core
	events <-chan Event

	mu     sync.Mutex
	state  clientState
	stopCh chan struct{} // closed by Stop, reset to nil by Start; see stopSignal.
}

// NewCoreMachineClient constructs a client wrapping core under id, relaying events
// unchanged from the given channel.
func NewCoreMachineClient(id string, core *Core, events <-chan Event) *CoreMachineClient {
	return &CoreMachineClient{id: id, core: core, events: events}
}

// SingleMachineAdapter is a temporary source alias pending removal of the obsolete
// compatibility manager and its historical tests. New v2 code must use
// NewCoreMachineClient and RegistryManager.
type SingleMachineAdapter = CoreMachineClient

// NewSingleMachineAdapter is kept only so the historical compatibility tests continue
// compiling until their blocked manager deletion can be completed.
func NewSingleMachineAdapter(id string, core *Core, events <-chan Event) *CoreMachineClient {
	return NewCoreMachineClient(id, core, events)
}

// ID is the pairing's identity.
func (a *CoreMachineClient) ID() string { return a.id }

// Core returns the EXACT *Core pointer this adapter was constructed with -- never a clone.
func (a *CoreMachineClient) Core() *Core { return a.core }

// Events is the adapter's own event stream, unchanged from construction.
func (a *CoreMachineClient) Events() <-chan Event { return a.events }

// Start marks the adapter running. Idempotent, and reverses a prior Stop: the manager's
// relay resumes forwarding this adapter's events once Start runs again. When undoing a
// completed Stop it retires that cycle's stop signal, so a relay that later parks on
// stopSignal() waits on a fresh, unclosed channel.
func (a *CoreMachineClient) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Retire the stop channel ONLY when undoing a completed Stop. A redundant Start on a
	// running adapter must not orphan a channel the relay may already be parked on: nilling
	// it here would make the NEXT Stop close a fresh channel nobody waits on, delivering a
	// parked event after Stop returned (the fail-open class the stop signal exists to close).
	if a.state == clientStopped {
		a.stopCh = nil
	}
	a.state = clientRunning
	return nil
}

// Stop marks the adapter stopped and, if this call is the one making the transition, closes
// the current stop signal so a relay already parked mid-send (see relay's inner select)
// abandons that in-flight event rather than delivering it after Stop has returned -- not just
// events not yet dequeued, which the outer stopped() check alone would catch. Idempotent: a
// second Stop is a no-op rather than a double-close panic.
func (a *CoreMachineClient) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == clientStopped {
		return nil
	}
	a.state = clientStopped
	if a.stopCh == nil {
		a.stopCh = make(chan struct{})
	}
	close(a.stopCh)
	return nil
}

// Running reports whether Start has run more recently than Stop.
func (a *CoreMachineClient) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state == clientRunning
}

// stopped reports whether Stop has been explicitly called and not since reversed by Start --
// distinct from Running()==false, which is also true of the never-started state where the
// manager's relay must still forward. Package-private: only the registry relay needs this
// half of the state.
func (a *CoreMachineClient) stopped() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state == clientStopped
}

// stopSignal returns the channel Stop closes, lazily creating it if no Stop cycle has
// started one yet. relay reads it as a third case alongside an in-flight send (see relay),
// so a send already parked when Stop runs is abandoned instead of delivered late. Reading
// this once per send attempt -- never caching the channel across attempts -- is what makes a
// later Start's fresh channel (see Start) take effect for the next event relay tries to send.
func (a *CoreMachineClient) stopSignal() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopCh == nil {
		a.stopCh = make(chan struct{})
	}
	return a.stopCh
}

// SingleMachineManager is retained temporarily only for source compatibility while callers
// move to RegistryManager. Production v2 construction uses RegistryManager exclusively.
// It holds exactly one
// CoreMachineClient and relays that client's events onto the
// aggregate stream, qualified with its machine id.
//
// It takes the concrete *SingleMachineAdapter, not a MachineClient, because relay needs
// adapter's private stopped() half of the lifecycle state (see stopped's doc) that
// MachineClient deliberately does not expose -- callers of the interface only ever need
// Running(). That is fine while Add/Remove refuse behind ErrMultiMachineNotImplemented, so no
// other MachineClient can ever reach this constructor; MM6/R4's real registry is what makes
// this join generic.
type SingleMachineManager struct {
	descriptor MachineDescriptor
	adapter    *CoreMachineClient
	events     chan MachineEvent

	done      chan struct{}
	closeOnce sync.Once
}

// NewSingleMachineManager constructs a manager around adapter, displayed as displayName, and
// starts relaying adapter's events onto the aggregate stream. Callers must call Close when
// done with the manager, or the relay goroutine runs forever.
func NewSingleMachineManager(displayName string, adapter *CoreMachineClient) *SingleMachineManager {
	m := &SingleMachineManager{
		descriptor: MachineDescriptor{ID: adapter.ID(), DisplayName: displayName},
		adapter:    adapter,
		events:     make(chan MachineEvent),
		done:       make(chan struct{}),
	}
	go m.relay()
	return m
}

// relay forwards the sole adapter's events onto the aggregate stream unchanged, qualified
// with its machine id (MM3). Once the adapter is Stop()ed its events are drained but not
// forwarded; an event already dequeued and parked in the inner send is abandoned via the
// stopSignal() case, so Stop's guarantee covers in-flight events too. relay exits when done
// closes, and closes m.events on every exit path so a ranging consumer terminates.
func (m *SingleMachineManager) relay() {
	defer close(m.events)
	for {
		select {
		case e, ok := <-m.adapter.Events():
			if !ok {
				return
			}
			if m.adapter.stopped() {
				continue
			}
			select {
			case m.events <- MachineEvent{MachineID: m.descriptor.ID, Event: e}:
			case <-m.done:
				return
			case <-m.adapter.stopSignal():
				continue
			}
		case <-m.done:
			return
		}
	}
}

// Close stops the relay goroutine. Idempotent; safe to call more than once or concurrently.
// It does not touch the wrapped adapter or Core -- Close is the manager's own shutdown, not
// the client's (that is Stop).
func (m *SingleMachineManager) Close() error {
	m.closeOnce.Do(func() { close(m.done) })
	return nil
}

// List returns the sole compatibility-adapter entry.
func (m *SingleMachineManager) List() []MachineDescriptor {
	return []MachineDescriptor{m.descriptor}
}

// Select resolves id to the wrapped adapter, or ErrMachineNotFound for anything else.
func (m *SingleMachineManager) Select(id string) (MachineClient, error) {
	if id != m.descriptor.ID {
		return nil, fmt.Errorf("phonecore: select %q: %w", id, ErrMachineNotFound)
	}
	return m.adapter, nil
}

// Add always refuses: there is nowhere to put a second pairing before MM6/R4.
func (m *SingleMachineManager) Add(MachineDescriptor, MachineClient) error {
	return ErrMultiMachineNotImplemented
}

// Remove always refuses: removing the sole entry is equally undefined before MM6/R4.
func (m *SingleMachineManager) Remove(string) error {
	return ErrMultiMachineNotImplemented
}

// Events is the aggregate event stream (MM3).
func (m *SingleMachineManager) Events() <-chan MachineEvent { return m.events }

// ConnectionCap is fixed at 1: the compatibility adapter can never exceed one live
// connection, so there is nothing to arbitrate.
func (m *SingleMachineManager) ConnectionCap() int { return 1 }
