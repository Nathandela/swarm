package phonecore

// MachineClient/MachineManager is the per-pairing client seam and its plural registry.

import (
	"errors"
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

// CoreMachineClient wraps one independently namespaced Core and exposes it unchanged as a
// MachineClient. Start/Stop/Running never touch Core itself -- Core has no Close (see
// core.go), so nothing destructive happens on either transition -- but Stop tells the
// owning registry relay to stop forwarding this client's events (see stopped, below).
// Both Start and Stop are idempotent so a caller never has to track whether it already
// called one.
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
