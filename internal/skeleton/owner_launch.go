package skeleton

import (
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func (a *coreAPI) OwnerLaunchReplay(operationID, requestHash string) (persist.Meta, bool, error) {
	return a.core.OwnerLaunchReplay(operationID, requestHash)
}

func (a *coreAPI) OwnerLaunchOutcome(operationID string) (schema.OperationOutcomeView, bool) {
	out, known := a.core.OwnerLaunchOutcome(operationID)
	view := schema.OperationOutcomeView{State: out.State}
	if out.SessionID != "" {
		view.SessionID = a.endpointID + "/" + out.SessionID
	}
	return view, known
}
