package schema

import (
	"errors"
	"strings"
	"unicode"
)

const EnvManagerVMCLI = "SWARM_MANAGER_VM_CLI"

const DiscussionConversationPrefix = "discord-teammate-"

func IsDiscordManager(role, conversationID string) bool {
	return role == "manager" && strings.HasPrefix(conversationID, DiscussionConversationPrefix)
}

// ManagerLaunchReq binds a native manager to its private per-place bridge.
// The service credential is read from AuthFile and never appears on the wire.
type ManagerLaunchReq struct {
	NativeCLI         bool   `json:"native_cli,omitempty"`
	ConversationID    string `json:"conversation_id"`
	BridgeSocket      string `json:"bridge_socket"`
	AuthFile          string `json:"auth_file"`
	RetireOperationID string `json:"retire_operation_id,omitempty"`
}

const (
	ManagerReplaceExited  = "exited"
	ManagerReplaceCustody = "custody_lost"
	ManagerReplaceQuota   = "provider_exhausted"
)

// ManagerRetireReq identifies the native writer and its durable retirement checkpoint.
// Ordinary parking verifies its last observed turn. Replacement requires separate
// supervisor proof of the named condition and the old native writers' exit.
type ManagerRetireReq struct {
	Replacement  string                `json:"replacement,omitempty"`
	Binding      ManagerRuntimeBinding `json:"binding"`
	Generation   uint64                `json:"generation"`
	Checkpoint   string                `json:"checkpoint"`
	ExpectedTurn ManagerNativeTurn     `json:"expected_turn"`
}

func (r ManagerRetireReq) Validate() error {
	validTurn := r.ExpectedTurn.State == "idle" || r.ExpectedTurn.State == "completed"
	if r.Replacement != "" {
		validCause := r.Replacement == ManagerReplaceCustody || r.Replacement == ManagerReplaceQuota || r.Replacement == ManagerReplaceExited
		validTurn = validCause && r.ExpectedTurn == (ManagerNativeTurn{State: "unknown"})
	}
	if (ManagerAdmission{Binding: r.Binding, Nonce: r.Checkpoint}).Validate() != nil || r.Generation == 0 || !validTurn {
		return errors.New("invalid manager retirement checkpoint")
	}
	for _, value := range []string{r.ExpectedTurn.NativeInputID, r.ExpectedTurn.NativeTurnID, r.ExpectedTurn.Reason} {
		if len(value) > 256 || strings.ContainsFunc(value, unicode.IsControl) {
			return errors.New("invalid manager retirement checkpoint turn")
		}
	}
	return nil
}

const (
	ManagerRetireDeleted       = "deleted"
	ManagerRetireDeletedParked = "deleted_parked"
)

type ManagerRetireReceipt struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
}

func (r ManagerRetireReceipt) ValidState() bool {
	switch r.State {
	case "unknown", "prepared", "uncertain", "retired", "cancelled", ManagerRetireDeleted, ManagerRetireDeletedParked:
		return true
	default:
		return false
	}
}
