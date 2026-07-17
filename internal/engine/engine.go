// Package engine is the Epic 10 status-detection engine: the single, CLI-agnostic
// authority that turns raw session signals into a session's authenticated, fresh,
// never-confidently-wrong status. It is the SOLE writer of a session's status
// (G6): every mutation flows through one Engine method, each serialized by a
// single mutex.
//
// Three signal paths feed one status per session:
//
//   - Typed signals (HandleCallback) — a `swarm hook` post authenticated against
//     the session's live token and a strictly-increasing sequence (S6/G5). These
//     are authoritative: a fresh typed signal outranks the heuristic.
//   - Grid heuristics (OnOutput) — a deterministic read of the emulated screen on
//     each output event. It only applies when no fresh typed signal outranks it,
//     and an inconclusive read maps to turn=unknown, never a confident guess.
//   - The fallback poll (Tick) — a low-frequency, daemon-driven re-evaluation.
//     The engine NEVER self-polls; all periodic work happens only when the daemon
//     calls Tick, so an idle system does no work (E10.8). Tick samples CPU once
//     per session and enforces the staleness guard (S7): a session left active
//     with no output and no CPU past the threshold is downgraded to unknown.
//
// Every side effect (clock, CPU sampling, emission) is injected via Config, so
// the whole engine is deterministic under test. Emit is called synchronously on
// the goroutine that observed the change, and only when a status dimension
// actually changed (L1 is per-dimension-change).
package engine

import (
	"errors"
	"fmt"
	"sync"
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

// Callback is one authenticated status post from a session's `swarm hook`
// invocation. Token and Sequence authenticate it (S6/G5); Payload carries the
// status dimensions to apply under the PayloadKey* keys.
type Callback struct {
	SessionID string            `json:"session_id"`
	Token     string            `json:"token"`
	Sequence  uint64            `json:"sequence"`
	Event     string            `json:"event"`
	Payload   map[string]string `json:"payload"`
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
	sessions  map[string]*session
}

// session is the engine's per-session state and the one place a session's status
// lives (G6 single writer).
type session struct {
	token   string
	pid     int
	sources []adapter.SignalSource // declared observation kinds (Epic 11 uses these)
	alive   bool

	status status.Status

	lastSeq      uint64    // last accepted callback sequence; 0 = none yet (first valid >=1)
	lastTypedAt  time.Time // when the last typed signal applied (precedence freshness)
	lastSignalAt time.Time // when any signal (typed or output) was last observed (staleness)
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
	return &Engine{
		now:       now,
		sampler:   sampler,
		staleness: cfg.StalenessThreshold,
		poll:      cfg.PollInterval,
		emit:      emit,
		sessions:  make(map[string]*session),
	}
}

// RegisterSession installs a session with its live authentication token, pid, and
// declared signal sources. The token is per-invocation and dies with the session
// (EndSession). The session starts at the humble unknown baseline; no status is
// emitted until a signal moves it.
func (e *Engine) RegisterSession(id, token string, pid int, sources []adapter.SignalSource) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessions[id] = &session{
		token:   token,
		pid:     pid,
		sources: sources,
		alive:   true,
		status: status.Status{
			Process:     status.ProcessRunning,
			Turn:        status.TurnUnknown,
			Interaction: status.InteractionUnknown,
		},
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
// foreign token, targets an unregistered or ended session, or replays a
// non-increasing sequence. An accepted callback updates only the dimensions its
// payload names and emits only if a dimension actually changed.
func (e *Engine) HandleCallback(cb Callback) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.sessions[cb.SessionID]
	if !ok || !s.alive {
		return fmt.Errorf("engine: callback for unregistered or ended session %q", cb.SessionID)
	}
	if cb.Token == "" || cb.Token != s.token {
		return errors.New("engine: callback token does not match the session's live token")
	}
	// Strictly increasing from a first valid sequence >= 1. lastSeq starts at 0,
	// so seq 0 (or any replay/reorder) is rejected without a separate guard.
	if cb.Sequence <= s.lastSeq {
		return fmt.Errorf("engine: callback sequence %d not greater than last accepted %d", cb.Sequence, s.lastSeq)
	}

	now := e.now()
	s.lastSeq = cb.Sequence
	s.lastTypedAt = now
	s.lastSignalAt = now
	e.applyStatus(cb.SessionID, s, applyPayload(s.status, cb.Payload))
	return nil
}

// OnOutput re-evaluates a session's status from a fresh screen snapshot (the grid
// heuristic, T-3). A still-fresh typed signal outranks the heuristic (S7
// precedence), so the read is discarded in that window; otherwise the heuristic
// result — including turn=unknown for an inconclusive grid — is applied.
func (e *Engine) OnOutput(id string, snap *vt.Snap) {
	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.sessions[id]
	if !ok || !s.alive {
		return
	}
	// Any output is evidence the session is not silent, so it refreshes the
	// staleness clock even when a fresher typed signal suppresses the reading.
	now := e.now()
	s.lastSignalAt = now
	if now.Sub(s.lastTypedAt) < e.staleness {
		return // a fresher typed signal outranks the heuristic
	}
	turn, interaction := evaluateGrid(snap)
	next := s.status
	next.Turn = turn
	next.Interaction = interaction
	e.applyStatus(id, s, next)
}

// Tick is the low-frequency, daemon-driven fallback poll. It samples each live
// session's CPU exactly once and enforces the staleness guard (S7): a session
// left turn=active with no output and no CPU past the threshold is downgraded to
// unknown. The engine does periodic work ONLY here — it never self-polls.
//
// CPU is sampled outside the lock (a real sampler blocks on a sampling window),
// so a Tick never stalls a concurrent hook callback's emit (L1); the flip is then
// applied under the lock against the session's live state.
func (e *Engine) Tick() {
	e.mu.Lock()
	jobs := make([]struct {
		id  string
		pid int
	}, 0, len(e.sessions))
	for id, s := range e.sessions {
		if s.alive {
			jobs = append(jobs, struct {
				id  string
				pid int
			}{id, s.pid})
		}
	}
	e.mu.Unlock()

	for _, j := range jobs {
		cpu, err := e.sampler(j.pid)
		busy := err == nil && cpu > 0

		e.mu.Lock()
		s, ok := e.sessions[j.id]
		if ok && s.alive && s.status.Turn == status.TurnActive && !busy &&
			e.now().Sub(s.lastSignalAt) >= e.staleness {
			next := s.status
			next.Turn = status.TurnUnknown
			e.applyStatus(j.id, s, next)
		}
		e.mu.Unlock()
	}
}

// applyStatus commits next as s's status and emits iff a dimension changed. It is
// the single choke point through which every status mutation and emission passes.
// Caller holds e.mu.
func (e *Engine) applyStatus(id string, s *session, next status.Status) {
	if next == s.status {
		return
	}
	s.status = next
	e.emit(id, next)
}

// applyPayload returns cur with only the dimensions named in payload overwritten,
// leaving unnamed dimensions untouched (so a turn-only signal never disturbs
// interaction).
func applyPayload(cur status.Status, payload map[string]string) status.Status {
	if v, ok := payload[PayloadKeyTurn]; ok {
		cur.Turn = status.Turn(v)
	}
	if v, ok := payload[PayloadKeyInteraction]; ok {
		cur.Interaction = status.Interaction(v)
	}
	return cur
}
