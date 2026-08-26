package adapter

// SessionNameRequest is a provider-native structured request that makes the
// provider's session label match Swarm's. The assembly executes it over the
// already-owned backend connection; adapters remain pure and own no I/O.
type SessionNameRequest struct {
	Method string
	Params map[string]string
}

// SessionNameSync is the optional naming extension for providers with a
// structured runtime naming plane. Absence is supported: launch-only providers
// can still consume LaunchSpec.Name without claiming live bidirectional sync.
type SessionNameSync interface {
	SetSessionName(conversationID, name string) (SessionNameRequest, bool)
	SessionNameFromEvent(HookPayload) (conversationID, name string, ok bool)
}

// AsSessionNameSync reports whether an adapter supports live structured naming.
func AsSessionNameSync(a Adapter) (SessionNameSync, bool) {
	syncer, ok := a.(SessionNameSync)
	return syncer, ok
}
