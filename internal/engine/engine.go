// Package engine is the Epic 10 status-detection engine: the single, CLI-agnostic
// authority that turns raw session signals into a session's authenticated, fresh,
// never-confidently-wrong status. It is the SOLE writer of a session's status
// (G6): every mutation flows through one Engine method, each serialized by a
// single mutex.
//
// Three signal paths feed one status per session:
//
//   - Typed signals (HandleCallback) — a `swarm hook` post authenticated against
//     the session's live token and a per-dimension monotonic sequence (S6/G5).
//     These are authoritative: a fresh typed signal outranks the heuristic.
//   - Grid heuristics (OnOutput) — a deterministic read of the emulated screen on
//     each output event, under the session's declared per-adapter grid signature.
//     It only applies when no fresh typed signal outranks it; a conclusive read is
//     applied, and an inconclusive read PRESERVES the committed status rather than
//     committing unknown — absence of evidence is not evidence of change (ADR-007).
//   - The fallback poll (Tick) — a low-frequency, daemon-driven re-evaluation.
//     The engine NEVER self-polls; all periodic work happens only when the daemon
//     calls Tick, so an idle system does no work (E10.8). Tick samples CPU once
//     per session and enforces the staleness guard (S7): a session left active
//     with no output and no CPU past the threshold is downgraded to unknown.
//
// Every side effect (clock, CPU sampling, emission) is injected via Config, so
// the whole engine is deterministic under test. Emit is called synchronously on
// the goroutine that observed the change, and only when a status dimension
// actually changed (L1 is per-dimension-change). A status mutation is committed
// under the global mutex (single writer, G6), but Emit itself runs OUTSIDE that
// mutex, serialized per session by a per-session emit lock — so a slow, blocking,
// or reentrant subscriber for one session can never stall the engine for another
// (L1/P-3), while one session's emits stay strictly ordered.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// Payload keys a typed signal carries its status dimensions under. The engine
// core is CLI-agnostic: an adapter (Epic 11) normalizes each CLI's raw hook into
// these generic keys, whose values are the status-package string constants
// (e.g. "active", "permission").
const (
	PayloadKeyTurn        = "turn"
	PayloadKeyInteraction = "interaction"
)

// Descriptor keys an adapter's SignalSource carries (adapter.SignalSource.Descriptor)
// and that the engine reads to NORMALIZE a raw hook callback's event into status
// dimensions — the mapping bridge. descKeyEvent matches a callback's Event to a
// source; descKeyTurn/descKeyInteraction are that event's status mapping. The two
// optional subtype keys let one event map by a payload field (e.g. a Claude
// Notification whose subtype distinguishes a permission prompt from an idle nudge):
// descKeySubtypeField names the payload field carrying the subtype, and
// descKeySubtypeMap is a "subtype=interaction;..." table selecting the interaction.
const (
	descKeyEvent        = "event"
	descKeyTurn         = "turn"
	descKeyInteraction  = "interaction"
	descKeySubtypeField = "subtype_field"
	descKeySubtypeMap   = "subtype_interaction"
)

