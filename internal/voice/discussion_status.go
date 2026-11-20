package voice

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
)

type DiscussionNativeStatus struct {
	State, ManagerSession, ManagerInstance, Project, Diagnostic string
	Generation, ExternalInput                                   uint64
	Jobs                                                        []DiscussionJobStatus
}

type DiscussionJobStatus struct {
	ID, AttemptID, Session, Instance, Project, State, Diagnostic string
}

// Status reads the controller ledger without launching or contacting the model.
func (d *DiscussionController) Status() (DiscussionNativeStatus, error) {
	st, err := d.store.readView()
	if err != nil {
		return DiscussionNativeStatus{}, err
	}
	c, exists := st.Conversations[d.cfg.ConversationID]
	result := DiscussionNativeStatus{State: "parked"}
	if !exists {
		if _, err := os.Lstat(filepath.Join(d.dir, managerIntentName(d.cfg.ConversationID))); err == nil {
			result.State = "starting"
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		return result, nil
	}
	result.Generation = max(c.ManagerGeneration, 1)
	result.ExternalInput = c.ExternalInput
	result.ManagerSession, result.ManagerInstance, result.Project = c.Binding.SessionID, c.Binding.SessionInstance, c.Project
	switch {
	case c.Parked:
		if c.FailedResume != nil {
			result.Diagnostic = c.FailedResume.Diagnostic
		}
	case c.Retirement != nil:
		result.State = "parking"
	case c.CustodyLost:
		result.State = "blocked"
		result.Diagnostic = discussionCustodyDiagnostic
	default:
		result.State = "active"
		switch {
		case c.SchedulerPaused:
			result.Diagnostic = "direct terminal activity needs reconciliation"
		case !d.journalReady.Load():
			result.Diagnostic = "daemon journal reconnecting"
		}
	}
	for _, worker := range st.Workers {
		job := DiscussionJobStatus{ID: worker.JobID, AttemptID: worker.AttemptID, Session: worker.Session, Instance: worker.SessionInstance, Project: worker.Project, State: "tracked"}
		switch {
		case worker.Reserved:
			job.ID, job.AttemptID, job.State = worker.ActionID, worker.ActionID, "pending_launch"
		case worker.EvidenceLimited:
			job.State, job.Diagnostic = "evidence_limited", discussionEvidenceLimitNotice
		case worker.RecoveryRequired:
			job.State = "uncertain"
		case worker.Ended:
			job.State = "exited"
			if _, verified := st.Events[key(d.cfg.ConversationID, discussionEvidenceID(worker))]; !verified && !worker.VMChild {
				job.State = "exited_unconfirmed"
			}
		}
		result.Jobs = append(result.Jobs, job)
	}
	sort.Slice(result.Jobs, func(i, j int) bool {
		a, b := result.Jobs[i], result.Jobs[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.AttemptID < b.AttemptID
	})
	return result, nil
}
