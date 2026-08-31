// Package contextguard contains the provider-independent, side-effect-free
// policy for ContextGuard auto-compaction. It deliberately has no dependency on
// adapters, persistence, or daemon workers: callers apply returned Decisions.
package contextguard

import (
	"errors"
	"math/bits"
	"time"
)

const (
	DefaultThreshold          = 80
	MinThreshold              = 40
	MaxThreshold              = 95
	RearmGapPercentagePoints  = 10
	MaxObservationDispatchAge = 30 * time.Second
)

// Config is the daemon-global, revisioned setting snapshot.
type Config struct {
	Enabled   bool
	Threshold int
	Revision  uint64
}

// DefaultConfig is the disabled-by-default product setting.
func DefaultConfig() Config { return Config{Threshold: DefaultThreshold} }

// Validate refuses thresholds outside the product contract.
func (c Config) Validate() error {
	if c.Threshold < MinThreshold || c.Threshold > MaxThreshold {
		return errors.New("contextguard: threshold must be in [40,95]")
	}
	return nil
}

// Key binds an observation/action cycle to its provider provenance.
type Key struct {
	SessionID        string
	SessionInstance  string
	BackendEpoch     string
	ProviderThreadID string
}

func (k Key) valid() bool {
	return k.SessionID != "" && k.SessionInstance != "" && k.BackendEpoch != "" && k.ProviderThreadID != ""
}

// Quality says whether the provider has supplied exact occupancy telemetry.
type Quality string

const (
	QualityExact         Quality = "exact"
	QualityCharacterized Quality = "characterized"
	QualityUnavailable   Quality = "unavailable"
)

// Observation is the complete provenance-bound input required to consider a
// provider action. ContextLimit is provider-authored and must be positive.
type Observation struct {
	Key              Key
	SettingsRevision uint64
	IngestSequence   uint64
	ObservedAt       time.Time
	UsedTokens       uint64
	ContextLimit     uint64
	Quality          Quality
}

// State is a durable policy phase. Only the worker performs I/O; this package
// chooses the state and reports the requested effect.
type State string

const (
	StateDisabled             State = "disabled"
	StateUnsupported          State = "unsupported"
	StateArmed                State = "armed"
	StatePendingIdle          State = "pending_idle"
	StatePrepared             State = "prepared"
	StateExecuting            State = "executing"
	StateAwaitingConfirmation State = "awaiting_confirmation"
	StateProviderCompacting   State = "provider_compacting"
	StateLatched              State = "latched"
	StateOutcomeUnknownHold   State = "outcome_unknown_hold"
	StateEventLossHold        State = "event_loss_hold"
	StateBlockedCorrupt       State = "blocked_corrupt"
)

func (s State) valid() bool {
	switch s {
	case StateDisabled, StateUnsupported, StateArmed, StatePendingIdle, StatePrepared,
		StateExecuting, StateAwaitingConfirmation, StateProviderCompacting, StateLatched,
		StateOutcomeUnknownHold, StateEventLossHold, StateBlockedCorrupt:
		return true
	default:
		return false
	}
}

// Machine is the complete durable policy state. LastObservation is retained in
// memory for revalidation but callers must not persist it; Recover always drops it.
type Machine struct {
	Config Config
	Key    Key
	State  State

	LastObservation   *Observation
	TriggerThreshold  int
	SettingsChangedAt time.Time
	FreshAfter        time.Time
	ProviderSupported bool
	BackendChangedAt  time.Time
	InstanceChangedAt time.Time
	// LastSourceSequence fences the one provider source/feed across exact
	// telemetry and external provider lifecycle edges. A new backend epoch or a
	// replacement session instance is the only reset boundary.
	LastSourceSequence uint64
}

