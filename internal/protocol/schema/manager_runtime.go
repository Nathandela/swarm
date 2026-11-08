package schema

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

type ManagerRuntimeBinding struct {
	Session               string `json:"session"`
	SessionInstance       string `json:"session_instance"`
	ConversationID        string `json:"conversation_id"`
	Provider              string `json:"provider"`
	NativeSessionID       string `json:"native_session_id"`
	NativeGeneration      string `json:"native_generation"`
	ObservationGeneration string `json:"observation_generation"`
	LoginEpoch            string `json:"login_epoch"`
}

func (b ManagerRuntimeBinding) Validate() error {
	if b.Provider != "claude" && b.Provider != "codex" {
		return errors.New("invalid manager runtime binding")
	}
	for _, value := range []string{b.Session, b.SessionInstance, b.ConversationID, b.NativeSessionID, b.NativeGeneration, b.ObservationGeneration, b.LoginEpoch} {
		if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) {
			return errors.New("invalid manager runtime binding")
		}
	}
	return nil
}

type ManagerAdmission struct {
	ExpectedExternalInput uint64                `json:"expected_external_input"`
	Binding               ManagerRuntimeBinding `json:"binding"`
	Nonce                 string                `json:"nonce"`
}

func (a ManagerAdmission) Validate() error {
	if a.Binding.Validate() != nil || len(a.Nonce) != 64 || strings.ToLower(a.Nonce) != a.Nonce {
		return errors.New("invalid manager admission")
	}
	if _, err := hex.DecodeString(a.Nonce); err != nil {
		return errors.New("invalid manager admission")
	}
	return nil
}

type ManagerNativeTurn struct {
	ExternallySteered bool   `json:"externally_steered,omitempty"`
	NativeInputID     string `json:"native_input_id,omitempty"`
	NativeTurnID      string `json:"native_turn_id,omitempty"`
	State             string `json:"state"`
	Reason            string `json:"reason,omitempty"`
	ExternalInput     uint64 `json:"external_input"`
}
type ManagerRuntimeReq struct {
	SessionInstance string `json:"session_instance"`
}
type ManagerRuntimeStatus struct {
	ReadyForInput bool                  `json:"ready_for_input"`
	BridgeSocket  string                `json:"bridge_socket"`
	AuthFile      string                `json:"auth_file"`
	Binding       ManagerRuntimeBinding `json:"binding"`
	Turn          ManagerNativeTurn     `json:"turn"`
}
type ManagerCallReq struct {
	Binding       ManagerRuntimeBinding `json:"binding"`
	ToolName      string                `json:"tool_name"`
	ArgumentsHash string                `json:"arguments_hash"`
	HostMeta      json.RawMessage       `json:"host_meta"`
}
type ManagerCallProof struct {
	NativeInputID string `json:"native_input_id"`
	NativeTurnID  string `json:"native_turn_id"`
	NativeCallID  string `json:"native_call_id"`
	Main          bool   `json:"main"`
}
