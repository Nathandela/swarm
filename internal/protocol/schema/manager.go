package schema

import (
	"errors"
	"strings"
	"unicode"
)

// ManagerLaunchReq binds a native manager to its private per-place bridge.
// The service credential is read from AuthFile and never appears on the wire.
type ManagerLaunchReq struct {
	ConversationID    string `json:"conversation_id"`
	BridgeSocket      string `json:"bridge_socket"`
	AuthFile          string `json:"auth_file"`
	RetireOperationID string `json:"retire_operation_id,omitempty"`
}

// ManagerRetireReq binds one quiescent checkpoint to the exact native writer
// and the last turn observed when the controller wrote the checkpoint.
type ManagerRetireReq struct {
	Binding      ManagerRuntimeBinding `json:"binding"`
	Generation   uint64                `json:"generation"`
	Checkpoint   string                `json:"checkpoint"`
	ExpectedTurn ManagerNativeTurn     `json:"expected_turn"`
}

func (r ManagerRetireReq) Validate() error {
	if (ManagerAdmission{Binding: r.Binding, Nonce: r.Checkpoint}).Validate() != nil || r.Generation == 0 ||
		(r.ExpectedTurn.State != "idle" && r.ExpectedTurn.State != "completed") {
		return errors.New("invalid manager retirement checkpoint")
	}
	for _, value := range []string{r.ExpectedTurn.NativeInputID, r.ExpectedTurn.NativeTurnID, r.ExpectedTurn.Reason} {
		if len(value) > 256 || strings.ContainsFunc(value, unicode.IsControl) {
			return errors.New("invalid manager retirement checkpoint turn")
		}
	}
	return nil
}

type ManagerRetireReceipt struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
}

func (r ManagerRetireReceipt) ValidState() bool {
	switch r.State {
	case "unknown", "prepared", "uncertain", "retired":
		return true
	default:
		return false
	}
}
