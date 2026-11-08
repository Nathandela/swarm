package schema

// ManagerLaunchReq binds a native manager to its private per-place bridge.
// The service credential is read from AuthFile and never appears on the wire.
type ManagerLaunchReq struct {
	ConversationID string `json:"conversation_id"`
	BridgeSocket   string `json:"bridge_socket"`
	AuthFile       string `json:"auth_file"`
}
