package voice

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/workermail"
)

type DiscussionNativeStatus struct {
	PendingAdmissions                                           int
	State, ManagerSession, ManagerInstance, Project, Diagnostic string
	Generation, ExternalInput                                   uint64
	Jobs                                                        []DiscussionJobStatus
	Notes                                                       []workermail.NoteStatus
	RetirementOperation                                         string
	OldestPendingNote                                           time.Time
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
	for _, admission := range st.Admissions {
		if admission.ConversationID == d.cfg.ConversationID && admission.SettledAt.IsZero() {
			result.PendingAdmissions++
		}
	}
	result.Generation = max(c.ManagerGeneration, 1)
	result.ExternalInput = c.ExternalInput
	if c.Retirement != nil {
		result.RetirementOperation = protocol.ManagerRetireOperationID(*c.Retirement)
	}
	result.ManagerSession, result.ManagerInstance, result.Project = c.Binding.SessionID, c.Binding.SessionInstance, c.Project
	switch {
	case c.Parked:
		if c.FailedResume != nil {
			result.Diagnostic = c.FailedResume.Diagnostic
		}
	case c.Retirement != nil:
		result.State = "parking"
		result.Diagnostic = "awaiting exact manager retirement receipt"
		if discussionPending(st) {
			result.Diagnostic = "queued work waiting for dispatcher retirement reconciliation"
		}
	case c.CustodyLost:
		result.State = "blocked"
		result.Diagnostic = discussionCustodyDiagnostic
	default:
		result.State = "active"
		switch {
		case c.SchedulerPaused:
			result.Diagnostic = "manual intervention: direct terminal activity needs reconciliation"
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
		case worker.VMChild && !worker.Ended && !worker.Mailbox:
			job.Diagnostic = "worker mailbox binding unavailable; manager retained"
		case worker.VMChild && !worker.Ended:
			job.Diagnostic = "awaiting worker evidence"
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
	if err := d.noteStatus(st, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (d *DiscussionController) noteStatus(st State, result *DiscussionNativeStatus) error {
	publications := make(map[string][]workermail.PublicationStatus)
	addOutput := func(source, id string) {
		if source != "" {
			publications[source] = append(publications[source], workermail.PublicationStatus{ID: id, State: "awaiting_delivery_receipt"})
		}
	}
	for _, p := range st.Publications {
		if p.ConversationID == d.cfg.ConversationID {
			addOutput(p.DiscussionSource.AdmissionID, p.ID)
		}
	}
	for _, r := range st.Requests {
		if r.ConversationID == d.cfg.ConversationID && r.Kind == "manager" {
			addOutput(r.DiscussionSource.AdmissionID, r.ID)
		}
	}
	type noteKey struct{ session, instance, attempt, id string }
	seen := make(map[noteKey]bool)
	for _, w := range st.Wakes {
		if w.ConversationID != d.cfg.ConversationID {
			continue
		}
		e := st.Events[key(w.ConversationID, w.EventID)]
		if !e.WorkerMessage {
			continue
		}
		var meta workerNoteMetadata
		if err := json.Unmarshal(e.Metadata, &meta); err != nil {
			return err
		}
		if meta.NoteID == "" || meta.Created == nil {
			continue
		}
		n := workermail.NoteStatus{ID: meta.NoteID, JobID: e.JobID, AttemptID: e.AttemptID, Session: meta.Session, Instance: meta.Instance, Kind: meta.Kind, Created: *meta.Created, State: w.State, Admission: w.Admission, Source: "wake:" + w.ID, Answered: w.WorkerReplyExported}
		if w.Admission != 0 {
			n.AdmissionPhase = st.Admissions[admissionKey(w.ConversationID, w.Admission)].Phase
			n.Publications = publications[w.ID]
			sort.Slice(n.Publications, func(i, j int) bool { return n.Publications[i].ID < n.Publications[j].ID })
		}
		seen[noteKey{n.Session, n.Instance, n.AttemptID, n.ID}] = true
		result.Notes = append(result.Notes, n)
	}
	if d.scope.VMProjectAccess && filepath.IsAbs(d.scope.ManagerSettingsDir) {
		pending, err := workermail.Pending(d.scope.ManagerSettingsDir, d.cfg.ConversationID)
		if err != nil {
			return err
		}
		for _, p := range pending {
			if seen[noteKey{p.Session, p.Instance, p.Attempt, p.ID}] {
				continue
			}
			result.Notes = append(result.Notes, workermail.NoteStatus{ID: p.ID, JobID: "vm-child-" + hashValue([]string{p.Discussion, p.Session}), AttemptID: p.Attempt, Session: p.Session, Instance: p.Instance, Kind: p.Kind, Created: p.Created, State: p.State})
		}
	}
	awaitingAdmission := func(n workermail.NoteStatus) bool {
		return n.State == "prepared" || n.State == "queued" || n.State == "admitted" || n.State == "native_input" && n.AdmissionPhase == "submitted"
	}
	for _, n := range result.Notes {
		if awaitingAdmission(n) && (result.OldestPendingNote.IsZero() || n.Created.Before(result.OldestPendingNote)) {
			result.OldestPendingNote = n.Created
		}
	}
	sort.Slice(result.Notes, func(i, j int) bool {
		a, b := result.Notes[i], result.Notes[j]
		aPending, bPending := awaitingAdmission(a), awaitingAdmission(b)
		if aPending != bPending {
			return aPending
		}
		if aPending && !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		if !a.Created.Equal(b.Created) {
			return a.Created.After(b.Created)
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.ID < b.ID
	})
	if !result.OldestPendingNote.IsZero() && result.Diagnostic == "" {
		switch result.State {
		case "parking":
			result.Diagnostic = "worker note queued while dispatcher is retiring"
		case "parked":
			result.Diagnostic = "worker note queued for parked dispatcher"
		default:
			result.Diagnostic = "worker note awaiting manager admission"
		}
	}
	if len(result.Notes) > 16 {
		result.Notes = result.Notes[:16]
	}
	return nil
}
