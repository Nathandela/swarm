package voice

import (
	"encoding/json"
	"sort"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func discussionWorkerFirstTurns(st State, conversation string) []schema.WorkerFirstTurnReceipt {
	var receipts []schema.WorkerFirstTurnReceipt
	for _, action := range st.Actions {
		if action.ConversationID != conversation || action.State != "settled" || action.ID == "" {
			continue
		}
		var launch controllerActionRequest
		var created createdWorkerResult
		admission := st.Admissions[admissionKey(conversation, action.Admission)]
		origin := admission.Origin
		if origin == nil || origin.Validate() != nil || origin.DiscussionID == "" ||
			admission.ConversationID != conversation || admission.Sequence != action.Admission ||
			admission.SourceID != "discussion:"+origin.RequestID || !admission.SourceCutover ||
			admission.NativeInputID == "" || admission.NativeInputID != action.NativeInputID || admission.NativeTurnID == "" ||
			json.Unmarshal(action.Request, &launch) != nil || launch.Tool != "swarm_create" || launch.Launch == nil ||
			launch.Launch.InitialPrompt == "" || launch.Origin == nil || *launch.Origin != *origin ||
			admission.Project == "" || launch.Project != admission.Project || launch.Launch.Cwd != launch.Project ||
			json.Unmarshal(action.Receipt, &created) != nil || created.State != "created" || created.SessionInstance == "" {
			continue
		}
		var input protocol.ComposerSendReq
		if json.Unmarshal(admission.Request, &input) != nil || input.Origin == nil || *input.Origin != *origin ||
			input.ManagerAdmission == nil || input.ManagerAdmission.Validate() != nil || input.ManagerAdmission.Nonce != admission.NativeInputID {
			continue
		}
		manager := input.ManagerAdmission.Binding
		owner, parent, managerIDValid := protocol.ParseID(manager.Session)
		workerOwner, _, workerIDValid := protocol.ParseID(created.Session)
		if !managerIDValid || !workerIDValid || manager.Session == created.Session || owner != workerOwner ||
			manager.ConversationID != conversation || manager.Session != input.Session || manager.SessionInstance != input.SessionInstance ||
			launch.Launch.SpawnedFrom != parent {
			continue
		}
		// Direct creation starts a job and an attempt. Derive its original
		// evidence key even when the worker later receives another action.
		worker := WorkerAssociation{
			Session: created.Session, SessionInstance: created.SessionInstance,
			JobID: action.ID, AttemptID: action.ID, ActionID: action.ID,
		}
		evidenceID := discussionEvidenceID(worker)
		event := st.Events[key(conversation, evidenceID)]
		if event.ID != evidenceID || event.ConversationID != conversation || event.WorkerMessage || event.VMWorkerStatus {
			continue
		}
		proof, err := storedDiscussionWorkerProof(event, worker)
		if err != nil || proof == nil || proof.State != "completed" {
			continue
		}
		receipts = append(receipts, schema.WorkerFirstTurnReceipt{
			Origin: *origin, AdmissionID: origin.RequestID, ManagerSession: manager.Session, ManagerInstance: manager.SessionInstance,
			WorkerSession: worker.Session, WorkerInstance: worker.SessionInstance, JobID: worker.JobID, AttemptID: worker.AttemptID,
			ActionID: worker.ActionID, NativeTurnID: proof.TurnID,
		})
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].ActionID < receipts[j].ActionID })
	return receipts
}
