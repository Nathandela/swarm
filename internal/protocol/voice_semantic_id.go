package protocol

import "time"

// SemanticOperationReplayable uses the same replay window and validation as SemanticSend.
func SemanticOperationReplayable(id string, now time.Time) bool {
	return validSemanticOperationID(id, now)
}