// Callback is one authenticated status post from a session's `swarm hook`
// invocation. Token and Sequence authenticate it (S6/G5); Payload carries the
// status dimensions to apply under the PayloadKey* keys.
type Callback struct {
	SessionID string            `json:"session_id"`
	Token     string            `json:"token"`
	Sequence  uint64            `json:"sequence"`
	Event     string            `json:"event"`
	Payload   map[string]string `json:"payload"`
	// Raw is the CLI's OWN event body, carried whole for events whose adapter
	// descriptor declares capture=raw and absent for every other event (ADR-010 §6).
	// It exists because Payload cannot hold it: the flattener keeps top-level STRINGS,
	// so a tool's input, its response and a diff — all nested objects — are gone by the
	// time a callback is composed, and the structured-interaction producer needs them.
	//
	// THE ENGINE NEVER READS IT. It is untrusted tool output: deriveDims works from the
	// declared descriptor and the flat payload only, so B5's degrade-to-none and the
	// 4-value status.Interaction enum are unaffected by anything a CLI writes here. The
	// sole consumer is the daemon-side interaction producer, which caps, redacts and
	// excerpts it before anything is journaled.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Config wires the Engine's injected effects and thresholds.
type Config struct {
	// Now reads the current time; every freshness and staleness comparison uses
	// it, so tests can drive time deterministically. Defaults to time.Now.
	Now func() time.Time
	// CPUSampler returns a per-process utilization (idle ~0, busy clearly
	// positive). Defaults to the platform SampleCPU when nil.
	CPUSampler func(pid int) (float64, error)
	// StalenessThreshold bounds both a typed signal's freshness (how long it
	// outranks the heuristic) and the staleness guard (how long an active turn may
	// stand with no output and no CPU before it is downgraded to unknown).
	StalenessThreshold time.Duration
	// PollInterval is the advisory cadence at which the daemon drives Tick. The
	// engine never self-polls; this documents the intended bounded frequency.
	PollInterval time.Duration
	// Emit is called synchronously whenever a session's status changes.
	Emit func(id string, s status.Status)
	// Metrics holds test-observable counters. Optional; defaults to a fresh
	// *Metrics when nil.
	Metrics *Metrics
}

// Metrics holds test-observable counters. All fields are safe for concurrent
// use.
type Metrics struct {
	// SamplerPanics counts CPUSampler calls Tick recovered from a panic (the
	// session is skipped for that tick; see Tick's doc comment).
	SamplerPanics atomic.Int64
}

// Engine is the per-daemon status authority. Its zero value is unusable; call
// New. Every method is goroutine-safe (one mutex serializes all state).
type Engine struct {
	mu        sync.Mutex
	now       func() time.Time
	sampler   func(pid int) (float64, error)
	staleness time.Duration
	poll      time.Duration
	emit      func(id string, s status.Status)
	metrics   *Metrics
	sessions  map[string]*session
}

// session is the engine's per-session state and the one place a session's status
// lives (G6 single writer).
type session struct {
	token   string
	pid     int
	sources []adapter.SignalSource // declared observation kinds (Epic 11 uses these)
	rules   gridRules              // parsed grid rules from sources (R-C1/R-C2), immutable after RegisterSession
	alive   bool

	// emitMu serializes this session's commit→emit so its emits stay ordered (G6)
	// without holding the global e.mu across Emit. It is acquired BEFORE e.mu and
	// held across the emit, so a wedged subscriber blocks only this session, never
	// the whole engine (F3, L1/P-3).
	emitMu sync.Mutex

	status status.Status

	// Per-dimension high-water sequence (G5): a typed callback writes a dimension
	// only if its sequence exceeds that dimension's last-applied sequence. This
	// tolerates reordered/concurrent hook delivery — a callback for one dimension is
	// not dropped just because a callback for ANOTHER dimension arrived first with a
	// higher sequence — while still rejecting an exact replay and a stale sequence
	// that would regress a dimension a newer sequence already set. 0 = none yet.
	turnSeq  uint64
	interSeq uint64
	// typedSeq is the IN-PROCESS producer's own monotonic counter (Wave R7,
	// ApplyTypedEvent). It is allocated under e.mu and is deliberately separate from the
	// hook path's on-disk sequence: a backend session has no hook token, so the two
	// producers are mutually exclusive and cannot share a namespace to corrupt.
	typedSeq uint64

	lastTypedAt  time.Time // when the last typed signal applied (precedence freshness)
	lastSignalAt time.Time // when any signal (typed or output) was last observed (staleness)
	lastStopAt   time.Time // when the last idle-setting Stop applied (the turn boundary, see postStopGrace)

	// children counts the background children currently outstanding (see
	// subagentStartEvent). It is IN-MEMORY ONLY and never persisted: a daemon
	// restart forgets it, and the grid path (the workflow row marker in
	// heuristic.go) re-establishes the truth from the screen, in both directions.
	children int
}

// New builds an Engine from cfg, defaulting the injectable effects so a partial
// production config cannot nil-panic.
func New(cfg Config) *Engine {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sampler := cfg.CPUSampler
	if sampler == nil {
		sampler = SampleCPU
	}
	emit := cfg.Emit
	if emit == nil {
		emit = func(string, status.Status) {}
	}
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = &Metrics{}
	}
	return &Engine{
		now:       now,
		sampler:   sampler,
		staleness: cfg.StalenessThreshold,
		poll:      cfg.PollInterval,
		emit:      emit,
		metrics:   metrics,
		sessions:  make(map[string]*session),
	}
}

