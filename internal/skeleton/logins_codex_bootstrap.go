package skeleton

import (
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
)

func (a *coreAPI) bootstrapFirstLaunchEligible(spec daemon.LaunchSpec) bool {
	if !spec.BootstrapFirstAttempt || spec.OperationID == "" || spec.AgentType != logins.ProviderCodex {
		return false
	}
	if a.core != nil {
		if _, exists := a.core.ComposerOperation(spec.OperationID); exists {
			return false
		}
	}
	return true
}
