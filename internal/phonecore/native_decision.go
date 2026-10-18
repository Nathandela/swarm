package phonecore

import (
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

type NativeDecisionInput struct {
	Machine     string
	OperationID string
	ExpiresAt   time.Time
	Request     schema.NativeDecisionReq
}

func SignNativeDecision(ks crypto.KeyStore, in NativeDecisionInput) (schema.DeviceCommandAuth, error) {
	return SignCommand(ks, CommandInput{Action: schema.ActionNativeDecide, Machine: in.Machine,
		Session: in.Request.Session, OperationID: in.OperationID, ExpiresAt: in.ExpiresAt,
		ContentHash: schema.NativeDecisionContentHash(&in.Request)})
}

func SealNativeDecisionEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, body *schema.NativeDecisionReq) ([]byte, error) {
	return sealPhoneFrame(key, epochID, seq, commandFrame{Kind: kindPhoneToMachine,
		RemoteCommand: schema.RemoteCommand{DeviceCommandAuth: cmd, BodyVersion: 1, NativeDecision: body}})
}
