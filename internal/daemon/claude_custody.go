package daemon

import (
	"os"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

// retireClaudeReservation preserves the existing metadata writer order. The
// shared Store guard stamps custody before cleanup; no workspace callback runs
// while its registry flock is held. This can briefly stall metadata writes.
func (d *Daemon) retireClaudeReservation(id string, originalNoFork func() error) (persist.Meta, error) {
	d.writeMu.Lock()
	m, err := d.store.RetireClaudeReservation(id, originalNoFork)
	if err == nil && m.ClaudeRetirement != nil && !d.isDeleted(id) {
		d.putMem(m)
	}
	d.writeMu.Unlock()
	if err == nil && m.ClaudeRetirement != nil && d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(m)
	}
	return m, err
}

// Candidate routing is conservative. The shared retirement guard decides
// the exact retirement authority before any destructive side effect.
func claudeCustodyCandidate(m persist.Meta) bool {
	return m.SchemaVersion == persist.ClaudeSchemaVersion || m.ClaudeRetirement != nil || m.AgentType == logins.ProviderClaude && m.LoginRuntime != nil
}

// Scan deliberately omits unreadable/future/corrupt metadata. Its missing row
// therefore cannot authorize another launch while any state may remain.
func (d *Daemon) sessionStateRetained(id string) bool {
	if !persist.ValidID(id) {
		return true
	}
	_, err := os.Lstat(d.sessionDir(id))
	return err == nil || !os.IsNotExist(err)
}