// New constructs an initial cycle. Config{Threshold: 0} means the documented
// default and is useful for a missing settings document.
func New(config Config, key Key) (Machine, error) {
	if config.Threshold == 0 {
		config.Threshold = DefaultThreshold
	}
	if err := config.Validate(); err != nil {
		return Machine{}, err
	}
	if !key.valid() {
		return Machine{}, errors.New("contextguard: incomplete key")
	}
	state := StateArmed
	if !config.Enabled {
		state = StateDisabled
	}
	return Machine{Config: config, Key: key, State: state, ProviderSupported: true}, nil
}

// EventKind identifies a serialized input to Reduce.
type EventKind string

const (
	EventObservation                 EventKind = "observation"
	EventConfigChanged               EventKind = "config_changed"
	EventProviderUnsupported         EventKind = "provider_unsupported"
	EventProviderSupported           EventKind = "provider_supported"
	EventSessionIdle                 EventKind = "session_idle"
	EventSessionBusy                 EventKind = "session_busy"
	EventDispatchStarted             EventKind = "dispatch_started"
	EventDispatchNoSideEffect        EventKind = "dispatch_no_side_effect"
	EventActionWritten               EventKind = "action_written"
	EventActionOutcomeUnknown        EventKind = "action_outcome_unknown"
	EventProviderCompactionStarted   EventKind = "provider_compaction_started"
	EventProviderCompactionCompleted EventKind = "provider_compaction_completed"
	EventEventLoss                   EventKind = "event_loss"
	EventBackendEpochChanged         EventKind = "backend_epoch_changed"
	EventNewInstance                 EventKind = "new_instance"
	EventRecovery                    EventKind = "recovery"
)

// Event has an explicit dispatch-time At so observation freshness remains a
// deterministic policy input. Key is used for replacement/epoch events.
type Event struct {
	Kind        EventKind
	At          time.Time
	Observation Observation
	Config      Config
	Key         Key
	// SourceSequence orders external provider lifecycle edges. It shares the
	// source/feed cursor with Observation.IngestSequence and is scoped by Key.
	SourceSequence uint64
	Corrupt        bool // meaningful only for EventRecovery
}

// RejectReason tells a caller why no state or observable effect was produced.
type RejectReason string

const (
	RejectNone              RejectReason = ""
	RejectInvalidEvent      RejectReason = "invalid_event"
	RejectWrongKey          RejectReason = "wrong_key"
	RejectSettingsRevision  RejectReason = "settings_revision"
	RejectSourceSequence    RejectReason = "source_sequence"
	RejectQuality           RejectReason = "quality"
	RejectMalformedLimit    RejectReason = "malformed_limit"
	RejectStaleObservation  RejectReason = "stale_observation"
	RejectInvalidConfig     RejectReason = "invalid_config"
	RejectInvalidTransition RejectReason = "invalid_transition"
)

// Decision lets the daemon shell persist/publish/dispatch without policy I/O.
// RequestDispatch is emitted only for Prepared, before a provider write boundary.
type Decision struct {
	Persist         bool
	Publish         bool
	RequestDispatch bool
	Rejected        RejectReason
}

func accepted() Decision               { return Decision{Publish: true} }
func durable() Decision                { return Decision{Persist: true, Publish: true} }
func rejected(r RejectReason) Decision { return Decision{Rejected: r} }

