package scheduledexec

import (
	"fmt"

	"github.com/Nathandela/swarm/internal/schedule"
)

func ValidateRoute(agent, authority string) error {
	switch agent {
	case schedule.RouteCodex:
		switch authority {
		case schedule.AuthorityObserve, schedule.AuthorityWorkspaceWrite, schedule.AuthorityAgentDefault:
			return nil
		}
	case schedule.RouteClaude:
		if authority == schedule.AuthorityAgentDefault {
			return nil
		}
	}
	return fmt.Errorf("%s does not support scheduled runs with %s authority", agent, authority)
}
