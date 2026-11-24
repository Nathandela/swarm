package workermail

import "time"

// NoteStatus correlates supervisor acceptance, manager admission and Discord receipts
// without exposing worker text or a manager's answer.
type NoteStatus struct {
	ID             string              `json:"id"`
	JobID          string              `json:"job_id"`
	AttemptID      string              `json:"attempt_id"`
	Session        string              `json:"session"`
	Instance       string              `json:"instance"`
	Kind           string              `json:"kind,omitempty"`
	Created        time.Time           `json:"created"`
	State          string              `json:"state"`
	Source         string              `json:"source,omitempty"`
	Admission      uint64              `json:"admission,omitempty"`
	AdmissionPhase string              `json:"admission_phase,omitempty"`
	Answered       bool                `json:"answered,omitempty"`
	Publications   []PublicationStatus `json:"publications,omitempty"`
}

type PublicationStatus struct {
	ID            string   `json:"id"`
	State         string   `json:"state"`
	MessageIDs    []string `json:"discord_message_ids,omitempty"`
	AttachmentIDs []string `json:"discord_attachment_ids,omitempty"`
}