// Reduce applies one serialized input. It never mutates its Machine argument and
// makes no I/O. A rejected event returns the original Machine unchanged.
func Reduce(machine Machine, event Event) (next Machine, decision Decision) {
	if !machine.State.valid() || !machine.Key.valid() || machine.Config.Validate() != nil {
		return machine, rejected(RejectInvalidEvent)
	}
	if eventRequiresCurrentKey(event.Kind) && event.Key != machine.Key {
		return machine, rejected(RejectWrongKey)
	}
	providerEdge := eventRequiresSourceSequence(event.Kind)
	if providerEdge && (event.SourceSequence == 0 || event.SourceSequence <= machine.LastSourceSequence) {
		return machine, rejected(RejectSourceSequence)
	}
	defer func() {
		if providerEdge && decision.Rejected == RejectNone {
			next.LastSourceSequence = event.SourceSequence
		}
	}()
	switch event.Kind {
	case EventObservation:
		return reduceObservation(machine, event)
	case EventConfigChanged:
		return reduceConfig(machine, event)
	case EventProviderUnsupported:
		return reduceUnsupported(machine)
	case EventProviderSupported:
		return reduceSupported(machine)
	case EventSessionIdle:
		if machine.State != StatePendingIdle {
			return machine, rejected(RejectInvalidTransition)
		}
		if !hasFreshObservation(machine, event.At) {
			return machine, rejected(RejectStaleObservation)
		}
		machine.State = StatePrepared
		d := durable()
		d.RequestDispatch = true
		return machine, d
	case EventSessionBusy:
		if machine.State != StatePrepared {
			return machine, rejected(RejectInvalidTransition)
		}
		machine.State = StatePendingIdle
		return machine, durable()
	case EventDispatchStarted:
		if machine.State != StatePrepared || !machine.Config.Enabled {
			return machine, rejected(RejectInvalidTransition)
		}
		if !hasFreshObservation(machine, event.At) {
			return machine, rejected(RejectStaleObservation)
		}
		machine.State = StateExecuting
		return machine, durable()
	case EventDispatchNoSideEffect:
		if machine.State != StateExecuting {
			return machine, rejected(RejectInvalidTransition)
		}
		return requireFresh(machine)
	case EventActionWritten:
		if machine.State != StateExecuting {
			return machine, rejected(RejectInvalidTransition)
		}
		machine.State = StateAwaitingConfirmation
		return machine, durable()
	case EventActionOutcomeUnknown:
		if machine.State != StateExecuting && machine.State != StateAwaitingConfirmation {
			return machine, rejected(RejectInvalidTransition)
		}
		machine.State = StateOutcomeUnknownHold
		return machine, durable()
	case EventProviderCompactionStarted:
		if event.At.IsZero() {
			return machine, rejected(RejectInvalidEvent)
		}
		return reduceProviderStarted(machine)
	case EventProviderCompactionCompleted:
		if machine.State != StateProviderCompacting || event.At.IsZero() {
			return machine, rejected(RejectInvalidTransition)
		}
		machine.State = StateLatched
		machine.FreshAfter = event.At
		return machine, durable()
	case EventEventLoss:
		if event.At.IsZero() {
			return machine, rejected(RejectInvalidEvent)
		}
		machine.State = StateEventLossHold
		return machine, durable()
	case EventBackendEpochChanged:
		return reduceBackendEpoch(machine, event)
	case EventNewInstance:
		return reduceNewInstance(machine, event)
	case EventRecovery:
		return recover(machine, event.Corrupt)
	default:
		return machine, rejected(RejectInvalidEvent)
	}
}

func reduceObservation(machine Machine, event Event) (Machine, Decision) {
	o := event.Observation
	if o.Key != machine.Key {
		return machine, rejected(RejectWrongKey)
	}
	if o.SettingsRevision != machine.Config.Revision {
		return machine, rejected(RejectSettingsRevision)
	}
	if o.IngestSequence == 0 || o.IngestSequence <= machine.LastSourceSequence {
		return machine, rejected(RejectSourceSequence)
	}
	if o.Quality != QualityExact {
		return machine, rejected(RejectQuality)
	}
	if o.ContextLimit == 0 {
		return machine, rejected(RejectMalformedLimit)
	}
	if event.At.IsZero() || o.ObservedAt.IsZero() ||
		(!machine.SettingsChangedAt.IsZero() && !o.ObservedAt.After(machine.SettingsChangedAt)) ||
		(!machine.FreshAfter.IsZero() && !o.ObservedAt.After(machine.FreshAfter)) ||
		o.ObservedAt.After(event.At) || event.At.Sub(o.ObservedAt) > MaxObservationDispatchAge {
		return machine, rejected(RejectStaleObservation)
	}
	machine.LastObservation = &o
	machine.LastSourceSequence = o.IngestSequence

	if !machine.Config.Enabled || machine.State == StateDisabled || machine.State == StateUnsupported || isHold(machine.State) {
		return machine, accepted()
	}
	if machine.State == StateArmed && atOrAbove(o.UsedTokens, o.ContextLimit, machine.Config.Threshold) {
		machine.State = StatePendingIdle
		machine.TriggerThreshold = machine.Config.Threshold
	}
	if machine.State == StateLatched && atOrBelow(o.UsedTokens, o.ContextLimit, rearmThreshold(machine.TriggerThreshold, machine.Config.Threshold)) {
		machine.State = StateArmed
		machine.TriggerThreshold = 0
		machine.FreshAfter = time.Time{}
	}
	return machine, accepted()
}

