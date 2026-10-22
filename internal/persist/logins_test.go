package persist

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
)

func loginMeta(home string) Meta {
	return Meta{ID: "native", AgentType: "codex", LoginRuntime: &logins.Runtime{Provider: "codex", Epoch: 3, Identity: strings.Repeat("a", 64), Home: home}, InputEmbargo: "switch-incident"}
}

func TestLoginRuntimeRoundtripAndSchema(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := loginMeta(filepath.Join(t.TempDir(), "native"))
	if err := store.Save(m); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(m.ID)
	if err != nil || got.SchemaVersion != LoginSchemaVersion || got.LoginRuntime == nil || *got.LoginRuntime != *m.LoginRuntime || got.InputEmbargo != m.InputEmbargo {
		t.Fatalf("runtime did not survive persistence: %+v, %v", got, err)
	}
	for _, change := range []func(*Meta){
		func(m *Meta) { m.SchemaVersion = 1 },
		func(m *Meta) { m.LoginRuntime.Provider = "claude" },
		func(m *Meta) { m.LoginRuntime.Epoch = 0 },
		func(m *Meta) { m.LoginRuntime.Identity = "unverified" },
		func(m *Meta) { m.LoginRuntime.Home = "../native" },
		func(m *Meta) { m.LoginRuntime = nil },
		func(m *Meta) { m.InputEmbargo = "../incident" },
	} {
		bad := loginMeta(m.LoginRuntime.Home)
		bad.SchemaVersion = LoginSchemaVersion
		change(&bad)
		raw, _ := json.Marshal(bad)
		if _, err := decodeMeta(raw, bad.ID); err == nil {
			t.Fatalf("invalid runtime accepted: %s", raw)
		}
	}
}

func TestPreCutoverSessionRequiresMigration(t *testing.T) {
	if _, err := decodeMeta([]byte(`{"schema_version":2,"id":"native","agent_type":"codex"}`), "native"); err == nil {
		t.Fatal("pre-cutover session silently became an ordinary session")
	}
}