// RegisterSession installs a session with its live authentication token, pid, and
// declared signal sources, seeding its INITIAL status in the SAME locked operation
// (C2): the optional initialStatus is installed atomically with registration, so
// there is no register-then-seed gap for an early authenticated callback to fall
// into and then be overwritten (which would lose the real signal while keeping its
// advanced high-water). Fresh launch omits it (or passes the humble baseline);
// reconcile passes the PERSISTED status so a reconnected session's engine view
// equals its persisted status after a restart (S7) — the engine then believes a
// persisted turn=active and the staleness guard (Tick) can downgrade a now-idle
// session. The token is per-invocation and dies with the session (EndSession). No
// status is emitted here (a mere register/reconnect raises no status event).
func (e *Engine) RegisterSession(id, token string, pid int, sources []adapter.SignalSource, initialStatus ...status.Status) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := status.Status{
		Process:     status.ProcessRunning,
		Turn:        status.TurnUnknown,
		Interaction: status.InteractionUnknown,
	}
	if len(initialStatus) > 0 {
		st = initialStatus[0]
	}
	e.sessions[id] = &session{
		token:   token,
		pid:     pid,
		sources: sources,
		rules:   parseGridRules(sources),
		alive:   true,
		status:  st,
	}
}

// EndSession retires a session; its token dies with it, so any later callback
// bearing that token is rejected (S6).
func (e *Engine) EndSession(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.sessions, id)
}

// HandleCallback authenticates and applies a typed status signal (S6/G5). It
// rejects — with an error and NO emit — any callback that is tokenless, carries a
// foreign token, targets an unregistered or ended session, names a status value
// outside the vocabulary (F2), or is stale/replayed for every dimension it names
// (its sequence exceeds none of the named dimensions' high-water). A rejected
// callback advances nothing. An accepted callback writes only the named dimensions
// whose sequence is newer than that dimension's high-water, and emits only if a
// dimension actually changed. Emit runs outside e.mu, serialized per session by
// s.emitMu (F3).
func (e *Engine) HandleCallback(cb Callback) error {
	e.mu.Lock()
	s, ok := e.sessions[cb.SessionID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("engine: callback for unregistered or ended session %q", cb.SessionID)
	}

	s.emitMu.Lock()
	defer s.emitMu.Unlock()

	e.mu.Lock()
	// Re-validate under e.mu: the session may have been ended or replaced between
	// the lookup above and acquiring its emit lock.
	if cur, ok := e.sessions[cb.SessionID]; !ok || cur != s || !s.alive {
		e.mu.Unlock()
		return fmt.Errorf("engine: callback for unregistered or ended session %q", cb.SessionID)
	}
	if cb.Token == "" || cb.Token != s.token {
		e.mu.Unlock()
		return errors.New("engine: callback token does not match the session's live token")
	}
	// Outstanding-children accounting, in ARRIVAL order and only for a callback
	// that authenticated: a rejected post must not move the count.
	countChild(s, cb.Event)
	// Normalize the callback's event + payload into status dimensions via the
	// session's registered SignalSources (the mapping bridge, seam c): a real hook
	// posts {event, payload fields} and the engine derives turn/interaction from the
	// adapter's declared event->status descriptor. A pre-normalized caller carrying
	// explicit turn/interaction dims is honored as-is.
	dims := deriveDims(s.sources, cb.Event, cb.Payload)
	if len(dims) == 0 {
		// The event maps to no status dimension and none was supplied explicitly (an
		// unmapped event): accept as a benign no-op — the grid heuristic still governs
		// — rather than reject it as an auth/replay failure.
		e.mu.Unlock()
		return nil
	}
	now := e.now()
	// A Stop is the turn boundary whether or not children mask its idle below, so
	// read the boundary off the Stop's OWN derived turn before masking.
	idleStop := cb.Event == stopEvent && dims[PayloadKeyTurn] == string(status.TurnIdle)
	dims = withChildrenHoldingTheTurn(s, cb.Event, dims)
	if dims = withoutPostStopReactivation(s, cb.Event, dims, now); len(dims) == 0 {
		// A post-Stop straggler naming nothing but the turn it may not reopen:
		// accept as a benign no-op, like an unmapped event.
		e.mu.Unlock()
		return nil
	}
	next, advanced, err := applyTyped(s, cb.Sequence, dims)
	if err != nil {
		e.mu.Unlock()
		return err // out-of-vocabulary payload: reject like an auth failure, advance nothing
	}
	if !advanced {
		// The sequence is not newer than the high-water of ANY dimension it names:
		// an exact replay or a stale reorder. Reject without advancing or emitting.
		e.mu.Unlock()
		return fmt.Errorf("engine: callback sequence %d is stale or replayed for every named dimension", cb.Sequence)
	}
	s.lastTypedAt = now
	s.lastSignalAt = now
	if idleStop {
		s.lastStopAt = now // arm the turn boundary (postStopGrace)
	}
	changed := commit(s, next)
	e.mu.Unlock()

	if changed {
		e.emit(cb.SessionID, next)
	}
	return nil
}