func reduceConfig(machine Machine, event Event) (Machine, Decision) {
	c := event.Config
	if c.Threshold == 0 {
		c.Threshold = DefaultThreshold
	}
	if c.Validate() != nil || c.Revision <= machine.Config.Revision || event.At.IsZero() || (!machine.SettingsChangedAt.IsZero() && !event.At.After(machine.SettingsChangedAt)) {
		return machine, rejected(RejectInvalidConfig)
	}
	machine.Config = c
	machine.LastObservation = nil
	machine.SettingsChangedAt = event.At
	// A revision supersedes every pre-write decision. The next dispatch must be
	// driven by a new exact observation tagged with this revision, never by a
	// pending/prepared decision made from the replaced settings.
	if isPreWrite(machine.State) {
		machine.State = stateForMachine(machine)
		machine.TriggerThreshold = 0
	}
	return machine, durable()
}

func reduceUnsupported(machine Machine) (Machine, Decision) {
	if isHold(machine.State) || machine.State == StateLatched || machine.State == StateProviderCompacting || machine.State == StateExecuting || machine.State == StateAwaitingConfirmation {
		return machine, rejected(RejectInvalidTransition)
	}
	machine.ProviderSupported = false
	machine.State = stateForMachine(machine)
	machine.LastObservation = nil
	return machine, durable()
}

func reduceSupported(machine Machine) (Machine, Decision) {
	if machine.ProviderSupported || (machine.State != StateUnsupported && machine.State != StateDisabled) {
		return machine, rejected(RejectInvalidTransition)
	}
	machine.ProviderSupported = true
	machine.State = stateForMachine(machine)
	machine.LastObservation = nil
	return machine, durable()
}

func reduceProviderStarted(machine Machine) (Machine, Decision) {
	if machine.State == StateBlockedCorrupt || machine.State == StateEventLossHold || machine.State == StateOutcomeUnknownHold {
		return machine, rejected(RejectInvalidTransition)
	}
	machine.State = StateProviderCompacting
	return machine, durable()
}

func reduceBackendEpoch(machine Machine, event Event) (Machine, Decision) {
	if !event.Key.valid() || event.At.IsZero() || (!machine.BackendChangedAt.IsZero() && !event.At.After(machine.BackendChangedAt)) || event.Key.SessionID != machine.Key.SessionID || event.Key.SessionInstance != machine.Key.SessionInstance || event.Key.ProviderThreadID != machine.Key.ProviderThreadID || event.Key.BackendEpoch == machine.Key.BackendEpoch {
		return machine, rejected(RejectWrongKey)
	}
	machine.Key = event.Key
	machine.BackendChangedAt = event.At
	machine.LastSourceSequence = 0
	machine.LastObservation = nil
	machine.FreshAfter = time.Time{}
	if machine.State == StateExecuting || machine.State == StateAwaitingConfirmation {
		machine.State = StateOutcomeUnknownHold
	} else if machine.State == StatePendingIdle || machine.State == StatePrepared {
		machine.State = stateForMachine(machine)
		machine.TriggerThreshold = 0
	}
	return machine, durable()
}

