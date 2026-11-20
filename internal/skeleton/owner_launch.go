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
		if out.State == schema.OutcomeApplied {
			if meta, ok := a.core.Get(out.SessionID); ok && meta.Role == "manager" && (meta.ResumedFrom != "" || schema.IsDiscordManager(meta.Role, meta.ManagerConversationID)) && verifyNativeWritersStopped(a.stateDir, meta, false) == nil {
				view.FailedManagerInstance = a.core.FailedManagerStartupInstance(meta)
			}
		}
	}
	return view, known
}
