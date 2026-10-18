package skeleton

import (
	"github.com/Nathandela/swarm/internal/adapter/opencode"
)

// OpenCodeState reduces OpenCode's legacy HTTP/SSE observation stream into
// bounded, deterministic facts: root busy/idle/retry status, the root session
// error, the requested user inputs, and their terminal outcomes.
//
// The reducer is caller-serialized: no locks, no goroutines, no I/O. Apply
// consumes raw event JSON from one observation epoch; BeginEpoch opens a new
// epoch after a resync. A parse refusal desynchronizes the epoch and every
// later Apply is refused until a new epoch, because a lost event makes outcome
// correlation untrustworthy. Degradation is explicit and never inferred from
// absence: a missing /session/status key is idle only when a SUCCESSFUL,
// correctly scoped read returns it (ReconcileRootStatus); a failed read must
// call MarkRootStatusUnknown instead and never claim idle.
//
// Unqualified continuations are not guessed at. A compaction user-summary
// message fabricates no requested input, an assistant summary message closes
// no outcome, and an auto-continue terminal outcome is recorded only on its
// own synthetic input while the epoch is flagged degraded -- the requested
// input it may actually continue is never finished by inference.
type OpenCodeState struct{}

// The reducer's bounded capacities. Inputs and uncertainty are AUTHORITY (they
// are what later slices consume), so they sit outside the message cache's FIFO
// eviction: an input is evicted only once its outcome is recorded, and an
// uncertainty record is dropped only when the explicit capacity is reached.
const (
	maxOCMessages     = 64       // informational message list (FIFO)
	maxOCInputs       = 128      // requested-input records (authority)
	maxOCUncertain    = 32       // explicit unattributable terminal records
	maxOCInputParts   = 64       // text parts tracked per input
	maxOCPartText     = 16 << 10 // bytes of text kept per part
	maxOCInputText    = 64 << 10 // bytes of text kept per input
	maxOCPendingParts = 32       // parts buffered until their message is known
)

// OpenCodeDisposition says what Apply did with one event.
type OpenCodeDisposition string

const (
	// OCApplied: the event was consumed into state.
	OCApplied OpenCodeDisposition = "applied"
	// OCIgnoredForeign: a session this reducer does not own (children included).
	OCIgnoredForeign OpenCodeDisposition = "ignored-foreign"
	// OCIgnoredUntracked: well-formed but intentionally without tracked effect
	// (unknown event kinds, tool parts, parts of untracked messages).
	OCIgnoredUntracked OpenCodeDisposition = "ignored-untracked"
	// OCIgnoredUnqualified: recognized content that cannot be qualified (unknown
	// status subtype, compaction artifacts). It flags the epoch degraded.
	OCIgnoredUnqualified OpenCodeDisposition = "ignored-unqualified"
	// OCRefusedEpoch: the caller observed an older epoch.
	OCRefusedEpoch OpenCodeDisposition = "refused-epoch"
	// OCRefusedDegraded: a prior refusal desynchronized the epoch; only a new
	// epoch resumes.
	OCRefusedDegraded OpenCodeDisposition = "refused-degraded"
	// OCRefusedParse: the event did not decode; the epoch is desynchronized.
	OCRefusedParse OpenCodeDisposition = "refused-parse"
	// OCRefusedCap: a bounded capacity refused the event.
	OCRefusedCap OpenCodeDisposition = "refused-cap"
)

// OpenCodeApply is Apply's result for one event.
type OpenCodeApply struct {
	Disposition OpenCodeDisposition
	Kind        string
	EventID     string
}

// OpenCodeOutcome is a requested input's terminal execution outcome. It says
// what the native runtime did, not whether the work itself succeeded.
type OpenCodeOutcome string

const (
	OpenCodeOutcomeNone      OpenCodeOutcome = ""
	OpenCodeOutcomeCompleted OpenCodeOutcome = "completed"
	OpenCodeOutcomeFailed    OpenCodeOutcome = "failed"
	OpenCodeOutcomeCancelled OpenCodeOutcome = "cancelled"
	OpenCodeOutcomeDeclined  OpenCodeOutcome = "declined"
)

