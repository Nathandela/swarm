package shimwire

const (
	MaxClaudeFixtureTextBytes = 16 << 10
	TypeClaudeMessage         = "claude_message"
	TypeClaudeMessageResult   = "claude_message_result"
)

type ClaudePrimaryIdentity struct {
	SessionID        string `json:"session_id"`
	RequiredRuntime  string `json:"required_runtime"`
	NativeSessionID  string `json:"native_session_id"`
	ShimPID          int    `json:"shim_pid"`
	ShimStartTime    int64  `json:"shim_start_time"`
	PrimaryPID       int    `json:"primary_pid"`
	PrimaryStartTime int64  `json:"primary_start_time"`
}

type ClaudePrimarySnapshot struct {
	Identity         ClaudePrimaryIdentity `json:"identity"`
	ObservedSequence uint64                `json:"observed_sequence"`
	DurableSequence  uint64                `json:"durable_sequence"`
	ReaderRevision   uint64                `json:"reader_revision"`
	OwnerRevision    uint64                `json:"owner_revision"`
	Healthy          bool                  `json:"healthy"`
	PendingPartial   bool                  `json:"pending_partial"`
	StdoutEOF        bool                  `json:"stdout_eof"`
	StderrEOF        bool                  `json:"stderr_eof"`
}

type ClaudeMessageRequest struct {
	Version               int                   `json:"v"`
	OperationID           string                `json:"operation_id"`
	NativeUserUUID        string                `json:"native_user_uuid"`
	Text                  string                `json:"text"`
	Identity              ClaudePrimaryIdentity `json:"identity"`
	ExpectedOwnerRevision uint64                `json:"expected_owner_revision"`
	OwnerRevision         uint64                `json:"owner_revision"`
	ThroughStreamSequence uint64                `json:"through_stream_sequence"`
	ReaderRevision        uint64                `json:"reader_revision"`
}

type ClaudeMessageResult struct {
	Version        int                   `json:"v"`
	OperationID    string                `json:"operation_id"`
	NativeUserUUID string                `json:"native_user_uuid"`
	Identity       ClaudePrimaryIdentity `json:"identity"`
	OwnerRevision  uint64                `json:"owner_revision"`
	Status         string                `json:"status"` // written, refused, uncertain ONLY
}
