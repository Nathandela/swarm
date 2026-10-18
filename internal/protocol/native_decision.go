package protocol

import "github.com/Nathandela/swarm/internal/protocol/schema"

const (
	OpNativeDecide     = "native_decide"
	ActionNativeDecide = schema.ActionNativeDecide
	CapNativeRequests  = "native-requests-v1"
)

type (
	NativeDecisionReq     = schema.NativeDecisionReq
	NativeDecisionReceipt = schema.NativeDecisionReceipt
	NativeRequestView     = schema.NativeRequestView
	NativeQuestionView    = schema.NativeQuestionView
	NativeQuestionOption  = schema.NativeQuestionOption
	NativeDecisionOption  = schema.NativeDecisionOption
)

type NativeDecisionApplier interface {
	ApplyNativeDecision(machine, operationID string, req NativeDecisionReq) (NativeDecisionReceipt, ErrorCode, error)
}
