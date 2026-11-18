package persist

import "testing"

func TestManagerRoleAndConversationSurviveMetadataRoundtrip(t *testing.T) {
	store, _ := newTestStore(t)
	meta := fullMeta()
	meta.Role = "manager"
	meta.ManagerConversationID = "voice-conversation"
	meta.ManagerBridgeSocket = "/private/service/bridge.sock"
	meta.ManagerAuthFile = "/private/service/auth"
	meta.ManagerClaudeConfigDir = "/private/native/profile"
	if err := store.Save(meta); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Role != "manager" || loaded.ManagerConversationID != meta.ManagerConversationID || loaded.ManagerBridgeSocket != meta.ManagerBridgeSocket || loaded.ManagerAuthFile != meta.ManagerAuthFile || loaded.ManagerClaudeConfigDir != meta.ManagerClaudeConfigDir {
		t.Fatalf("manager binding lost: %+v", loaded)
	}
}