// OnOutput re-evaluates a session's status from a fresh screen snapshot (the grid
// heuristic, T-3), under the session's declared per-adapter grid signature. A
// still-fresh typed signal outranks the heuristic (S7 precedence), so the read is
// discarded in that window. Otherwise a CONCLUSIVE reading (active or idle) is
// applied; an INCONCLUSIVE reading is absence of evidence, not evidence of change,
// so it PRESERVES the committed status rather than committing unknown (ADR-007) —
// this is what stops an unreadable frame from clobbering a known idle/active into
// Working. The output still refreshes the staleness clock (the session is alive);
// the Tick guard, not this tap, downgrades a truly silent active turn. Emit runs
// outside e.mu, serialized per session by s.emitMu (F3).
func (e *Engine) OnOutput(id string, snap *vt.Snap) {
	e.mu.Lock()
	s, ok := e.sessions[id]
	e.mu.Unlock()
	if !ok {
		return
	}

	s.emitMu.Lock()
	defer s.emitMu.Unlock()

	e.mu.Lock()
	if cur, ok := e.sessions[id]; !ok || cur != s || !s.alive {
		e.mu.Unlock()
		return
	}
	// Any output is evidence the session is not silent, so it refreshes the
	// staleness clock even when a fresher typed signal suppresses the reading.
	now := e.now()
	s.lastSignalAt = now
	if now.Sub(s.lastTypedAt) < e.staleness {
		e.mu.Unlock()
		return // a fresher typed signal outranks the heuristic
	}
	var (
		turn        status.Turn
		interaction status.Interaction
		conclusive  bool
	)
	if len(s.rules.busy) > 0 || len(s.rules.idle) > 0 {
		// Declarative descriptor rules (agy/opencode, R-C1/R-C2). Conclusive iff a
		// declared rule or the generic fallback classified the frame; an unknown
		// reading is inconclusive and preserves the committed status (ADR-007).
		turn, interaction = evaluateGridWithRules(snap, s.rules)
		conclusive = turn != status.TurnUnknown
	} else {
		turn, interaction, conclusive = evaluateGridSig(snap, gridSignature(s.sources))
	}
	if !conclusive {
		e.mu.Unlock()
		return // inconclusive grid tap: preserve the committed status (ADR-007)
	}
	next := s.status
	next.Turn = turn
	next.Interaction = interaction
	changed := commit(s, next)
	e.mu.Unlock()

	if changed {
		e.emit(id, next)
	}
}