// Outcome sources.
const (
	OpenCodeOutcomeSourceLive     = "live"
	OpenCodeOutcomeSourceImported = "imported"
)

// OpenCodeError is a root session error kept for display.
type OpenCodeError struct {
	Name    string
	Message string
}

// OpenCodeInput is one requested user input and its terminal outcome.
type OpenCodeInput struct {
	MessageID     string
	Text          string
	TextTruncated bool
	// Synthetic: the message consisted only of synthetic/ignored text parts --
	// an auto-continue re-prompt, not owner text. It is recorded (and its own
	// terminal outcome is), but it is never the owner's current input and its
	// outcome never completes a requested input.
	Synthetic     bool
	At            int64
	Outcome       OpenCodeOutcome
	OutcomeSource string
	OutcomeAt     int64
	ErrorName     string
}

// OpenCodeMessage is one observed native message (informational).
type OpenCodeMessage struct {
	ID        string
	Role      string
	At        int64
	Finish    string
	ErrorName string
	// Summary marks both compaction summary carriers (user artifacts and
	// assistant continuation summaries).
	Summary bool
}

// OpenCodeUncertain is a terminal assistant outcome that cannot be attributed
// to a known requested input. It is explicit: it is recorded, never guessed
// into an input.
type OpenCodeUncertain struct {
	MessageID string
	InputID   string
	Outcome   OpenCodeOutcome
	ErrorName string
	At        int64
	Reason    string
}

// OpenCodeView is a deterministic snapshot of the reducer. Slices and pointers
// are copies: mutating a view never mutates the state.
type OpenCodeView struct {
	Root       string
	Generation string
	Epoch      uint64

	// Degraded is sticky for the epoch: status-dimension degradation is cleared
	// by a successful ReconcileRootStatus, other degradation only by BeginEpoch.
	Degraded       bool
	DegradedReason string

	Busy      bool
	BusyKnown bool
	Retry     *opencode.SessionStatus

	Error *OpenCodeError

	// Current is the newest genuine (non-synthetic) input with no outcome yet.
	Current   *OpenCodeInput
	Inputs    []OpenCodeInput
	Messages  []OpenCodeMessage
	Uncertain []OpenCodeUncertain
}

// NewOpenCodeState builds the reducer for one root session. The root id is the
// only identity; every event naming a different session is foreign.
func NewOpenCodeState(root, generation string) (*OpenCodeState, error) {
	return &OpenCodeState{}, nil
}

// BeginEpoch opens a new observation epoch and returns it. Apply and
// ReconcileRootStatus accept only the current epoch, so stale observers cannot
// interleave a resync. A new epoch clears the degraded hold.
func (s *OpenCodeState) BeginEpoch() uint64 {
	return 0
}

// Apply consumes one raw legacy event JSON document from the given epoch.
func (s *OpenCodeState) Apply(raw []byte, epoch uint64) OpenCodeApply {
	return OpenCodeApply{}
}

// ReconcileRootStatus applies a successful, correctly scoped
// GET /session/status snapshot. A missing root key means idle -- only here,
// because only here is a successful read on the record.
func (s *OpenCodeState) ReconcileRootStatus(epoch uint64, snap map[string]opencode.SessionStatus) error {
	return nil
}

// MarkRootStatusUnknown records that the root's status could not be read. It
// never claims idle: a failed read proves nothing, and a later successful
// ReconcileRootStatus (or a live status event) restores confidence.
func (s *OpenCodeState) MarkRootStatusUnknown(reason string) {}

// Degrade records an unqualified observation (sticky for the epoch).
func (s *OpenCodeState) Degrade(reason string) {}

// View returns a copy of the current facts.
func (s *OpenCodeState) View() OpenCodeView {
	return OpenCodeView{}
}
