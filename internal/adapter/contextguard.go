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
	// ContextGuardAutomatic authorizes the daemon's automatic dispatch of the
	// native action, UNDER THE DAEMON'S OWN SERIALIZATION. The 2026-09-01 live
	// gates (ADR-023 amendment 1) proved the provider itself serializes
	// nothing -- a compact sent mid-turn cancels the turn, and two concurrent
	// compacts interrupt each other -- so this value asserts only that the
	// action's method and telemetry are characterized for the version; every
	// concurrency guarantee is the dispatch lane's to enforce.
	ContextGuardAutomatic ContextGuardSupport = "automatic"
)

// ContextGuardAction is a pure native-action template. AutomaticDispatch is the
// adapter's version-fenced assertion that the daemon MAY dispatch the method
// automatically; the daemon still gates every dispatch behind its enforced
// serialization, quiet revalidation, and the unattended rule (ADR-023 D5/D6).
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

// ContextGuardContinuer is a second optional extension (ADR-023 amendment 2):
// after a compaction the guard itself dispatched has verifiably completed, the
// daemon resumes the interrupted task by starting one ordinary turn. The
// adapter only shapes that request; the daemon worker owns when (and whether)
// to send it -- at the composer lane's head, fully revalidated, at-most-once
// per compaction cycle, and never for a compaction the daemon did not itself
// dispatch. The provider's native queue was investigated and rejected for
// this: a queued message is unrevokable while the compaction runs, so a human
// arriving in that window would still receive a surprise turn
// (docs/design/context-guard-continuation.md).
type ContextGuardContinuer interface {
	// ContextGuardContinuation returns the request that starts text as the
	// continuation turn on thread. Pure, total, no I/O, and version-fenced
	// exactly like the automatic action: ok=false means nothing may be sent.
	ContextGuardContinuation(version, threadID, text string) (method string, params map[string]any, ok bool)
}

// AsContextGuardContinuer discovers the optional extension without widening Adapter.
func AsContextGuardContinuer(a Adapter) (ContextGuardContinuer, bool) {
	continuer, ok := a.(ContextGuardContinuer)
	return continuer, ok
}
