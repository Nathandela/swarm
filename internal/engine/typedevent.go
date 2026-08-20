package engine

// ApplyTypedEvent (Wave R7, Mirror M4.5; ADR-013 §R7.3): the IN-PROCESS typed-status seam, so
// a producer that lives inside the daemon does not have to authenticate to the daemon.
//
// WHY NOT HandleCallback, and the retraction that goes with it. An earlier draft routed M4.5's
// typed events through HandleCallback and justified the reuse with a sequence-namespace claim
// that INVERTS the actual property: sharing one counter between two allocators is safe because
// hookclient.nextSequence takes LOCK_EX, not because the file is the same. And getting it
// wrong is a SILENT DROP -- a duplicate sequence is discarded and the drain errors -- while
// the hook path FSYNCS a durable seen-set PER CALLBACK, which is tolerable at turn-lifecycle
// rates and fatal if item frames ever reach it.
//
// More basically: the daemon does not need to authenticate to itself. The token check, the
// durable replay set and the on-disk counter buy nothing an in-process producer does not
// already have, and the frames arrive IN ORDER ON ONE WebSocket connection, which is what
// makes an in-memory counter sufficient.
//
// WHAT IS RETAINED: applyTyped's per-dimension high-water, which is what rejects a stale
// reorder and is real value. WHAT IS NOT: the token, the fsync, the durable seen-set.
//
// SINGLE-WRITER IS ENFORCED, NOT ASSUMED. A backend session is registered with NO hook token,
// and HandleCallback refuses an empty or mismatched token outright -- so the two typed
// producers are mutually exclusive by construction and can never compete for one high-water
// namespace.

import (
	"fmt"

	"github.com/Nathandela/swarm/internal/status"
)

// ApplyTypedEvent applies one typed status event for sessionID, exactly as HandleCallback
// does AFTER the token check: derive the dimensions from the session's own declared
// SignalSources, apply the children/post-Stop rules, commit, and emit if anything changed.
//
// An event the session's adapter maps to nothing is a BENIGN NO-OP, mirroring HandleCallback's
// own posture: the grid heuristic still governs, which is what makes a CLI upgrade that adds
// or renames a method degrade rather than error into a log nobody reads.
//
// payload is the flat, already-normalized dimension map a caller may supply directly; nil is
// the ordinary case, where the mapping comes from the adapter's declaration alone.
func (e *Engine) ApplyTypedEvent(sessionID, event string, payload map[string]string) error {
	e.mu.Lock()
	s, ok := e.sessions[sessionID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("engine: typed event for unregistered or ended session %q", sessionID)
	}

	s.emitMu.Lock()
	defer s.emitMu.Unlock()

	e.mu.Lock()
	// Re-validate under e.mu: the session may have been ended or replaced between the lookup
	// above and acquiring its emit lock (HandleCallback's own rule).
	if cur, ok := e.sessions[sessionID]; !ok || cur != s || !s.alive {
		e.mu.Unlock()
		return fmt.Errorf("engine: typed event for unregistered or ended session %q", sessionID)
	}
	countChild(s, event)
	dims := deriveDims(s.sources, event, payload)
	if len(dims) == 0 {
		e.mu.Unlock()
		return nil // unmapped: benign, exactly as on the hook path
	}
	now := e.now()
	idleStop := event == stopEvent && dims[PayloadKeyTurn] == string(status.TurnIdle)
	dims = withChildrenHoldingTheTurn(s, event, dims)
	if dims = withoutPostStopReactivation(s, event, dims, now); len(dims) == 0 {
		e.mu.Unlock()
		return nil
	}
	// The sequence is drawn HERE, from a per-session in-memory monotonic counter, and it is
	// allocated under e.mu -- so two frames can never draw the same number and have the
	// second silently rejected as a replay by applyTyped's high-water.
	s.typedSeq++
	next, advanced, err := applyTyped(s, s.typedSeq, dims)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	if !advanced {
		// Unreachable with a strictly increasing counter, and reported rather than hidden:
		// silence here is the exact drop shape §R7.3 refuses.
		e.mu.Unlock()
		return fmt.Errorf("engine: typed event %q for %q advanced no dimension at sequence %d", event, sessionID, s.typedSeq)
	}
	s.lastTypedAt = now
	s.lastSignalAt = now
	if idleStop {
		s.lastStopAt = now
	}
	changed := commit(s, next)
	e.mu.Unlock()

	if changed {
		e.emit(sessionID, next)
	}
	return nil
}
