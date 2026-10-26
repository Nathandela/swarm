package schema

// SemanticMessageReceipt separates transport submission, native admission and execution outcome.
type SemanticMessageReceipt struct {
	Origin             *MessageOrigin `json:"origin,omitempty"`
	OperationID        string         `json:"operation_id"`
	Session            string         `json:"session"`
	SessionInstance    string         `json:"session_instance,omitempty"`
	State              string         `json:"state"`
	Reason             string         `json:"reason,omitempty"`
	NativeSessionID    string         `json:"native_session_id,omitempty"`
	NativeMessageID    string         `json:"native_message_id,omitempty"`
	NativeGeneration   string         `json:"native_generation,omitempty"`
	NativeCreatedAtMS  int64          `json:"native_created_at_ms,omitempty"`
	Agent              string         `json:"agent,omitempty"`
	Model              string         `json:"model,omitempty"`
	Variant            string         `json:"variant,omitempty"`
	SelectionBasis     string         `json:"selection_basis,omitempty"`
	SelectionMessageID string         `json:"selection_message_id,omitempty"`
	OutcomeSource      string         `json:"outcome_source,omitempty"`
}
