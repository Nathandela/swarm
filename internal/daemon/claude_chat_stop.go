package daemon

import (
	"errors"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
)

// Only the retained fresh dev launch SHIM binding is Stop authority. Fake rows
// contain no persisted primary PID; initialization/readiness is not needed to
// contain that shim's one child, and cannot turn this into native custody.
func (d *Daemon) KillClaudeFixture(id string, expected shimwire.ClaudePrimaryIdentity) error {
	unconfirmed := errors.New("daemon: fixture stop unconfirmed")
	if !d.mu.TryLock() {
		return unconfirmed
	}
	s, ok := d.sessions[id]
	if !ok || d.closed {
		d.mu.Unlock()
		return unconfirmed
	}
	m := s.meta
	valid := m.ID == id && m.SchemaVersion == persist.ClaudeChatSchemaVersion && m.RequiredRuntime == persist.RequiredRuntimeClaudeChat && m.AgentType == "fake" && m.LoginRuntime == nil && m.CLIIdentity == nil && m.ClaudeRetirement == nil && m.OpenCodeBinding == nil && m.InputEmbargo == "" && m.Status.Process == status.ProcessRunning && expected.SessionID == id && expected.RequiredRuntime == m.RequiredRuntime && expected.NativeSessionID == m.ConversationID && adapter.IsCanonicalConversationID(m.ConversationID) && expected.ShimPID == m.ShimPID && expected.ShimStartTime == m.ShimStartTime && m.ShimPID > 0 && m.ShimStartTime > 0
	d.mu.Unlock()
	if !valid || !d.shimIdentityMatches(m) {
		return unconfirmed
	}
	// Neither mismatch nor delivery failure invokes markLost or a writer. The
	// supervisor may finalize later; this return is delivery, not death proof.
	return signalShim(shimSocketPath(d.cfg.StateDir, id), shimwire.SigTerm)
}