// gridSignature returns the grid-signature name the session's heuristic
// SignalSource declares (Descriptor["grid"]), or "" for the generic reader.
func gridSignature(sources []adapter.SignalSource) string {
	for _, s := range sources {
		if s.Kind == "heuristic" {
			return s.Descriptor["grid"]
		}
	}
	return ""
}

// Tick is the low-frequency, daemon-driven fallback poll. It enforces the
// staleness guard (S7): a session left turn=active with no output and no CPU
// past the threshold is downgraded to unknown. The engine does periodic work
// ONLY here — it never self-polls.
//
// Sampling is gated at collection time (under e.mu) to sessions whose sample
// Tick will actually consume: turn=active AND already past the staleness
// threshold — the only case the commit check below can flip. Because time only
// advances, a session already stale at collection is still stale once its
// sample completes; a session not yet stale is simply left for a later Tick,
// which costs it at most one poll interval. This is the same condition the
// commit check re-verifies below (re-verified because a concurrent
// HandleCallback/OnOutput may have changed the session in between).
//
// Gated sessions are sampled CONCURRENTLY, one goroutine per session (N is
// capped at 128 by the daemon, so this is cheap), all joined before Tick
// returns: wall-clock cost is ~one cpuSampleWindow regardless of N, not
// Nx100ms. CPU is sampled outside any lock (a real sampler blocks on a
// sampling window), so a Tick never stalls a concurrent hook callback's emit
// (L1); each flip is then applied under the lock against the session's live
// state, exactly as before.
//
// Tick takes no context. Engine.Run drives it from a ticker and calls it
// synchronously, so a ctx cancellation that lands mid-Tick is observed only
// once Tick returns — i.e. cancellation may wait up to one cpuSampleWindow for
// the in-flight sampler goroutines to join (down from up to Nx100ms before
// this change). A sampler that panics is recovered: that one session is
// skipped for this tick (no flip, its state untouched), the panic is counted
// in the engine's Metrics, and every other session's sampling and the engine
// itself are unaffected. A sampler that never returns (blocks forever, as
// opposed to panicking) is NOT bounded by anything here — its goroutine, and
// therefore Tick and Run's next cancellation check, would block indefinitely;
// this is accepted because the production sampler is a short, bounded local
// syscall-based read, not an operation expected to hang.
func (e *Engine) Tick() {
	e.mu.Lock()
	type job struct {
		id  string
		pid int
		s   *session
	}
	now := e.now()
	jobs := make([]job, 0, len(e.sessions))
	for id, s := range e.sessions {
		if s.alive && s.status.Turn == status.TurnActive && now.Sub(s.lastSignalAt) >= e.staleness {
			jobs = append(jobs, job{id, s.pid, s})
		}
	}
	e.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(len(jobs))
	for _, j := range jobs {
		go func(j job) {
			defer wg.Done()
			busy, panicked := e.sampleRecovered(j.pid)
			if panicked {
				e.metrics.SamplerPanics.Add(1)
				return // session skipped this tick; no flip either way
			}

			j.s.emitMu.Lock()
			e.mu.Lock()
			cur, ok := e.sessions[j.id]
			changed := false
			var next status.Status
			if ok && cur == j.s && j.s.alive && j.s.status.Turn == status.TurnActive && !busy &&
				e.now().Sub(j.s.lastSignalAt) >= e.staleness {
				next = j.s.status
				next.Turn = status.TurnUnknown
				changed = commit(j.s, next)
			}
			e.mu.Unlock()

			if changed {
				e.emit(j.id, next)
			}
			j.s.emitMu.Unlock()
		}(j)
	}
	wg.Wait()
}

