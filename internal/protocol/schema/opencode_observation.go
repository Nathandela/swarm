package schema

// OpenCodeObservationView carries bounded owner-readable facts, never request authorization payloads.
type OpenCodeObservationView struct {
	Version           int                      `json:"v"`
	RootID            string                   `json:"root_id"`
	Generation        string                   `json:"generation"`
	Phase             string                   `json:"binding_phase"`
	Epoch             uint64                   `json:"observation_epoch"`
	Busy              bool                     `json:"busy"`
	BusyKnown         bool                     `json:"busy_known"`
	RootBusy          bool                     `json:"root_busy"`
	RootBusyKnown     bool                     `json:"root_busy_known"`
	CurrentScopeKnown bool                     `json:"current_scope_known"`
	Attention         bool                     `json:"attention"`
	Degraded          bool                     `json:"degraded"`
	Reason            string                   `json:"reason,omitempty"`
	Error             string                   `json:"error,omitempty"`
	RetryAttempt      int                      `json:"retry_attempt,omitempty"`
	RetryAtMS         int64                    `json:"retry_at_ms,omitempty"`
	CurrentInputID    string                   `json:"current_input_id,omitempty"`
	Inputs            []OpenCodeInputView      `json:"inputs,omitempty"`
	InputCount        int                      `json:"input_count"`
	Pending           []OpenCodeRequestView    `json:"pending,omitempty"`
	PendingCount      int                      `json:"pending_count"`
	Descendants       []OpenCodeDescendantView `json:"descendants,omitempty"`
	DescendantCount   int                      `json:"descendant_count"`
	Truncated         bool                     `json:"truncated,omitempty"`
}

type OpenCodeInputView struct {
	MessageID     string `json:"message_id"`
	AtMS          int64  `json:"at_ms"`
	Text          string `json:"text,omitempty"`
	Synthetic     bool   `json:"synthetic,omitempty"`
	Classified    bool   `json:"classified"`
	Outcome       string `json:"outcome,omitempty"`
	OutcomeSource string `json:"outcome_source,omitempty"`
	Error         string `json:"error,omitempty"`
	Agent         string `json:"agent,omitempty"`
	Model         string `json:"model,omitempty"`
	Variant       string `json:"variant,omitempty"`
}

type OpenCodeRequestView struct {
	Kind          string `json:"kind"`
	RequestID     string `json:"request_id"`
	SessionID     string `json:"session_id"`
	Summary       string `json:"summary"`
	QuestionCount int    `json:"question_count,omitempty"`
}

type OpenCodeDescendantView struct {
	SessionID string `json:"session_id"`
	ParentID  string `json:"parent_id"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}