func reduceNewInstance(machine Machine, event Event) (Machine, Decision) {
	if !event.Key.valid() || event.At.IsZero() || (!machine.InstanceChangedAt.IsZero() && !event.At.After(machine.InstanceChangedAt)) || event.Key.SessionID != machine.Key.SessionID || event.Key.SessionInstance == machine.Key.SessionInstance {
		return machine, rejected(RejectWrongKey)
	}
	machine.Key = event.Key
	machine.InstanceChangedAt = event.At
	machine.LastSourceSequence = 0
	machine.LastObservation = nil
	machine.TriggerThreshold = 0
	machine.SettingsChangedAt = event.At
	machine.FreshAfter = time.Time{}
	// Lifecycle events from the prior instance are rejected by Key matching; a
	// replacement begins a genuinely independent cycle.
	machine.State = stateForMachine(machine)
	return machine, durable()
}

func recover(machine Machine, corrupt bool) (Machine, Decision) {
	machine.LastObservation = nil
	machine.FreshAfter = time.Time{}
	if corrupt || machine.State == StateBlockedCorrupt || !machine.State.valid() {
		machine.State = StateBlockedCorrupt
		return machine, durable()
	}
	switch machine.State {
	case StateArmed, StatePendingIdle, StatePrepared, StateDisabled, StateUnsupported:
		machine.State = stateForMachine(machine)
		machine.TriggerThreshold = 0
	case StateExecuting, StateAwaitingConfirmation:
		machine.State = StateOutcomeUnknownHold
	}
	return machine, durable()
}

func requireFresh(machine Machine) (Machine, Decision) {
	machine.State = stateForMachine(machine)
	machine.LastObservation = nil
	machine.TriggerThreshold = 0
	return machine, durable()
}

func eventRequiresCurrentKey(kind EventKind) bool {
	switch kind {
	case EventProviderUnsupported, EventProviderSupported, EventSessionIdle, EventSessionBusy,
		EventDispatchStarted, EventDispatchNoSideEffect, EventActionWritten, EventActionOutcomeUnknown,
		EventProviderCompactionStarted, EventProviderCompactionCompleted, EventEventLoss:
		return true
	default:
		return false
	}
}

func eventRequiresSourceSequence(kind EventKind) bool {
	switch kind {
	case EventProviderCompactionStarted, EventProviderCompactionCompleted, EventEventLoss:
		return true
	default:
		return false
	}
}

func hasFreshObservation(machine Machine, at time.Time) bool {
	if machine.LastObservation == nil || at.IsZero() || machine.LastObservation.ObservedAt.After(at) {
		return false
	}
	return at.Sub(machine.LastObservation.ObservedAt) <= MaxObservationDispatchAge
}

func stateForMachine(machine Machine) State {
	if !machine.Config.Enabled {
		return StateDisabled
	}
	if machine.ProviderSupported {
		return StateArmed
	}
	return StateUnsupported
}

func isPreWrite(s State) bool {
	return s == StateArmed || s == StatePendingIdle || s == StatePrepared || s == StateUnsupported || s == StateDisabled
}

func isHold(s State) bool {
	return s == StateOutcomeUnknownHold || s == StateEventLossHold || s == StateBlockedCorrupt
}

func rearmThreshold(captured, fallback int) int {
	if captured == 0 {
		captured = fallback
	}
	return captured - RearmGapPercentagePoints
}

// atOrAbove implements used*100 >= limit*threshold without overflowing.
func atOrAbove(used, limit uint64, threshold int) bool {
	leftHi, leftLo := bits.Mul64(used, 100)
	rightHi, rightLo := bits.Mul64(limit, uint64(threshold))
	return leftHi > rightHi || (leftHi == rightHi && leftLo >= rightLo)
}

// atOrBelow implements used*100 <= limit*percentage without overflowing.
func atOrBelow(used, limit uint64, percentage int) bool {
	leftHi, leftLo := bits.Mul64(used, 100)
	rightHi, rightLo := bits.Mul64(limit, uint64(percentage))
	return leftHi < rightHi || (leftHi == rightHi && leftLo <= rightLo)
}