// sampleRecovered calls e.sampler(pid), reporting busy the same way the
// unrecovered call would (err == nil && cpu > 0), except a panic inside the
// sampler is caught and reported via panicked instead of propagating — the
// caller then skips this session for the tick rather than crashing Tick.
func (e *Engine) sampleRecovered(pid int) (busy, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	cpu, err := e.sampler(pid)
	return err == nil && cpu > 0, false
}

// Run drives the fallback poll (E10.8): it calls Tick every Config.PollInterval
// until ctx is cancelled, then returns. This is the ONLY periodic driver — the
// engine never self-polls — and it does no busy work: between ticks it blocks on a
// ticker, so an idle system consumes nothing beyond one wakeup per interval. With
// a non-positive PollInterval there is no cadence to drive, so Run blocks until
// ctx is cancelled. Run adds no goroutines of its own beyond the ticker, so it
// leaks nothing once it returns.
func (e *Engine) Run(ctx context.Context) {
	if e.poll <= 0 {
		<-ctx.Done()
		return
	}
	t := time.NewTicker(e.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Tick()
		}
	}
}

// commit writes next as s's status and reports whether a dimension changed. It is
// the single choke point through which every status mutation passes (G6). The
// caller holds e.mu; emission is the caller's job, done AFTER e.mu is released and
// under the session's emit lock (F3, L1/P-3).
func commit(s *session, next status.Status) bool {
	if next == s.status {
		return false
	}
	s.status = next
	return true
}

// applyTyped computes the would-be status of a typed callback under the
// per-dimension anti-replay rule (G5) and reports whether any dimension advanced.
// A named dimension is written only if seq exceeds that dimension's high-water, so
// reordered/concurrent hooks touching DIFFERENT dimensions are both kept, while a
// replayed or stale sequence never regresses a dimension a newer sequence already
// set. Vocabulary is validated for EVERY named dimension BEFORE any high-water
// advances, so a malformed payload advances nothing and stores no bogus dimension
// a downstream Derive would have to interpret (F2). Caller holds e.mu.
func applyTyped(s *session, seq uint64, payload map[string]string) (next status.Status, advanced bool, err error) {
	tv, hasTurn := payload[PayloadKeyTurn]
	if hasTurn && !validTurn(tv) {
		return status.Status{}, false, fmt.Errorf("engine: callback turn %q is not in the status vocabulary", tv)
	}
	iv, hasInter := payload[PayloadKeyInteraction]
	if hasInter && !validInteraction(iv) {
		return status.Status{}, false, fmt.Errorf("engine: callback interaction %q is not in the status vocabulary", iv)
	}
	next = s.status
	if hasTurn && seq > s.turnSeq {
		s.turnSeq = seq
		next.Turn = status.Turn(tv)
		advanced = true
	}
	if hasInter && seq > s.interSeq {
		s.interSeq = seq
		next.Interaction = status.Interaction(iv)
		advanced = true
	}
	return next, advanced, nil
}

// Stop is a TURN BOUNDARY, not merely another sequence-numbered write. Typed
// signals are ordered by sequence, and a sequence carries no causal order: it is
// handed out by racing `swarm hook` processes contending on a flock
// (internal/hookclient.nextSequence). A SubagentStop, or the PostToolUse of a
// tool that finished around the same instant, can therefore take a HIGHER
// sequence than the Stop that closed the turn and flip the settled idle back to
// active — permanently, since no further hook is coming to correct it and the
// grid tap only governs once the typed signal goes stale (agents-tracker-707).
const (
	stopEvent = "Stop"

	// postStopGrace is how long an idle-setting Stop outranks a trailing-edge
	// active hook. The corpus carries no measured Stop->straggler distribution, so
	// this is a BOUND rather than a fitted value: L1/ADR-008 contracts the whole
	// hook-to-rendered-TUI path at <=1s, so a hook racing the same turn boundary
	// lands well inside 2s, while a background task that genuinely finishes later
	// still reads active. It is deliberately an order of magnitude below
	// StalenessThreshold (30s in production): this is a tie-break at the boundary,
	// not a second staleness rule.
	postStopGrace = 2 * time.Second
)

