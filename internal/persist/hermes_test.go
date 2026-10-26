package persist

import "testing"

func TestHermesNativeRuntimeCannotBeReadAsClassicSession(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := Meta{ID: "hermes-native", AgentType: "hermes", HermesNative: true, CLIIdentity: &CLIIdentity{Version: "0.22.5"}}
	if err = s.Save(m); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(m.ID)
	if err != nil || !got.HermesNative || got.SchemaVersion != HermesSchemaVersion || got.SchemaVersion <= LoginSchemaVersion {
		t.Fatalf("native Hermes protection=%+v %v", got, err)
	}
	m.AgentType = "claude"
	if err = s.Save(m); err == nil {
		t.Fatal("Hermes runtime marker accepted for another provider")
	}
}
