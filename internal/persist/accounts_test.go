package persist

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
)

func managedMeta() Meta {
	return Meta{ID: "managed", AgentType: "codex", AccountBinding: &accounts.Binding{SchemaVersion: 1, Provider: "codex", AccountID: strings.Repeat("a", 32), Identity: strings.Repeat("b", 64), CredentialGeneration: 1, ConfigurationGeneration: 1}, AccountProjectionRef: strings.Repeat("c", 64), InputEmbargo: "incident"}
}

func TestManagedMetaRoundtripAndExecutionCriticalSchema(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := managedMeta()
	if err := store.Save(m); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(m.ID)
	if err != nil || loaded.SchemaVersion != ManagedSchemaVersion || *loaded.AccountBinding != *m.AccountBinding || loaded.InputEmbargo != m.InputEmbargo {
		t.Fatal("managed binding did not survive persistence")
	}
	for _, mutate := range []func(*Meta){
		func(m *Meta) { m.SchemaVersion = 1 },
		func(m *Meta) { m.AccountBinding.SchemaVersion = 2 },
		func(m *Meta) { m.AccountBinding.Identity = "" },
		func(m *Meta) { m.AccountBinding.Provider = "claude" },
		func(m *Meta) { m.AccountProjectionRef = "../profile" },
		func(m *Meta) { m.AccountBinding = nil },
	} {
		bad := managedMeta()
		bad.SchemaVersion = ManagedSchemaVersion
		mutate(&bad)
		data, _ := json.Marshal(bad)
		if _, err := decodeMeta(data, bad.ID); err == nil {
			t.Fatal("invalid managed metadata accepted")
		}
	}
}