// The hooks that bracket a BACKGROUND CHILD (docs/verification/spike-SE.md F1):
// a claude orchestrating background agents fires Stop when its MAIN turn ends
// while the workflow keeps running, and only these two say whether anything is
// still out there. Like stopEvent these are Claude Code's hook names
// (internal/adapter/claude), the one place the otherwise CLI-agnostic engine core
// knows an event by name; the eventual home for the classification is an adapter
// descriptor key alongside descKeySubtypeField.
const (
	subagentStartEvent = "SubagentStart"
	subagentStopEvent  = "SubagentStop"
)

// trailingEdgeEvents are the hooks whose turn=active means "work that was
// already running has ENDED", so they can never legitimately start a turn.
// UserPromptSubmit and PreToolUse are deliberately absent: they report work
// STARTING, so they are a genuine new turn and must always win. SubagentStop was
// a member until agents-tracker-c7i4 made it turn-NEUTRAL at the adapter: with no
// turn to withhold there is nothing here to do, so it left this table rather than
// being guarded twice.
//
// These are Claude Code's hook names (internal/adapter/claude), the one place the
// otherwise CLI-agnostic engine core knows an event by name. The eventual home
// for the classification is an adapter descriptor key alongside
// descKeySubtypeField — an adapter-side change this fix does not reach into.
var trailingEdgeEvents = map[string]bool{
	"PostToolUse": true,
}

// countChild moves the session's outstanding-children count for the bracketing
// hooks and leaves every other event alone. The DECREMENT FLOORS AT ZERO, which
// is not defensive rounding: the live capture carries three SubagentStops for two
// SubagentStarts (an agent RESUME re-fires SubagentStart, and a child can stop
// without this session ever having seen its start), so an unfloored count drifts
// negative and the next real child then fails to hold the turn. Nothing resets
// the count — in particular NOT UserPromptSubmit, which the auto-continuation
// fires mid-workflow (spike-SE F3). Caller holds e.mu.
func countChild(s *session, event string) {
	switch event {
	case subagentStartEvent:
		s.children++
	case subagentStopEvent:
		if s.children > 0 {
			s.children--
		}
	}
}

// withChildrenHoldingTheTurn masks a Stop's idle turn to active while background
// children are still outstanding: the MAIN loop ended, the workflow did not, and
// reporting "done" there is the false completion agents-tracker-c7i4 removes
// (76-91 per session per day, spike-SE). Only Stop is masked — PermissionRequest
// is also turn=idle, but a session waiting on the human is waiting on the human
// whether or not children run, and needs_input must never be hidden behind
// working. Every other callback is returned unchanged, and dims is never mutated
// in place (deriveDims may return the caller's own payload map). Caller holds
// e.mu.
func withChildrenHoldingTheTurn(s *session, event string, dims map[string]string) map[string]string {
	if event != stopEvent || s.children == 0 || dims[PayloadKeyTurn] != string(status.TurnIdle) {
		return dims
	}
	held := make(map[string]string, len(dims))
	for k, v := range dims {
		held[k] = v
	}
	held[PayloadKeyTurn] = string(status.TurnActive)
	return held
}

// withoutPostStopReactivation drops the turn dimension of a trailing-edge hook
// that would reopen a turn a Stop closed less than postStopGrace ago; every other
// callback is returned unchanged. Only the TURN is withheld — the hook is still a
// real signal and its other dimensions, freshness and staleness effects stand. It
// never mutates dims (deriveDims may return the caller's own payload map), and
// withholding a dimension also leaves that dimension's high-water where it was,
// so a later signal is never locked out. Caller holds e.mu.
func withoutPostStopReactivation(s *session, event string, dims map[string]string, now time.Time) map[string]string {
	if !trailingEdgeEvents[event] || dims[PayloadKeyTurn] != string(status.TurnActive) {
		return dims
	}
	if s.lastStopAt.IsZero() || now.Sub(s.lastStopAt) >= postStopGrace {
		return dims
	}
	kept := make(map[string]string, len(dims))
	for k, v := range dims {
		if k != PayloadKeyTurn {
			kept[k] = v
		}
	}
	return kept
}

