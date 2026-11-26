package schema

// WorkerFirstTurnReceipt identifies a verified, completed native turn from a
// discussion's direct worker launch.
type WorkerFirstTurnReceipt struct {
	AdmissionID     string        `json:"admission_id"`
	ManagerSession  string        `json:"manager_session"`
	ManagerInstance string        `json:"manager_instance"`
	WorkerSession   string        `json:"worker_session"`
	WorkerInstance  string        `json:"worker_instance"`
	JobID           string        `json:"job_id"`
	AttemptID       string        `json:"attempt_id"`
	ActionID        string        `json:"action_id"`
	NativeTurnID    string        `json:"native_turn_id"`
	Origin          MessageOrigin `json:"-"`
}
