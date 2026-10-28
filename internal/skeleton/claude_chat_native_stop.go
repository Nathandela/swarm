package skeleton

import (
	"path/filepath"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
)

type claudeNativeStopHandle struct {
	socket  string
	binding shimwire.ClaudeStopBinding
}

// Establish custody before publishing a native chat. Kill uses only this copy,
// so stalled consumer, coordinator and storage locks cannot delay containment.
func (d *Daemon) retainClaudeNativeStop(m persist.Meta, generation string) error {
	if !persist.ValidID(m.ID) || m.SchemaVersion != persist.ClaudeChatSchemaVersion || m.AgentType != logins.ProviderClaude || m.RequiredRuntime != persist.RequiredRuntimeClaudeChat || m.LoginRuntime == nil || m.CLIIdentity == nil || m.ClaudeRetirement != nil || !logins.IsClaudeProfileRuntime(d.stateDir, *m.LoginRuntime) {
		return daemon.ErrClaudeNativeStopUnconfirmed
	}
	d.claudeNativeStops.LoadOrStore(m.ID, claudeNativeStopHandle{})
	expected := shimwire.ClaudeStopBinding{Version: 1, SchemaVersion: m.SchemaVersion, SessionID: m.ID, RequiredRuntime: m.RequiredRuntime, NativeSessionID: m.ConversationID, Incarnation: executionIncarnation(m), Runtime: *m.LoginRuntime, Cwd: m.ProviderCwd(), CLIIdentity: *m.CLIIdentity, ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime, ScopeGeneration: generation}
	socket := filepath.Join(d.stateDir, m.ID, "shim.sock")
	binding, err := daemon.ReadClaudeNativeStopBinding(socket, expected)
	if err != nil {
		return err
	}
	handle := claudeNativeStopHandle{socket: socket, binding: binding}
	current, _ := d.claudeNativeStops.Load(m.ID)
	old := current.(claudeNativeStopHandle)
	if old == handle || old == (claudeNativeStopHandle{}) && d.claudeNativeStops.CompareAndSwap(m.ID, old, handle) {
		return nil
	}
	return daemon.ErrClaudeNativeStopUnconfirmed
}