// deriveDims normalizes a callback's event + raw payload into the status
// dimensions to apply, using the session's registered SignalSources (the adapter's
// declared event->status table). This is the mapping bridge: a real hook posts an
// event name (and, for payload-dependent events, a subtype field), and the engine —
// CLI-agnostically, purely from the descriptor data the adapter declared —
// derives turn/interaction. A caller that already carries explicit turn/interaction
// dims (a pre-normalized post, or the in-process test path) is honored verbatim, so
// this is backward compatible. An unmapped event with no explicit dims yields an
// empty result (nothing typed to apply). It is a pure function of its inputs.
func deriveDims(sources []adapter.SignalSource, event string, payload map[string]string) map[string]string {
	if _, ok := payload[PayloadKeyTurn]; ok {
		return payload
	}
	if _, ok := payload[PayloadKeyInteraction]; ok {
		return payload
	}
	desc, ok := descriptorForEvent(sources, event)
	if !ok {
		return nil // no descriptor for this event: nothing typed to apply
	}
	dims := make(map[string]string, 2)
	if t := desc[descKeyTurn]; t != "" {
		dims[PayloadKeyTurn] = t
	}
	interaction := desc[descKeyInteraction]
	// Subtype-driven events (a descriptor with a subtype field) derive their
	// interaction ENTIRELY from the payload subtype, NEVER the descriptor's nominal
	// interaction — the engine must not assert a permission prompt it cannot confirm
	// (B5). A MISSING subtype degrades to the safe default (none): the whole
	// refinement is absent, so there is nothing to interpret. An UNRECOGNIZED subtype
	// is different — the CLI IS saying something, this build just does not know what
	// — so it emits NO interaction dimension and the previous interaction stands
	// untouched. Defaulting it to none is what let a claude Notification wipe the
	// needs_input its own PermissionRequest had set 6s earlier. Events without a
	// subtype field keep the descriptor's static interaction.
	if field := desc[descKeySubtypeField]; field != "" {
		if sub := payload[field]; sub == "" {
			interaction = string(status.InteractionNone)
		} else {
			// An unrecognized subtype yields "", which the guard below drops.
			interaction, _ = lookupSubtype(desc[descKeySubtypeMap], sub)
		}
	}
	if interaction != "" {
		dims[PayloadKeyInteraction] = interaction
	}
	return dims
}

// descriptorForEvent finds the SignalSource descriptor whose event matches event.
func descriptorForEvent(sources []adapter.SignalSource, event string) (map[string]string, bool) {
	if event == "" {
		return nil, false
	}
	for _, s := range sources {
		if s.Descriptor[descKeyEvent] == event {
			return s.Descriptor, true
		}
	}
	return nil, false
}

// lookupSubtype parses a "sub=interaction;sub2=interaction2" table and returns the
// interaction mapped to sub. It is total (a malformed table simply yields no match).
func lookupSubtype(table, sub string) (string, bool) {
	for _, pair := range strings.Split(table, ";") {
		if k, v, ok := strings.Cut(pair, "="); ok && k == sub {
			return v, true
		}
	}
	return "", false
}

// validTurn / validInteraction gate a payload's dimension values to the status
// vocabulary (status.Turn / status.Interaction constants).
func validTurn(v string) bool {
	switch status.Turn(v) {
	case status.TurnActive, status.TurnIdle, status.TurnUnknown:
		return true
	}
	return false
}

func validInteraction(v string) bool {
	switch status.Interaction(v) {
	case status.InteractionNone, status.InteractionPrompt, status.InteractionPermission, status.InteractionUnknown:
		return true
	}
	return false
}
