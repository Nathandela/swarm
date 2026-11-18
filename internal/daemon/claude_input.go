package daemon

import (
	"errors"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/managerproof"
)

// SessionClaudeInput binds a provider broker to the exact shim that still owns the PTY.
func (d *Daemon) SessionClaudeInput(id string) (claudeinput.Descriptor, error) {
	meta, ok := d.Get(id)
	if !ok || meta.AgentType != "claude" || meta.CLIIdentity == nil {
		return claudeinput.Descriptor{}, errors.New("claude native input needs an explicitly refreshed session")
	}
	desc, err := claudeinput.ReadDescriptor(d.sessionDir(id))
	if err != nil || desc.SessionID != id || desc.ShimPID != meta.ShimPID || desc.ShimStartTime != meta.ShimStartTime || desc.ProviderVersion != meta.CLIIdentity.Version {
		return claudeinput.Descriptor{}, errors.New("claude native input is not proven for this shim; refresh this session")
	}
	if meta.Role == "manager" && (meta.ManagerReplayID == "" || desc.ReplayID != meta.ManagerReplayID) {
		return claudeinput.Descriptor{}, managerproof.ErrQuarantined
	}
	return desc, nil
}
