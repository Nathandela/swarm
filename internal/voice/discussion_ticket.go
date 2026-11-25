package voice

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// ticketWorkerCompletion observes the retained proof for the current worker
// action, without using worker notes, publication text, or the live manager.
func (d *DiscussionController) ticketWorkerCompletion(st State) *schema.TicketWorkerCompletion {
	conversation := d.cfg.ConversationID
	if current := st.Conversations[conversation]; current.CustodyLost || current.SchedulerPaused {
		return nil
	}
	var completed *schema.TicketWorkerCompletion
	for _, worker := range st.Workers {
		if worker.ConversationID != conversation || worker.Reserved || worker.VMChild || worker.RecoveryRequired ||
			worker.EvidenceLimited || worker.Session == "" || worker.SessionInstance == "" ||
			worker.JobID == "" || worker.JobID != worker.AttemptID || worker.ActionID == "" {
			continue
		}
		launch := st.Actions[key(conversation, worker.AttemptID)]
		if launch.State != "settled" || launch.ID != worker.AttemptID || launch.ConversationID != conversation {
			continue
		}
		var intent controllerActionRequest
		if json.Unmarshal(launch.Request, &intent) != nil || intent.Tool != "swarm_create" || intent.Launch == nil ||
			intent.Origin == nil || intent.Origin.Kind != "ticket_assignment" || intent.Origin.Validate() != nil || worker.InitialSourceID != "discussion:"+intent.Origin.RequestID ||
			worker.Project == "" || intent.Project != worker.Project || intent.Launch.Cwd != worker.Project {
			continue
		}
		var receipt createdWorkerResult
		if json.Unmarshal(launch.Receipt, &receipt) != nil || receipt.State != "created" ||
			receipt.Session != worker.Session || receipt.SessionInstance != worker.SessionInstance {
			continue
		}
		admission := st.Admissions[admissionKey(conversation, launch.Admission)]
		var input protocol.ComposerSendReq
		if admission.ConversationID != conversation || admission.Project != worker.Project ||
			admission.Origin == nil || *admission.Origin != *intent.Origin || !admission.SourceCutover ||
			admission.NativeTurnID == "" || admission.NativeInputID == "" || admission.NativeInputID != launch.NativeInputID ||
			json.Unmarshal(admission.Request, &input) != nil || input.Origin == nil || *input.Origin != *intent.Origin || input.ManagerAdmission == nil ||
			input.ManagerAdmission.Validate() != nil || input.ManagerAdmission.Nonce != admission.NativeInputID {
			continue
		}
		origin := input.ManagerAdmission.Binding
		owner, parent, valid := protocol.ParseID(origin.Session)
		workerOwner, _, validWorker := protocol.ParseID(worker.Session)
		if !valid || !validWorker || owner != workerOwner || origin.ConversationID != conversation ||
			input.Session != origin.Session || input.SessionInstance != origin.SessionInstance ||
			worker.Parent != parent || intent.Launch.SpawnedFrom != parent || intent.Launch.Agent != origin.Provider {
			continue
		}
		if !currentTicketWorkerAction(st, worker, launch) {
			continue
		}
		event := st.Events[key(conversation, discussionEvidenceID(worker))]
		if event.RecoveryOnly || event.WorkerMessage || event.VMWorkerStatus || event.ConversationID != conversation || event.ID != discussionEvidenceID(worker) {
			continue
		}
		proof, err := storedDiscussionWorkerProof(event, worker)
		if err != nil || proof == nil || proof.State != "completed" {
			continue
		}
		if completed != nil {
			return nil
		}
		completed = &schema.TicketWorkerCompletion{
			AdmissionID: intent.Origin.RequestID, ManagerSession: origin.Session, ManagerInstance: origin.SessionInstance,
			WorkerSession: worker.Session, WorkerInstance: worker.SessionInstance, JobID: worker.JobID,
			AttemptID: worker.AttemptID, ActionID: worker.ActionID, NativeTurnID: proof.TurnID,
		}
	}
	return completed
}

func currentTicketWorkerAction(st State, worker WorkerAssociation, launch Action) bool {
	target := key(worker.Session, worker.SessionInstance)
	for _, action := range st.Actions {
		if action.ConversationID == worker.ConversationID && action.Target == target && action.State != "settled" {
			return false
		}
	}
	if worker.ActionID == launch.ID {
		return true
	}
	current := st.Actions[key(worker.ConversationID, worker.ActionID)]
	var intent controllerActionRequest
	return current.ID == worker.ActionID && current.State == "settled" && current.Target == target &&
		current.ConversationID == worker.ConversationID && json.Unmarshal(current.Request, &intent) == nil &&
		intent.Tool == "swarm_send" && intent.Origin != nil && intent.Send != nil &&
		intent.Send.Session == worker.Session && intent.Send.SessionInstance == worker.SessionInstance
}
