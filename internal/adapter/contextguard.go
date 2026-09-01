package adapter

// ContextGuard is an optional, additive extension of the frozen Adapter
// contract. It names only provider-normalized evidence and a native action
// descriptor; daemon workers own sequencing, persistence, timers, and all I/O.

// ContextGuardEventKind is the normalized meaning of one provider notification.
type ContextGuardEventKind string

const (
	ContextGuardUsage               ContextGuardEventKind = "usage"
	ContextGuardCompactionStarted   ContextGuardEventKind = "compaction_started"
	ContextGuardCompactionCompleted ContextGuardEventKind = "compaction_completed"
)

// ContextGuardQuality says how exact provider usage evidence is. Only Exact may
// be action-eligible; parser absence is the fail-closed result for every other
// provider shape.
type ContextGuardQuality string

const (
	ContextGuardExact ContextGuardQuality = "exact"
)

// ContextGuardNotification is pure provider evidence. Usage fields are set only
// for ContextGuardUsage; Deprecated records a characterized legacy lifecycle
// frame so a daemon can observe it without treating it as a new contract.
type ContextGuardNotification struct {
	Kind         ContextGuardEventKind
	ThreadID     string
	UsedTokens   uint64
	ContextLimit uint64
	Quality      ContextGuardQuality
	Deprecated   bool
}

// ContextGuardSupport is the action rollout state expressed by a provider.
type ContextGuardSupport string

const (
	ContextGuardObserveOnly ContextGuardSupport = "observe_only"
)

// ContextGuardAction is a pure native-action template. AutomaticDispatch stays
// false until the daemon establishes its independent concurrency gates.
type ContextGuardAction struct {
	Method            string
	ThreadIDParameter string
	AutomaticDispatch bool
	Support           ContextGuardSupport
}

// ContextGuardSource is optionally implemented by an Adapter. It is pure,
// total, deterministic, and performs no transport I/O. expectedThreadID binds
// an event to the daemon's current provider identity.
type ContextGuardSource interface {
	ParseContextGuardNotification(raw []byte, expectedThreadID string) (ContextGuardNotification, bool)
	ContextGuardAction(version string) (ContextGuardAction, bool)
}

// AsContextGuardSource discovers the optional extension without widening Adapter.
func AsContextGuardSource(a Adapter) (ContextGuardSource, bool) {
	source, ok := a.(ContextGuardSource)
	return source, ok
}
